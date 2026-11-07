// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// A token limited to some stacks doesn't learn the names of others from a reference.
func TestStackUsesFollowTheToken(t *testing.T) {
	tokens := &auth.Store{Dir: t.TempDir()}
	limited, _, err := tokens.Create("limited", auth.ScopeRead, []string{"web", "db"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	h := Handler(Options{Tokens: tokens, Stacks: func() ([]StackInfo, error) {
		return []StackInfo{{Name: "web", Uses: []StackRef{
			{Key: "DB_URL", From: "db", Value: "URL"},
			{Key: "SECRET_THING", From: "hidden", Secret: "KEY"},
		}}, {Name: "hidden"}, {Name: "db"}}, nil
	}})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/stacks", nil)
	req.Header.Set("Authorization", "Bearer "+limited)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var got []StackInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(rec.Code, err)
	}
	if len(got) != 2 || len(got[0].Uses) != 1 || got[0].Uses[0].From != "db" {
		t.Errorf("%+v", got)
	}
}
