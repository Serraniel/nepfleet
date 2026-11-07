// SPDX-License-Identifier: AGPL-3.0-or-later

package deploy

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/Serraniel/nepfleet/internal/template"
)

// PlannedMigration is a migration as a plan shows it.
type PlannedMigration struct {
	From        string   `json:"from"`
	To          string   `json:"to"`
	Description string   `json:"description,omitempty"`
	Backup      string   `json:"backup"`
	Rollback    string   `json:"rollback"`
	Steps       []string `json:"steps"`
}

// Migration is what Apply runs around the new version's start.
type Migration struct {
	// Before runs in the old version, After in the new one.
	Before, After []template.Step
	// RestoreData puts the snapshot back on failure (rollback: restore).
	RestoreData bool
	Log         io.Writer
}

// NewMigration splits a template's steps at its deploy step; without one, the
// new version starts last.
func NewMigration(m template.Migration, log io.Writer) *Migration {
	mg := &Migration{Before: m.Steps, RestoreData: m.Rollback == "restore", Log: log}
	for i, s := range m.Steps {
		if s.Deploy {
			mg.Before, mg.After = m.Steps[:i], m.Steps[i+1:]
			break
		}
	}
	return mg
}

func (m *Migration) before(ctx context.Context, r Runner, stack string) error {
	if m == nil {
		return nil
	}
	return m.run(ctx, r, stack, m.Before)
}

func (m *Migration) after(ctx context.Context, r Runner, stack string) error {
	if m == nil {
		return nil
	}
	return m.run(ctx, r, stack, m.After)
}

func (m *Migration) run(ctx context.Context, r Runner, stack string, steps []template.Step) error {
	for _, s := range steps {
		name := s.Name
		if name == "" {
			name = strings.Join(s.Run, " ")
		}
		if m.Log != nil {
			fmt.Fprintf(m.Log, "%s: migration step: %s\n", stack, name)
		}
		if err := runStep(ctx, r, stack, s); err != nil {
			return fmt.Errorf("migration step %q: %w", name, err)
		}
	}
	return nil
}

// runStep runs a command in a container of the stack, and only there. It finds
// the container by its labels, not through compose: before the deploy, the files
// on disk already describe the new version.
func runStep(ctx context.Context, r Runner, stack string, s template.Step) error {
	out, err := r.Run(ctx, "", "ps", "-q", "--filter", "status=running",
		"--filter", "label=com.docker.compose.project="+stack,
		"--filter", "label=com.docker.compose.service="+s.Service)
	if err != nil {
		return err
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return fmt.Errorf("service %s is not running", s.Service)
	}
	_, err = r.Run(ctx, "", append([]string{"exec", ids[0]}, s.Run...)...)
	return err
}
