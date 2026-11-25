// SPDX-License-Identifier: AGPL-3.0-or-later

// Package signontest is a fake Pocket ID admin API for tests: the client
// and secret endpoints NepFleet uses, kept in memory.
package signontest

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// Secret is a client secret as Pocket ID keeps it: a hash and a prefix.
type Secret struct {
	ID     string `json:"id"`
	Prefix string `json:"prefix"`
	Hash   string `json:"-"`
	Active bool   `json:"isActive"`
}

// PocketID is the fake server.
type PocketID struct {
	*httptest.Server
	APIKey string
	// AutoSecret: Pocket ID's "create a client secret automatically".
	AutoSecret bool

	mu sync.Mutex
	// Clients are the stored clients as JSON objects, by ID.
	Clients map[string]map[string]any
	Secrets map[string][]Secret
	// Requests counts calls by "METHOD path".
	Requests map[string]int
	n        int
}

// New starts a fake Pocket ID that accepts apiKey.
func New(apiKey string) *PocketID {
	p := &PocketID{APIKey: apiKey, Clients: map[string]map[string]any{}, Secrets: map[string][]Secret{}, Requests: map[string]int{}}
	p.Server = httptest.NewServer(http.HandlerFunc(p.serve))
	return p
}

// Knows reports whether the client has secret as an active secret.
func (p *PocketID) Knows(client, secret string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.Secrets[client] {
		if s.Active && s.Hash == hash(secret) {
			return true
		}
	}
	return false
}

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h)
}

func (p *PocketID) newSecret(client, value string) Secret {
	p.n++
	s := Secret{ID: fmt.Sprintf("s%d", p.n), Hash: hash(value), Active: true}
	if len(value) > 4 {
		s.Prefix = value[:4]
	}
	p.Secrets[client] = append(p.Secrets[client], s)
	return s
}

func (p *PocketID) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Requests[r.Method+" "+r.URL.Path]++
	if r.Header.Get("X-API-Key") != p.APIKey {
		fail(w, http.StatusUnauthorized, "invalid API key")
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/oidc/clients")
	if !ok {
		fail(w, http.StatusNotFound, "no route")
		return
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	var body map[string]any
	if r.Body != nil && (r.Method == http.MethodPost || r.Method == http.MethodPut) {
		if r.Header.Get("Content-Type") != "application/json" {
			fail(w, http.StatusBadRequest, "request Content-Type is not JSON")
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	switch {
	case rest == "" && r.Method == http.MethodPost:
		id, _ := body["id"].(string)
		if _, dup := p.Clients[id]; dup || id == "" {
			fail(w, http.StatusBadRequest, "client ID already in use")
			return
		}
		if _, ok := body["name"].(string); !ok {
			fail(w, http.StatusBadRequest, "name is required")
			return
		}
		p.Clients[id] = body
		out := map[string]any{}
		for k, v := range body {
			out[k] = v
		}
		if p.AutoSecret {
			s := p.newSecret(id, fmt.Sprintf("auto-generated-%d", p.n))
			out["createdSecret"] = map[string]any{"id": s.ID, "prefix": s.Prefix, "isActive": true, "secret": "auto"}
		}
		reply(w, http.StatusCreated, out)
	case len(parts) == 1 && r.Method == http.MethodGet:
		c, ok := p.Clients[parts[0]]
		if !ok {
			fail(w, http.StatusNotFound, "OIDC client not found")
			return
		}
		reply(w, http.StatusOK, c)
	case len(parts) == 1 && r.Method == http.MethodPut:
		if _, ok := p.Clients[parts[0]]; !ok {
			fail(w, http.StatusNotFound, "OIDC client not found")
			return
		}
		body["id"] = parts[0]
		p.Clients[parts[0]] = body
		reply(w, http.StatusOK, body)
	case len(parts) == 2 && parts[1] == "secrets" && r.Method == http.MethodGet:
		if _, ok := p.Clients[parts[0]]; !ok {
			fail(w, http.StatusNotFound, "OIDC client not found")
			return
		}
		reply(w, http.StatusOK, append([]Secret{}, p.Secrets[parts[0]]...))
	case len(parts) == 2 && parts[1] == "secrets" && r.Method == http.MethodPost:
		value, _ := body["secret"].(string)
		if len(value) < 16 {
			fail(w, http.StatusBadRequest, "secret too short")
			return
		}
		reply(w, http.StatusCreated, p.newSecret(parts[0], value))
	case len(parts) == 3 && parts[1] == "secrets" && r.Method == http.MethodDelete:
		list := p.Secrets[parts[0]]
		for i, s := range list {
			if s.ID == parts[2] {
				p.Secrets[parts[0]] = append(list[:i], list[i+1:]...)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		fail(w, http.StatusNotFound, "Client secret not found")
	default:
		fail(w, http.StatusNotFound, "no route")
	}
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	reply(w, status, map[string]string{"error": msg})
}
