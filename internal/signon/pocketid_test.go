// SPDX-License-Identifier: AGPL-3.0-or-later

package signon_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/signon"
	"github.com/Serraniel/nepfleet/internal/signon/signontest"
)

const key = "static-api-key-0123456789"

func client(domain string) signon.Client {
	return signon.Client{
		ID: "wiki", Name: "wiki", Description: "Managed by NepFleet",
		Callbacks:       []string{"https://" + domain + "/auth/callback"},
		LogoutCallbacks: []string{"https://" + domain + "/"},
		Secret:          "Sec0ndsAreTooShortForThis1234567890abcd",
	}
}

func TestPocketIDCreatesOnceAndKeeps(t *testing.T) {
	srv := signontest.New(key)
	defer srv.Close()
	ctx := context.Background()
	p := &signon.PocketID{URL: srv.URL + "/", APIKey: key}
	if p.Issuer() != srv.URL {
		t.Errorf("issuer %q", p.Issuer())
	}
	c := client("wiki.example.test")

	ch, err := p.Plan(ctx, c)
	if err != nil || !ch.Create || !ch.Secret || ch.Update {
		t.Fatalf("plan for a new client: %+v %v", ch, err)
	}
	if srv.Requests["POST /api/oidc/clients"] != 0 {
		t.Fatal("Plan changed something")
	}
	if err := p.Ensure(ctx, c); err != nil {
		t.Fatal(err)
	}
	got := srv.Clients["wiki"]
	if got["name"] != "wiki" || !strings.Contains(strings.Join(anyStrings(got["callbackURLs"]), ","), "https://wiki.example.test/auth/callback") {
		t.Errorf("created %v", got)
	}
	if !srv.Knows("wiki", c.Secret) {
		t.Error("the stack's secret wasn't added")
	}

	// Idempotent: a second apply changes nothing.
	ch, err = p.Plan(ctx, c)
	if err != nil || !ch.Empty() {
		t.Fatalf("second plan: %+v %v", ch, err)
	}
	before := len(srv.Requests)
	if err := p.Ensure(ctx, c); err != nil {
		t.Fatal(err)
	}
	if srv.Requests["POST /api/oidc/clients"] != 1 || srv.Requests["POST /api/oidc/clients/wiki/secrets"] != 1 || srv.Requests["PUT /api/oidc/clients/wiki"] != 0 {
		t.Errorf("a second Ensure wrote: %v (before %d)", srv.Requests, before)
	}
}

func TestPocketIDUpdatesCallbacksKeepingSettings(t *testing.T) {
	srv := signontest.New(key)
	defer srv.Close()
	ctx := context.Background()
	p := &signon.PocketID{URL: srv.URL, APIKey: key}
	if err := p.Ensure(ctx, client("old.example.test")); err != nil {
		t.Fatal(err)
	}
	// An admin restricted the client to a group and skips consent.
	srv.Clients["wiki"]["isGroupRestricted"] = true
	srv.Clients["wiki"]["skipConsent"] = true

	c := client("new.example.test")
	ch, err := p.Plan(ctx, c)
	if err != nil || !ch.Update || ch.Create || ch.Secret {
		t.Fatalf("plan after a domain change: %+v %v", ch, err)
	}
	diff := strings.Join(ch.Diff, "\n")
	if !strings.Contains(diff, "- callback https://old.example.test/auth/callback") || !strings.Contains(diff, "+ callback https://new.example.test/auth/callback") {
		t.Errorf("diff:\n%s", diff)
	}
	if err := p.Ensure(ctx, c); err != nil {
		t.Fatal(err)
	}
	got := srv.Clients["wiki"]
	if anyStrings(got["callbackURLs"])[0] != "https://new.example.test/auth/callback" {
		t.Errorf("callbacks %v", got["callbackURLs"])
	}
	if got["isGroupRestricted"] != true || got["skipConsent"] != true || got["name"] != "wiki" {
		t.Errorf("the admin's settings were lost: %v", got)
	}
}

// A secret Pocket ID doesn't know (restored from a backup, client made by
// hand) is added; one it made by itself is removed on create.
func TestPocketIDSecrets(t *testing.T) {
	srv := signontest.New(key)
	defer srv.Close()
	srv.AutoSecret = true
	ctx := context.Background()
	p := &signon.PocketID{URL: srv.URL, APIKey: key}
	c := client("wiki.example.test")
	if err := p.Ensure(ctx, c); err != nil {
		t.Fatal(err)
	}
	if n := len(srv.Secrets["wiki"]); n != 1 || !srv.Knows("wiki", c.Secret) {
		t.Errorf("secrets after create: %+v", srv.Secrets["wiki"])
	}
	srv.Secrets["wiki"] = nil
	ch, err := p.Plan(ctx, c)
	if err != nil || !ch.Secret || ch.Create || ch.Update {
		t.Fatalf("plan with the secret gone: %+v %v", ch, err)
	}
	if strings.Contains(strings.Join(ch.Diff, "\n"), c.Secret) {
		t.Error("the plan shows the secret")
	}
	if err := p.Ensure(ctx, c); err != nil || !srv.Knows("wiki", c.Secret) {
		t.Errorf("secret not added again: %v", err)
	}
}

func TestPocketIDErrors(t *testing.T) {
	srv := signontest.New(key)
	ctx := context.Background()
	p := &signon.PocketID{URL: srv.URL, APIKey: "wrong-key-0123456789"}
	if _, err := p.Plan(ctx, client("x.example.test")); err == nil || !strings.Contains(err.Error(), "API key was refused") {
		t.Errorf("wrong key: %v", err)
	}
	srv.Close()
	p.APIKey = key
	_, err := p.Plan(ctx, client("x.example.test"))
	if err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("server down: %v", err)
	}
	if err := p.Ensure(ctx, client("x.example.test")); err == nil {
		t.Error("Ensure succeeded against a server that is down")
	}
}

func anyStrings(v any) []string {
	var out []string
	l, _ := v.([]any)
	for _, x := range l {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}
