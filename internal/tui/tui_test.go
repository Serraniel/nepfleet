// SPDX-License-Identifier: AGPL-3.0-or-later

package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/control"
	"github.com/Serraniel/nepfleet/internal/deploy"
)

type fake struct {
	plan      *Plan
	applied   string
	confirmed bool
	connected []string
}

func (f *fake) Remotes(context.Context) ([]control.Remote, error) {
	return []control.Remote{
		{Name: "nas", Address: "ssh://nas", Tunnel: control.TunnelUp},
		{Name: "off", Address: "ssh://off", Tunnel: control.TunnelOff},
	}, nil
}
func (f *fake) Connect(_ context.Context, r string, on bool) error {
	if on {
		f.connected = append(f.connected, r)
	}
	return nil
}
func (f *fake) Stacks(context.Context, string) ([]api.StackInfo, error) {
	return []api.StackInfo{{Name: "web", Target: "local"}}, nil
}
func (f *fake) Plan(context.Context, string, string) (*Plan, error) {
	if f.plan == nil {
		return nil, errors.New("no plan")
	}
	return f.plan, nil
}
func (f *fake) Apply(_ context.Context, _, _, id string, confirm bool) (*api.Job, error) {
	f.applied, f.confirmed = id, confirm
	return &api.Job{ID: "j1", State: "running", Log: "starting\n"}, nil
}
func (f *fake) Job(context.Context, string, string) (*api.Job, error) {
	return &api.Job{ID: "j1", State: "succeeded", Log: "starting\ndone\n"}, nil
}

// send runs a message and the commands it starts (ticks excluded).
func send(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, cmd := m.Update(msg)
	m = next.(Model)
	if cmd != nil {
		if out := cmd(); out != nil {
			if _, isTick := out.(tickMsg); !isTick {
				return send(t, m, out)
			}
		}
	}
	return m
}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	}
	return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
}

func opened(t *testing.T, f *fake) Model {
	t.Helper()
	m := New(f)
	m = send(t, m, m.loadRemotes()())
	m = send(t, m, key("enter")) // nas
	return send(t, m, key("enter"))
}

func TestFlowToApply(t *testing.T) {
	f := &fake{plan: &Plan{ID: "p1", Plan: deploy.Plan{Changes: []deploy.Change{{Path: ".env", Kind: deploy.Changed, Diff: "+ A=1\n"}}}}}
	m := opened(t, f)
	if m.screen != screenPlan || !strings.Contains(m.render(), "+ A=1") {
		t.Fatalf("plan screen:\n%s", m.render())
	}
	m = send(t, m, key("a"))
	if !m.confirming {
		t.Fatal("a must ask first")
	}
	m = send(t, m, key("enter")) // nothing typed: nothing happens
	if f.applied != "" || !m.confirming {
		t.Errorf("applied without a yes: %q", f.applied)
	}
	m = send(t, m, key("y"))
	m = send(t, m, key("enter"))
	if f.applied != "p1" || f.confirmed {
		t.Errorf("apply: %q %v", f.applied, f.confirmed)
	}
	if m.screen != screenJob || !strings.Contains(m.render(), "starting") {
		t.Errorf("job screen:\n%s", m.render())
	}
	// Following the running job.
	next, cmd := m.Update(tickMsg{})
	m = next.(Model)
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			if out := c(); out != nil {
				if j, ok := out.(jobMsg); ok {
					m = send(t, m, j)
				}
			}
		}
	}
	if !strings.Contains(m.render(), "Applied.") {
		t.Errorf("after the job:\n%s", m.render())
	}
}

func TestDestructiveNeedsTheName(t *testing.T) {
	f := &fake{plan: &Plan{ID: "p2", Plan: deploy.Plan{Destructive: []string{`removes volume "data"`}, Changes: []deploy.Change{{Path: "compose.yaml", Kind: deploy.Changed}}}}}
	m := opened(t, f)
	if !strings.Contains(m.render(), "DESTRUCTIVE") {
		t.Errorf("must be shown:\n%s", m.render())
	}
	m = send(t, m, key("a"))
	m = send(t, m, key("y"))
	m = send(t, m, key("enter"))
	if f.applied != "" {
		t.Fatal("y must not confirm a destructive plan")
	}
	m = send(t, m, key("esc"))
	if m.confirming {
		t.Fatal("esc cancels")
	}
	m = send(t, m, key("a"))
	m = send(t, m, key("web"))
	m = send(t, m, key("enter"))
	if f.applied != "p2" || !f.confirmed {
		t.Errorf("apply: %q %v", f.applied, f.confirmed)
	}
	_ = m
}

func TestNotConnected(t *testing.T) {
	f := &fake{}
	m := New(f)
	m = send(t, m, m.loadRemotes()())
	m = send(t, m, key("down"))
	m = send(t, m, key("enter"))
	if m.screen != screenRemotes || !strings.Contains(m.render(), "not connected") {
		t.Errorf("view:\n%s", m.render())
	}
	m = send(t, m, key("c"))
	if len(f.connected) != 1 || f.connected[0] != "off" {
		t.Errorf("connect: %v", f.connected)
	}
	_ = m
}

func TestRemoteTextCannotDriveTheTerminal(t *testing.T) {
	f := &fake{plan: &Plan{ID: "p", Plan: deploy.Plan{Changes: []deploy.Change{{Path: "a\x1b]0;evil\x07", Kind: deploy.Changed, Diff: "+ \x1b[2Jcleared\n"}}}}}
	m := opened(t, f)
	v := m.render()
	if strings.ContainsAny(v, "\x1b\x07") {
		t.Errorf("control characters in the view: %q", v)
	}
	if !strings.Contains(v, "cleared") {
		t.Errorf("text must stay: %q", v)
	}
}
