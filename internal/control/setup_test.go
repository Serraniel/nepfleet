// SPDX-License-Identifier: AGPL-3.0-or-later

package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Serraniel/nepfleet/internal/remote"
)

func TestSetup(t *testing.T) {
	var mu sync.Mutex
	report := &remote.Report{Kernel: "Linux", Arch: "x86_64", OSID: "arch", User: "me", Sudo: true}
	probe := func(_ context.Context, tg remote.Target) (*remote.Report, error) {
		mu.Lock()
		defer mu.Unlock()
		r := *report
		r.Target = tg.String()
		return &r, nil
	}
	var ran []string
	fail := false
	s := &Server{Dir: t.TempDir(), Probe: probe}
	s.Setup.Run = func(_ context.Context, _ remote.Target, script string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		ran = append(ran, script)
		if fail {
			return nil, errors.New("exit status 1")
		}
		return nil, nil
	}
	s.Setup.Compose = []byte("services: {}\n")
	s.Setup.Ingress = "nepfleet_ingress"
	h := s.Handler()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	if rec := do(http.MethodPost, "/v1/remotes", `{"address":"ssh://me@vps","name":"vps"}`); rec.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	plan := func(kind string) SetupPlan {
		t.Helper()
		rec := do(http.MethodGet, "/v1/remotes/vps/setup?kind="+kind, "")
		var p SetupPlan
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &p) != nil {
			t.Fatalf("plan %s: %d %s", kind, rec.Code, rec.Body)
		}
		return p
	}
	// A bare machine: bootstrap has steps; install can't run yet and says why.
	b := plan(SetupBootstrap)
	if b.ID == "" || len(b.Steps) == 0 || b.Error != "" || !strings.Contains(b.Steps[0].Text, "install Docker") {
		t.Errorf("bootstrap plan: %+v", b)
	}
	if i := plan(SetupInstall); i.ID != "" || !strings.Contains(i.Error, "host bootstrap") {
		t.Errorf("install before bootstrap: %+v", i)
	}
	if rec := do(http.MethodGet, "/v1/remotes/vps/setup?kind=other", ""); rec.Code != http.StatusBadGateway {
		t.Errorf("unknown kind: %d", rec.Code)
	}
	if rec := do(http.MethodGet, "/v1/remotes/nope/setup?kind=install", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown remote: %d", rec.Code)
	}
	// Only the reviewed plan runs.
	if rec := do(http.MethodPost, "/v1/remotes/vps/setup", `{"kind":"bootstrap","plan":"0000"}`); rec.Code != http.StatusConflict {
		t.Errorf("a stale plan: %d", rec.Code)
	}
	if rec := do(http.MethodPost, "/v1/remotes/vps/setup", `{"kind":"bootstrap","plan":"`+b.ID+`"}`); rec.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	job := func() SetupJob {
		t.Helper()
		var j SetupJob
		for range 100 {
			rec := do(http.MethodGet, "/v1/remotes/vps/setup/job", "")
			_ = json.Unmarshal(rec.Body.Bytes(), &j)
			if j.State != "running" {
				return j
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("the job never finished")
		return j
	}
	if j := job(); j.State != "succeeded" || !strings.Contains(j.Log, "install Docker") {
		t.Errorf("job: %+v", j)
	}
	mu.Lock()
	if len(ran) != len(b.Steps) || !strings.Contains(ran[0], "sudo -n sh") {
		t.Errorf("ran %d scripts: %q", len(ran), ran)
	}
	// Now Docker runs: install is the next setup, and a failure is reported.
	report.Docker, report.DockerRunning, report.Compose = true, true, "2.40"
	fail = true
	mu.Unlock()
	i := plan(SetupInstall)
	if i.ID == "" {
		t.Fatalf("install plan: %+v", i)
	}
	if rec := do(http.MethodPost, "/v1/remotes/vps/setup", `{"kind":"install","plan":"`+i.ID+`"}`); rec.Code != http.StatusAccepted {
		t.Fatalf("start install: %d %s", rec.Code, rec.Body)
	}
	if j := job(); j.State != "failed" || !strings.Contains(j.Error, "step 1") {
		t.Errorf("a failed setup: %+v", j)
	}
}

// A user whose sudo asks for a password: the plan says so, starting needs the
// password, and it reaches the root steps but no log, job or error.
func TestSetupSudoPassword(t *testing.T) {
	const pw = "c0rrect-h0rse"
	report := &remote.Report{Kernel: "Linux", Arch: "x86_64", OSID: "arch", User: "me", SudoPassword: true}
	var mu sync.Mutex
	var ran []string
	s := &Server{Dir: t.TempDir(), Probe: func(_ context.Context, tg remote.Target) (*remote.Report, error) {
		r := *report
		r.Target = tg.String()
		return &r, nil
	}}
	s.Setup.Run = func(_ context.Context, _ remote.Target, script string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		ran = append(ran, script)
		// A failing step whose output repeats the password must not leak it.
		return nil, errors.New("sh: " + pw + ": not found")
	}
	s.Setup.Ingress = "nepfleet_ingress"
	h := s.Handler()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	if rec := do(http.MethodPost, "/v1/remotes", `{"address":"ssh://me@vps","name":"vps"}`); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body)
	}
	var p SetupPlan
	if rec := do(http.MethodGet, "/v1/remotes/vps/setup?kind=bootstrap", ""); json.Unmarshal(rec.Body.Bytes(), &p) != nil || !p.SudoPassword || p.ID == "" || p.Error != "" {
		t.Fatalf("plan: %s", rec.Body)
	}
	if rec := do(http.MethodPost, "/v1/remotes/vps/setup", `{"kind":"bootstrap","plan":"`+p.ID+`"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("no password: %d %s", rec.Code, rec.Body)
	}
	if rec := do(http.MethodPost, "/v1/remotes/vps/setup", `{"kind":"bootstrap","plan":"`+p.ID+`","sudoPassword":"`+pw+`"}`); rec.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	var job SetupJob
	for range 200 {
		rec := do(http.MethodGet, "/v1/remotes/vps/setup/job", "")
		if json.Unmarshal(rec.Body.Bytes(), &job) == nil && job.State != "running" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if job.State != "failed" || strings.Contains(job.Error+job.Log, pw) {
		t.Errorf("job: %+v", job)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ran) == 0 || !strings.Contains(ran[0], "sudo -k -S") {
		t.Errorf("the root step didn't get the password: %q", ran)
	}
	if rec := do(http.MethodGet, "/v1/remotes", ""); strings.Contains(rec.Body.String(), pw) {
		t.Error("the password shows in the remotes")
	}
}
