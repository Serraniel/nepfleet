// SPDX-License-Identifier: AGPL-3.0-or-later

// Package tui is the terminal client of the local control daemon: machines,
// their stacks, plans with diffs, and applying with a live log. It holds no
// state of its own, so closing it changes nothing.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/control"
	"github.com/Serraniel/nepfleet/internal/deploy"
)

// Plan is a stack's plan as the API returns it.
type Plan struct {
	deploy.Plan
	ID string `json:"id"`
}

// Client is what the TUI needs from the daemon.
type Client interface {
	Remotes(ctx context.Context) ([]control.Remote, error)
	Connect(ctx context.Context, remote string, on bool) error
	Stacks(ctx context.Context, remote string) ([]api.StackInfo, error)
	Plan(ctx context.Context, remote, stack string) (*Plan, error)
	Apply(ctx context.Context, remote, stack, planID string, confirmDestructive bool) (*api.Job, error)
	Job(ctx context.Context, remote, id string) (*api.Job, error)
}

type screen int

const (
	screenRemotes screen = iota
	screenStacks
	screenPlan
	screenJob
)

// Model is the application state.
type Model struct {
	c      Client
	screen screen
	width  int
	height int
	err    string
	cursor int
	scroll int

	remotes []control.Remote
	remote  string
	stacks  []api.StackInfo
	stack   string
	plan    *Plan
	job     *api.Job

	// confirming asks before applying; a destructive plan needs the stack's
	// name typed into typed.
	confirming bool
	typed      string
}

// New starts at the list of machines.
func New(c Client) Model { return Model{c: c, width: 80, height: 24} }

type (
	remotesMsg []control.Remote
	stacksMsg  []api.StackInfo
	planMsg    *Plan
	jobMsg     *api.Job
	errMsg     struct{ err error }
	tickMsg    struct{}
)

const timeout = 90 * time.Second

func call[T any](f func(context.Context) (T, error), wrap func(T) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		v, err := f(ctx)
		if err != nil {
			return errMsg{err}
		}
		return wrap(v)
	}
}

func (m Model) loadRemotes() tea.Cmd {
	return call(m.c.Remotes, func(r []control.Remote) tea.Msg { return remotesMsg(r) })
}

func (m Model) loadStacks() tea.Cmd {
	remote := m.remote
	return call(func(ctx context.Context) ([]api.StackInfo, error) { return m.c.Stacks(ctx, remote) },
		func(s []api.StackInfo) tea.Msg { return stacksMsg(s) })
}

func (m Model) loadPlan() tea.Cmd {
	remote, stack := m.remote, m.stack
	return call(func(ctx context.Context) (*Plan, error) { return m.c.Plan(ctx, remote, stack) },
		func(p *Plan) tea.Msg { return planMsg(p) })
}

func tick() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

// Init loads the machines.
func (m Model) Init() tea.Cmd { return tea.Batch(m.loadRemotes(), tick()) }

