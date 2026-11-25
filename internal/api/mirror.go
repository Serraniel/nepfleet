// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// MirrorStatus is how pushing the fleet repository to one of spec.mirrors went.
type MirrorStatus struct {
	// ID names the mirror in the API: the start of its URL's SHA-256.
	ID string `json:"id"`
	// URL without any password or token in it.
	URL   string    `json:"url"`
	OK    time.Time `json:"ok,omitzero"`
	Error string    `json:"error,omitempty"`
	// Auth is how the push signs in: "deploy-key" (a key the app made),
	// "token" (an HTTPS token the app stores) or "host" (whatever git on the
	// host uses: its SSH keys or credential helper).
	Auth string `json:"auth"`
	// PublicKey is the deploy key's public half, to add to the forge.
	PublicKey string `json:"publicKey,omitempty"`
}

// MirrorAdd adds a mirror from the app.
type MirrorAdd struct {
	// URL is ssh://, user@host:path, https:// or a local path (file://).
	URL string `json:"url"`
	// Token is for an https URL, stored encrypted and sent as HTTP basic
	// auth; never in the URL.
	Token string `json:"token,omitempty"`
	// User goes with Token (default "git"; some forges want their own).
	User string `json:"user,omitempty"`
}

// MirrorAdded is the mirror an add made, and its commit.
type MirrorAdded struct {
	Mirror MirrorStatus `json:"mirror"`
	Commit string       `json:"commit"`
}

// ErrNoMirror means spec.mirrors has no mirror of that ID.
var ErrNoMirror = errors.New("no mirror of that ID in fleet.yaml")

// MirrorKind is how a mirror URL is reached.
type MirrorKind string

// The kinds of mirror URL the app may add.
const (
	MirrorSSH   MirrorKind = "ssh"
	MirrorHTTPS MirrorKind = "https"
	MirrorLocal MirrorKind = "local"
)

// MirrorID is a mirror's ID in the API (and in its secrets' names).
func MirrorID(u string) string {
	sum := sha256.Sum256([]byte(u))
	return hex.EncodeToString(sum[:6])
}

