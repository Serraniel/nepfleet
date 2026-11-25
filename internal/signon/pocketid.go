// SPDX-License-Identifier: AGPL-3.0-or-later

package signon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PocketID manages clients through Pocket ID's admin API (2.19 or newer:
// client IDs and secrets chosen by the caller), with its static API key.
// Endpoints as in pocket-id's backend/internal/controller/oidc_controller.go.
type PocketID struct {
	// URL is Pocket ID's APP_URL, which is also its issuer.
	URL    string
	APIKey string
	// HTTP is the client to use; nil means one with a 20 second timeout.
	HTTP *http.Client
}

// Kind implements Provider.
func (p *PocketID) Kind() string { return "pocket-id" }

// Issuer implements Provider.
func (p *PocketID) Issuer() string { return strings.TrimSuffix(p.URL, "/") }

// errNotFound is an answer 404: no such client (or, from an older Pocket
// ID, no such endpoint).
var errNotFound = errors.New("not found")

// pidClient is the part of Pocket ID's OidcClientDto NepFleet compares.
type pidClient struct {
	CallbackURLs       []string `json:"callbackURLs"`
	LogoutCallbackURLs []string `json:"logoutCallbackURLs"`
	IsPublic           bool     `json:"isPublic"`
}

// pidSecret is an OidcClientSecretDto: its value never comes back, only
// the first characters.
type pidSecret struct {
	ID       string `json:"id"`
	Prefix   string `json:"prefix"`
	IsActive bool   `json:"isActive"`
}

// secretPrefixLen is how many characters of a secret Pocket ID shows
// (model.OidcClientSecretPrefixLength).
const secretPrefixLen = 4

// Plan implements Provider.
func (p *PocketID) Plan(ctx context.Context, c Client) (Change, error) {
	var ch Change
	raw, err := p.client(ctx, c.ID)
	if errors.Is(err, errNotFound) {
		ch.Create, ch.Secret = true, c.Secret != ""
		ch.Diff = append(ch.Diff, "+ client "+c.ID)
		for _, u := range c.Callbacks {
			ch.Diff = append(ch.Diff, "+ callback "+u)
		}
		for _, u := range c.LogoutCallbacks {
			ch.Diff = append(ch.Diff, "+ logout callback "+u)
		}
		return ch, nil
	}
	if err != nil {
		return ch, err
	}
	var have pidClient
	if err := json.Unmarshal(raw, &have); err != nil {
		return ch, fmt.Errorf("pocket-id: reading client %s: %w", c.ID, err)
	}
	ch.Diff = append(ch.Diff, urlDiff("callback", have.CallbackURLs, c.Callbacks)...)
	ch.Diff = append(ch.Diff, urlDiff("logout callback", have.LogoutCallbackURLs, c.LogoutCallbacks)...)
	ch.Update = len(ch.Diff) > 0
	if c.Secret != "" && !have.IsPublic {
		known, err := p.knowsSecret(ctx, c)
		if err != nil {
			return ch, err
		}
		if !known {
			ch.Secret = true
			ch.Diff = append(ch.Diff, "+ client secret (stored encrypted in the stack's secrets)")
		}
	}
	return ch, nil
}

// Ensure implements Provider.
func (p *PocketID) Ensure(ctx context.Context, c Client) error {
	ch, err := p.Plan(ctx, c)
	if err != nil || ch.Empty() {
		return err
	}
	if ch.Create {
		body := map[string]any{
			"id": c.ID, "name": c.Name, "description": c.Description,
			"callbackURLs": nonNil(c.Callbacks), "logoutCallbackURLs": nonNil(c.LogoutCallbacks),
		}
		var created struct {
			CreatedSecret *pidSecret `json:"createdSecret"`
		}
		if err := p.do(ctx, http.MethodPost, "/api/oidc/clients", body, &created); err != nil {
			return fmt.Errorf("pocket-id: creating client %s: %w", c.ID, err)
		}
		// With "create a client secret automatically" set, Pocket ID made
		// one nobody knows: the stack uses its own.
		if s := created.CreatedSecret; s != nil && s.ID != "" {
			if err := p.do(ctx, http.MethodDelete, p.clientPath(c.ID)+"/secrets/"+url.PathEscape(s.ID), nil, nil); err != nil {
				return fmt.Errorf("pocket-id: removing the generated secret of client %s: %w", c.ID, err)
			}
		}
	}
	if ch.Update {
		// The client as Pocket ID has it, with only the callbacks changed:
		// what an admin set there (groups, consent, token lifetimes) stays.
		raw, err := p.client(ctx, c.ID)
		if err != nil {
			return err
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			return fmt.Errorf("pocket-id: reading client %s: %w", c.ID, err)
		}
		body["callbackURLs"] = nonNil(c.Callbacks)
		body["logoutCallbackURLs"] = nonNil(c.LogoutCallbacks)
		if err := p.do(ctx, http.MethodPut, p.clientPath(c.ID), body, nil); err != nil {
			return fmt.Errorf("pocket-id: updating client %s: %w", c.ID, err)
		}
	}
	if ch.Secret {
		if err := p.do(ctx, http.MethodPost, p.clientPath(c.ID)+"/secrets", map[string]any{"secret": c.Secret}, nil); err != nil {
			return fmt.Errorf("pocket-id: adding the secret of client %s: %w", c.ID, err)
		}
	}
	return nil
}

// knowsSecret: an active secret of the client starts like the stored one.
// Pocket ID keeps only a hash and the first characters, so this is as
// close as the API lets anyone check.
func (p *PocketID) knowsSecret(ctx context.Context, c Client) (bool, error) {
	var secrets []pidSecret
	if err := p.do(ctx, http.MethodGet, p.clientPath(c.ID)+"/secrets", nil, &secrets); err != nil {
		return false, fmt.Errorf("pocket-id: reading the secrets of client %s: %w", c.ID, err)
	}
	if len(c.Secret) <= secretPrefixLen {
		return false, nil
	}
	for _, s := range secrets {
		if s.IsActive && s.Prefix == c.Secret[:secretPrefixLen] {
			return true, nil
		}
	}
	return false, nil
}

func (p *PocketID) clientPath(id string) string {
	return "/api/oidc/clients/" + url.PathEscape(id)
}

// client is the client's JSON, or errNotFound.
func (p *PocketID) client(ctx context.Context, id string) (json.RawMessage, error) {
	var raw json.RawMessage
	err := p.do(ctx, http.MethodGet, p.clientPath(id), nil, &raw)
	if err != nil && !errors.Is(err, errNotFound) {
		return nil, fmt.Errorf("pocket-id: reading client %s: %w", id, err)
	}
	return raw, err
}

// do sends one API request; out, when given, takes the JSON answer.
func (p *PocketID) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.Issuer()+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", p.APIKey)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := p.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s is unreachable: %w", p.Issuer(), err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return errNotFound
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("the API key was refused (%s): is STATIC_API_KEY set in the provider stack?", resp.Status)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return fmt.Errorf("%s: %s", resp.Status, apiError(raw))
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("unexpected answer: %w", err)
	}
	return nil
}

// apiError is the error message of Pocket ID's ErrorDto, or a short body.
func apiError(raw []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error != "" {
		return e.Error
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// urlDiff lists the URLs to remove and to add, or nothing when they match.
func urlDiff(what string, have, want []string) []string {
	if sameURLs(have, want) {
		return nil
	}
	var out []string
	for _, u := range have {
		out = append(out, "- "+what+" "+u)
	}
	for _, u := range want {
		out = append(out, "+ "+what+" "+u)
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