// Update handles a message.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case errMsg:
		m.err = Clean(msg.err.Error())
	case remotesMsg:
		m.remotes, m.err = msg, ""
		m.cursor = min(m.cursor, max(len(m.remotes)-1, 0))
	case stacksMsg:
		m.stacks, m.err = msg, ""
		m.cursor = min(m.cursor, max(len(m.stacks)-1, 0))
	case planMsg:
		m.plan, m.err, m.scroll = msg, "", 0
	case jobMsg:
		m.job, m.err = msg, ""
		m.scroll = max(len(m.jobLines())-m.bodyHeight(), 0)
	case tickMsg:
		// Follow what can change by itself: tunnels and a running apply.
		cmds := []tea.Cmd{tick()}
		switch {
		case m.screen == screenRemotes:
			cmds = append(cmds, m.loadRemotes())
		case m.screen == screenJob && m.job != nil && m.job.State == "running":
			remote, id := m.remote, m.job.ID
			cmds = append(cmds, call(func(ctx context.Context) (*api.Job, error) { return m.c.Job(ctx, remote, id) },
				func(j *api.Job) tea.Msg { return jobMsg(j) }))
		}
		return m, tea.Batch(cmds...)
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m Model) key(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.confirming {
		return m.confirmKey(k)
	}
	switch k.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "esc", "backspace", "left", "h":
		return m.back()
	case "up", "k":
		if m.screen <= screenStacks {
			m.cursor = max(m.cursor-1, 0)
		} else {
			m.scroll = max(m.scroll-1, 0)
		}
	case "down", "j":
		switch m.screen {
		case screenRemotes:
			m.cursor = min(m.cursor+1, max(len(m.remotes)-1, 0))
		case screenStacks:
			m.cursor = min(m.cursor+1, max(len(m.stacks)-1, 0))
		default:
			m.scroll++
		}
	case "enter", "right", "l":
		return m.enter()
	case "c":
		if m.screen == screenRemotes && m.cursor < len(m.remotes) {
			r := m.remotes[m.cursor]
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), timeout)
				defer cancel()
				if err := m.c.Connect(ctx, r.Name, !r.Connect); err != nil {
					return errMsg{err}
				}
				all, err := m.c.Remotes(ctx)
				if err != nil {
					return errMsg{err}
				}
				return remotesMsg(all)
			}
		}
	case "r":
		switch m.screen {
		case screenRemotes:
			return m, m.loadRemotes()
		case screenStacks:
			return m, m.loadStacks()
		case screenPlan:
			m.plan = nil
			return m, m.loadPlan()
		}
	case "a":
		if m.screen == screenPlan && m.plan != nil && len(m.plan.Changes) > 0 {
			m.confirming, m.typed, m.err = true, "", ""
		}
	}
	return m, nil
}

func (m Model) confirmKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	destructive := len(m.plan.Destructive) > 0
	switch k.String() {
	case "ctrl+c", "esc":
		m.confirming = false
		return m, nil
	case "backspace":
		if r := []rune(m.typed); len(r) > 0 {
			m.typed = string(r[:len(r)-1])
		}
		return m, nil
	case "enter":
		if destructive && m.typed != m.stack {
			return m, nil
		}
		if !destructive && m.typed != "y" {
			return m, nil
		}
		m.confirming, m.screen, m.scroll = false, screenJob, 0
		m.job = nil
		remote, stack, id := m.remote, m.stack, m.plan.ID
		return m, call(func(ctx context.Context) (*api.Job, error) { return m.c.Apply(ctx, remote, stack, id, destructive) },
			func(j *api.Job) tea.Msg { return jobMsg(j) })
	}
	m.typed += k.Text
	return m, nil
}

func (m Model) enter() (tea.Model, tea.Cmd) {
	switch m.screen {
	case screenRemotes:
		if m.cursor >= len(m.remotes) {
			return m, nil
		}
		r := m.remotes[m.cursor]
		if r.Tunnel != control.TunnelUp {
			m.err = r.Name + " is not connected: press c"
			return m, nil
		}
		m.remote, m.screen, m.cursor, m.stacks, m.err = r.Name, screenStacks, 0, nil, ""
		return m, m.loadStacks()
	case screenStacks:
		if m.cursor >= len(m.stacks) {
			return m, nil
		}
		m.stack, m.screen, m.plan, m.err = m.stacks[m.cursor].Name, screenPlan, nil, ""
		return m, m.loadPlan()
	}
	return m, nil
}

func (m Model) back() (tea.Model, tea.Cmd) {
	m.err, m.scroll = "", 0
	switch m.screen {
	case screenStacks:
		m.screen, m.cursor = screenRemotes, 0
		return m, m.loadRemotes()
	case screenPlan:
		m.screen, m.cursor = screenStacks, 0
		return m, m.loadStacks()
	case screenJob:
		// A running apply goes on: the daemon and the server own it.
		m.screen, m.plan = screenPlan, nil
		return m, m.loadPlan()
	}
	return m, nil
}

func (m Model) bodyHeight() int { return max(m.height-4, 3) }

func (m Model) jobLines() []string {
	if m.job == nil {
		return nil
	}
	return strings.Split(Clean(m.job.Log), "\n")
}

