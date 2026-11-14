// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// Alert is a problem another tool found, such as a log line Dozzle matched. It
// goes to the fleet's notification channels like NepFleet's own events.
type Alert struct {
	// Source names the tool, e.g. dozzle.
	Source string `json:"source,omitempty"`
	// Rule is the tool's name for what matched.
	Rule string `json:"rule,omitempty"`
	// Container is where it happened; its compose project names the stack.
	Container string `json:"container,omitempty"`
	// Host is the Docker host the tool runs on.
	Host string `json:"host,omitempty"`
	// Detail is what matched, e.g. the log line.
	Detail string `json:"detail,omitempty"`
	// Title and Message are what shoutrrr's generic webhook sends
	// (template=json), so tools that notify through shoutrrr URLs can
	// alert here too: they stand for Rule and Detail.
	Title   string `json:"title,omitempty"`
	Message string `json:"message,omitempty"`
}

// Lengths alerts are cut to: they end up in chat messages and on phones.
const (
	alertShort  = 120
	alertDetail = 2000
)

func clip(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' {
			return -1 // no terminal escapes or other control characters
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

func (a *Alert) clean() {
	if a.Rule == "" {
		a.Rule = a.Title
	}
	if a.Detail == "" {
		a.Detail = a.Message
	}
	a.Title, a.Message = "", ""
	a.Source = strings.TrimSpace(clip(strings.ReplaceAll(a.Source, "\n", " "), alertShort))
	a.Rule = strings.TrimSpace(clip(strings.ReplaceAll(a.Rule, "\n", " "), alertShort))
	a.Container = strings.TrimSpace(clip(strings.ReplaceAll(a.Container, "\n", " "), alertShort))
	a.Host = strings.TrimSpace(clip(strings.ReplaceAll(a.Host, "\n", " "), alertShort))
	a.Detail = strings.TrimSpace(clip(a.Detail, alertDetail))
}

// handleAlert takes an alert from a notify token (or admin, or a person).
func handleAlert(o Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if o.Alert == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		var a Alert
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&a); err != nil {
			http.Error(w, `send {"source", "rule", "container", "host", "detail"} (or shoutrrr's {"title", "message"})`, http.StatusBadRequest)
			return
		}
		a.clean()
		// A stack's own token (stack:<stack>/<NAME>) names its source when
		// the tool doesn't.
		t, _ := TokenFrom(r.Context())
		if a.Source == "" {
			if rest, ok := strings.CutPrefix(t.Name, "stack:"); ok {
				a.Source, _, _ = strings.Cut(rest, "/")
			}
		}
		if a.Rule == "" && a.Detail == "" {
			http.Error(w, "an alert needs a rule or a detail", http.StatusBadRequest)
			return
		}
		if writeErr(w, o.Alert(r.Context(), a)) {
			return
		}
		logf("api: alert %q from %s via %s (token %s)", a.Rule, a.Source, t.Name, t.ID)
		w.WriteHeader(http.StatusAccepted)
	}
}

func registerAlerts(mux *http.ServeMux, o Options, l *limiter) {
	mux.HandleFunc("POST /api/v1/alerts", requireAuth(o, l, auth.ScopeNotify, handleAlert(o)))
}
