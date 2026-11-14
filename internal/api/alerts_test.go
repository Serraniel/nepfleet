// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
	"github.com/go-webauthn/webauthn/webauthn"
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

func TestEgressEndpoint(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	all, _, _ := store.Create("all", auth.ScopeRead, nil, time.Hour)
	db, _, _ := store.Create("db", auth.ScopeRead, []string{"db"}, time.Hour)
	h := Handler(Options{Tokens: store, Egress: func(context.Context, string) (*StackEgress, error) {
		return &StackEgress{Mode: "enforce", Sidecars: 1, Hosts: []string{"api.example.org"}, Addresses: []string{}, LookedUp: []string{}}, nil
	}})
	get := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/stacks/web/egress", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := get(all); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"hosts":["api.example.org"]`) || !strings.Contains(rec.Body.String(), `"mode":"enforce"`) {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	if rec := get(db); rec.Code != http.StatusNotFound {
		t.Errorf("a token for db must not see web: %d", rec.Code)
	}
}

func TestEditEndpoints(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	tok := map[string]string{}
	for _, s := range []string{auth.ScopeRead, auth.ScopeApply, auth.ScopeEdit} {
		secret, _, _ := store.Create(s, s, nil, time.Hour)
		tok[s] = secret
	}
	webOnly, _, _ := store.Create("web-only", auth.ScopeEdit, []string{"web"}, time.Hour)
	next := error(nil)
	var got StackEdit
	h := Handler(Options{Tokens: store,
		StackFiles: func(context.Context, string) ([]StackFile, error) {
			return []StackFile{{Path: "stack.yaml", Content: "x", Revision: "r1"}}, nil
		},
		EditStack: func(_ context.Context, _ string, e StackEdit, _ string, _ []string) (EditResult, error) {
			got = e
			return EditResult{Commit: "c1"}, next
		}})
	do := func(method, token, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := do(http.MethodGet, tok[auth.ScopeRead], "/api/v1/stacks/web/files", ""); rec.Code != http.StatusForbidden {
		t.Errorf("a read token reads files: %d", rec.Code)
	}
	if rec := do(http.MethodGet, tok[auth.ScopeEdit], "/api/v1/stacks/web/files", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"revision":"r1"`) {
		t.Errorf("read files: %d %s", rec.Code, rec.Body)
	}
	edit := `{"files":{"compose.yaml":"services: {}\n"},"base":{"compose.yaml":"r0"},"message":"bump"}`
	for scope, want := range map[string]int{auth.ScopeRead: 403, auth.ScopeApply: 403, auth.ScopeEdit: 200} {
		if rec := do(http.MethodPut, tok[scope], "/api/v1/stacks/web/files", edit); rec.Code != want {
			t.Errorf("%s token: %d, want %d", scope, rec.Code, want)
		}
	}
	if got.Message != "bump" || got.Base["compose.yaml"] != "r0" {
		t.Errorf("edit: %+v", got)
	}
	if rec := do(http.MethodPut, webOnly, "/api/v1/stacks/db/files", edit); rec.Code != http.StatusNotFound {
		t.Errorf("a token for web edits db: %d", rec.Code)
	}
	for err, want := range map[error]int{ErrStale: 409, ErrBadPath: 400, InvalidError{Reason: "bad"}: 422, ErrBusy: 409} {
		next = err
		if rec := do(http.MethodPut, tok[auth.ScopeEdit], "/api/v1/stacks/web/files", edit); rec.Code != want {
			t.Errorf("%v: %d, want %d", err, rec.Code, want)
		}
	}
	next = nil
	for _, body := range []string{`{}`, `{"files":{}}`, `{"files":{"a":"b"},"x":1}`, `nope`} {
		if rec := do(http.MethodPut, tok[auth.ScopeEdit], "/api/v1/stacks/web/files", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", body, rec.Code)
		}
	}
}

func TestEditCapsFiles(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	secret, _, _ := store.Create("e", auth.ScopeEdit, nil, time.Hour)
	h := Handler(Options{Tokens: store, EditStack: func(context.Context, string, StackEdit, string, []string) (EditResult, error) {
		t.Error("too many files reached the server")
		return EditResult{}, nil
	}})
	files := map[string]string{}
	for i := range maxEditFiles + 1 {
		files[fmt.Sprintf("files/%d", i)] = "x"
	}
	body, _ := json.Marshal(StackEdit{Files: files})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/stacks/web/files", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+secret)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("%d files: %d", len(files), rec.Code)
	}
}

func TestSyncEndpoints(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	apply, _, _ := store.Create("ci", auth.ScopeApply, nil, time.Hour)
	read, _, _ := store.Create("r", auth.ScopeRead, nil, time.Hour)
	kicks := 0
	o := Options{Tokens: store, SyncSecret: "s3cret", SyncNow: func() { kicks++ },
		SyncStatus: func() SyncStatus { return SyncStatus{Enabled: true, Commit: "abc"} }}
	h := Handler(o)
	sign := func(body string) string {
		m := hmac.New(sha256.New, []byte("s3cret"))
		m.Write([]byte(body))
		return hex.EncodeToString(m.Sum(nil))
	}
	do := func(hdr map[string]string, body string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/sync", strings.NewReader(body))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	body := `{"ref":"refs/heads/main"}`
	for name, c := range map[string]struct {
		hdr  map[string]string
		want int
	}{
		"github":      {map[string]string{"X-Hub-Signature-256": "sha256=" + sign(body)}, 202},
		"forgejo":     {map[string]string{"X-Forgejo-Signature": sign(body)}, 202},
		"gitea":       {map[string]string{"X-Gitea-Signature": sign(body)}, 202},
		"wrong":       {map[string]string{"X-Hub-Signature-256": "sha256=" + sign("other")}, 401},
		"unsigned":    {map[string]string{}, 401},
		"apply token": {map[string]string{"Authorization": "Bearer " + apply}, 202},
		"read token":  {map[string]string{"Authorization": "Bearer " + read}, 403},
	} {
		if got := do(c.hdr, body); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
	if kicks != 4 {
		t.Errorf("syncs started: %d", kicks)
	}
	// Without a secret, no signature counts.
	o.SyncSecret = ""
	h = Handler(o)
	if got := do(map[string]string{"X-Hub-Signature-256": "sha256=" + sign(body)}, body); got != 401 {
		t.Errorf("no secret: %d", got)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sync", nil)
	req.Header.Set("Authorization", "Bearer "+read)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"commit":"abc"`) {
		t.Errorf("status: %d %s", rec.Code, rec.Body)
	}
}

func TestNewStackEndpoint(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	tok := map[string]string{}
	for _, s := range []string{auth.ScopeApply, auth.ScopeEdit} {
		secret, _, _ := store.Create(s, s, nil, time.Hour)
		tok[s] = secret
	}
	onlyWeb, _, _ := store.Create("web-only", auth.ScopeEdit, []string{"web"}, time.Hour)
	next := error(nil)
	var got NewStack
	h := Handler(Options{Tokens: store, NewStack: func(_ context.Context, req NewStack, _ string) (NewStackResult, error) {
		got = req
		return NewStackResult{Name: req.Name, Commit: "c1"}, next
	}})
	do := func(token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/stacks", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	body := `{"template":"hello","name":"web","values":{"GREETING":"hi"}}`
	if rec := do(tok[auth.ScopeApply], body); rec.Code != http.StatusForbidden {
		t.Errorf("apply token: %d", rec.Code)
	}
	if rec := do(tok[auth.ScopeEdit], body); rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"commit":"c1"`) {
		t.Errorf("edit token: %d %s", rec.Code, rec.Body)
	}
	if got.Values["GREETING"] != "hi" {
		t.Errorf("values: %+v", got)
	}
	if rec := do(onlyWeb, body); rec.Code != http.StatusCreated {
		t.Errorf("a token for web creates web: %d", rec.Code)
	}
	if rec := do(onlyWeb, `{"template":"hello","name":"db"}`); rec.Code != http.StatusForbidden {
		t.Errorf("a token for web creates db: %d", rec.Code)
	}
	for err, want := range map[error]int{ErrStackExists: 409, InvalidError{Reason: "x"}: 422, ErrBusy: 409} {
		next = err
		if rec := do(tok[auth.ScopeEdit], body); rec.Code != want {
			t.Errorf("%v: %d, want %d", err, rec.Code, want)
		}
	}
	next = nil
	for _, b := range []string{`{}`, `{"template":""}`, `{"template":"x","extra":1}`, `nope`} {
		if rec := do(tok[auth.ScopeEdit], b); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", b, rec.Code)
		}
	}
}

func TestSecretEndpoints(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	tok := map[string]string{}
	for _, s := range []string{auth.ScopeApply, auth.ScopeEdit, auth.ScopeAdmin} {
		secret, _, _ := store.Create(s, s, nil, time.Hour)
		tok[s] = secret
	}
	onlyWeb, _, _ := store.Create("web-only", auth.ScopeEdit, []string{"web"}, time.Hour)
	type call struct {
		stack, key string
		value      *string
	}
	var calls []call
	h := Handler(Options{Tokens: store,
		SecretKeys: func(_ context.Context, stack string) ([]string, error) {
			if stack == "" {
				return []string{"FLEET_KEY"}, nil
			}
			return []string{"DB_PASSWORD"}, nil
		},
		SetSecret: func(_ context.Context, stack, key string, value *string, _ string) (string, error) {
			calls = append(calls, call{stack, key, value})
			if key == "MISSING" {
				return "", ErrNoSecret
			}
			return "c1", nil
		}})
	do := func(method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for _, c := range []struct {
		method, path, token, body string
		want                      int
	}{
		{"GET", "/api/v1/stacks/web/secrets", tok[auth.ScopeApply], "", 403},
		{"GET", "/api/v1/stacks/web/secrets", tok[auth.ScopeEdit], "", 200},
		{"PUT", "/api/v1/stacks/web/secrets/DB_PASSWORD", tok[auth.ScopeApply], `{"value":"s"}`, 403},
		{"PUT", "/api/v1/stacks/web/secrets/DB_PASSWORD", tok[auth.ScopeEdit], `{"value":"s3cret-value"}`, 200},
		{"PUT", "/api/v1/stacks/web/secrets/1BAD", tok[auth.ScopeEdit], `{"value":"s"}`, 400},
		{"PUT", "/api/v1/stacks/web/secrets/sops", tok[auth.ScopeEdit], `{"value":"s"}`, 400},
		{"PUT", "/api/v1/stacks/web/secrets/OK", tok[auth.ScopeEdit], `{"value":"s","x":1}`, 400},
		{"PUT", "/api/v1/stacks/web/secrets/OK", tok[auth.ScopeEdit], `{"value":"` + strings.Repeat("a", maxSecret+1) + `"}`, 400},
		{"PUT", "/api/v1/stacks/db/secrets/OK", onlyWeb, `{"value":"s"}`, 404},
		{"DELETE", "/api/v1/stacks/web/secrets/DB_PASSWORD", tok[auth.ScopeEdit], "", 200},
		{"DELETE", "/api/v1/stacks/web/secrets/MISSING", tok[auth.ScopeEdit], "", 404},
		{"GET", "/api/v1/secrets", tok[auth.ScopeEdit], "", 403},
		{"GET", "/api/v1/secrets", tok[auth.ScopeAdmin], "", 200},
		{"PUT", "/api/v1/secrets/FLEET_KEY", tok[auth.ScopeEdit], `{"value":"s"}`, 403},
		{"PUT", "/api/v1/secrets/FLEET_KEY", tok[auth.ScopeAdmin], `{"value":"s"}`, 200},
	} {
		rec := do(c.method, c.path, c.token, c.body)
		if rec.Code != c.want {
			t.Errorf("%s %s: %d, want %d (%s)", c.method, c.path, rec.Code, c.want, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "s3cret-value") {
			t.Errorf("%s %s echoed the value", c.method, c.path)
		}
	}
	if len(calls) != 4 || calls[0].stack != "web" || *calls[0].value != "s3cret-value" || calls[1].value != nil || calls[3].stack != "" {
		t.Errorf("calls: %+v", calls)
	}
	if rec := do("GET", "/api/v1/stacks/web/secrets", tok[auth.ScopeEdit], ""); !strings.Contains(rec.Body.String(), `"keys":["DB_PASSWORD"]`) {
		t.Errorf("keys: %s", rec.Body)
	}
}

func TestFeaturesEndpoint(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	all, _, _ := store.Create("all", auth.ScopeRead, nil, time.Hour)
	onlyWeb, _, _ := store.Create("web", auth.ScopeRead, []string{"web"}, time.Hour)
	h := Handler(Options{Tokens: store, Features: func() ([]Feature, error) {
		return []Feature{{Name: "logs", Template: "dozzle", What: "logs", Stacks: []string{"dozzle@nas", "web@nas"}}}, nil
	}})
	get := func(token string) string {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/features", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return rec.Body.String()
	}
	if b := get(all); !strings.Contains(b, `"stacks":["dozzle@nas","web@nas"]`) {
		t.Errorf("all: %s", b)
	}
	if b := get(onlyWeb); !strings.Contains(b, `"stacks":["web@nas"]`) {
		t.Errorf("a token for web sees only web: %s", b)
	}
}

func TestChannelEndpoints(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	read, _, _ := store.Create("r", auth.ScopeRead, nil, time.Hour)
	admin, _, _ := store.Create("a", auth.ScopeAdmin, nil, time.Hour)
	h := Handler(Options{Tokens: store,
		Channels: func() ([]string, error) { return []string{"discord", "ntfy"}, nil },
		TestChannel: func(_ context.Context, name string) error {
			switch name {
			case "discord":
				return nil
			case "ntfy":
				return errors.New(`channel ntfy: Post "https://ntfy.example.org/topic?auth=tok3n": 401 Unauthorized`)
			}
			return ErrNoChannel
		}})
	do := func(method, path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := do("GET", "/api/v1/notifications/channels", read); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"channels":["discord","ntfy"]`) {
		t.Errorf("list: %d %s", rec.Code, rec.Body)
	}
	if rec := do("POST", "/api/v1/notifications/channels/discord/test", read); rec.Code != http.StatusForbidden {
		t.Errorf("read sends a test: %d", rec.Code)
	}
	if rec := do("POST", "/api/v1/notifications/channels/discord/test", admin); rec.Code != http.StatusNoContent {
		t.Errorf("test: %d", rec.Code)
	}
	rec := do("POST", "/api/v1/notifications/channels/ntfy/test", admin)
	if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "tok3n") || !strings.Contains(rec.Body.String(), "401 Unauthorized") {
		t.Errorf("failure: %d %s", rec.Code, rec.Body)
	}
	if rec := do("POST", "/api/v1/notifications/channels/nope/test", admin); rec.Code != http.StatusNotFound {
		t.Errorf("unknown: %d", rec.Code)
	}
}

