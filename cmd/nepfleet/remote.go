// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Serraniel/nepfleet/internal/notify"
	"github.com/Serraniel/nepfleet/internal/remote"
)

// cmdRemote: nepfleet remote probe <address>.
func cmdRemote(args []string, stdout, stderr io.Writer) error {
	const usage = "usage: nepfleet remote probe ssh://[user@]host[:port] [--json] [--security] [--ssh-config FILE] | nepfleet remote add|list|refresh|connect|disconnect|remove"
	if len(args) > 0 && (args[0] == "add" || args[0] == "list" || args[0] == "refresh" || args[0] == "remove" || args[0] == "connect" || args[0] == "disconnect") {
		return cmdRemoteManage(args[0], args[1:], stdout, stderr)
	}
	if len(args) == 0 || args[0] != "probe" {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet("remote probe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print the result as JSON")
	config := fs.String("ssh-config", "", "ssh config file instead of ~/.ssh/config")
	security := fs.Bool("security", false, "also check its security: SSH, firewall, exposed ports, updates, Docker")
	pos, err := parseInterspersed(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New(usage)
	}
	t, err := remote.ParseTarget(pos[0])
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	ssh := remote.SSH{ConfigFile: *config}
	r, err := remote.Probe(ctx, ssh.Run, t)
	if err != nil {
		return err
	}
	var findings []remote.Finding
	if *security {
		findings, err = remote.CheckSecurity(ctx, func(ctx context.Context, script string) ([]byte, error) {
			return ssh.Run(ctx, t, script)
		})
		if err != nil {
			return err
		}
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			*remote.Report
			Next     string           `json:"next"`
			Security []remote.Finding `json:"security,omitempty"`
		}{r, r.Next(), findings})
	}
	printReport(stdout, r)
	if *security {
		fmt.Fprintln(stdout, "\nSecurity:")
		remote.PrintFindings(stdout, findings)
	}
	return nil
}

func printReport(w io.Writer, r *remote.Report) {
	line := func(k, v string) {
		if v != "" {
			fmt.Fprintf(w, "  %-10s %s\n", k, v)
		}
	}
	fmt.Fprintln(w, r.Target)
	line("system", fmt.Sprintf("%s (%s, %s)", r.OS, r.Kernel, r.Arch))
	who := r.User
	switch {
	case r.Root:
		who += " (root)"
	case r.Sudo:
		who += " (sudo without a password)"
	}
	line("user", who)
	switch {
	case !r.Docker:
		line("docker", "not installed")
	case !r.DockerRunning:
		line("docker", "installed, not reachable by this user")
	default:
		line("docker", fmt.Sprintf("%s, compose %s, %d running containers in %d projects", r.DockerVersion, noneIfEmpty(r.Compose), r.Containers, r.Projects))
	}
	if r.Swarm != "" {
		line("swarm", r.Swarm+" "+r.SwarmCluster)
	}
	line("nepfleet", r.NepFleet)
	line("container", r.NepFleetImage)
	if r.NepFleetRunning {
		line("server", "answers on port 7480")
	}
	line("fleet", r.Fleet)
	fmt.Fprintf(w, "\n%s\n", r.Next())
}

func noneIfEmpty(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// localSecurity is nepfleet doctor --security: the same check on this host.
func localSecurity(c common, asJSON, notifyNew bool, stdout io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// A fleet is optional here: a host can be checked before it joins one.
	e, openErr := c.open()
	if openErr != nil {
		e = nil
	}
	if notifyNew && openErr != nil {
		return fmt.Errorf("--notify needs a fleet: %w", openErr)
	}
	findings, err := securityFindings(ctx, c, e)
	if err != nil {
		return err
	}
	if notifyNew {
		if err := e.notifySecurity(findings, stdout); err != nil {
			return err
		}
	}
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(findings)
	}
	remote.PrintFindings(stdout, findings)
	return nil
}

// notifySecurity reports problems that are new since the last check, once each.
func (e *env) notifySecurity(findings []remote.Finding, stdout io.Writer) error {
	path := filepath.Join(e.stateDir, "security", "problems.json")
	var seen []string
	if raw, err := os.ReadFile(path); err == nil { //nolint:gosec // the installation's own state directory
		if err := json.Unmarshal(raw, &seen); err != nil {
			seen = nil // damaged: report everything again rather than nothing
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fresh, now := remote.NewProblems(findings, seen)
	n, err := e.notifier()
	if err != nil {
		return err
	}
	for _, f := range fresh {
		e.emit(n, stdout, notify.Event{Name: "host.security_problem", Severity: notify.Warning, Title: "security problem on " + e.host, Message: f.Message})
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(now)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

// securityFindings checks this host's security, as doctor --security. With a
// fleet (e not nil), the stacks planned here count as managed.
func securityFindings(ctx context.Context, c common, e *env) ([]remote.Finding, error) {
	var managed []string
	if e != nil {
		stacks, err := e.fleet.Stacks()
		if err != nil {
			return nil, err
		}
		managed = []string{}
		for _, s := range stacks {
			if s.Spec.Target == c.host || s.Spec.Target == "local" {
				managed = append(managed, s.Metadata.Name)
			}
		}
	}
	return remote.CheckSecurityFor(ctx, func(ctx context.Context, script string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "sh", "-s")
		cmd.Stdin = strings.NewReader(script)
		return cmd.Output()
	}, managed)
}
