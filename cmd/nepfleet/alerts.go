// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/notify"
)

var alertSourceRe = regexp.MustCompile(`[^a-z0-9_-]+`)

// alertStack is the stack a container belongs to, by compose's container name
// (<project>-<service>-<n>): the longest stack name that fits. "" when none does.
func alertStack(container string, stacks []string) string {
	best := ""
	for _, s := range stacks {
		if strings.HasPrefix(container, s+"-") && len(s) > len(best) {
			best = s
		}
	}
	return best
}

// alertEvent turns another tool's alert into a notification. The same rule on
// the same container is one event for dedup; the line itself only varies the
// message.
func alertEvent(a api.Alert, stacks []string) notify.Event {
	source := strings.Trim(alertSourceRe.ReplaceAllString(strings.ToLower(a.Source), "-"), "-")
	if source == "" {
		source = "external"
	}
	rule := a.Rule
	if rule == "" {
		rule = "alert"
	}
	title := rule
	if a.Container != "" {
		title += " in " + a.Container
	}
	msg := a.Detail
	if a.Host != "" {
		msg += fmt.Sprintf("\n\n(%s on %s)", source, a.Host)
	}
	return notify.Event{Name: "alert." + source, Severity: notify.Warning, Stack: alertStack(a.Container, stacks),
		Title: title, Message: strings.TrimSpace(msg)}
}

// sendAlert records an alert and sends it to the fleet's channels, with their
// dedup and quiet hours, as watch does with container events.
func sendAlert(c common, log io.Writer, a api.Alert) error {
	e, err := c.open()
	if err != nil {
		return err
	}
	all, err := e.fleet.Stacks()
	if err != nil {
		return err
	}
	names := make([]string, 0, len(all))
	for _, s := range all {
		names = append(names, s.Metadata.Name)
	}
	n, err := e.notifier()
	if err != nil {
		return err
	}
	e.emit(n, log, alertEvent(a, names))
	return nil
}
