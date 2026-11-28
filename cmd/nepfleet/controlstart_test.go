// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// A restart while the app asks for starts ends with one new daemon, which gets
// the port back: the old one has let go of it first.
func TestRestartWhileStarting(t *testing.T) {
	dir := controlTestDir(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := ensureControl(ctx); err != nil {
		t.Fatal(err)
	}
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := free.Addr().String()
	_ = free.Close()
	if err := controlCall(ctx, http.MethodPost, "/v1/http", map[string]any{"addr": addr, "origins": []string{"http://ui.example.test"}}, nil); err != nil {
		t.Fatal(err)
	}
	var old controlInfo
	if err := controlCall(ctx, http.MethodGet, "/v1/info", nil, &old); err != nil {
		t.Fatal(err)
	}
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
	if now.PID == old.PID {
		t.Error("the old daemon still runs")
	}
	log := controlLog(t, dir)
	if n := strings.Count(log, "nepfleet control listening"); n != 2 {
		t.Errorf("%d daemons started, want 2:\n%s", n, log)
	}
	if strings.Contains(log, "address already in use") || strings.Contains(log, "another program uses") {
		t.Errorf("the port was still taken:\n%s", log)
	}
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("the new daemon doesn't listen on %s: %v", addr, err)
	}
	_ = c.Close()
}
