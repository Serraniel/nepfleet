// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/Serraniel/nepfleet/internal/deploy"
	"github.com/Serraniel/nepfleet/internal/template"
)

// planMigration finds what moving this stack to its template's version takes, or
// why it can't move. An error stays in the plan: looking must still work.
func (e *env) planMigration(pr *prepared) {
	p := pr.plan
	if pr.tpl == nil || p.First {
		return
	}
	man, err := deploy.Applied(p.Dir)
	if err != nil || man == nil || (man.Template != "" && man.Template != pr.tpl.Metadata.Name) {
		return // nothing to migrate from
	}
	if man.TemplateVersion == "" {
		// Applied before versions were recorded: this apply records it, but
		// nothing says if the stack is past the template's migrations. Someone
		// has to say so, as they won't run.
		if len(p.Changes) > 0 && len(pr.tpl.Spec.Migrations) > 0 {
			p.Destructive = append(p.Destructive, fmt.Sprintf("this stack's template version isn't recorded, so no migration runs: continue only if it already runs a version that needs none on the way to %s", pr.tpl.Spec.Version))
		}
		return
	}
	path, err := template.UpgradePath(pr.tpl.Spec.Migrations, man.TemplateVersion, pr.tpl.Spec.Version)
	switch {
	case errors.Is(err, template.ErrDowngrade):
		// A rollback comes this way, and has to work. It runs no steps, so the
		// data stays as the newer version left it: that takes a confirmation.
		// With the same files nothing changes but the recorded version.
		if len(p.Changes) > 0 {
			p.Destructive = append(p.Destructive, fmt.Sprintf("goes back from template version %s to %s; the data isn't migrated back, so restore a backup if the newer version changed it, as upgrading again runs its migrations again", man.TemplateVersion, pr.tpl.Spec.Version))
		}
		return
	case err != nil:
		p.MigrationError = err.Error()
		return
	case len(path) == 0:
		return
	case len(path) > 1:
		// The steps in between need that version's own compose file.
		p.MigrationError = fmt.Sprintf("upgrading from %s to %s takes several migrations: apply version %s first",
			man.TemplateVersion, pr.tpl.Spec.Version, path[0].To)
		return
	case p.Mode == "swarm":
		p.MigrationError = "migrations run on compose stacks for now: upgrade this swarm stack by hand"
		return
	}
	m := path[0]
	needsSnapshot := m.Backup == "required" || m.Rollback == "restore"
	canSnapshot := !e.noSnapshot && pr.stack.Spec.Snapshot != "off"
	if needsSnapshot && !canSnapshot {
		p.MigrationError = fmt.Sprintf("the migration to %s needs a snapshot of the data, which this stack has turned off", m.To)
		return
	}
	pr.dataSafe = canSnapshot
	pr.migrationSteps = &m
	planned := &deploy.PlannedMigration{From: man.TemplateVersion, To: m.To, Description: m.Description, Backup: m.Backup, Rollback: m.Rollback}
	for _, s := range m.Steps {
		switch {
		case s.Deploy:
			planned.Steps = append(planned.Steps, "start the new version")
		case s.Name != "":
			planned.Steps = append(planned.Steps, s.Name+" (in "+s.Service+")")
		default:
			planned.Steps = append(planned.Steps, "run in "+s.Service)
		}
	}
	p.Migration = planned
	// Its own confirmation: it changes data in a way a plan can't show.
	p.Destructive = append(p.Destructive, fmt.Sprintf("migrates data from template version %s to %s", planned.From, planned.To))
}

func (pr *prepared) migration(log io.Writer) *deploy.Migration {
	if pr.migrationSteps == nil {
		return nil
	}
	return deploy.NewMigration(*pr.migrationSteps, log)
}

func printMigration(w io.Writer, p *deploy.Plan) {
	if p.MigrationError != "" {
		fmt.Fprintf(w, "  ✗ can't upgrade: %s\n", p.MigrationError)
	}
	m := p.Migration
	if m == nil {
		return
	}
	fmt.Fprintf(w, "  migration %s → %s", m.From, m.To)
	if m.Description != "" {
		fmt.Fprintf(w, ": %s", m.Description)
	}
	fmt.Fprintln(w)
	for i, s := range m.Steps {
		fmt.Fprintf(w, "    %d. %s\n", i+1, s)
	}
	rollback := "restores the data snapshot and the previous version"
	if m.Rollback == "revert" {
		rollback = "goes back to the previous version, keeping the data as it is"
	}
	fmt.Fprintf(w, "    if a step fails: %s\n", rollback)
}
