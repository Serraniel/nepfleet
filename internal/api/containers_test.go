// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

func TestContainersEndpoints(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	read, _, _ := store.Create("read", auth.ScopeRead, nil, time.Hour)
	apply, _, _ := store.Create("apply", auth.ScopeApply, nil, time.Hour)
	db, _, _ := store.Create("db", auth.ScopeApply, []string{"db"}, time.Hour)
	var did []string
	h := Handler(Options{Tokens: store,
		Containers: func(_ context.Context, stack string) ([]Container, error) {
			if stack == "empty" {
				return nil, nil
			}
			return []Container{{Service: "web", Name: stack + "-web-1", State: "running", Health: "healthy", Image: "nginx:1.29", Restarts: 2}}, nil
		},
		ServiceAction: func(_ context.Context, stack, service, action, _ string) error {
			if stack == "swarm" && action != ServiceRestart {
				return ErrNotSupported
			}
			if service == "nope" {
				return ErrNotFound
			}
			did = append(did, action+" "+stack+"/"+service)
			return nil
		}})
	do := func(token, method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := do(read, http.MethodGet, "/api/v1/stacks/web/containers"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"restarts":2`) {
		t.Errorf("list: %d %s", rec.Code, rec.Body)
	}
	if rec := do(read, http.MethodGet, "/api/v1/stacks/empty/containers"); strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("empty: %s", rec.Body)
	}
	if rec := do(read, http.MethodPost, "/api/v1/stacks/web/services/web/restart"); rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
		t.Errorf("a read token restarted: %d", rec.Code)
	}
	if rec := do(apply, http.MethodPost, "/api/v1/stacks/web/services/web/restart"); rec.Code != http.StatusNoContent {
		t.Errorf("restart: %d %s", rec.Code, rec.Body)
	}
	if rec := do(apply, http.MethodPost, "/api/v1/stacks/web/services/web/kill"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown action: %d", rec.Code)
	}
	if rec := do(apply, http.MethodPost, "/api/v1/stacks/web/services/-x/stop"); rec.Code != http.StatusNotFound {
		t.Errorf("odd service name: %d", rec.Code)
	}
	if rec := do(apply, http.MethodPost, "/api/v1/stacks/web/services/nope/stop"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown service: %d", rec.Code)
	}
	if rec := do(apply, http.MethodPost, "/api/v1/stacks/swarm/services/web/stop"); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("swarm stop: %d", rec.Code)
	}
	if rec := do(db, http.MethodPost, "/api/v1/stacks/web/services/web/stop"); rec.Code != http.StatusNotFound {
		t.Errorf("a token for db stopped web: %d", rec.Code)
	}
	if strings.Join(did, ";") != "restart web/web" {
		t.Errorf("done: %v", did)
	}
}
