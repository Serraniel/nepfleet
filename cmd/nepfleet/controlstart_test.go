// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// controlTestDir points the daemon at a fresh private directory, short enough
// for a socket path, and stops whatever daemon runs there at the end.
func controlTestDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "nfc")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEPFLEET_CONTROL_DIR", dir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv(testCLIEnv, "1")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var info controlInfo
		if controlCall(ctx, http.MethodGet, "/v1/info", nil, &info) == nil {
			_ = stopControl(ctx, info)
		}
		_ = os.RemoveAll(dir)
	})
	return dir
}

// controlLog is what the daemons started in dir wrote.
func controlLog(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "control.log")) //nolint:gosec // the test's own directory
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// freeAddr is a loopback address nothing listens on.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// probe calls the daemon on its socket and dials its port until stop, and
// counts what failed: a handover must not refuse anyone.
func probe(ctx context.Context, addr string, stop <-chan struct{}) (calls, failed *atomic.Int32, done func()) {
	calls, failed = new(atomic.Int32), new(atomic.Int32)
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			calls.Add(1)
			if controlCall(ctx, http.MethodGet, "/v1/info", nil, nil) != nil {
				failed.Add(1)
			}
			if c, err := net.DialTimeout("tcp", addr, time.Second); err != nil {
				failed.Add(1)
			} else {
				_ = c.Close()
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
	return calls, failed, wg.Wait
}

// Commands started at once (the app runs several) share one daemon.
func TestConcurrentStartsMakeOneDaemon(t *testing.T) {
	dir := controlTestDir(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { errs <- ensureControl(ctx) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	log := controlLog(t, dir)
	if n := strings.Count(log, "nepfleet control listening"); n != 1 {
		t.Errorf("%d daemons started, want 1:\n%s", n, log)
	}
}

// A second daemon refuses while the first runs, even with its socket gone.
func TestSecondDaemonRefuses(t *testing.T) {
	dir := controlTestDir(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := ensureControl(ctx); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "control.sock")
	if err := os.Rename(sock, sock+".away"); err != nil {
		t.Fatal(err)
	}
	err := cmdControl(nil, io.Discard, io.Discard)
	// Back, so the cleanup can stop the daemon.
	if err := os.Rename(sock+".away", sock); err != nil {
		t.Fatal(err)
	}
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("second daemon: %v", err)
	}
}

// A restart while the app asks for starts hands over in place: no second
// daemon, no refused call, no bind error, the same port.
func TestRestartWhileStarting(t *testing.T) {
	dir := controlTestDir(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := ensureControl(ctx); err != nil {
		t.Fatal(err)
	}
	addr := freeAddr(t)
	if err := controlCall(ctx, http.MethodPost, "/v1/http", map[string]any{"addr": addr, "origins": []string{"http://ui.example.test"}}, nil); err != nil {
		t.Fatal(err)
	}
	var old controlInfo
	if err := controlCall(ctx, http.MethodGet, "/v1/info", nil, &old); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	calls, failed, probed := probe(ctx, addr, stop)
	var wg sync.WaitGroup
	errs := make(chan error, 5)
	wg.Go(func() { errs <- cmdControlRestart(io.Discard) })
	for range 4 {
		wg.Go(func() {
			time.Sleep(20 * time.Millisecond)
			errs <- ensureControl(ctx)
		})
	}
	wg.Wait()
	close(stop)
	probed()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	var now controlInfo
	if err := controlCall(ctx, http.MethodGet, "/v1/info", nil, &now); err != nil {
		t.Fatal(err)
	}
	if now.PID != old.PID || !now.Started.After(old.Started) {
		t.Errorf("not restarted in place: %+v, before %+v", now, old)
	}
	if n := failed.Load(); n > 0 {
		t.Errorf("%d of %d calls failed during the restart", n, calls.Load())
	}
	log := controlLog(t, dir)
	if n := strings.Count(log, "nepfleet control listening"); n != 1 {
		t.Errorf("%d daemons started, want 1:\n%s", n, log)
	}
	if !strings.Contains(log, "restarted in place") {
		t.Errorf("no handover:\n%s", log)
	}
	if strings.Contains(log, "address already in use") || strings.Contains(log, "another program uses") {
		t.Errorf("the port was taken:\n%s", log)
	}
}

// An updated binary takes over by itself, keeping the pid, the port and the
// keys in its agent; the same build written again changes nothing.
func TestUpdateRestartsInPlace(t *testing.T) {
	for _, p := range []string{"ssh-agent", "ssh-add", "ssh-keygen"} {
		if _, err := exec.LookPath(p); err != nil {
			t.Skip(p + " isn't installed")
		}
	}
	dir := controlTestDir(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin", "nepfleet")
	install := func() {
		// As a package manager does: a new file renamed over the old one.
		raw, err := os.ReadFile(self) //nolint:gosec // this test binary
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(bin), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bin+".new", raw, 0o700); err != nil { //nolint:gosec // an executable
			t.Fatal(err)
		}
		if err := os.Rename(bin+".new", bin); err != nil {
			t.Fatal(err)
		}
	}
	versionFile := filepath.Join(dir, "version")
	if err := os.WriteFile(versionFile, []byte("0.1.0"), 0o600); err != nil {
		t.Fatal(err)
	}
	install()
	t.Setenv(testVersionEnv, versionFile)
	t.Setenv(exeWatchEnv, "50ms")
	t.Setenv("SSH_AUTH_SOCK", "") // the daemon starts its own agent
	addr := freeAddr(t)
	logFile, err := os.Create(filepath.Join(dir, "control.log")) //nolint:gosec // the test's own directory
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logFile.Close() }()
	cmd := exec.Command(bin, "control", "--http", addr, "--allowed-origins", "http://ui.example.test") //nolint:gosec,noctx // the daemon outlives this call
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait() }()
	var old controlInfo
	for range 100 {
		if controlCall(ctx, http.MethodGet, "/v1/info", nil, &old) == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if old.Version != "0.1.0" {
		t.Fatalf("daemon: %+v\n%s", old, controlLog(t, dir))
	}
	// A key in the daemon's agent, as an unlock leaves it.
	key := filepath.Join(dir, "id_ed25519")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	agent := "SSH_AUTH_SOCK=" + filepath.Join(dir, "ssh-agent.sock")
	add := exec.Command("ssh-add", key)
	add.Env = append(os.Environ(), agent)
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("ssh-add: %v %s", err, out)
	}

	install() // the same build again
	time.Sleep(500 * time.Millisecond)
	var same controlInfo
	if err := controlCall(ctx, http.MethodGet, "/v1/info", nil, &same); err != nil || !same.Started.Equal(old.Started) {
		t.Fatalf("restarted for the same build: %+v %v", same, err)
	}

	// A build from before handovers would ignore what it's handed: left alone.
	if err := os.WriteFile(versionFile, []byte("0.1.5 nohandoff"), 0o600); err != nil {
		t.Fatal(err)
	}
	install()
	time.Sleep(500 * time.Millisecond)
	if err := controlCall(ctx, http.MethodGet, "/v1/info", nil, &same); err != nil || !same.Started.Equal(old.Started) || same.Version != "0.1.0" {
		t.Fatalf("handed over to a build that can't take over: %+v %v", same, err)
	}

	stop := make(chan struct{})
	calls, failed, probed := probe(ctx, addr, stop)
	if err := os.WriteFile(versionFile, []byte("0.2.0"), 0o600); err != nil {
		t.Fatal(err)
	}
	install()
	var now controlInfo
	for range 200 {
		if controlCall(ctx, http.MethodGet, "/v1/info", nil, &now) == nil && now.Version == "0.2.0" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	close(stop)
	probed()
	if now.Version != "0.2.0" || now.PID != old.PID {
		t.Fatalf("not updated in place: %+v, before %+v\n%s", now, old, controlLog(t, dir))
	}
	if n := failed.Load(); n > 0 {
		t.Errorf("%d of %d calls failed during the update", n, calls.Load())
	}
	list := exec.Command("ssh-add", "-l")
	list.Env = append(os.Environ(), agent)
	if out, err := list.CombinedOutput(); err != nil || !strings.Contains(string(out), "ED25519") {
		t.Errorf("the key is gone after the update: %v %s", err, out)
	}
	checkLockClosesOnExec(t, now.PID)
}

// checkLockClosesOnExec: the daemon's lock must close on exec, or every ssh it
// runs would hold it, and one left behind would keep daemons from starting.
func checkLockClosesOnExec(t *testing.T, pid int) {
	t.Helper()
	fds := fmt.Sprintf("/proc/%d/fd", pid)
	entries, err := os.ReadDir(fds)
	if err != nil {
		t.Skip("no /proc here")
	}
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join(fds, e.Name()))
		if err != nil || !strings.HasSuffix(target, "control.lock") {
			continue
		}
		info, err := os.ReadFile(fmt.Sprintf("/proc/%d/fdinfo/%s", pid, e.Name())) //nolint:gosec // procfs
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(info), "\n") {
			if v, ok := strings.CutPrefix(line, "flags:"); ok {
				flags, err := strconv.ParseInt(strings.TrimSpace(v), 8, 64)
				if err != nil || flags&0o2000000 == 0 { // O_CLOEXEC
					t.Errorf("the lock (fd %s) doesn't close on exec: flags %s", e.Name(), strings.TrimSpace(v))
				}
				return
			}
		}
	}
	t.Error("the daemon holds no lock")
}

