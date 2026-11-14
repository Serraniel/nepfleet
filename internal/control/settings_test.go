// SPDX-License-Identifier: AGPL-3.0-or-later

package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Serraniel/nepfleet/internal/remote"
)

func TestSettings(t *testing.T) {
	s := &Server{Dir: t.TempDir()}
	h := s.Handler()
	do := func(method, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, "/v1/settings", strings.NewReader(body)))
		return rec
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	home, _ := os.UserHomeDir()
	fleet := filepath.Join(home, "fleet")
	if err := os.Mkdir(fleet, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "fleet.yaml"), []byte("kind: Fleet\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	put := func(dir string) string {
		raw, _ := json.Marshal(Settings{FleetDir: dir})
		return string(raw)
	}
	if err := os.WriteFile(filepath.Join(fleet, "fleet.yaml"), []byte("kind: Fleet\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for body, want := range map[string]int{
		put("relative/path"):                    422,
		put(filepath.Join(home, "empty")):       422,
		put(outside):                            422,
		put(fleet + string(filepath.Separator)): 200,
		`{"other":1}`:                           400,
	} {
		if rec := do(http.MethodPut, body); rec.Code != want {
			t.Errorf("%s: %d, want %d (%s)", body, rec.Code, want, rec.Body)
		}
	}
	if got := s.LoadSettings().FleetDir; got != fleet {
		t.Errorf("saved %q", got)
	}
	if rec := do(http.MethodGet, ""); !strings.Contains(rec.Body.String(), fleet) {
		t.Errorf("get: %s", rec.Body)
	}
}

// Mounts are a setup like the others: planned, reviewed, run as a job.
func TestSetupMounts(t *testing.T) {
	report := &remote.Report{Kernel: "Linux", Arch: "x86_64", OSID: "debian", User: "me", Sudo: true, Docker: true, DockerRunning: true}
	s := &Server{Dir: t.TempDir(), Probe: func(_ context.Context, tg remote.Target) (*remote.Report, error) {
		r := *report
		r.Target = tg.String()
		return &r, nil
	}}
	var ranMount bool
	s.Setup.Run = func(_ context.Context, _ remote.Target, script string) ([]byte, error) {
		if strings.Contains(script, "MOUNT-SECRET") {
			ranMount = true
		}
		return nil, nil
	}
	steps := []remote.Step{{Text: "mount nas:/media (nfs) at /mnt/media, on first use", Root: true, Script: "echo MOUNT-SECRET"}}
	s.Setup.Mounts = func(context.Context, string, func(context.Context, string) ([]byte, error), *remote.Report) ([]remote.Step, error) {
		return steps, nil
	}
	h := s.Handler()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	if rec := do(http.MethodPost, "/v1/remotes", `{"address":"ssh://me@vps","name":"vps"}`); rec.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	rec := do(http.MethodGet, "/v1/remotes/vps/setup?kind=mounts", "")
	var p SetupPlan
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &p) != nil || p.ID == "" || strings.Contains(rec.Body.String(), "MOUNT-SECRET") {
		t.Fatalf("plan: %d %s", rec.Code, rec.Body)
	}
	if rec := do(http.MethodPost, "/v1/remotes/vps/setup", `{"kind":"mounts","plan":"`+p.ID+`"}`); rec.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	var j SetupJob
	for range 100 {
		_ = json.Unmarshal(do(http.MethodGet, "/v1/remotes/vps/setup/job", "").Body.Bytes(), &j)
		if j.State != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if j.State != "succeeded" || !ranMount || strings.Contains(j.Log, "MOUNT-SECRET") {
		t.Errorf("job: %+v ran=%v", j, ranMount)
	}
	// Nothing to do is said, not run.
	steps = nil
	if rec := do(http.MethodGet, "/v1/remotes/vps/setup?kind=mounts", ""); !strings.Contains(rec.Body.String(), "nothing to do") {
		t.Errorf("no steps: %s", rec.Body)
	}
}
