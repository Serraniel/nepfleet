// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

func TestSignOnEndpoints(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	read, _, _ := store.Create("r", auth.ScopeRead, nil, time.Hour)
	wikiRead, _, _ := store.Create("wr", auth.ScopeRead, []string{"wiki"}, time.Hour)
	edit, _, _ := store.Create("e", auth.ScopeEdit, nil, time.Hour)
	admin, _, _ := store.Create("a", auth.ScopeAdmin, nil, time.Hour)
	var providers []string
	var updates []StackSignOnUpdate
	removed := 0
	h := Handler(Options{Tokens: store,
		SignOn: func(context.Context) (SignOnSettings, error) {
			return SignOnSettings{Provider: "id", Kinds: []string{"pocket-id"}, Candidates: []string{"id"},
				Stacks: []StackSignOnMode{{Name: "wiki", Mode: "integrated"}, {Name: "git", Mode: "custom", Explicit: true}}}, nil
		},
		SetSignOn: func(_ context.Context, p, _ string) (string, error) {
			providers = append(providers, p)
			if p == "wiki" {
				return "", InvalidError{Reason: `wiki runs "wiki", not a provider`}
			}
			return "c1", nil
		},
		RemoveSignOn: func(context.Context, string) (string, error) {
			removed++
			if removed > 1 {
				return "", ErrNoSignOn
			}
			return "c2", nil
		},
		StackSignOn: func(_ context.Context, stack string) (StackSignOn, error) {
			set := true
			return StackSignOn{Supported: true, Mode: "custom", Set: "custom", Issuer: "https://login.example.test", ClientID: stack, SecretSet: &set}, nil
		},
		SetStackSignOn: func(_ context.Context, _ string, u StackSignOnUpdate, _ string) (string, error) {
			updates = append(updates, u)
			if u.Mode == "custom" && u.ClientID == "" {
				return "", InvalidError{Reason: "spec.signOn.clientID is required in mode custom"}
			}
			return "c3", nil
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
		{"GET", "/api/v1/signon", read, "", 200},
		{"PUT", "/api/v1/signon", edit, `{"provider":"id"}`, 403},
		{"PUT", "/api/v1/signon", admin, `{"provider":"id"}`, 200},
		{"PUT", "/api/v1/signon", admin, `{"provider":"wiki"}`, 422},
		{"PUT", "/api/v1/signon", admin, `{"provider":"../x"}`, 400},
		{"PUT", "/api/v1/signon", admin, `{"provider":"id","x":1}`, 400},
		{"DELETE", "/api/v1/signon", edit, "", 403},
		{"DELETE", "/api/v1/signon", admin, "", 200},
		{"DELETE", "/api/v1/signon", admin, "", 404},
		{"GET", "/api/v1/stacks/wiki/signon", read, "", 200},
		{"GET", "/api/v1/stacks/git/signon", wikiRead, "", 404},
		{"PUT", "/api/v1/stacks/wiki/signon", read, `{"mode":"none"}`, 403},
		{"PUT", "/api/v1/stacks/wiki/signon", edit, `{"mode":"none"}`, 200},
		{"PUT", "/api/v1/stacks/wiki/signon", edit, `{"mode":""}`, 200},
		{"PUT", "/api/v1/stacks/wiki/signon", edit, `{"mode":"custom","issuer":"https://login.example.test","clientID":"wiki"}`, 200},
		{"PUT", "/api/v1/stacks/wiki/signon", edit, `{"mode":"custom","issuer":"https://login.example.test"}`, 422},
		{"PUT", "/api/v1/stacks/wiki/signon", edit, `{"mode":"sso"}`, 400},
		{"PUT", "/api/v1/stacks/wiki/signon", edit, `{"mode":"custom","issuer":"https://x'\n"}`, 400},
	} {
		if rec := do(c.method, c.path, c.token, c.body); rec.Code != c.want {
			t.Errorf("%s %s %s: %d, want %d (%s)", c.method, c.path, c.body, rec.Code, c.want, rec.Body)
		}
	}
	if len(providers) != 2 || len(updates) != 4 {
		t.Errorf("calls: %v %v", providers, updates)
	}
	// A token limited to wiki sees only wiki.
	if body := do("GET", "/api/v1/signon", wikiRead, "").Body.String(); strings.Contains(body, `"git"`) || strings.Contains(body, `"candidates":["id"]`) || !strings.Contains(body, `"wiki"`) {
		t.Errorf("limited token: %s", body)
	}
	// Whether the secret is stored is for edit scope.
	if body := do("GET", "/api/v1/stacks/wiki/signon", read, "").Body.String(); strings.Contains(body, "secretSet") {
		t.Errorf("read scope saw secretSet: %s", body)
	}
	if body := do("GET", "/api/v1/stacks/wiki/signon", edit, "").Body.String(); !strings.Contains(body, `"secretSet":true`) {
		t.Errorf("edit scope: %s", body)
	}
}

func TestNewStackSignOnChecked(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	edit, _, _ := store.Create("e", auth.ScopeEdit, nil, time.Hour)
	var got []NewStack
	h := Handler(Options{Tokens: store, NewStack: func(_ context.Context, req NewStack, _ string) (NewStackResult, error) {
		got = append(got, req)
		return NewStackResult{Name: "wiki", Commit: "c"}, nil
	}})
	for body, want := range map[string]int{
		`{"template":"wiki","signOn":{"mode":"custom","issuer":"https://login.example.test","clientID":"wiki","clientSecret":"s3cret-value"}}`: 201,
		`{"template":"wiki","signOn":{"mode":"sso"}}`:                         400,
		`{"template":"wiki","signOn":{"mode":"custom","clientSecret":"a'b"}}`: 400,
	} {
		req := httptest.NewRequest("POST", "/api/v1/stacks", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+edit)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("%s: %d, want %d (%s)", body, rec.Code, want, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "s3cret-value") {
			t.Error("the secret was echoed")
		}
	}
	if len(got) != 1 || got[0].SignOn == nil || got[0].SignOn.ClientSecret != "s3cret-value" {
		t.Errorf("passed on: %+v", got)
	}
}
