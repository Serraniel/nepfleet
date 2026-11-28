// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Serraniel/nepfleet"
	"github.com/Serraniel/nepfleet/internal/control"
	"github.com/Serraniel/nepfleet/internal/flock"
	"github.com/Serraniel/nepfleet/internal/remote"
)

// controlDir holds the daemon's socket, state and log, private to the user.
func controlDir() string {
	return envOr("NEPFLEET_CONTROL_DIR", filepath.Join(defaultState(), "control"))
}

func controlSocket() string { return filepath.Join(controlDir(), "control.sock") }

// cmdControl: nepfleet control [status] — the local daemon.
func cmdControl(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "status" {
		var info controlInfo
		if err := controlCall(context.Background(), http.MethodGet, "/v1/info", nil, &info); err != nil {
			return errors.New("the control daemon is not running; start it with nepfleet control")
		}
		printControlInfo(stdout, info)
		return nil
	}
	if len(args) > 0 && args[0] == "restart" {
		return cmdControlRestart(stdout)
	}
	if len(args) > 0 && args[0] == "token" {
		t, err := control.LoadHTTPToken(controlDir())
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, t)
		return nil
	}
	if len(args) > 0 && args[0] == "http" {
		return cmdControlHTTP(args[1:], stdout, stderr)
	}
	fs := flag.NewFlagSet("control", flag.ContinueOnError)
	fs.SetOutput(stderr)
	httpAddr := fs.String("http", "", "also listen on this loopback address for browsers, e.g. 127.0.0.1:7481")
	origins := fs.String("allowed-origins", "", "comma separated origins of the web UI allowed to call --http")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("usage: nepfleet control [--http ADDR --allowed-origins URLS] | control http ADDR --allowed-origins URLS | control status | control restart | control token")
	}
	if err := os.MkdirAll(controlDir(), 0o700); err != nil {
		return err
	}
	// One daemon at a time. Held until this process exits, so a restart knows
	// when the old daemon has let go of everything, its port too.
	unlock, err := flock.Lock(controlLock(), false)
	if errors.Is(err, flock.ErrHeld) {
		return control.ErrRunning
	}
	if err != nil {
		return err
	}
	defer unlock()
	l, err := control.Listen(controlSocket())
	if err != nil {
		return err
	}
	defer func() { _ = l.Close() }()
	s := &control.Server{Dir: controlDir(), Version: version, Commit: commit, Connector: remote.SSH{ConfigFile: os.Getenv("NEPFLEET_SSH_CONFIG")}, Probe: func(ctx context.Context, t remote.Target) (*remote.Report, error) {
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		return remote.Probe(ctx, remote.SSH{ConfigFile: os.Getenv("NEPFLEET_SSH_CONFIG")}.Run, t)
	}}
	// Setting remotes up from clients: what host bootstrap and host install do.
	ssh := remote.SSH{ConfigFile: os.Getenv("NEPFLEET_SSH_CONFIG")}
	s.Setup.Run, s.Setup.Ingress = ssh.Run, "nepfleet_ingress"
	// Installs and updates write the image pinned to a digest signed by
	// NepFleet's release workflow, checked when they're planned.
	verify := remote.SigstoreVerifier()
	s.Setup.ComposeFor = func(ctx context.Context) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		return remote.VerifiedCompose(ctx, nepfleet.Compose, version, verify)
	}
	s.Doctor = func(ctx context.Context, t remote.Target) ([]remote.DoctorCheck, error) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		return remote.Doctor(ctx, ssh.Run, t)
	}
	news := &remote.NewsFeed{URL: remote.ArchNewsURL}
	s.HostUpdates = func(ctx context.Context, t remote.Target) (*remote.HostUpdates, error) {
		// checkupdates and dnf may sync package metadata first.
		ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		h, err := remote.CheckHostUpdates(ctx, ssh.Run, t)
		if err != nil || !h.Arch() || h.LastUpgrade.IsZero() {
			return h, err
		}
		// Arch news since the last upgrade usually asks for manual steps.
		if h.News, err = news.Since(ctx, h.LastUpgrade); err != nil {
			h.Notes = append(h.Notes, "couldn't read the Arch news: "+err.Error())
		}
		return h, nil
	}
	s.Setup.Mounts = controlMounts(func() string { return s.LoadSettings().FleetDir })
	if err := s.Load(); err != nil {
		return err
	}
	// Keys with a passphrase need an agent: the session's, or our own.
	if !remote.AgentReachable(os.Getenv("SSH_AUTH_SOCK")) {
		sock, stopAgent, err := remote.StartAgent(controlDir())
		if err != nil {
			fmt.Fprintf(stderr, "nepfleet control: no SSH agent (%v); keys with a passphrase can't be unlocked\n", err)
		} else {
			_ = os.Setenv("SSH_AUTH_SOCK", sock)
			defer stopAgent()
		}
	}
	if exe, err := os.Executable(); err == nil {
		s.Keys = control.Keys{Askpass: exe, Unlock: func(r *http.Request, path, passphrase, askpass string) error {
			return remote.Unlock(r.Context(), path, passphrase, askpass, remote.KeyLifetime)
		}}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s.Stop = stop
	if *httpAddr != "" {
		// Before listening on the socket's clients: a bad flag stops the start.
		if err := os.MkdirAll(controlDir(), 0o700); err != nil {
			return err
		}
		if err := s.EnableHTTP(*httpAddr, splitOrigins(*origins)); err != nil {
			return err
		}
		fmt.Fprintf(stderr, "nepfleet control also on http://%s (token: nepfleet control token)\n", *httpAddr)
	}
	fmt.Fprintf(stderr, "nepfleet control listening on %s\n", controlSocket())
	return s.Serve(ctx, l)
}

