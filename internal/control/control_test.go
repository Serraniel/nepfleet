// SPDX-License-Identifier: AGPL-3.0-or-later

package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/remote"
)

func TestRemotes(t *testing.T) {
	dir := t.TempDir()
	down := false
	probe := func(_ context.Context, tg remote.Target) (*remote.Report, error) {
		if tg.Host == "unreachable" || down {
			return nil, errors.New("no route")
		}
		return &remote.Report{Kernel: "Linux", Target: tg.String()}, nil
	}
	s := &Server{Dir: dir, Probe: probe}
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	for body, want := range map[string]int{
		`{"address":"ssh://me@nas"}`:             http.StatusCreated,
		`{"address":"-oProxyCommand=x"}`:         http.StatusBadRequest,
		`{"address":"ssh://unreachable"}`:        http.StatusBadGateway,
		`{"address":"ssh://b","name":"../evil"}`: http.StatusBadRequest,
		`{"address":"ssh://b","other":1}`:        http.StatusBadRequest,
	} {
		if rec := do(http.MethodPost, "/v1/remotes", body); rec.Code != want {
			t.Errorf("%s: %d, want %d: %s", body, rec.Code, want, rec.Body)
		}
	}
	if rec := do(http.MethodPost, "/v1/remotes", `{"address":"ssh://nas"}`); rec.Code != http.StatusConflict {
		t.Errorf("same name: %d", rec.Code)
	}
	// No name and an IP address: the name is derived from it.
	if rec := do(http.MethodPost, "/v1/remotes", `{"address":"ssh://me@203.0.113.10"}`); rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"name":"203-0-113-10"`) {
		t.Errorf("an IP address without a name: %d %s", rec.Code, rec.Body)
	}
	if rec := do(http.MethodDelete, "/v1/remotes/203-0-113-10", ""); rec.Code >= 300 {
		t.Errorf("removing it: %d %s", rec.Code, rec.Body)
	}
	// A failed check keeps the last report and says why.
	down = true
	rec := do(http.MethodPost, "/v1/remotes/nas/refresh", "")
	var r Remote
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil || r.Error == "" || r.Report == nil {
		t.Errorf("refresh while down: %s", rec.Body)
	}
	fi, err := os.Stat(filepath.Join(dir, "remotes.json"))
	if err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Errorf("remotes.json: %v %v", fi, err)
	}
	// State survives a restart.
	s2 := &Server{Dir: dir, Probe: probe}
	if err := s2.Load(); err != nil || len(s2.remotes) != 1 || s2.remotes[0].Name != "nas" {
		t.Errorf("after a restart: %+v %v", s2.remotes, err)
	}
	if rec := do(http.MethodDelete, "/v1/remotes/nas", ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete: %d", rec.Code)
	}
	if rec := do(http.MethodDelete, "/v1/remotes/nas", ""); rec.Code != http.StatusNotFound {
		t.Errorf("delete again: %d", rec.Code)
	}
}

func TestListen(t *testing.T) {
	base := t.TempDir()
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(base, "c.sock")
	l, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(sock); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("socket mode %v", fi.Mode())
	}
	if _, err := Listen(sock); !errors.Is(err, ErrRunning) {
		t.Errorf("a second daemon: %v", err)
	}
	_ = l.Close()
	// A plain file there is not ours to replace.
	file := filepath.Join(base, "f.sock")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(file); err == nil {
		t.Error("a regular file must not be replaced")
	}
	if runtime.GOOS == "windows" {
		return // no such modes there
	}
	open := filepath.Join(base, "open")
	if err := os.Mkdir(open, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o755); err != nil { //nolint:gosec // testing the refusal
		t.Fatal(err)
	}
	if _, err := Listen(filepath.Join(open, "c.sock")); err == nil {
		t.Error("a directory others can enter must be refused")
	}
}
