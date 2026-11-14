// SPDX-License-Identifier: AGPL-3.0-or-later

package control

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/remote"
)

func TestKeys(t *testing.T) {
	wrongPause = 0
	var got []string
	locked := true
	s := &Server{Dir: t.TempDir(), Probe: func(_ context.Context, tg remote.Target) (*remote.Report, error) {
		if locked {
			return nil, &remote.LockedKeyError{Target: tg, Keys: []string{"/home/me/.ssh/id_rsa"}}
		}
		return &remote.Report{Kernel: "Linux", Target: tg.String()}, nil
	}}
	s.Keys = Keys{Askpass: "/usr/bin/nepfleet", Unlock: func(_ *http.Request, path, passphrase, _ string) error {
		got = append(got, path)
		if passphrase != "right" {
			return remote.ErrWrongPassphrase
		}
		locked = false
		return nil
	}}
	h := s.Handler()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	// Adding names the locked key, so a client can ask for its passphrase.
	rec := do(http.MethodPost, "/v1/remotes", `{"address":"ssh://me@vps","name":"vps"}`)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), `"lockedKeys":["/home/me/.ssh/id_rsa"]`) {
		t.Fatalf("add with a locked key: %d %s", rec.Code, rec.Body)
	}
	if rec := do(http.MethodPost, "/v1/keys/unlock", `{"path":"/home/me/.ssh/id_rsa","passphrase":"wrong"}`); rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "wrong") && !strings.Contains(rec.Body.String(), "doesn't unlock") {
		t.Errorf("wrong passphrase: %d %s", rec.Code, rec.Body)
	}
	if rec := do(http.MethodPost, "/v1/keys/unlock", `{"path":"/home/me/.ssh/id_rsa"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("no passphrase: %d", rec.Code)
	}
	if rec := do(http.MethodPost, "/v1/keys/unlock", `{"path":"/home/me/.ssh/id_rsa","passphrase":"right"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("unlock: %d %s", rec.Code, rec.Body)
	}
	if rec := do(http.MethodPost, "/v1/remotes", `{"address":"ssh://me@vps","name":"vps"}`); rec.Code != http.StatusCreated {
		t.Errorf("add after unlocking: %d %s", rec.Code, rec.Body)
	}
	if len(got) != 2 {
		t.Errorf("unlock calls: %v", got)
	}
	if keys := lockedKeys(errors.New("other")); keys != nil {
		t.Errorf("an unrelated error has no locked keys: %v", keys)
	}
}