var (
	mirrorID = regexp.MustCompile(`^[0-9a-f]{12}$`)
	// user@host:path, as git and scp write SSH URLs.
	scpLike = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9][A-Za-z0-9.-]*:\S+$`)
	// A host ssh can't take for an option.
	hostName = regexp.MustCompile(`^[A-Za-z0-9\[]`)
)

// CheckMirrorURL says what kind of mirror u is, or why the app won't add
// it: a password or token never goes into a URL (fleet.yaml isn't
// encrypted), and plain http or git:// would send the fleet unprotected.
func CheckMirrorURL(u string) (MirrorKind, error) {
	switch {
	case u == "" || len(u) > 500:
		return "", errors.New("a mirror URL is 1 to 500 characters")
	case strings.HasPrefix(u, "-") || strings.ContainsFunc(u, func(r rune) bool { return r <= ' ' || r == 0x7f }):
		return "", errors.New("a mirror URL has no spaces or control characters and doesn't start with -")
	case filepath.IsAbs(u):
		return MirrorLocal, nil
	case scpLike.MatchString(u) && !strings.Contains(u, "://"):
		return MirrorSSH, nil
	}
	p, err := url.Parse(u)
	if err != nil || p.Host == "" && p.Scheme != "file" || p.Host != "" && !hostName.MatchString(p.Host) {
		return "", errors.New("not a git URL: use ssh://, user@host:path, https:// or a local path")
	}
	if _, hasPassword := p.User.Password(); hasPassword || p.User != nil && p.Scheme == "https" {
		return "", errors.New("no user name, password or token in the URL: it would be stored in plain text; send the token separately")
	}
	switch p.Scheme {
	case "ssh":
		return MirrorSSH, nil
	case "https":
		return MirrorHTTPS, nil
	case "file":
		if !filepath.IsAbs(p.Path) {
			return "", errors.New("a file:// URL needs an absolute path")
		}
		return MirrorLocal, nil
	default:
		return "", errors.New("use ssh://, user@host:path, https:// or a local path: " + p.Scheme + ":// isn't encrypted or isn't supported")
	}
}

// RedactURL replaces a URL's user part (a user, password or token) with ***.
func RedactURL(s string) string {
	if u, err := url.Parse(s); err == nil && u.User != nil {
		u.User = url.User("***")
		return u.String()
	}
	return s
}

func registerMirror(mux *http.ServeMux, o Options, l *limiter) {
	mux.HandleFunc("GET /api/v1/mirror", requireAuth(o, l, auth.ScopeAdmin, func(w http.ResponseWriter, _ *http.Request) {
		if o.Mirror == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		st, err := o.Mirror(context.Background(), false)
		if writeErr(w, err) {
			return
		}
		writeJSON(w, redactMirrors(st))
	}))
	mux.HandleFunc("POST /api/v1/mirror", requireAuth(o, l, auth.ScopeAdmin, func(w http.ResponseWriter, r *http.Request) {
		if o.Mirror == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		st, err := o.Mirror(ctx, true)
		if writeErr(w, err) {
			return
		}
		writeJSON(w, redactMirrors(st))
	}))
	// Adding or removing a mirror decides where the whole fleet repository
	// goes: worth a passkey, as changing access is.
	mux.HandleFunc("POST /api/v1/mirror/remotes", requireAuth(o, l, auth.ScopeAdmin, func(w http.ResponseWriter, r *http.Request) {
		if o.AddMirror == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		if !mayChangeAccess(w, r, o) {
			return
		}
		var req MirrorAdd
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			http.Error(w, `send {"url": "…", "token": "…"} (the token only for https)`, http.StatusBadRequest)
			return
		}
		kind, err := CheckMirrorURL(req.URL)
		switch {
		case err != nil:
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		case (req.Token != "" || req.User != "") && kind != MirrorHTTPS:
			http.Error(w, "a token is only for https URLs; an SSH mirror gets a deploy key", http.StatusUnprocessableEntity)
			return
		case len(req.Token) > 4096 || strings.ContainsFunc(req.Token+req.User, func(r rune) bool { return r < ' ' || r == 0x7f }) || strings.Contains(req.User, ":"):
			http.Error(w, "the token or user has characters a forge wouldn't give out", http.StatusUnprocessableEntity)
			return
		}
		t, _ := TokenFrom(r.Context())
		added, err := o.AddMirror(r.Context(), req, kind, t.Name)
		if mirrorErr(w, err) {
			return
		}
		logf("api: %s added mirror %s (token %s)", t.Name, added.Mirror.ID, t.ID)
		added.Mirror = redactMirrors([]MirrorStatus{added.Mirror})[0]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(added)
	}))
	mux.HandleFunc("DELETE /api/v1/mirror/remotes/{id}", requireAuth(o, l, auth.ScopeAdmin, func(w http.ResponseWriter, r *http.Request) {
		if o.RemoveMirror == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		id := r.PathValue("id")
		if !mirrorID.MatchString(id) {
			http.Error(w, ErrNoMirror.Error(), http.StatusNotFound)
			return
		}
		if !mayChangeAccess(w, r, o) {
			return
		}
		t, _ := TokenFrom(r.Context())
		commit, err := o.RemoveMirror(r.Context(), id, t.Name)
		if mirrorErr(w, err) {
			return
		}
		logf("api: %s removed mirror %s (token %s)", t.Name, id, t.ID)
		writeJSON(w, map[string]string{"commit": commit})
	}))
}

// mirrorErr answers a mirror change's error; reasons never carry a URL.
func mirrorErr(w http.ResponseWriter, err error) bool {
	var invalid InvalidError
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrNoMirror):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, ErrBusy), errors.Is(err, ErrUncommitted):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.As(err, &invalid):
		http.Error(w, redactURLs(err.Error()), http.StatusUnprocessableEntity)
	default:
		logf("api: mirror: %s", redactURLs(err.Error()))
		http.Error(w, "the mirror couldn't be saved; see the server log", http.StatusInternalServerError)
	}
	return true
}

func redactMirrors(st []MirrorStatus) []MirrorStatus {
	out := make([]MirrorStatus, 0, len(st))
	for _, m := range st {
		if m.ID == "" {
			m.ID = MirrorID(m.URL)
		}
		m.URL, m.Error = RedactURL(m.URL), redactURLs(m.Error)
		out = append(out, m)
	}
	return out
}
