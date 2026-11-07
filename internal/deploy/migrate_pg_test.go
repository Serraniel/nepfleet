// SPDX-License-Identifier: AGPL-3.0-or-later

package deploy

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nengine "github.com/Serraniel/neptide/pkg/engine"
	"github.com/Serraniel/neptide/pkg/snapshot"

	"github.com/Serraniel/nepfleet/internal/render"
	"github.com/Serraniel/nepfleet/internal/template"
)

// Each major version keeps its data in its own folder of the volume: a new
// Postgres can't start on the old one's files, so it starts empty beside them.
const postgresCompose = `services:
  db:
    image: postgres:%VERSION%-alpine
    stop_grace_period: 30s
    environment:
      POSTGRES_PASSWORD: test
      POSTGRES_DB: app
      PGDATA: /var/lib/postgresql/data/%VERSION%
    volumes: ["data:/var/lib/postgresql/data"]
    healthcheck:
      test: ["CMD", "pg_isready", "-U", "postgres", "-d", "app"]
      interval: 1s
      timeout: 3s
      retries: 60
volumes:
  data:
`

// The reference migration from docs/spec/template.md, on a real database: a real
// major upgrade that carries the data over, and one that fails and goes back.
func TestPostgresMigrationInDocker(t *testing.T) {
	if os.Getenv("NEPFLEET_TEST_DOCKER") == "" {
		t.Skip("set NEPFLEET_TEST_DOCKER to run with Docker")
	}
	dump := template.Step{Name: "dump", Service: "db", Run: []string{"sh", "-c", "pg_dump -U postgres -d app > /var/lib/postgresql/data/dump.sql"}}
	switchImages := template.Step{Name: "switch images", Deploy: true, Wait: "healthy"}
	load := template.Step{Name: "restore", Service: "db", Run: []string{"sh", "-c", "psql -U postgres -d app -v ON_ERROR_STOP=1 -f /var/lib/postgresql/data/dump.sql"}}
	broken := template.Step{Name: "restore", Service: "db", Run: []string{"sh", "-c", "psql -U postgres -d app -c 'CREATE TABLE half_done(x int)'; exit 1"}}
	for _, tc := range []struct {
		name        string
		restore     template.Step
		wantVersion string
		wantErr     bool
	}{
		{"carries the data over", load, "17", false},
		{"a failed restore goes back", broken, "16", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
			defer cancel()
			stack := "nepfleet-pg-test-" + strings.ReplaceAll(tc.name, " ", "-")
			dir := t.TempDir()
			file := filepath.Join(dir, render.ComposeFile)
			t.Cleanup(func() {
				_ = exec.Command("docker", "compose", "-p", stack, "-f", file, "down", "-v", "-t", "5").Run()
			})
			eng, err := nengine.New("")
			if err != nil {
				t.Fatal(err)
			}
			root, err := eng.DockerRoot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			p := &Plan{Stack: stack, Mode: "compose", Dir: dir}
			out := func(version string) *render.Output {
				return &render.Output{Files: map[string][]byte{
					render.ComposeFile: []byte(strings.ReplaceAll(postgresCompose, "%VERSION%", version)),
					render.EnvFile:     nil,
				}}
			}
			if _, err := Apply(ctx, Docker{}, p, out("16"), Options{Template: "t", TemplateVersion: "1.0.0", WaitTimeout: 2 * time.Minute}); err != nil {
				t.Fatal(err)
			}
			sql := func(query string) (string, error) {
				o, err := exec.CommandContext(ctx, "docker", "compose", "-p", stack, "-f", file, "exec", "-T", "db",
					"psql", "-U", "postgres", "-d", "app", "-tAc", query).CombinedOutput()
				return strings.TrimSpace(string(o)), err
			}
			if o, err := sql("CREATE TABLE notes(body text); INSERT INTO notes VALUES ('keep me')"); err != nil {
				t.Fatalf("%s: %v", o, err)
			}

			_, err = Apply(ctx, Docker{}, p, out("17"), Options{
				Template: "t", TemplateVersion: "2.0.0", WaitTimeout: 2 * time.Minute,
				Migration: NewMigration(template.Migration{Backup: "required", Rollback: "restore",
					Steps: []template.Step{dump, switchImages, tc.restore}}, nil),
				DataSafety: &DataSafety{Snapshots: &snapshot.Snapshotter{Engine: eng, Keep: 3, DockerRoot: root}, Engine: eng},
			})
			if tc.wantErr != (err != nil) {
				t.Fatalf("apply: %v", err)
			}
			if tc.wantErr && !errors.Is(err, ErrRolledBack) {
				t.Errorf("not rolled back: %v", err)
			}
			if got, err := sql("SHOW server_version"); err != nil || !strings.HasPrefix(got, tc.wantVersion+".") {
				t.Errorf("running version %q (%v), want %s", got, err, tc.wantVersion)
			}
			if got, err := sql("SELECT body FROM notes"); err != nil || got != "keep me" {
				t.Errorf("the data: %q (%v)", got, err)
			}
			if tc.wantErr {
				if got, err := sql("SELECT to_regclass('half_done')"); err != nil || got != "" {
					t.Errorf("the failed attempt left something: %q (%v)", got, err)
				}
			}
		})
	}
}
