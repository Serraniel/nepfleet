// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

func TestCheckMirrorURL(t *testing.T) {
	for u, want := range map[string]MirrorKind{
		"git@codeberg.example.test:me/fleet.git":    MirrorSSH,
		"ssh://git@git.example.test:2222/fleet.git": MirrorSSH,
		"https://git.example.test/me/fleet.git":     MirrorHTTPS,
		"/srv/backup/fleet.git":                     MirrorLocal,
		"file:///srv/backup/fleet.git":              MirrorLocal,
		"https://bot:s3cr3t@git.example.test/x.git": "",
		"https://tok3n@git.example.test/x.git":      "",
		"ssh://git:pw@git.example.test/x.git":       "",
		"http://git.example.test/x.git":             "",
		"git://git.example.test/x.git":              "",
		"ext::sh -c touch% /tmp/x":                  "",
		"--upload-pack=x":                           "",
		"git@host:a\nb":                             "",
		"relative/path.git":                         "",
		"ssh://-oProxyCommand=x/y":                  "",
		"git@-oProxyCommand=x:y":                    "",
		"file://relative":                           "",
		"":                                          "",
	} {
		got, err := CheckMirrorURL(u)
		if got != want || (want == "") != (err != nil) {
			t.Errorf("%q: %q %v, want %q", u, got, err, want)
		}
	}
}

func TestMirrorSetupEndpoints(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	edit, _, _ := store.Create("e", auth.ScopeEdit, nil, time.Hour)
	admin, _, _ := store.Create("a", auth.ScopeAdmin, nil, time.Hour)
	var got MirrorAdd
	removed := ""
	h := Handler(Options{Tokens: store,
		AddMirror: func(_ context.Context, req MirrorAdd, kind MirrorKind, _ string) (MirrorAdded, error) {
			got = req
			if req.URL == "/srv/taken.git" {
				return MirrorAdded{}, InvalidError{Reason: "that URL is a mirror already"}
			}
			st := MirrorStatus{ID: MirrorID(req.URL), URL: req.URL, Auth: "host"}
			if kind == MirrorSSH {
				st.Auth, st.PublicKey = "deploy-key", "ssh-ed25519 AAAA nepfleet-mirror"
			}
			return MirrorAdded{Mirror: st, Commit: "abc"}, nil
		},
		RemoveMirror: func(_ context.Context, id, _ string) (string, error) {
			if id != MirrorID("/srv/backup.git") {
				return "", ErrNoMirror
			}
			removed = id
			return "def", nil
		},
	})
	do := func(method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := do("POST", "/api/v1/mirror/remotes", edit, `{"url":"/srv/backup.git"}`); rec.Code != http.StatusForbidden {
		t.Errorf("edit scope: %d", rec.Code)
	}
	rec := do("POST", "/api/v1/mirror/remotes", admin, `{"url":"git@git.example.test:me/fleet.git"}`)
	var added MirrorAdded
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &added) != nil || added.Mirror.PublicKey == "" || added.Commit != "abc" {
		t.Errorf("ssh: %d %s", rec.Code, rec.Body)
	}
	for body, want := range map[string]int{
		`{"url":"https://bot:s3cr3t@git.example.test/x.git"}`:     422,
		`{"url":"git@git.example.test:x.git","token":"t"}`:        422,
		`{"url":"https://git.example.test/x.git","user":"a:b"}`:   422,
		`{"url":"https://git.example.test/x.git","token":"a\nb"}`: 422,
		`{"url":"/srv/taken.git"}`:                                422,
		`{"url":"/srv/x.git","extra":1}`:                          400,
	} {
		if rec := do("POST", "/api/v1/mirror/remotes", admin, body); rec.Code != want || strings.Contains(rec.Body.String(), "s3cr3t") {
			t.Errorf("%s: %d %s, want %d", body, rec.Code, rec.Body, want)
		}
	}
	if rec := do("POST", "/api/v1/mirror/remotes", admin, `{"url":"https://git.example.test/x.git","token":"tok"}`); rec.Code != http.StatusCreated || got.Token != "tok" || strings.Contains(rec.Body.String(), "tok\"") {
		t.Errorf("https with a token: %d %s", rec.Code, rec.Body)
	}
	if rec := do("DELETE", "/api/v1/mirror/remotes/"+MirrorID("/srv/backup.git"), admin, ""); rec.Code != 200 || removed == "" {
		t.Errorf("remove: %d %s", rec.Code, rec.Body)
	}
	if rec := do("DELETE", "/api/v1/mirror/remotes/"+MirrorID("/srv/other.git"), admin, ""); rec.Code != http.StatusNotFound {
		t.Errorf("remove unknown: %d", rec.Code)
	}
	if rec := do("DELETE", "/api/v1/mirror/remotes/not-an-id", admin, ""); rec.Code != http.StatusNotFound {
		t.Errorf("remove a bad ID: %d", rec.Code)
	}
}

// A person needs a recent passkey check to add a mirror.
func TestMirrorAddNeedsVerification(t *testing.T) {
	l := newLoginHarness(t)
	called := false
	l.h = Handler(Options{Accounts: l.a, AllowedOrigins: []string{testOrigin}, Tokens: &auth.Store{Dir: t.TempDir()},
		AddMirror: func(context.Context, MirrorAdd, MirrorKind, string) (MirrorAdded, error) {
			called = true
			return MirrorAdded{}, nil
		}})
	session := l.setup()
	now := time.Now()
	l.a.Sessions.Now = func() time.Time { return now.Add(auth.ReverifyWindow + time.Minute) }
	if rec := l.do(http.MethodPost, "/api/v1/mirror/remotes", `{"url":"/srv/x.git"}`, session, testOrigin); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"verify":true`) || called {
		t.Errorf("an old sign-in: %d %s", rec.Code, rec.Body)
	}
}
