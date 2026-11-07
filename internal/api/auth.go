// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net"
	"net/http"
	"slices"
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
	swept  time.Time
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
	if len(l.fails) >= maxTracked && l.now().Sub(l.swept) >= time.Second {
		l.swept = l.now()
		for a := range l.fails {
			l.recent(a) // drops addresses whose failures are older than the window
		}
	}
	if len(l.fails) >= maxTracked {
		if _, known := l.fails[addr]; !known {
			return // still full: stop tracking new addresses rather than grow
		}
	}
	l.fails[addr] = append(l.recent(addr), l.now())
}

// clientAddr is the address failures are counted for: the connection's
// address, or behind trusted reverse proxies (Caddy, a CDN) the nearest
// address in X-Forwarded-For that is not one of them. Read from the right,
// across all header lines: entries further left come from the client and
// can be forged.
func clientAddr(r *http.Request, trusted []*net.IPNet) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	isTrusted := func(ip net.IP) bool {
		return ip != nil && slices.ContainsFunc(trusted, func(n *net.IPNet) bool { return n.Contains(ip) })
	}
	if !isTrusted(net.ParseIP(host)) {
		return host
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(hops[i]))
		if ip == nil {
			break // garbage: stop at the last trusted address
		}
		host = ip.String()
		if !isTrusted(ip) {
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
		// A valid token always gets in: the lockout slows down guessing
		// without letting others on a shared address lock its owner out.
		authz := r.Header.Get("Authorization")
		scheme, secret, ok := strings.Cut(authz, " ")
		ok = ok && strings.EqualFold(scheme, "Bearer") && store != nil
		var t auth.Token
		var err error
		if ok {
			t, err = store.Check(strings.TrimSpace(secret))
		}
		if !ok || err != nil {
			if l.blocked(addr) {
				w.Header().Set("Retry-After", strconv.Itoa(int(l.window.Seconds())))
				http.Error(w, "too many failed attempts", http.StatusTooManyRequests)
				return
			}
			l.fail(addr)
			if ok {
				w.Header().Set("WWW-Authenticate", `Bearer realm="nepfleet", error="invalid_token"`)
			} else {
				w.Header().Set("WWW-Authenticate", `Bearer realm="nepfleet"`)
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !t.Allows(scope, "") {
			w.Header().Set("WWW-Authenticate", `Bearer realm="nepfleet", error="insufficient_scope"`)
			http.Error(w, "the token's scope does not allow this", http.StatusForbidden)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, t)))
	}
}
