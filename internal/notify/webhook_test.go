// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSignedWebhook(t *testing.T) {
	secret := "hmac-key"
	var got payload
	var verified bool
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ts := r.Header.Get("X-NepFleet-Timestamp")
		verified = ts != "" && r.Header.Get("X-NepFleet-Signature") == Sign([]byte(secret), ts, body) &&
			r.Header.Get("X-NepFleet-Event") == "apply.failed" && r.Header.Get("Content-Type") == "application/json"
		_ = json.Unmarshal(body, &got)
		// A body changed in transit does not verify.
		if Sign([]byte(secret), ts, append(body, ' ')) == r.Header.Get("X-NepFleet-Signature") {
			verified = false
		}
	}))
	defer srv.Close()
	n, err := New([]Channel{{Name: "ci", URL: "generic+" + srv.URL, Format: "json", SigningSecret: secret}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Send(Event{Name: "apply.failed", Severity: Error, Stack: "web", Host: "nas", Title: "applying web failed", Message: "boom"}); err != nil {
		t.Fatal(err)
	}
	if !verified {
		t.Error("the signature does not verify")
	}
	if got.Event != "apply.failed" || got.Stack != "web" || got.Severity != Error || got.Message != "boom" || got.Time == "" {
		t.Errorf("payload = %+v", got)
	}
}

func TestSignedWebhookErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer srv.Close()
	n, err := New([]Channel{{Name: "x", URL: "generic+" + srv.URL + "/t0k3n", SigningSecret: "k", Format: "json"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Send(Event{Name: "a", Severity: Info}); err == nil || !strings.Contains(err.Error(), "HTTP 403") || strings.Contains(err.Error(), "t0k3n") {
		t.Errorf("err = %v", err)
	}
	if _, err := New([]Channel{{Name: "x", URL: "discord://t@w", SigningSecret: "k", Format: "json"}}, nil); err == nil {
		t.Error("signing needs an http(s) webhook")
	}
}