func splitOrigins(s string) []string {
	var out []string
	for _, o := range strings.Split(s, ",") {
		if o = strings.TrimSpace(o); o != "" {
			out = append(out, o)
		}
	}
	return out
}

// cmdControlHTTP turns on the port for browsers in a running daemon (starting
// it if needed), without touching its tunnels. It is remembered.
func cmdControlHTTP(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("control http", flag.ContinueOnError)
	fs.SetOutput(stderr)
	origins := fs.String("allowed-origins", "", "comma separated origins of the web UI; added to those allowed already")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: nepfleet control http 127.0.0.1:7481 --allowed-origins URLS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := ensureControl(ctx); err != nil {
		return err
	}
	body := map[string]any{"addr": pos[0], "origins": splitOrigins(*origins)}
	if err := controlCall(ctx, http.MethodPost, "/v1/http", body, nil); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "http://%s\n", pos[0])
	return nil
}

func controlClient() *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", controlSocket())
	}}}
}

// controlCall sends one request to the daemon.
func controlCall(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://control"+path, rd) //nolint:gosec // always the local socket, see controlClient
	if err != nil {
		return err
	}
	res, err := controlClient().Do(req) //nolint:gosec // always the local socket: the client dials nothing else
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&e)
		return &controlError{Status: res.StatusCode, Message: e.Error}
	}
	if out == nil || res.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// controlLock is held by the running daemon for its whole life; the system
// drops it when the process exits, after its port and tunnels are gone.
func controlLock() string { return filepath.Join(controlDir(), "control.lock") }

// controlLockHeld tells whether a daemon still holds controlLock.
func controlLockHeld() bool {
	unlock, err := flock.Lock(controlLock(), false)
	if err != nil {
		return errors.Is(err, flock.ErrHeld)
	}
	unlock()
	return false
}

