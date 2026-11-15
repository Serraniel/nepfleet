// SPDX-License-Identifier: AGPL-3.0-or-later

package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/remote"
)

func TestRemoteDoctor(t *testing.T) {
	probe := func(_ context.Context, tg remote.Target) (*remote.Report, error) {
		return &remote.Report{Kernel: "Linux", Target: tg.String()}, nil
	}
	var asked string
	s := &Server{Dir: t.TempDir(), Probe: probe, Doctor: func(_ context.Context, tg remote.Target) ([]remote.DoctorCheck, error) {
		asked = tg.Host
		return []remote.DoctorCheck{{Name: "age key", Status: "fail", Detail: "no recipients", Fix: "nepfleet key add-recipient"}}, nil
	}}
	if err := s.Load(); err != nil {
		t.Fatal(err)
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
	rec := do(http.MethodPost, "/v1/remotes/vps/doctor", "")
	var checks []remote.DoctorCheck
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &checks) != nil || len(checks) != 1 || checks[0].Fix == "" || asked != "vps" {
		t.Errorf("doctor: %d %s (asked %q)", rec.Code, rec.Body, asked)
	}
	if rec := do(http.MethodPost, "/v1/remotes/nope/doctor", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown remote: %d", rec.Code)
	}
	s.Doctor = nil
	if rec := do(http.MethodPost, "/v1/remotes/vps/doctor", ""); rec.Code != http.StatusNotImplemented {
		t.Errorf("no doctor: %d", rec.Code)
	}
}

func TestRemoteHostUpdates(t *testing.T) {
	probe := func(_ context.Context, tg remote.Target) (*remote.Report, error) {
		return &remote.Report{Kernel: "Linux", Target: tg.String()}, nil
	}
	yes := true
	s := &Server{Dir: t.TempDir(), Probe: probe, HostUpdates: func(context.Context, remote.Target) (*remote.HostUpdates, error) {
		return &remote.HostUpdates{OS: "arch", Supported: true, Pending: 2, Security: 1, RebootRequired: &yes, Packages: []string{"linux", "openssl"}}, nil
	}}
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	do := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"address":"ssh://me@vps","name":"vps"}`)))
		return rec
	}
	if rec := do("/v1/remotes"); rec.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	rec := do("/v1/remotes/vps/host-updates")
	var got remote.HostUpdates
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Pending != 2 || got.RebootRequired == nil || !*got.RebootRequired {
		t.Errorf("host updates: %d %s", rec.Code, rec.Body)
	}
	if rec := do("/v1/remotes/nope/host-updates"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown remote: %d", rec.Code)
	}
	s.HostUpdates = nil
	if rec := do("/v1/remotes/vps/host-updates"); rec.Code != http.StatusNotImplemented {
		t.Errorf("no checker: %d", rec.Code)
	}
}
