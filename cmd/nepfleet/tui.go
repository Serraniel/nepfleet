// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/term"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/control"
	"github.com/Serraniel/nepfleet/internal/tui"
)

// tuiClient talks to the control daemon.
type tuiClient struct{}

func remotePath(remote string) string { return "/v1/remotes/" + url.PathEscape(remote) }

func (tuiClient) Remotes(ctx context.Context) ([]control.Remote, error) {
	var all []control.Remote
	return all, controlCall(ctx, http.MethodGet, "/v1/remotes", nil, &all)
}

func (tuiClient) Connect(ctx context.Context, remote string, on bool) error {
	action := "/disconnect"
	if on {
		action = "/connect"
	}
	return controlCall(ctx, http.MethodPost, remotePath(remote)+action, nil, nil)
}

func (tuiClient) Stacks(ctx context.Context, remote string) ([]api.StackInfo, error) {
	var all []api.StackInfo
	return all, controlCall(ctx, http.MethodGet, remotePath(remote)+"/api/v1/stacks", nil, &all)
}

func (tuiClient) Plan(ctx context.Context, remote, stack string) (*tui.Plan, error) {
	var p tui.Plan
	return &p, controlCall(ctx, http.MethodGet, remotePath(remote)+"/api/v1/stacks/"+url.PathEscape(stack)+"/plan", nil, &p)
}

func (tuiClient) Apply(ctx context.Context, remote, stack, planID string, confirm bool) (*api.Job, error) {
	var j api.Job
	body := map[string]any{"plan": planID, "confirmDestructive": confirm}
	return &j, controlCall(ctx, http.MethodPost, remotePath(remote)+"/api/v1/stacks/"+url.PathEscape(stack)+"/apply", body, &j)
}

func (tuiClient) Job(ctx context.Context, remote, id string) (*api.Job, error) {
	var j api.Job
	return &j, controlCall(ctx, http.MethodGet, remotePath(remote)+"/api/v1/jobs/"+url.PathEscape(id), nil, &j)
}

// cmdTUI: nepfleet tui — the terminal client of the control daemon.
func cmdTUI(stdout io.Writer) error {
	if !term.IsTerminal(1) {
		return errors.New("nepfleet tui needs a terminal; nepfleet remote list works without one")
	}
	if err := ensureControl(context.Background()); err != nil {
		return err
	}
	if _, err := tea.NewProgram(tui.New(tuiClient{})).Run(); err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	fmt.Fprintln(stdout, "The control daemon keeps running: nepfleet control status")
	return nil
}