// lockControlStart serializes starting the daemon: concurrent commands (the
// app runs several at once) share one start instead of racing for the socket.
func lockControlStart(ctx context.Context) (func(), error) {
	if err := os.MkdirAll(controlDir(), 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(controlDir(), "start.lock")
	for {
		unlock, err := flock.Lock(path, false)
		if !errors.Is(err, flock.ErrHeld) {
			return unlock, err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for another start of the control daemon: %w", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// ensureControl starts the daemon if it isn't running. It keeps running after
// this command ends, with its output in a log file.
func ensureControl(ctx context.Context) error {
	if controlCall(ctx, http.MethodGet, "/v1/info", nil, nil) == nil {
		return nil
	}
	unlock, err := lockControlStart(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return startControlLocked(ctx)
}

// startControlLocked starts the daemon unless one answers; the caller holds the
// start lock.
func startControlLocked(ctx context.Context) error {
	// Another command may have started it while this one waited for the lock.
	if controlCall(ctx, http.MethodGet, "/v1/info", nil, nil) == nil {
		return nil
	}
	// A daemon that is stopping still holds its port: start after it's gone.
	if err := waitControlGone(ctx, 0); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	dir := controlDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(dir, "control.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // the daemon's own directory
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()
	cmd := exec.Command(self, "control") //nolint:gosec,noctx // this binary; it must outlive this command
	cmd.Stdout, cmd.Stderr = logFile, logFile
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting the control daemon: %w", err)
	}
	// Reaped when it ends, should this process still run then: a zombie would
	// look like a daemon that never stops.
	go func() { _ = cmd.Wait() }()
	for range 50 {
		if controlCall(ctx, http.MethodGet, "/v1/info", nil, nil) == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("the control daemon did not start; see %s", logFile.Name())
}

func printRemotes(stdout io.Writer, all []control.Remote) {
	if len(all) == 0 {
		fmt.Fprintln(stdout, "no remotes yet; add one with nepfleet remote add ssh://user@host")
		return
	}
	for _, r := range all {
		fmt.Fprintf(stdout, "%-20s %-30s tunnel %s\n", r.Name, r.Address, r.Tunnel)
		if r.TunnelError != "" {
			fmt.Fprintf(stdout, "  ⚠ %s\n", r.TunnelError)
		}
		if r.Error != "" {
			fmt.Fprintf(stdout, "  ⚠ last check failed: %s\n", r.Error)
		}
		if r.Next != "" {
			fmt.Fprintf(stdout, "  %s\n", r.Next)
		}
	}
}

// cmdRemoteManage handles the remote subcommands that go through the daemon.
func cmdRemoteManage(sub string, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("remote "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "name of the remote (default: its host)")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := ensureControl(ctx); err != nil {
		return err
	}
	switch sub {
	case "list":
		var all []control.Remote
		if err := controlCall(ctx, http.MethodGet, "/v1/remotes", nil, &all); err != nil {
			return err
		}
		if *asJSON {
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(all)
		}
		printRemotes(stdout, all)
		return nil
	case "add":
		if len(pos) != 1 {
			return errors.New("usage: nepfleet remote add ssh://[user@]host[:port] [--name NAME]")
		}
		var r control.Remote
		if err := controlCall(ctx, http.MethodPost, "/v1/remotes", map[string]string{"name": *name, "address": pos[0]}, &r); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "added %s\n", r.Name)
		printReport(stdout, r.Report)
		return nil
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: nepfleet remote %s <name>", sub)
	}
	path := "/v1/remotes/" + url.PathEscape(pos[0])
	if sub == "connect" || sub == "disconnect" {
		var r control.Remote
		if err := controlCall(ctx, http.MethodPost, path+"/"+sub, nil, &r); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s: tunnel %s\n", r.Name, r.Tunnel)
		return nil
	}
	if sub == "remove" {
		if err := controlCall(ctx, http.MethodDelete, path, nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "removed %s\n", pos[0])
		return nil
	}
	var r control.Remote
	if err := controlCall(ctx, http.MethodPost, path+"/refresh", nil, &r); err != nil {
		return err
	}
	if r.Error != "" {
		return errors.New(r.Error)
	}
	printReport(stdout, r.Report)
	return nil
}