// View is the screen, on the terminal's alternate screen.
func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m Model) render() string {
	var title, help string
	var lines []string
	switch m.screen {
	case screenRemotes:
		title, help = "NepFleet: machines", "enter open · c connect/disconnect · r refresh · q quit"
		lines = m.remoteLines()
	case screenStacks:
		title, help = "NepFleet: "+Clean(m.remote)+": stacks", "enter plan · r refresh · esc back · q quit"
		lines = m.stackLines()
	case screenPlan:
		title, help = "NepFleet: "+Clean(m.remote)+"/"+Clean(m.stack), "↑↓ scroll · a apply · r plan again · esc back · q quit"
		lines = m.planLines()
	case screenJob:
		title, help = "NepFleet: applying "+Clean(m.stack), "↑↓ scroll · esc back (the apply goes on) · q quit"
		lines = m.jobScreen()
	}
	if m.screen >= screenPlan {
		start := min(m.scroll, max(len(lines)-1, 0))
		lines = lines[start:]
	}
	lines = lines[:min(len(lines), m.bodyHeight())]
	var b strings.Builder
	b.WriteString(title + "\n\n")
	for _, l := range lines {
		b.WriteString(fit(l, m.width) + "\n")
	}
	switch {
	case m.confirming && len(m.plan.Destructive) > 0:
		fmt.Fprintf(&b, "\nDestructive. Type %s and press enter, esc to cancel: %s", Clean(m.stack), Clean(m.typed))
	case m.confirming:
		fmt.Fprintf(&b, "\nApply this plan? Type y and press enter, esc to cancel: %s", Clean(m.typed))
	case m.err != "":
		b.WriteString("\n! " + fit(m.err, m.width-2))
	default:
		b.WriteString("\n" + fit(help, m.width))
	}
	return b.String()
}

func (m Model) remoteLines() []string {
	if len(m.remotes) == 0 {
		return []string{"No machines yet. Add one: nepfleet remote add ssh://user@host"}
	}
	var out []string
	for i, r := range m.remotes {
		out = append(out, fmt.Sprintf("%s %-20s %-11s %s", marker(i == m.cursor), Clean(r.Name), Clean(r.Tunnel), Clean(r.Address)))
		if i == m.cursor && r.TunnelError != "" {
			out = append(out, "    "+Clean(r.TunnelError))
		}
	}
	return out
}

func (m Model) stackLines() []string {
	if m.stacks == nil {
		return []string{"Loading…"}
	}
	if len(m.stacks) == 0 {
		return []string{"No stacks here, or the token covers none."}
	}
	var out []string
	for i, s := range m.stacks {
		tpl := s.Template
		if tpl == "" {
			tpl = "plain compose"
		}
		out = append(out, fmt.Sprintf("%s %-24s %-12s %s", marker(i == m.cursor), Clean(s.Name), Clean(s.Target), Clean(tpl)))
	}
	return out
}

func (m Model) planLines() []string {
	if m.plan == nil {
		return []string{"Planning… (renders the stack on the remote)"}
	}
	p := m.plan
	if len(p.Changes) == 0 {
		return []string{"Nothing to apply: the running stack matches the fleet repository."}
	}
	var out []string
	if p.First {
		out = append(out, "Not applied yet: the first apply creates everything below.", "")
	}
	for _, d := range p.Destructive {
		out = append(out, "DESTRUCTIVE: "+Clean(d))
	}
	for i, s := range p.Steps {
		mark := ""
		if s.Destructive {
			mark = " (destructive)"
		}
		out = append(out, fmt.Sprintf("%2d. %s%s", i+1, Clean(s.Text), mark))
	}
	for _, c := range p.Changes {
		out = append(out, "", fmt.Sprintf("%s %s", Clean(string(c.Kind)), Clean(c.Path)))
		if c.Diff != "" {
			out = append(out, strings.Split(strings.TrimRight(Clean(c.Diff), "\n"), "\n")...)
		}
	}
	return out
}

func (m Model) jobScreen() []string {
	if m.job == nil {
		return []string{"Starting…"}
	}
	state := "Running…"
	switch m.job.State {
	case "succeeded":
		state = "Applied."
	case "failed":
		state = "Failed: " + Clean(m.job.Error)
	}
	return append([]string{state, ""}, m.jobLines()...)
}

func marker(on bool) string {
	if on {
		return ">"
	}
	return " "
}

// fit cuts a line to the terminal's width.
func fit(s string, width int) string {
	if width <= 0 {
		return s
	}
	if r := []rune(s); len(r) > width {
		return string(r[:max(width-1, 0)]) + "…"
	}
	return s
}

// Clean removes control characters except newlines and tabs: this text comes
// from another machine, and escape sequences could drive the terminal.
func Clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}
