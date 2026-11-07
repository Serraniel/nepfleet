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

func (l *limiter) fail(addr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[addr] = append(l.recent(addr), l.now())
}

func clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// requireToken lets a request through with a valid bearer token whose scope
// allows it. Unknown, expired and missing tokens get the same 401.
func requireToken(store *auth.Store, l *limiter, scope string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		addr := clientAddr(r)
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