func TestTokenEndpoints(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	admin, _, _ := store.Create("admin", auth.ScopeAdmin, nil, time.Hour)
	edit, _, _ := store.Create("edit", auth.ScopeEdit, nil, time.Hour)
	h := Handler(Options{Tokens: store})
	do := func(method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := do("GET", "/api/v1/tokens", edit, ""); rec.Code != http.StatusForbidden {
		t.Errorf("edit lists tokens: %d", rec.Code)
	}
	rec := do("GET", "/api/v1/tokens", admin, "")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "hash") || !strings.Contains(rec.Body.String(), `"name":"edit"`) {
		t.Errorf("list: %d %s", rec.Code, rec.Body)
	}
	for body, want := range map[string]int{
		`{"name":"ci","scope":"apply","stacks":["web"],"days":30}`: 201,
		`{"name":"ci","scope":"apply","days":0}`:                   422,
		`{"name":"ci","scope":"root","days":3}`:                    422,
		`{"name":"x","scope":"admin","stacks":["web"],"days":3}`:   422,
		`{"name":"x","scope":"read","stacks":["../x"],"days":3}`:   422,
		`{"name":"x","scope":"read","days":3,"more":1}`:            400,
	} {
		if rec := do("POST", "/api/v1/tokens", admin, body); rec.Code != want {
			t.Errorf("%s: %d, want %d (%s)", body, rec.Code, want, rec.Body)
		}
	}
	if rec := do("POST", "/api/v1/tokens", edit, `{"name":"ci","scope":"admin","days":3}`); rec.Code != http.StatusForbidden {
		t.Errorf("edit creates an admin token: %d", rec.Code)
	}
	rec = do("POST", "/api/v1/tokens", admin, `{"name":"tmp","scope":"read","days":1}`)
	var created CreatedToken
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || !strings.HasPrefix(created.Secret, auth.Prefix) || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("created: %s %v", rec.Body, err)
	}
	if got, err := store.Check(created.Secret); err != nil || got.Name != "tmp" {
		t.Errorf("the new secret works: %v", err)
	}
	if rec := do("DELETE", "/api/v1/tokens/"+created.Token.ID, admin, ""); rec.Code != http.StatusNoContent {
		t.Errorf("revoke: %d", rec.Code)
	}
	if _, err := store.Check(created.Secret); err == nil {
		t.Error("a revoked token still works")
	}
	for _, id := range []string{created.Token.ID, "nothex!!"} {
		if rec := do("DELETE", "/api/v1/tokens/"+id, admin, ""); rec.Code != http.StatusNotFound {
			t.Errorf("revoke %s: %d", id, rec.Code)
		}
	}
}

