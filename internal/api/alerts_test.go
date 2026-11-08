// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

func TestAlertEndpoint(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	tok := map[string]string{}
	for _, s := range []string{auth.ScopeNotify, auth.ScopeRead, auth.ScopeApply, auth.ScopeAdmin} {
		secret, _, err := store.Create(s, s, nil, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		tok[s] = secret
	}
	var got []Alert
	h := Handler(Options{Tokens: store,
		Alert:  func(_ context.Context, a Alert) error { got = append(got, a); return nil },
		Stacks: func() ([]StackInfo, error) { return []StackInfo{{Name: "web"}}, nil }})
	do := func(method, token, path, body string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	ok := `{"source":"dozzle","rule":"panic","container":"web-app-1","host":"nas","detail":"panic: boom"}`
	for scope, want := range map[string]int{auth.ScopeNotify: 202, auth.ScopeAdmin: 202, auth.ScopeRead: 403, auth.ScopeApply: 403, "": 401} {
		if code := do(http.MethodPost, tok[scope], "/api/v1/alerts", ok); code != want {
			t.Errorf("%q token: %d, want %d", scope, code, want)
		}
	}
	if len(got) != 2 || got[0].Rule != "panic" || got[0].Container != "web-app-1" {
		t.Errorf("alerts: %+v", got)
	}
	if code := do(http.MethodGet, tok[auth.ScopeNotify], "/api/v1/stacks", ""); code != http.StatusForbidden {
		t.Errorf("a notify token must read nothing: %d", code)
	}
	for _, body := range []string{`{"rule":"x","extra":1}`, `{}`, `not json`, `{"detail":"` + strings.Repeat("x", 20<<10) + `"}`} {
		if code := do(http.MethodPost, tok[auth.ScopeNotify], "/api/v1/alerts", body); code != http.StatusBadRequest {
			t.Errorf("%.30s: %d", body, code)
		}
	}
	got = nil
	long := `{"rule":"` + strings.Repeat("r", 500) + `","container":"a\nb","detail":"line\u001b[31m red` + strings.Repeat("d", 3000) + `"}`
	if code := do(http.MethodPost, tok[auth.ScopeNotify], "/api/v1/alerts", long); code != http.StatusAccepted {
		t.Fatalf("long alert: %d", code)
	}
	a := got[0]
	if len([]rune(a.Rule)) != alertShort || len([]rune(a.Detail)) != alertDetail || strings.Contains(a.Detail, "\x1b") || a.Container != "a b" {
		t.Errorf("not cleaned: rule %d, detail %d, %q", len([]rune(a.Rule)), len([]rune(a.Detail)), a.Container)
	}
}

func TestUpdatesEndpoint(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	all, _, _ := store.Create("all", auth.ScopeRead, nil, time.Hour)
	db, _, _ := store.Create("db", auth.ScopeRead, []string{"db"}, time.Hour)
	h := Handler(Options{Tokens: store, Updates: func(_ context.Context, stack string) ([]ImageUpdate, error) {
		if stack == "none" {
			return nil, nil
		}
		return []ImageUpdate{{Stack: stack, Image: "nginx", Track: "minor", Current: "1.28", Next: "1.29"}}, nil
	}})
	get := func(token, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := get(all, "/api/v1/stacks/web/updates"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"next":"1.29"`) {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	if rec := get(all, "/api/v1/stacks/none/updates"); strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("none: %s", rec.Body)
	}
	if rec := get(db, "/api/v1/stacks/web/updates"); rec.Code != http.StatusNotFound {
		t.Errorf("a token for db must not see web: %d", rec.Code)
	}
}

func TestUpdateLockReachesPlanAndApply(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	tok, _, _ := store.Create("ci", auth.ScopeApply, nil, time.Hour)
	var planned, applied []bool
	h := Handler(Options{Tokens: store,
		Plan: func(_ context.Context, _ string, updateLock bool) (any, error) {
			planned = append(planned, updateLock)
			return map[string]string{"id": "p1"}, nil
		},
		StartApply: func(_ context.Context, _, _ string, _, updateLock bool, _ string) (func(io.Writer) error, error) {
			applied = append(applied, updateLock)
			return func(io.Writer) error { return nil }, nil
		}})
	do := func(method, path, body string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	do(http.MethodGet, "/api/v1/stacks/web/plan", "")
	do(http.MethodGet, "/api/v1/stacks/web/plan?updateLock=true", "")
	do(http.MethodPost, "/api/v1/stacks/web/apply", `{"plan":"p1"}`)
	if code := do(http.MethodPost, "/api/v1/stacks/web/apply", `{"plan":"p1","updateLock":true}`); code != http.StatusAccepted {
		t.Fatalf("apply: %d", code)
	}
	if fmt.Sprint(planned) != "[false true]" || fmt.Sprint(applied) != "[false true]" {
		t.Errorf("plan %v, apply %v", planned, applied)
	}
}

func TestMaintenanceEndpoint(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	all, _, _ := store.Create("all", auth.ScopeRead, nil, time.Hour)
	web, _, _ := store.Create("web", auth.ScopeRead, []string{"web"}, time.Hour)
	h := Handler(Options{Tokens: store, Maintenance: func(context.Context) ([]KumaMaintenance, error) {
		return []KumaMaintenance{{Kuma: "home", Windows: []MaintenanceWindow{{ID: 1, Title: "NepFleet: update of web", Status: "under-maintenance"}}}}, nil
	}})
	get := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/maintenance", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := get(all); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"under-maintenance"`) {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	if rec := get(web); rec.Code != http.StatusForbidden {
		t.Errorf("a token limited to stacks: %d", rec.Code)
	}
}
