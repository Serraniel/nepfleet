// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// FuzzRequests sends odd bodies, names and queries to everything that decodes
// them, with a valid token. Nothing may crash, or start work from a bad request.
func FuzzRequests(f *testing.F) {
	w, err := NewWebAuthn(testRP, []string{testOrigin})
	if err != nil {
		f.Fatal(err)
	}
	tokens := &auth.Store{Dir: f.TempDir()}
	secret, _, err := tokens.Create("fuzz", auth.ScopeAdmin, nil, time.Hour)
	if err != nil {
		f.Fatal(err)
	}
	started := atomic.Int64{}
	start := func(_ context.Context, _, id string, _, _ bool, _ string) (func(io.Writer) error, error) {
		if id == "" {
			f.Error("started with an empty ID")
		}
		started.Add(1)
		return nil, ErrBusy // nothing runs in the background
	}
	h := Handler(Options{
		Tokens: tokens, AllowedOrigins: []string{testOrigin},
		Accounts:   &Accounts{Users: &auth.UserStore{Dir: f.TempDir()}, Sessions: &auth.Sessions{}, WebAuthn: w, SetupToken: setupToken},
		StartApply: start,
		StartRollback: func(ctx context.Context, s, id string, c bool, by string) (func(io.Writer) error, error) {
			return start(ctx, s, id, c, false, by)
		},
		Plan:   func(context.Context, string, bool) (any, error) { return map[string]string{}, nil },
		Stacks: func() ([]StackInfo, error) { return nil, nil },
		History: func(_ context.Context, _ string, n int) ([]HistoryEntry, error) {
			if n < 1 || n > 200 {
				f.Errorf("history of %d commits", n)
			}
			return nil, nil
		},
		Events: func(_ int64, limit int, _ func(string) bool) (any, error) {
			if limit < 1 || limit > 500 {
				f.Errorf("%d events", limit)
			}
			return []string{}, nil
		},
	})
	for _, seed := range []struct{ body, name, query string }{
		{`{"plan":"abc","confirmDestructive":true}`, "web", "n=5"},
		{`{"commit":"abc"}`, "web", "after=-1&limit=99999"},
		{`{"setupToken":"` + setupToken + `","name":"Ada"}`, "../x", "ceremony=x"},
		{`{"name":"Ada","code":"123456"}`, "web", "n=999999999999999999999"},
		{`{"plan":"a","x":1}`, "%00", "ceremony=%00"},
		{`[]`, "", ""},
	} {
		f.Add(seed.body, seed.name, seed.query)
	}
	var addr atomic.Int64
	f.Fuzz(func(t *testing.T, body, name, query string) {
		stack := url.PathEscape(name)
		for _, r := range []struct {
			method, path string
		}{
			{http.MethodPost, "/api/v1/stacks/" + stack + "/apply"},
			{http.MethodPost, "/api/v1/stacks/" + stack + "/rollback"},
			{http.MethodGet, "/api/v1/stacks/" + stack + "/history"},
			{http.MethodGet, "/api/v1/stacks/" + stack + "/plan"},
			{http.MethodGet, "/api/v1/jobs/" + stack},
			{http.MethodGet, "/api/v1/events"},
			{http.MethodPost, "/api/v1/auth/setup/begin"},
			{http.MethodPost, "/api/v1/auth/setup/finish"},
			{http.MethodPost, "/api/v1/auth/login/finish"},
			{http.MethodPost, "/api/v1/auth/totp/login"},
			{http.MethodPost, "/api/v1/auth/totp/finish"},
		} {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Method = r.method
			req.URL = &url.URL{Path: strings.ReplaceAll(r.path, "%2F", "/"), RawQuery: query}
			req.Body = io.NopCloser(strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+secret)
			req.Header.Set("Origin", testOrigin)
			// A new address each time: the lockout would end the run early.
			n := addr.Add(1)
			req.RemoteAddr = fmt.Sprintf("10.%d.%d.%d:1", n>>16&255, n>>8&255, n&255)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code >= 500 && rec.Code != http.StatusServiceUnavailable {
				t.Errorf("%s %s: %d %s", r.method, r.path, rec.Code, rec.Body)
			}
		}
	})
}