func TestSBOMEndpoints(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	all, _, _ := store.Create("all", auth.ScopeRead, nil, time.Hour)
	onlyWeb, _, _ := store.Create("web", auth.ScopeRead, []string{"web"}, time.Hour)
	h := Handler(Options{Tokens: store, SBOM: func(_ context.Context, stack string, allow func(string) bool, contents bool) (any, error) {
		var seen []string
		for _, s := range []string{"web", "db"} {
			if (stack == "" || s == stack) && allow(s) {
				seen = append(seen, s)
			}
		}
		if stack != "" && len(seen) == 0 {
			return nil, ErrNotFound
		}
		return map[string]any{"bomFormat": "CycloneDX", "stacks": seen, "contents": contents, "purl": "pkg:a?x=1&y=2"}, nil
	}})
	get := func(path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	rec := get("/api/v1/sbom", all)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != cycloneDX || !strings.Contains(rec.Body.String(), `"web",`) || !strings.Contains(rec.Body.String(), "&y=2") ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), "fleet.cdx.json") {
		t.Errorf("fleet: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	if rec := get("/api/v1/sbom", onlyWeb); strings.Contains(rec.Body.String(), `"db"`) {
		t.Errorf("a token for web sees db: %s", rec.Body)
	}
	if rec := get("/api/v1/stacks/web/sbom?contents=true", onlyWeb); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"contents": true`) {
		t.Errorf("stack: %d %s", rec.Code, rec.Body)
	}
	if rec := get("/api/v1/stacks/db/sbom", onlyWeb); rec.Code != http.StatusNotFound {
		t.Errorf("a token for web reads db: %d", rec.Code)
	}
}

func TestDoctorEndpoint(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	edit, _, _ := store.Create("e", auth.ScopeEdit, nil, time.Hour)
	admin, _, _ := store.Create("a", auth.ScopeAdmin, nil, time.Hour)
	h := Handler(Options{Tokens: store, Doctor: func(context.Context) (DoctorReport, error) {
		return DoctorReport{Host: "nas", Checks: []DoctorCheck{{Name: "Docker", Status: "ok", Detail: "29.1"}}, SecurityError: "no sh"}, nil
	}})
	get := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/doctor", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := get(edit); rec.Code != http.StatusForbidden {
		t.Errorf("edit: %d", rec.Code)
	}
	rec := get(admin)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"security":[]`) || !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Errorf("admin: %d %s", rec.Code, rec.Body)
	}
}

