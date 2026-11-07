// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

type ctxKey struct{}

// TokenFrom returns the token a request was authenticated with.
func TokenFrom(ctx context.Context) (auth.Token, bool) {
	t, ok := ctx.Value(ctxKey{}).(auth.Token)
	return t, ok
}

// limiter counts failed authentications per client address and refuses an
// address for a while after too many (brute force, scanners).
type limiter struct {
	mu     sync.Mutex
	window time.Duration
	max    int
	fails  map[string][]time.Time
	now    func() time.Time
}

func newLimiter() *limiter {
	return &limiter{window: time.Minute, max: 10, fails: map[string][]time.Time{}, now: time.Now}
}

func (l *limiter) recent(addr string) []time.Time {
	cut := l.now().Add(-l.window)
	kept := l.fails[addr][:0]
	for _, t := range l.fails[addr] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.fails, addr)
		return nil
	}
	l.fails[addr] = kept
	return kept
}

func (l *limiter) blocked(addr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(addr)) >= l.max
}

// maxTracked bounds the limiter's memory: many addresses failing at once
// (a scan) must not grow it without end.
const maxTracked = 10000

func (l *limiter) fail(addr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.fails) >= maxTracked {
		for a := range l.fails {
			l.recent(a) // drops addresses whose failures are older than the window
		}
		if len(l.fails) >= maxTracked {
			return // still full: stop tracking new addresses rather than grow
		}
	}
	l.fails[addr] = append(l.recent(addr), l.now())
}

// clientAddr is the address failures are counted for. Behind a trusted
// reverse proxy (Caddy) it is the client the proxy names last in
// X-Forwarded-For; otherwise the connection's address, as headers from
// anyone else can be forged.
func clientAddr(r *http.Request, trusted []*net.IPNet) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	for _, n := range trusted {
		if ip != nil && n.Contains(ip) {
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				parts := strings.Split(xff, ",")
				if c := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(c) != nil {
					return c
				}
			}
			break
		}
	}
	return host
}

// requireToken lets a request through with a valid bearer token whose scope
// allows it. Unknown, expired and missing tokens get the same 401.
func requireToken(store *auth.Store, l *limiter, trusted []*net.IPNet, scope string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		addr := clientAddr(r, trusted)
		if l.blocked(addr) {
			w.Header().Set("Retry-After", strconv.Itoa(int(l.window.Seconds())))
			http.Error(w, "too many failed attempts", http.StatusTooManyRequests)
			return
		}
		secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || store == nil {
			l.fail(addr)
			w.Header().Set("WWW-Authenticate", `Bearer realm="nepfleet"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		t, err := store.Check(strings.TrimSpace(secret))
		if err != nil {
			l.fail(addr)
			w.Header().Set("WWW-Authenticate", `Bearer realm="nepfleet", error="invalid_token"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !t.Allows(scope, "") {
			http.Error(w, "the token's scope does not allow this", http.StatusForbidden)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, t)))
	}
}
