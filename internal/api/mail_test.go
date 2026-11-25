// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

func TestMailEndpoints(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	edit, _, _ := store.Create("e", auth.ScopeEdit, nil, time.Hour)
	admin, _, _ := store.Create("a", auth.ScopeAdmin, nil, time.Hour)
	var saved []MailUpdate
	var sentTo []string
	removed := 0
	h := Handler(Options{Tokens: store,
		Mail: func(context.Context) (MailSettings, error) {
			return MailSettings{Configured: true, Host: "smtp.example.test", Port: 587, TLS: "starttls", PasswordSet: true}, nil
		},
		SetMail: func(_ context.Context, m MailUpdate, _ string) (string, error) {
			saved = append(saved, m)
			if m.Host == "bad" {
				return "", InvalidError{Reason: "host is wrong"}
			}
			return "c1", nil
		},
		RemoveMail: func(context.Context, string) (string, error) {
			removed++
			if removed > 1 {
				return "", ErrNoMail
			}
			return "c2", nil
		},
		TestMail: func(_ context.Context, to string) error {
			sentTo = append(sentTo, to)
			if to == "fail@example.test" {
				return errors.New("logging in as u: 535 authentication failed")
			}
			return nil
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
		{"GET", "/api/v1/mail", edit, "", 403},
		{"GET", "/api/v1/mail", admin, "", 200},
		{"PUT", "/api/v1/mail", edit, `{"host":"smtp.example.test"}`, 403},
		{"PUT", "/api/v1/mail", admin, `{"host":"smtp.example.test","port":587,"user":"u","password":"s3cret-value","from":"f@example.test","tls":"starttls"}`, 200},
		{"PUT", "/api/v1/mail", admin, `{"host":"smtp.example.test"}`, 200},
		{"PUT", "/api/v1/mail", admin, `{"host":"smtp.example.test","password":""}`, 200},
		{"PUT", "/api/v1/mail", admin, `{"host":"bad"}`, 400},
		{"PUT", "/api/v1/mail", admin, `{}`, 400},
		{"PUT", "/api/v1/mail", admin, `{"host":"a","extra":1}`, 400},
		{"PUT", "/api/v1/mail", admin, `{"host":"a","password":"it's"}`, 400},
		{"PUT", "/api/v1/mail", admin, `{"host":"a","from":"x@example.test\nBcc: y"}`, 400},
		{"DELETE", "/api/v1/mail", edit, "", 403},
		{"DELETE", "/api/v1/mail", admin, "", 200},
		{"DELETE", "/api/v1/mail", admin, "", 404},
		{"POST", "/api/v1/mail/test", edit, `{"to":"admin@example.test"}`, 403},
		{"POST", "/api/v1/mail/test", admin, `{"to":"not an address"}`, 400},
		{"POST", "/api/v1/mail/test", admin, `{"to":"a@example.test\r\nBcc: b@example.test"}`, 400},
		{"POST", "/api/v1/mail/test", admin, `{"to":"admin@example.test"}`, 204},
		{"POST", "/api/v1/mail/test", admin, `{"to":"fail@example.test"}`, 502},
	} {
		rec := do(c.method, c.path, c.token, c.body)
		if rec.Code != c.want {
			t.Errorf("%s %s %s: %d, want %d (%s)", c.method, c.path, c.body, rec.Code, c.want, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "s3cret-value") {
			t.Errorf("%s %s echoed the password", c.method, c.path)
		}
	}
	if rec := do("GET", "/api/v1/mail", admin, ""); !strings.Contains(rec.Body.String(), `"passwordSet":true`) || strings.Contains(rec.Body.String(), `"password"`) {
		t.Errorf("GET: %s", rec.Body)
	}
	// Absent keeps the password, "" removes it, a value replaces it.
	if len(saved) != 4 || saved[0].Password == nil || *saved[0].Password != "s3cret-value" || saved[1].Password != nil || saved[2].Password == nil || *saved[2].Password != "" {
		t.Errorf("saved: %+v", saved)
	}
	// Test mails are limited: 2 sent above, 3 more, then refused.
	for i := range 3 {
		if rec := do("POST", "/api/v1/mail/test", admin, `{"to":"admin@example.test"}`); rec.Code != 204 {
			t.Fatalf("test %d: %d", i, rec.Code)
		}
	}
	if rec := do("POST", "/api/v1/mail/test", admin, `{"to":"admin@example.test"}`); rec.Code != 429 || len(sentTo) != 5 {
		t.Errorf("over the limit: %d, sent %d", rec.Code, len(sentTo))
	}
}

// A test that sent nothing (no settings, settings that can't send) doesn't
// count against the limit.
func TestMailTestRefunds(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	admin, _, _ := store.Create("a", auth.ScopeAdmin, nil, time.Hour)
	var next error
	h := Handler(Options{Tokens: store, TestMail: func(context.Context, string) error { return next }})
	do := func() int {
		req := httptest.NewRequest("POST", "/api/v1/mail/test", strings.NewReader(`{"to":"a@example.test"}`))
		req.Header.Set("Authorization", "Bearer "+admin)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	for range 10 {
		next = ErrNoMail
		if c := do(); c != 404 {
			t.Fatalf("no mail: %d", c)
		}
		next = InvalidError{Reason: "set a sender address (from) in the mail settings"}
		if c := do(); c != 400 {
			t.Fatalf("invalid: %d", c)
		}
	}
	next = nil
	for i := range 5 {
		if c := do(); c != 204 {
			t.Fatalf("test %d after refunds: %d", i, c)
		}
	}
	if c := do(); c != 429 {
		t.Errorf("over the limit: %d", c)
	}
}

func TestMailTestLimit(t *testing.T) {
	m := newMailTests()
	now := time.Now()
	m.now = func() time.Time { return now }
	for range m.max {
		if !m.allow() {
			t.Fatal("refused within the limit")
		}
	}
	if m.allow() {
		t.Error("allowed over the limit")
	}
	now = now.Add(m.window + time.Second)
	if !m.allow() {
		t.Error("still refused after the window")
	}
}