func TestUserEndpoints(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	admin, _, _ := store.Create("a", auth.ScopeAdmin, nil, time.Hour)
	edit, _, _ := store.Create("e", auth.ScopeEdit, nil, time.Hour)
	users := &auth.UserStore{Dir: t.TempDir()}
	for _, u := range []auth.User{
		{ID: []byte("u1"), Name: "Ada", Credentials: []webauthn.Credential{{ID: []byte("passkey-one-id")}, {ID: []byte("passkey-two-id")}}},
		{ID: []byte("u2"), Name: "Bob", Credentials: []webauthn.Credential{{ID: []byte("bobs-only-key")}}},
	} {
		if err := users.Add(&u, false); err != nil {
			t.Fatal(err)
		}
	}
	h := Handler(Options{Tokens: store, Accounts: &Accounts{Users: users}})
	do := func(method, path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := do("GET", "/api/v1/users", edit); rec.Code != http.StatusForbidden {
		t.Errorf("edit lists users: %d", rec.Code)
	}
	rec := do("GET", "/api/v1/users", admin)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"Ada"`) || strings.Contains(rec.Body.String(), "publicKey") {
		t.Errorf("list: %d %s", rec.Code, rec.Body)
	}
	one := base64.RawURLEncoding.EncodeToString([]byte("passkey-one-id"))
	if rec := do("DELETE", "/api/v1/users/Ada/passkeys/"+auth.CredentialName([]byte("passkey-one-id")), admin); rec.Code != http.StatusConflict {
		t.Errorf("an ambiguous start: %d", rec.Code)
	}
	if rec := do("DELETE", "/api/v1/users/Ada/passkeys/"+one, admin); rec.Code != http.StatusNoContent {
		t.Errorf("remove a passkey: %d %s", rec.Code, rec.Body)
	}
	bobs := base64.RawURLEncoding.EncodeToString([]byte("bobs-only-key"))
	if rec := do("DELETE", "/api/v1/users/Bob/passkeys/"+bobs, admin); rec.Code != http.StatusConflict {
		t.Errorf("remove the last way in: %d", rec.Code)
	}
	if rec := do("DELETE", "/api/v1/users/Bob", admin); rec.Code != http.StatusNoContent {
		t.Errorf("remove a user: %d", rec.Code)
	}
	if rec := do("DELETE", "/api/v1/users/Bob", admin); rec.Code != http.StatusNotFound {
		t.Errorf("remove a removed user: %d", rec.Code)
	}
	if all, _ := users.List(); len(all) != 1 || len(all[0].Credentials) != 1 {
		t.Errorf("left: %+v", all)
	}
}

func TestEgressAllowEndpoint(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	apply, _, _ := store.Create("a", auth.ScopeApply, nil, time.Hour)
	edit, _, _ := store.Create("e", auth.ScopeEdit, nil, time.Hour)
	var got []string
	h := Handler(Options{Tokens: store, EgressAllow: func(_ context.Context, _ string, add []string, _ string) (EgressAllowResult, error) {
		got = add
		return EgressAllowResult{Commit: "c1", Added: add}, nil
	}})
	do := func(token, body string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/stacks/web/egress/allow", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	for body, want := range map[string]int{
		`{"add":["api.example.org","2001:db8::/32"]}`: 200,
		`{"add":["bad host"]}`:                        422,
		`{"add":["x: y"]}`:                            422,
		`{"add":[]}`:                                  400,
	} {
		if code := do(edit, body); code != want {
			t.Errorf("%s: %d, want %d", body, code, want)
		}
	}
	if do(apply, `{"add":["a.org"]}`) != http.StatusForbidden {
		t.Error("an apply token edits")
	}
	if !slices.Equal(got, []string{"api.example.org", "2001:db8::/32"}) {
		t.Errorf("got %v", got)
	}
}

func TestRenderEndpoint(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	onlyWeb, _, _ := store.Create("w", auth.ScopeRead, []string{"web"}, time.Hour)
	h := Handler(Options{Tokens: store, Render: func(_ context.Context, stack string) ([]RenderedFile, error) {
		if stack == "broken" {
			return nil, InvalidError{Reason: "compose.yaml: bad"}
		}
		return []RenderedFile{{Path: ".env", Content: "PW='••••••'\n"}}, nil
	}})
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+onlyWeb)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := get("/api/v1/stacks/web/render"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"path":".env"`) {
		t.Errorf("web: %d %s", rec.Code, rec.Body)
	}
	if rec := get("/api/v1/stacks/db/render"); rec.Code != http.StatusNotFound {
		t.Errorf("db: %d", rec.Code)
	}
}

