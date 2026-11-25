// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// MailSettings are fleet.yaml's spec.mail: the SMTP server stacks get as
// ${NEPFLEET_SMTP_*}. The password is write-only: only whether it is set.
type MailSettings struct {
	// Configured: fleet.yaml has spec.mail; the other fields are empty
	// otherwise.
	Configured  bool   `json:"configured"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	User        string `json:"user"`
	From        string `json:"from"`
	TLS         string `json:"tls"`
	PasswordSet bool   `json:"passwordSet"`
}

// MailUpdate is what PUT /api/v1/mail takes. Password: absent (null) keeps
// the stored one, "" removes it, anything else replaces it.
type MailUpdate struct {
	Host     string  `json:"host"`
	Port     int     `json:"port,omitempty"`
	User     string  `json:"user,omitempty"`
	From     string  `json:"from,omitempty"`
	TLS      string  `json:"tls,omitempty"`
	Password *string `json:"password,omitempty"`
}

// MailTest is what POST /api/v1/mail/test takes: where the test goes.
type MailTest struct {
	To string `json:"to"`
}

// ErrNoMail means fleet.yaml has no mail settings.
var ErrNoMail = errors.New("no mail settings in fleet.yaml")

// mailTests bounds test mails: each goes to an address the client names.
type mailTests struct {
	mu     sync.Mutex
	window time.Duration
	max    int
	sent   []time.Time
	now    func() time.Time
}

func newMailTests() *mailTests {
	return &mailTests{window: 10 * time.Minute, max: 5, now: time.Now}
}

// allow records a test and reports whether it is within the limit.
func (m *mailTests) allow() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := m.now().Add(-m.window)
	kept := m.sent[:0]
	for _, t := range m.sent {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	m.sent = kept
	if len(m.sent) >= m.max {
		return false
	}
	m.sent = append(m.sent, m.now())
	return true
}

// refund takes back the newest attempt: one that sent nothing (no settings,
// or settings that can't send) doesn't count.
func (m *mailTests) refund() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) > 0 {
		m.sent = m.sent[:len(m.sent)-1]
	}
}

// envUnsafe are what a .env value can't hold (render.dotenv).
func envUnsafe(s string) bool {
	return strings.ContainsAny(s, "'\r\n") || strings.HasSuffix(s, `\`)
}

func registerMail(mux *http.ServeMux, o Options, l *limiter) {
	tests := newMailTests()
	verified := func(w http.ResponseWriter, r *http.Request) bool {
		t, _ := TokenFrom(r.Context())
		if t.ID == "session" && !o.Accounts.recentlyVerified(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"verify":true,"message":"confirm it's you with a passkey, then save again"}` + "\n"))
			return false
		}
		return true
	}
	// Every stack may use the password: admin, as fleet-wide secrets.
	mux.HandleFunc("GET /api/v1/mail", requireAuth(o, l, auth.ScopeAdmin, func(w http.ResponseWriter, r *http.Request) {
		if o.Mail == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		m, err := o.Mail(r.Context())
		if writeErr(w, err) {
			return
		}
		writeJSON(w, m)
	}))
	mux.HandleFunc("PUT /api/v1/mail", requireAuth(o, l, auth.ScopeAdmin, func(w http.ResponseWriter, r *http.Request) {
		if o.SetMail == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		if !verified(w, r) {
			return
		}
		var req MailUpdate
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || req.Host == "" {
			http.Error(w, `send {"host": "…", "port": 587, "user": "…", "password": "…", "from": "…", "tls": "starttls|tls|none"}`, http.StatusBadRequest)
			return
		}
		if req.Password != nil && (envUnsafe(*req.Password) || len(*req.Password) > 1024) {
			http.Error(w, "the password can't contain a single quote or a line break, end with a backslash, or be longer than 1024 bytes", http.StatusBadRequest)
			return
		}
		for _, v := range []string{req.Host, req.User, req.From, req.TLS} {
			if envUnsafe(v) {
				http.Error(w, "host, user, from and tls can't contain a single quote or a line break", http.StatusBadRequest)
				return
			}
		}
		t, _ := TokenFrom(r.Context())
		commit, err := o.SetMail(r.Context(), req, t.Name)
		if mailErr(w, err) {
			return
		}
		// Never the password.
		logf("api: %q set the mail settings in %s (token %s)", t.Name, commit, t.ID)
		writeJSON(w, SecretResult{Commit: commit})
	}))
	mux.HandleFunc("DELETE /api/v1/mail", requireAuth(o, l, auth.ScopeAdmin, func(w http.ResponseWriter, r *http.Request) {
		if o.RemoveMail == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		if !verified(w, r) {
			return
		}
		t, _ := TokenFrom(r.Context())
		commit, err := o.RemoveMail(r.Context(), t.Name)
		if mailErr(w, err) {
			return
		}
		logf("api: %q removed the mail settings in %s (token %s)", t.Name, commit, t.ID)
		writeJSON(w, SecretResult{Commit: commit})
	}))
	mux.HandleFunc("POST /api/v1/mail/test", requireAuth(o, l, auth.ScopeAdmin, func(w http.ResponseWriter, r *http.Request) {
		if o.TestMail == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		var req MailTest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || strings.ContainsAny(req.To, "\r\n") {
			http.Error(w, `send {"to": "<mail address>"}`, http.StatusBadRequest)
			return
		}
		if _, err := mail.ParseAddress(req.To); err != nil {
			http.Error(w, "to is not a mail address", http.StatusBadRequest)
			return
		}
		if !tests.allow() {
			w.Header().Set("Retry-After", "600")
			http.Error(w, "at most 5 test mails in 10 minutes; try again later", http.StatusTooManyRequests)
			return
		}
		t, _ := TokenFrom(r.Context())
		err := o.TestMail(r.Context(), req.To)
		var invalid InvalidError
		switch {
		case errors.Is(err, ErrNoMail):
			tests.refund()
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		case errors.As(err, &invalid):
			tests.refund()
			http.Error(w, invalid.Reason, http.StatusBadRequest)
			return
		case errors.Is(err, ErrBusy):
			tests.refund()
			writeErr(w, err)
			return
		case err != nil:
			// The mailer keeps the password out of its errors.
			logf("api: %q: test mail failed: %s", t.Name, err)
			http.Error(w, "sending failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		logf("api: %q sent a test mail (token %s)", t.Name, t.ID)
		w.WriteHeader(http.StatusNoContent)
	}))
}

// mailErr answers a mail settings change's error.
func mailErr(w http.ResponseWriter, err error) bool {
	var invalid InvalidError
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrNoMail):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, ErrBusy), errors.Is(err, ErrUncommitted):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.As(err, &invalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		logf("api: mail settings: %v", err)
		http.Error(w, "the mail settings couldn't be saved; see the server log", http.StatusInternalServerError)
	}
	return true
}