// restartWith runs a daemon whose handover execs target instead (or with the
// version file's contents), then nepfleet control restart, and says the daemon
// before and after.
func restartWith(t *testing.T, target, versionLine string) (before, after controlInfo, log string) {
	t.Helper()
	dir := controlTestDir(t)
	if target != "" {
		t.Setenv(testExecEnv, target)
	}
	if versionLine != "" {
		f := filepath.Join(dir, "version")
		if err := os.WriteFile(f, []byte(versionLine), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(testVersionEnv, f)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := ensureControl(ctx); err != nil {
		t.Fatal(err)
	}
	if err := controlCall(ctx, http.MethodGet, "/v1/info", nil, &before); err != nil {
		t.Fatal(err)
	}
	if err := cmdControlRestart(io.Discard); err != nil {
		t.Fatalf("restart: %v\n%s", err, controlLog(t, dir))
	}
	if err := controlCall(ctx, http.MethodGet, "/v1/info", nil, &after); err != nil {
		t.Fatal(err)
	}
	return before, after, controlLog(t, dir)
}

// A handover whose exec fails (the binary gone mid-update) leaves the daemon
// serving; the restart then stops and starts it.
func TestFailedHandoverCarriesOn(t *testing.T) {
	before, after, log := restartWith(t, "/nonexistent/nepfleet", "")
	if !strings.Contains(log, "this daemon carries on") {
		t.Errorf("no failed handover:\n%s", log)
	}
	if after.PID == before.PID {
		t.Errorf("not restarted after the failed handover: %+v\n%s", after, log)
	}
}

// A new program that ends at once (it can't take over after all): the restart
// starts a daemon normally.
func TestHandoverToAnEndingProgramFallsBack(t *testing.T) {
	f, err := exec.LookPath("false")
	if err != nil {
		t.Skip("no false here")
	}
	before, after, log := restartWith(t, f, "")
	if after.PID == before.PID {
		t.Errorf("no daemon after the new program ended: %+v\n%s", after, log)
	}
}

// Restarting into a binary from before handovers stops and starts instead.
func TestRestartIntoOldBinaryStopsAndStarts(t *testing.T) {
	before, after, log := restartWith(t, "", "0.3.0 nohandoff")
	if strings.Contains(log, "restarted in place") || after.PID == before.PID {
		t.Errorf("not stopped and started: %+v\n%s", after, log)
	}
}
