// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

type metricsEvent struct {
	Name  string    `json:"name"`
	Stack string    `json:"stack,omitempty"`
	Time  time.Time `json:"time"`
}

func TestMetrics(t *testing.T) {
	tokens := &auth.Store{Dir: t.TempDir()}
	limited, _, err := tokens.Create("limited", auth.ScopeRead, []string{"web"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	all, _, err := tokens.Create("all", auth.ScopeRead, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	early, late := time.Unix(1000, 0), time.Unix(2000, 0)
	events := []metricsEvent{
		{Name: "apply.succeeded", Stack: "web", Time: early},
		{Name: "apply.succeeded", Stack: "web", Time: late},
		{Name: "apply.failed", Stack: "db", Time: late},
		{Name: "host.security_problem", Time: late},
	}
	h := Handler(Options{Tokens: tokens, Info: Info{Name: `fleet "one"`, Version: "1.2.3"},
		Stacks: func() ([]StackInfo, error) { return []StackInfo{{Name: "web"}, {Name: "db"}}, nil },
		Events: func(_ int64, _ int, allow func(string) bool) (any, error) {
			out := []metricsEvent{}
			for _, e := range events {
				if allow(e.Stack) {
					out = append(out, e)
				}
			}
			return out, nil
		}})
	get := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := get(""); rec.Code != http.StatusUnauthorized {
		t.Errorf("without a token: %d", rec.Code)
	}
	body := get(all).Body.String()
	for _, want := range []string{
		`nepfleet_info{name="fleet \"one\"",version="1.2.3"} 1`,
		"nepfleet_stacks 2\n",
		`nepfleet_stack_last_event_timestamp_seconds{stack="web",event="apply.succeeded"} 2000`,
		`nepfleet_stack_last_event_timestamp_seconds{stack="db",event="apply.failed"} 2000`,
		"# TYPE nepfleet_stacks gauge",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("no %q in:\n%s", want, body)
		}
	}
	if strings.Contains(body, "security_problem") {
		t.Errorf("host-wide events have no stack label to go by:\n%s", body)
	}
	body = get(limited).Body.String()
	if strings.Contains(body, `stack="db"`) || !strings.Contains(body, "nepfleet_stacks 1\n") {
		t.Errorf("a limited token sees too much:\n%s", body)
	}
}

func TestMetricsEscapeLabels(t *testing.T) {
	var b strings.Builder
	writeMetrics(&b, Info{Name: "a\nb"}, 0, map[[2]string]time.Time{{`x"y`, `z\w`}: time.Unix(5, 0)})
	if !strings.Contains(b.String(), `stack="x\"y",event="z\\w"} 5`) {
		t.Errorf("%s", b.String())
	}
}
