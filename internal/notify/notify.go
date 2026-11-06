// SPDX-License-Identifier: AGPL-3.0-or-later

// Package notify sends NepFleet events to channels (shoutrrr URLs) chosen by
// routes: event names, stacks and a minimum severity (docs/notifications.md).
package notify

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nicholas-fedor/shoutrrr"
	"github.com/nicholas-fedor/shoutrrr/pkg/types"
)

// Severities, in rising order.
const (
	Info    = "info"
	Warning = "warning"
	Error   = "error"
)

var rank = map[string]int{Info: 0, Warning: 1, Error: 2}

// ValidSeverity reports a known severity.
func ValidSeverity(s string) bool { _, ok := rank[s]; return ok }

// Event is something that happened, e.g. apply.rolled_back of a stack.
type Event struct {
	Name     string // apply.succeeded, update.available, …
	Severity string
	Stack    string
	Host     string
	Title    string
	Message  string
}

// Channel is one destination.
type Channel struct {
	Name string
	URL  string
	// Format "json" sends the event as JSON to a generic webhook.
	Format string
	// SigningSecret signs json webhooks (X-NepFleet-Signature); NepFleet
	// then posts the event itself instead of through shoutrrr.
	SigningSecret string
}

// Route sends matching events to channels. Empty Events or Stacks match
// everything; patterns are "*", exact names or a prefix like "apply.*".
type Route struct {
	Channels    []string
	Events      []string
	Stacks      []string
	MinSeverity string
}

// Sender is what Notifier needs from shoutrrr; tests replace it.
type Sender interface {
	Send(message string, params *types.Params) []error
}

// Notifier delivers events.
type Notifier struct {
	webhooks map[string]*webhook
	senders  map[string]Sender
	ownTitle map[string]bool
	routes   []Route
}

// New creates senders for the channels. Without routes, every event goes to
// every channel.
func New(channels []Channel, routes []Route) (*Notifier, error) {
	n := &Notifier{senders: map[string]Sender{}, webhooks: map[string]*webhook{}, ownTitle: map[string]bool{}, routes: routes}
	var names []string
	for _, c := range channels {
		if c.SigningSecret != "" {
			w, err := newWebhook(c.URL, c.SigningSecret)
			if err != nil {
				return nil, fmt.Errorf("notification channel %s: %w", c.Name, err)
			}
			n.webhooks[c.Name] = w
			names = append(names, c.Name)
			continue
		}
		raw := c.URL
		if c.Format == "json" {
			raw = withTemplateJSON(raw)
		}
		s, err := shoutrrr.CreateSenderWithOptions(types.SenderOptions{Timeout: 20 * time.Second}, raw)
		if err != nil {
			// The URL holds tokens: never repeat it in an error.
			return nil, fmt.Errorf("notification channel %s: invalid URL (%s)", c.Name, scheme(raw))
		}
		n.senders[c.Name] = s
		n.ownTitle[c.Name] = hasOwnTitle(raw)
		names = append(names, c.Name)
	}
	if len(routes) == 0 && len(names) > 0 {
		n.routes = []Route{{Channels: names}}
	}
	for i, r := range n.routes {
		for _, c := range r.Channels {
			_, isWebhook := n.webhooks[c]
			if _, ok := n.senders[c]; !ok && !isWebhook {
				return nil, fmt.Errorf("notification route %d: unknown channel %q", i+1, c)
			}
		}
	}
	return n, nil
}

// SetSender replaces a channel's sender (tests).
func (n *Notifier) SetSender(channel string, s Sender) { n.senders[channel] = s }

// Channels returns the channels an event goes to, sorted.
func (n *Notifier) Channels(e Event) []string {
	if n == nil {
		return nil
	}
	set := map[string]bool{}
	for _, r := range n.routes {
		if r.matches(e) {
			for _, c := range r.Channels {
				set[c] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// Send delivers an event to the channels its routes name. Delivery errors
// are returned (without URLs); they never stop the caller's work.
func (n *Notifier) Send(e Event) error {
	return n.SendTo(n.Channels(e), e)
}

// SendTo delivers an event to the named channels, ignoring routes (used by
// nepfleet notify test).
func (n *Notifier) SendTo(channels []string, e Event) error {
	if n == nil {
		return nil
	}
	if !ValidSeverity(e.Severity) {
		e.Severity = Info
	}
	var errs []error
	for _, c := range channels {
		if w, ok := n.webhooks[c]; ok {
			if err := w.send(e); err != nil {
				errs = append(errs, fmt.Errorf("channel %s: %w", c, err))
			}
			continue
		}
		s, ok := n.senders[c]
		if !ok {
			errs = append(errs, fmt.Errorf("unknown channel %q", c))
			continue
		}
		title := "NepFleet: " + e.Title
		if e.Host != "" {
			title = "NepFleet@" + e.Host + ": " + e.Title
		}
		msg := e.Message
		params := types.Params{types.TitleKey: title, types.LevelKey: e.Severity}
		if n.ownTitle[c] {
			delete(params, types.TitleKey)
			msg = title + "\n" + msg
		}
		for _, err := range s.Send(msg, &params) {
			if err != nil {
				errs = append(errs, fmt.Errorf("channel %s: %w", c, err))
			}
		}
	}
	return errors.Join(errs...)
}

func (r Route) matches(e Event) bool {
	if r.MinSeverity != "" && rank[e.Severity] < rank[r.MinSeverity] {
		return false
	}
	if len(r.Stacks) > 0 && !slices.Contains(r.Stacks, e.Stack) {
		return false
	}
	if len(r.Events) == 0 {
		return true
	}
	return slices.ContainsFunc(r.Events, func(p string) bool { return Match(p, e.Name) })
}

// Match reports whether an event name matches a route pattern: "*", the
// exact name, or "group.*" for every event of a group.
func Match(pattern, name string) bool {
	if pattern == "*" || pattern == name {
		return true
	}
	if prefix, ok := strings.CutSuffix(pattern, ".*"); ok {
		return strings.HasPrefix(name, prefix+".")
	}
	return false
}

func withTemplateJSON(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || !strings.HasPrefix(u.Scheme, "generic") {
		return raw
	}
	q := u.Query()
	if !q.Has("template") {
		q.Set("template", "json")
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func hasOwnTitle(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	q := u.Query()
	return q.Has("title") || q.Has("subject")
}

// scheme names a URL's service without its secrets.
func scheme(raw string) string {
	if i := strings.Index(raw, "://"); i > 0 {
		return raw[:i] + "://…"
	}
	return "not a URL"
}
