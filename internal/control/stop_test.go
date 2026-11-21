// SPDX-License-Identifier: AGPL-3.0-or-later

package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Serraniel/nepfleet/internal/remote"
)

func TestStop(t *testing.T) {
	s := &Server{Dir: t.TempDir()}
	h := s.Handler()
	do := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader("")))
		return rec
	}
	if rec := do(http.MethodPost, "/v1/stop"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("without Stop: %d %s", rec.Code, rec.Body)
	}

	stopped := make(chan struct{})
	s.Stop = func() { close(stopped) }

	// A running setup must not be cut off.
	s.Setup.jobs = map[string]*setupJob{"vps": {job: SetupJob{Kind: "update", State: "running"}}}
	if rec := do(http.MethodPost, "/v1/stop"); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "vps") {
		t.Fatalf("while a setup runs: %d %s", rec.Code, rec.Body)
	}
	s.Setup.jobs["vps"].job.State = "succeeded"
	s.Setup.starting = map[string]bool{"nas": true}
	if rec := do(http.MethodPost, "/v1/stop"); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "nas") {
		t.Fatalf("while a setup starts: %d %s", rec.Code, rec.Body)
	}
	delete(s.Setup.starting, "nas")

	if rec := do(http.MethodPost, "/v1/stop"); rec.Code != http.StatusAccepted {
		t.Fatalf("stop: %d %s", rec.Code, rec.Body)
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop was not called")
	}
	// No setup starts once stopping.
	s.Setup.Run = func(context.Context, remote.Target, string) ([]byte, error) { return nil, nil }
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/remotes/vps/setup", strings.NewReader(`{"kind":"update","plan":"x"}`)))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "stopping") {
		t.Fatalf("setup after stop: %d %s", rec.Code, rec.Body)
	}
}
