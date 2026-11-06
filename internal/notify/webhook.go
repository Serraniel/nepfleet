// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// webhook posts events as signed JSON. Receivers verify
// X-NepFleet-Signature: sha256=hex(HMAC-SHA256(secret, timestamp + "." + body))
// with X-NepFleet-Timestamp, and can reject old timestamps (replays).
type webhook struct {
	url    string
	secret []byte
	client *http.Client
	now    func() time.Time
}

// payload is the JSON a signed webhook receives.
type payload struct {
	Event    string `json:"event"`
	Severity string `json:"severity"`
	Stack    string `json:"stack,omitempty"`
	Host     string `json:"host,omitempty"`
	Title    string `json:"title"`
	Message  string `json:"message"`
	URL      string `json:"url,omitempty"`
	Time     string `json:"time"`
}

func newWebhook(raw, secret string) (*webhook, error) {
	u := strings.TrimPrefix(raw, "generic+")
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return nil, fmt.Errorf("a signed webhook needs a generic+https:// (or http://) URL (%s)", scheme(raw))
	}
	return &webhook{url: u, secret: []byte(secret), client: &http.Client{Timeout: 20 * time.Second}, now: time.Now}, nil
}

// Sign returns the signature header value for a timestamp and body; exported
// for receivers written in Go and for tests.
func Sign(secret []byte, timestamp string, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(timestamp + "."))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func (w *webhook) send(e Event) error {
	now := w.now().UTC()
	body, err := json.Marshal(payload{Event: e.Name, Severity: e.Severity, Stack: e.Stack, Host: e.Host, Title: e.Title, Message: e.Message, URL: e.URL, Time: now.Format(time.RFC3339)})
	if err != nil {
		return err
	}
	ts := strconv.FormatInt(now.Unix(), 10)
	req, err := http.NewRequest(http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("signed webhook: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "NepFleet")
	req.Header.Set("X-NepFleet-Event", e.Name)
	req.Header.Set("X-NepFleet-Timestamp", ts)
	req.Header.Set("X-NepFleet-Signature", Sign(w.secret, ts, body))
	res, err := w.client.Do(req) //nolint:gosec // the webhook URL the operator configured
	if err != nil {
		// The URL may carry tokens: report the error without it.
		return fmt.Errorf("signed webhook: request failed")
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	if res.StatusCode >= 300 {
		return fmt.Errorf("signed webhook: HTTP %d", res.StatusCode)
	}
	return nil
}
