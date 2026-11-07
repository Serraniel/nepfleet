// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

type totpHarness struct {
	*loginHarness
	now     time.Time
	session string
	secret  string
}

func newTOTPHarness(t *testing.T) *totpHarness {
	t.Helper()
	h := &totpHarness{loginHarness: newLoginHarness(t), now: time.Unix(1800000000, 0)}
	h.a.Sessions.Now = func() time.Time { return h.now }
	h.session = h.setup()
	return h
}

func (h *totpHarness) code() string {
	h.t.Helper()
	c, err := auth.TOTPCode(h.secret, h.now)
	if err != nil {
		h.t.Fatal(err)
	}
	return c
}

func (h *totpHarness) enroll() {
	h.t.Helper()
	rec := h.do(http.MethodPost, "/api/v1/auth/totp/begin", "", h.session, testOrigin)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		h.t.Fatalf("begin: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Ceremony string `json:"ceremony"`
		Secret   string `json:"secret"`
		URL      string `json:"url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		h.t.Fatal(err)
	}
	h.secret = out.Secret
	if !strings.Contains(out.URL, out.Secret) {
		h.t.Errorf("url: %s", out.URL)
	}
	path := "/api/v1/auth/totp/finish?ceremony=" + out.Ceremony
	// A typo doesn't cost the secret, and turns nothing on.
	if rec := h.do(http.MethodPost, path, `{"code":"000000"}`, h.session, testOrigin); rec.Code != http.StatusUnauthorized {
		h.t.Errorf("wrong code: %d", rec.Code)
	}
	if users, _ := h.a.Users.List(); users[0].TOTP != nil {
		h.t.Error("turned on by a wrong code")
	}
	if rec := h.do(http.MethodPost, path, `{"code":"`+h.code()+`"}`, h.session, testOrigin); rec.Code != http.StatusNoContent {
		h.t.Fatalf("finish: %d %s", rec.Code, rec.Body)
	}
	// What the confirming code used is spent.
	h.now = h.now.Add(time.Minute)
}

func (h *totpHarness) login(name, code string) int {
	h.t.Helper()
	return h.do(http.MethodPost, "/api/v1/auth/totp/login", `{"name":"`+name+`","code":"`+code+`"}`, "", testOrigin).Code
}

func TestTOTPSignIn(t *testing.T) {
	h := newTOTPHarness(t)
	h.enroll()

	rec := h.do(http.MethodPost, "/api/v1/auth/totp/login", `{"name":"Ada","code":"`+h.code()+`"}`, "", testOrigin)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	session := sessionCookie(t, rec).Value
	if got := h.do(http.MethodGet, "/api/v1/stacks", "", session, ""); got.Code != http.StatusOK {
		t.Errorf("a session from a code: %d", got.Code)
	}
	// A code works once.
	if got := h.login("Ada", h.code()); got != http.StatusUnauthorized {
		t.Errorf("a code used twice: %d", got)
	}
	h.now = h.now.Add(time.Minute)
	// Only for the origins a session may come from.
	if got := h.do(http.MethodPost, "/api/v1/auth/totp/login", `{"name":"Ada","code":"`+h.code()+`"}`, "", "https://evil.example.test"); got.Code != http.StatusForbidden {
		t.Errorf("another origin: %d", got.Code)
	}
}

func TestTOTPStatusStaysPrivate(t *testing.T) {
	h := newTOTPHarness(t)
	h.enroll()
	status := func(cookie string) string {
		return h.do(http.MethodGet, "/api/v1/auth/status", "", cookie, "").Body.String()
	}
	if got := status(h.session); !strings.Contains(got, `"totp":true`) {
		t.Errorf("signed in: %s", got)
	}
	if got := status(""); !strings.Contains(got, `"totp":false`) {
		t.Errorf("signed out, it must not tell: %s", got)
	}
	// Unknown users and users without codes read like a wrong code.
	if got := h.login("Nobody", "123456"); got != http.StatusUnauthorized {
		t.Errorf("unknown: %d", got)
	}
}

func TestTOTPLocksPerUser(t *testing.T) {
	h := newTOTPHarness(t)
	h.enroll()
	// From a different address each time: the address lockout doesn't see these.
	for range totpMaxFails {
		if got := h.login("Ada", "000000"); got != http.StatusUnauthorized {
			t.Fatalf("wrong code: %d", got)
		}
	}
	if got := h.login("Ada", h.code()); got != http.StatusUnauthorized {
		t.Errorf("the right code during a lockout: %d", got)
	}
	// Passkeys don't care.
	id, resp := h.assertion()
	if got := h.do(http.MethodPost, "/api/v1/auth/login/finish?ceremony="+id, resp, "", testOrigin); got.Code != http.StatusNoContent {
		t.Errorf("a passkey during a lockout: %d %s", got.Code, got.Body)
	}
	h.now = h.now.Add(totpLockout + time.Minute)
	if got := h.login("Ada", h.code()); got != http.StatusNoContent {
		t.Errorf("after the lockout: %d", got)
	}
	// Names nobody has don't fill the table.
	for range 20 {
		h.login("Nobody", "000000")
	}
	if _, found := h.a.totpFails["Nobody"]; found {
		t.Error("counted a user that doesn't exist")
	}
}

func TestTOTPEnrolmentNeedsASession(t *testing.T) {
	h := newTOTPHarness(t)
	if rec := h.do(http.MethodPost, "/api/v1/auth/totp/begin", "", "", testOrigin); rec.Code != http.StatusUnauthorized {
		t.Errorf("begin without a session: %d", rec.Code)
	}
	if rec := h.do(http.MethodPost, "/api/v1/auth/totp/remove", "", "", testOrigin); rec.Code != http.StatusUnauthorized {
		t.Errorf("remove without a session: %d", rec.Code)
	}
	// A login ceremony can't finish this.
	id, _ := h.begin("/api/v1/auth/login/begin", "")
	if rec := h.do(http.MethodPost, "/api/v1/auth/totp/finish?ceremony="+id, `{"code":"000000"}`, h.session, testOrigin); rec.Code != http.StatusUnauthorized {
		t.Errorf("another ceremony: %d", rec.Code)
	}
	h.enroll()
	if rec := h.do(http.MethodPost, "/api/v1/auth/totp/remove", "", h.session, testOrigin); rec.Code != http.StatusNoContent {
		t.Errorf("remove: %d", rec.Code)
	}
	if got := h.login("Ada", h.code()); got != http.StatusUnauthorized {
		t.Errorf("turned off: %d", got)
	}
}
