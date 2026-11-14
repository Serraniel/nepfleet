// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"net/http"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// TemplateMatches says which catalogue templates run a plain stack's images,
// as nepfleet template match: the first step to adopting one.
type TemplateMatches struct {
	// Template is set when the stack uses one already (source/name).
	Template string          `json:"template,omitempty"`
	Matches  []TemplateMatch `json:"matches"`
}

// TemplateMatch is one template and how its images compare.
type TemplateMatch struct {
	Source  string `json:"source"`
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
	// Same are images both run, with each side's tag.
	Same         []MatchedImage `json:"same"`
	OnlyStack    []string       `json:"onlyStack"`
	OnlyTemplate []string       `json:"onlyTemplate"`
}

// MatchedImage is an image a stack and a template both run.
type MatchedImage struct {
	Image    string `json:"image"`
	Stack    string `json:"stack"`
	Template string `json:"template"`
}

func registerMatch(mux *http.ServeMux, o Options, l *limiter) {
	mux.HandleFunc("GET /api/v1/stacks/{name}/match", requireAuth(o, l, auth.ScopeRead, forStack(auth.ScopeRead, func(w http.ResponseWriter, r *http.Request, name string) {
		if o.Match == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		source := r.URL.Query().Get("source")
		if source == "" {
			source = "official"
		}
		if !stackName.MatchString(source) {
			http.Error(w, "not a template source name", http.StatusBadRequest)
			return
		}
		m, err := o.Match(r.Context(), name, source)
		var invalid InvalidError
		if errors.As(err, &invalid) {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		if writeErr(w, err) {
			return
		}
		if m.Matches == nil {
			m.Matches = []TemplateMatch{}
		}
		writeJSON(w, m)
	})))
}
