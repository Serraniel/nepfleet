// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// SyncStatus is where the fleet repository stands against its origin.
type SyncStatus struct {
	// Enabled: the server pulls by itself (serve --sync), or on webhooks.
	Enabled bool `json:"enabled"`
	// Interval between pulls; empty when only webhooks trigger them.
	Interval string `json:"interval,omitempty"`
	// Last is when it last looked, Commit what it is at now.
	Last   time.Time `json:"last,omitzero"`
	Commit string    `json:"commit,omitempty"`
	// Error is why the last sync failed (diverged, uncommitted changes, …).
	Error string `json:"error,omitempty"`
	// Applied are stacks with apply: auto that the last pulled change applied;
	// Failed those whose apply failed; Pending the changed ones waiting for a
	// person (apply: manual, or a destructive change).
	Applied []string `json:"applied,omitempty"`
	Failed  []string `json:"failed,omitempty"`
	Pending []string `json:"pending,omitempty"`
}

// maxWebhookBody bounds what a forge may send.
const maxWebhookBody = 1 << 20

// webhookSigned says whether a forge signed body with secret: GitHub's
// X-Hub-Signature-256 (sha256=<hex>), or Forgejo's and Gitea's signature
// header (<hex>), both HMAC-SHA256.
func webhookSigned(r *http.Request, body []byte, secret string) bool {
	if secret == "" {
		return false
	}
	var given string
	switch {
	case r.Header.Get("X-Hub-Signature-256") != "":
		given = strings.TrimPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256=")
	case r.Header.Get("X-Forgejo-Signature") != "":
		given = r.Header.Get("X-Forgejo-Signature")
	case r.Header.Get("X-Gitea-Signature") != "":
		given = r.Header.Get("X-Gitea-Signature")
	default:
		return false
	}
	got, err := hex.DecodeString(given)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

func registerSync(mux *http.ServeMux, o Options, l *limiter) {
	mux.HandleFunc("GET /api/v1/sync", requireAuth(o, l, auth.ScopeRead, func(w http.ResponseWriter, _ *http.Request) {
		if o.SyncStatus == nil {
			writeJSON(w, SyncStatus{})
			return
		}
		writeJSON(w, o.SyncStatus())
	}))
	trigger := func(w http.ResponseWriter) {
		if o.SyncNow == nil {
			http.Error(w, "sync is off on this server: start serve with --sync", http.StatusServiceUnavailable)
			return
		}
		// It runs in the background: a forge waits only seconds for an answer.
		o.SyncNow()
		w.WriteHeader(http.StatusAccepted)
	}
	withToken := requireAuth(o, l, auth.ScopeApply, func(w http.ResponseWriter, _ *http.Request) { trigger(w) })
	mux.HandleFunc("POST /api/v1/sync", func(w http.ResponseWriter, r *http.Request) {
		// People and tokens as everywhere; forges by their signature.
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			withToken(w, r)
			return
		}
		addr := clientAddr(r, o.TrustedProxies)
		if l.blocked(addr) {
			http.Error(w, "too many failed attempts", http.StatusTooManyRequests)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
		if err != nil || !webhookSigned(r, body, o.SyncSecret) {
			l.fail(addr)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		trigger(w)
	})
}