func TestUpdatesRunEndpoint(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	read, _, _ := store.Create("r", auth.ScopeRead, nil, time.Hour)
	apply, _, _ := store.Create("a", auth.ScopeApply, nil, time.Hour)
	onlyWeb, _, _ := store.Create("w", auth.ScopeApply, []string{"web"}, time.Hour)
	gotNow := false
	h := Handler(Options{Tokens: store, StartUpdates: func(_ context.Context, now bool, _ string) (func(io.Writer) error, error) {
		gotNow = now
		return func(w io.Writer) error { _, err := io.WriteString(w, "web: updating\n"); return err }, nil
	}})
	do := func(method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := do("POST", "/api/v1/updates/run", read, ""); rec.Code != http.StatusForbidden {
		t.Errorf("read: %d", rec.Code)
	}
	if rec := do("POST", "/api/v1/updates/run", onlyWeb, ""); rec.Code != http.StatusForbidden {
		t.Errorf("limited: %d", rec.Code)
	}
	if rec := do("POST", "/api/v1/updates/run", apply, `{"x":1}`); rec.Code != http.StatusBadRequest {
		t.Errorf("odd body: %d", rec.Code)
	}
	rec := do("POST", "/api/v1/updates/run", apply, `{"now":true}`)
	var job Job
	if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &job) != nil || job.Kind != "updates" || !gotNow {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	if rec := do("POST", "/api/v1/updates/run", apply, ""); rec.Code != http.StatusAccepted {
		t.Errorf("no body: %d", rec.Code)
	}
	if rec := do("GET", "/api/v1/jobs/"+job.ID, onlyWeb, ""); rec.Code != http.StatusNotFound {
		t.Errorf("a limited token reads the fleet's job: %d", rec.Code)
	}
	if rec := do("GET", "/api/v1/jobs/"+job.ID, read, ""); rec.Code != 200 {
		t.Errorf("read: %d", rec.Code)
	}
}
