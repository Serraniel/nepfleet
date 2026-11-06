// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"slices"
	"strings"
	"testing"

	"github.com/nicholas-fedor/shoutrrr/pkg/types"
)

type fake struct{ got []string }

func (f *fake) Send(msg string, p *types.Params) []error {
	f.got = append(f.got, (*p)[types.TitleKey]+"|"+(*p)[types.LevelKey]+"|"+msg)
	return nil
}

func TestMatch(t *testing.T) {
	for _, c := range []struct {
		pattern, name string
		want          bool
	}{
		{"*", "apply.failed", true},
		{"apply.*", "apply.failed", true},
		{"apply.*", "applyx.failed", false},
		{"apply.failed", "apply.failed", true},
		{"apply", "apply.failed", false},
	} {
		if Match(c.pattern, c.name) != c.want {
			t.Errorf("Match(%q, %q) != %v", c.pattern, c.name, c.want)
		}
	}
}

func TestRoutes(t *testing.T) {
	n, err := New([]Channel{
		{Name: "discord", URL: "generic+https://example.org/a"},
		{Name: "phone", URL: "generic+https://example.org/b"},
		{Name: "ci", URL: "generic+https://example.org/c", Format: "json"},
	}, []Route{
		{Channels: []string{"discord"}, Events: []string{"*"}, MinSeverity: Warning},
		{Channels: []string{"phone"}, Events: []string{"apply.rolled_back", "host.*"}},
		{Channels: []string{"ci"}, Events: []string{"apply.*"}, Stacks: []string{"forgejo"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		ev   Event
		want []string
	}{
		{Event{Name: "apply.succeeded", Severity: Info, Stack: "web"}, nil},
		{Event{Name: "apply.succeeded", Severity: Info, Stack: "forgejo"}, []string{"ci"}},
		{Event{Name: "apply.rolled_back", Severity: Warning, Stack: "forgejo"}, []string{"ci", "discord", "phone"}},
		{Event{Name: "host.unreachable", Severity: Error}, []string{"discord", "phone"}},
	} {
		if got := n.Channels(c.ev); !slices.Equal(got, c.want) && (len(got) != 0 || len(c.want) != 0) {
			t.Errorf("%+v → %v, want %v", c.ev, got, c.want)
		}
	}
	// No network in tests: every channel gets a fake.
	f, other := &fake{}, &fake{}
	n.SetSender("phone", f)
	n.SetSender("discord", other)
	n.SetSender("ci", other)
	if err := n.Send(Event{Name: "apply.rolled_back", Severity: Warning, Stack: "web", Host: "nas", Title: "web rolled back", Message: "details"}); err != nil {
		t.Fatal(err)
	}
	if len(f.got) != 1 || f.got[0] != "NepFleet@nas: web rolled back|warning|details\nstack web · host nas" {
		t.Errorf("sent %q", f.got)
	}
	if len(other.got) != 1 { // discord (warning ≥ warning); ci is only for forgejo
		t.Errorf("other channels got %q", other.got)
	}
}

func TestDefaultsAndErrors(t *testing.T) {
	n, err := New([]Channel{{Name: "a", URL: "generic+https://example.org/a"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := n.Channels(Event{Name: "anything", Severity: Info}); !slices.Equal(got, []string{"a"}) {
		t.Errorf("without routes everything goes everywhere: %v", got)
	}
	if _, err := New([]Channel{{Name: "a", URL: "generic+https://example.org/a"}}, []Route{{Channels: []string{"b"}}}); err == nil {
		t.Error("an unknown channel in a route must fail")
	}
	_, err = New([]Channel{{Name: "a", URL: "nosuchservice://s3cr3t-token@x"}}, nil)
	if err == nil || strings.Contains(err.Error(), "s3cr3t") {
		t.Errorf("a bad URL must fail without showing it: %v", err)
	}
	if withTemplateJSON("generic+https://h/x?a=1") != "generic+https://h/x?a=1&template=json" || withTemplateJSON("discord://t@w") != "discord://t@w" {
		t.Error("json format only applies to generic webhooks")
	}
}

func TestEventText(t *testing.T) {
	e := Event{Message: "m", Stack: "web", Host: "nas", URL: "https://ui.example.org/stacks/web"}
	if got := e.text(); got != "m\nstack web · host nas\nhttps://ui.example.org/stacks/web" {
		t.Errorf("text = %q", got)
	}
	if got := (Event{Message: "m"}).text(); got != "m" {
		t.Errorf("text without context = %q", got)
	}
}
