// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

func TestCORSAllowlist(t *testing.T) {
	h := Handler(Options{AllowedOrigins: []string{"https://ui.example"}})
	for origin, want := range map[string]string{
		"https://ui.example":   "https://ui.example",
		"https://evil.example": "",
		"*":                    "",
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/info", nil)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != want {
			t.Errorf("origin %q: allow-origin = %q, want %q", origin, got, want)
		}
	}
}

func TestInfo(t *testing.T) {
	h := Handler(Options{Info: Info{Name: "home", Version: "dev", APIVersion: "v1"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/info", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("code=%d headers=%v", rec.Code, rec.Header())
	}
}

func TestStacksNeedToken(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	all, _, err := store.Create("all", auth.ScopeRead, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	webOnly, _, _ := store.Create("web", auth.ScopeRead, []string{"web"}, time.Hour)
	h := Handler(Options{Tokens: store, Stacks: func() ([]StackInfo, error) {
		return []StackInfo{{Name: "web"}, {Name: "db"}}, nil
	}})
	get := func(token, addr string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/stacks", nil)
		req.RemoteAddr = addr + ":1234"
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := get("", "10.0.0.1"); rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
		t.Errorf("no token: %d", rec.Code)
	}
	if rec := get(all, "10.0.0.1"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"db"`) {
		t.Errorf("all stacks: %d %s", rec.Code, rec.Body)
	}
	var got []StackInfo
	rec := get(webOnly, "10.0.0.1")
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got) != 1 || got[0].Name != "web" {
		t.Errorf("a token limited to web sees %v (%v)", got, err)
	}
	// Ten wrong tokens from one address lock it out for a while; others are fine.
	for range 10 {
		get("nft_wrong", "10.0.0.9")
	}
	if rec := get("nft_wrong", "10.0.0.9"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("after 10 failures: %d", rec.Code)
	}
	if rec := get(all, "10.0.0.9"); rec.Code != http.StatusOK {
		t.Errorf("a valid token gets in from a locked-out address: %d", rec.Code)
	}
	if rec := get(all, "10.0.0.1"); rec.Code != http.StatusOK {
		t.Errorf("another address: %d", rec.Code)
	}
}

func TestClientAddr(t *testing.T) {
	_, proxy, _ := net.ParseCIDR("172.16.0.0/12")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "172.18.0.5:443"
	req.Header.Set("X-Forwarded-For", "203.0.113.9, 198.51.100.7")
	if got := clientAddr(req, []*net.IPNet{proxy}); got != "198.51.100.7" {
		t.Errorf("behind a trusted proxy: %s", got)
	}
	if got := clientAddr(req, nil); got != "172.18.0.5" {
		t.Errorf("without trusted proxies the header is ignored: %s", got)
	}
	// A client-sent header line before the proxy's own line counts for nothing.
	req.Header.Del("X-Forwarded-For")
	req.Header.Add("X-Forwarded-For", "6.6.6.6")
	req.Header.Add("X-Forwarded-For", "198.51.100.7")
	if got := clientAddr(req, []*net.IPNet{proxy}); got != "198.51.100.7" {
		t.Errorf("several header lines: %s", got)
	}
	// A chain of trusted proxies (CDN → Caddy) is walked to the client.
	req.Header.Set("X-Forwarded-For", "6.6.6.6, 198.51.100.7, 172.20.0.9")
	if got := clientAddr(req, []*net.IPNet{proxy}); got != "198.51.100.7" {
		t.Errorf("proxy chain: %s", got)
	}
	req.RemoteAddr = "203.0.113.50:1"
	if got := clientAddr(req, []*net.IPNet{proxy}); got != "203.0.113.50" {
		t.Errorf("an untrusted sender's header is ignored: %s", got)
	}
}

func TestLimiterBounded(t *testing.T) {
	l := newLimiter()
	for i := range maxTracked + 50 {
		l.fail(fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255))
	}
	if len(l.fails) > maxTracked {
		t.Errorf("tracked %d addresses", len(l.fails))
	}
}

func TestStackEndpoints(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	all, _, err := store.Create("all", auth.ScopeRead, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	webOnly, _, _ := store.Create("web", auth.ScopeRead, []string{"web"}, time.Hour)
	h := Handler(Options{Tokens: store,
		Plan: func(_ context.Context, s string) (any, error) {
			switch s {
			case "web", "db":
				return map[string]string{"stack": s}, nil
			case "broken":
				return nil, errors.New("/srv/secret/path: git failed")
			}
			return nil, ErrNotFound
		},
		History: func(_ context.Context, s string, n int) ([]HistoryEntry, error) {
			return []HistoryEntry{{Commit: "abc", Subject: fmt.Sprintf("%s %d", s, n), Applied: true}}, nil
		},
	})
	get := func(token, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := get(all, "/api/v1/stacks/db/plan"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"db"`) {
		t.Errorf("plan: %d %s", rec.Code, rec.Body)
	}
	if rec := get(webOnly, "/api/v1/stacks/db/plan"); rec.Code != http.StatusNotFound {
		t.Errorf("a token limited to web must not see db: %d", rec.Code)
	}
	if rec := get(all, "/api/v1/stacks/nope/plan"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown stack: %d", rec.Code)
	}
	if rec := get(all, "/api/v1/stacks/broken/plan"); rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "/srv") {
		t.Errorf("errors must not leak internals: %d %s", rec.Code, rec.Body)
	}
	if rec := get(all, "/api/v1/stacks/Bad..Name/plan"); rec.Code != http.StatusNotFound {
		t.Errorf("invalid name: %d", rec.Code)
	}
	if rec := get(webOnly, "/api/v1/stacks/web/history?n=5"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"web 5"`) {
		t.Errorf("history: %d %s", rec.Code, rec.Body)
	}
	if rec := get("", "/api/v1/stacks/web/history"); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", rec.Code)
	}
}

func TestCheckListen(t *testing.T) {
	for _, c := range []struct {
		addr      string
		tls, prox bool
		ok        bool
	}{
		{"127.0.0.1:7480", false, false, true},
		{"[::1]:7480", false, false, true},
		{"localhost:7480", false, false, true},
		{":7480", false, false, false},
		{"0.0.0.0:7480", false, false, false},
		{"192.168.1.2:7480", false, false, false},
		{"nas.local:7480", false, false, false},
		{":7480", true, false, true},
		{":7480", false, true, true},
	} {
		if err := CheckListen(c.addr, c.tls, c.prox); (err == nil) != c.ok {
			t.Errorf("%+v: %v", c, err)
		}
	}
}

func writeCert(t *testing.T, certFile, keyFile, cn string, mod time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	for f, b := range map[string][]byte{
		certFile: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		keyFile:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}),
	} {
		if err := os.WriteFile(f, b, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(f, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCertReloader(t *testing.T) {
	dir := t.TempDir()
	cf, kf := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	now := time.Now()
	writeCert(t, cf, kf, "one", now.Add(-time.Hour))
	r := &CertReloader{CertFile: cf, KeyFile: kf}
	if err := r.Load(); err != nil {
		t.Fatal(err)
	}
	name := func() string {
		t.Helper()
		c, err := r.GetCertificate(nil)
		if err != nil {
			t.Fatal(err)
		}
		x, err := x509.ParseCertificate(c.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		return x.Subject.CommonName
	}
	if got := name(); got != "one" {
		t.Errorf("first: %s", got)
	}
	writeCert(t, cf, kf, "two", now)
	r.checked = time.Time{}
	if got := name(); got != "two" {
		t.Errorf("after renewal: %s", got)
	}
	// A broken renewal keeps the last good pair.
	if err := os.WriteFile(kf, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.checked = time.Time{}
	if got := name(); got != "two" {
		t.Errorf("after a broken renewal: %s", got)
	}
	if err := (&CertReloader{CertFile: filepath.Join(dir, "none"), KeyFile: kf}).Load(); err == nil {
		t.Error("a missing file must fail at start")
	}
}

func TestApplyJobs(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	applier, _, err := store.Create("ci", auth.ScopeApply, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	reader, _, _ := store.Create("ro", auth.ScopeRead, nil, time.Hour)
	dbOnly, _, _ := store.Create("db", auth.ScopeRead, []string{"db"}, time.Hour)
	done := make(chan struct{})
	h := Handler(Options{Tokens: store,
		StartApply: func(_ context.Context, s, plan string, confirm bool, by string) (func(io.Writer) error, error) {
			switch {
			case s != "web":
				return nil, ErrNotFound
			case plan == "old":
				return nil, ErrPlanChanged
			case plan == "destructive" && !confirm:
				return nil, ErrNeedsConfirm
			}
			return func(w io.Writer) error {
				fmt.Fprintf(w, "applied by %s\n", by)
				close(done)
				return nil
			}, nil
		},
	})
	do := func(method, token, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := do(http.MethodPost, reader, "/api/v1/stacks/web/apply", `{"plan":"x"}`); rec.Code != http.StatusForbidden {
		t.Errorf("a read token must not apply: %d", rec.Code)
	}
	for body, want := range map[string]int{
		`{"plan":"old"}`:         http.StatusConflict,
		`{"plan":"destructive"}`: http.StatusConflict,
		`{}`:                     http.StatusBadRequest,
		`{"plan":"x","extra":1}`: http.StatusBadRequest,
		`{"plan":"x","confirmDestructive":"yes"}`:      http.StatusBadRequest,
		`{"plan":"` + strings.Repeat("x", 5000) + `"}`: http.StatusBadRequest,
	} {
		if rec := do(http.MethodPost, applier, "/api/v1/stacks/web/apply", body); rec.Code != want {
			t.Errorf("%.40s: %d, want %d", body, rec.Code, want)
		}
	}
	if rec := do(http.MethodPost, applier, "/api/v1/stacks/nope/apply", `{"plan":"x"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown stack: %d", rec.Code)
	}
	rec := do(http.MethodPost, applier, "/api/v1/stacks/web/apply", `{"plan":"destructive","confirmDestructive":true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body)
	}
	var job Job
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil || job.ID == "" || rec.Header().Get("Location") != "/api/v1/jobs/"+job.ID {
		t.Fatalf("job: %+v %v", job, err)
	}
	<-done
	var got Job
	for range 100 {
		r := do(http.MethodGet, reader, "/api/v1/jobs/"+job.ID, "")
		if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.State != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got.State != "succeeded" || got.Log != "applied by ci\n" || got.By != "ci" {
		t.Errorf("finished job: %+v", got)
	}
	if r := do(http.MethodGet, dbOnly, "/api/v1/jobs/"+job.ID, ""); r.Code != http.StatusNotFound {
		t.Errorf("a token limited to db must not see web's job: %d", r.Code)
	}
}

func TestJobPanicFails(t *testing.T) {
	js := newJobs()
	job := js.start("apply", "web", "ci", func(*jobLog) error { panic("boom") })
	var got Job
	for range 200 {
		got, _ = js.get(job.ID)
		if got.State != "running" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got.State != "failed" || !strings.Contains(got.Error, "boom") {
		t.Errorf("job: %+v", got)
	}
}

func TestCheckOrigin(t *testing.T) {
	for o, ok := range map[string]bool{
		"https://ui.example.org":      true,
		"http://localhost:5173":       true,
		"*":                           false,
		"ui.example.org":              false,
		"https://ui.example.org/":     false,
		"https://ui.example.org/a":    false,
		"https://user@ui.example.org": false,
		"ftp://ui.example.org":        false,
	} {
		if err := CheckOrigin(o); (err == nil) != ok {
			t.Errorf("%s: %v", o, err)
		}
	}
}

func TestTemplates(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	tok, _, err := store.Create("ui", auth.ScopeRead, []string{"web"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	h := Handler(Options{Tokens: store, Templates: func(context.Context) (any, error) {
		return []map[string]string{{"source": "official"}}, nil
	}})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/templates", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "official") {
		t.Errorf("templates: %d %s", rec.Code, rec.Body)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/templates", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("without a token: %d", rec.Code)
	}
}
