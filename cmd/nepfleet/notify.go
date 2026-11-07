// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/Serraniel/nepfleet/internal/events"
	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/notify"
)

// notifier builds the fleet's notifier; nil without channels. Channel URLs
// with secrets come from fleet.secret.yaml.
func (e *env) notifier() (*notify.Notifier, error) {
	cfg := e.fleet.Spec.Notifications
	if len(cfg.Channels) == 0 {
		return nil, nil
	}
	var secrets map[string]string
	var channels []notify.Channel
	names := make([]string, 0, len(cfg.Channels))
	for n := range cfg.Channels {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		c := cfg.Channels[name]
		url := c.URL
		if c.URLSecret != "" {
			if secrets == nil {
				var err error
				if secrets, err = e.store.Load(filepath.Join(e.fleet.Dir, fleet.FleetSecretsFile)); err != nil {
					return nil, err
				}
			}
			if url = secrets[c.URLSecret]; url == "" {
				return nil, fmt.Errorf("notification channel %s: %s is not set (nepfleet secret set --global %s)", name, c.URLSecret, c.URLSecret)
			}
		}
		ch := notify.Channel{Name: name, URL: url, Format: c.Format}
		if c.SigningSecret != "" {
			if secrets == nil {
				var err error
				if secrets, err = e.store.Load(filepath.Join(e.fleet.Dir, fleet.FleetSecretsFile)); err != nil {
					return nil, err
				}
			}
			if ch.SigningSecret = secrets[c.SigningSecret]; ch.SigningSecret == "" {
				return nil, fmt.Errorf("notification channel %s: %s is not set (nepfleet secret set --global %s)", name, c.SigningSecret, c.SigningSecret)
			}
		}
		channels = append(channels, ch)
	}
	var routes []notify.Route
	for _, r := range cfg.Routes {
		routes = append(routes, notify.Route{Channels: r.Channels, Events: r.Events, Stacks: r.Stacks, MinSeverity: r.MinSeverity})
	}
	return notify.New(channels, routes)
}

// emit sends an event; a failing channel only warns.
func eventStore(stateDir string) *events.Store {
	return &events.Store{Dir: filepath.Join(stateDir, "events")}
}

func (e *env) emit(n *notify.Notifier, stdout io.Writer, ev notify.Event) {
	ev.Host = e.host
	// Always recorded: clients catch up from here, whatever channels exist.
	if _, err := eventStore(e.stateDir).Add(events.Event{Name: ev.Name, Severity: ev.Severity, Stack: ev.Stack, Host: ev.Host, Title: ev.Title, Message: ev.Message}); err != nil {
		fmt.Fprintf(stdout, "%s: ⚠ recording the event: %v\n", ev.Stack, err)
	}
	if n == nil {
		return
	}
	if u := e.fleet.Spec.Notifications.UIURL; u != "" && ev.Stack != "" && ev.URL == "" {
		ev.URL = strings.TrimRight(u, "/") + "/stacks/" + url.PathEscape(ev.Stack)
	}
	if err := n.Send(ev); err != nil {
		fmt.Fprintf(stdout, "%s: ⚠ notification: %v\n", ev.Stack, err)
	}
}

// cmdNotify: nepfleet notify test <channel>
func cmdNotify(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "test" {
		return errors.New("usage: nepfleet notify test <channel>")
	}
	var c common
	fs := flag.NewFlagSet("notify test", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c.register(fs)
	pos, err := parseInterspersed(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: nepfleet notify test <channel>")
	}
	e, err := c.open()
	if err != nil {
		return err
	}
	if _, ok := e.fleet.Spec.Notifications.Channels[pos[0]]; !ok {
		var names []string
		for n := range e.fleet.Spec.Notifications.Channels {
			names = append(names, n)
		}
		slices.Sort(names)
		return fmt.Errorf("no channel %q in fleet.yaml (have: %v)", pos[0], names)
	}
	n, err := e.notifier()
	if err != nil {
		return err
	}
	ev := notify.Event{Name: "notify.test", Severity: notify.Info, Host: e.host, Title: "test message",
		Message: "If you read this, NepFleet's channel " + pos[0] + " works."}
	if err := n.SendTo([]string{pos[0]}, ev); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "sent a test message to %s\n", pos[0])
	return nil
}
