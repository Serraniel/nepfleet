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

func TestNewMigrationSplitsAtDeploy(t *testing.T) {
	run := func(n string) template.Step { return template.Step{Name: n, Service: "db", Run: []string{n}} }
	m := NewMigration(template.Migration{Rollback: "restore", Steps: []template.Step{run("a"), {Deploy: true}, run("b")}}, nil)
	if len(m.Before) != 1 || m.Before[0].Name != "a" || len(m.After) != 1 || m.After[0].Name != "b" || !m.RestoreData {
		t.Errorf("%+v", m)
	}
	m = NewMigration(template.Migration{Rollback: "revert", Steps: []template.Step{run("a")}}, nil)
	if len(m.Before) != 1 || len(m.After) != 0 || m.RestoreData {
		t.Errorf("no deploy step: the new version starts last: %+v", m)
	}
	var none *Migration
	if none.before(context.Background(), nil, "x") != nil || none.after(context.Background(), nil, "x") != nil {
		t.Error("no migration runs nothing")
	}
}

type stepRunner struct {
	calls [][]string
	ps    string
	fail  error
}

func (r *stepRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, args)
	if args[0] == "ps" {
		return []byte(r.ps), nil
	}
	return nil, r.fail
}

func TestRunStepStaysInTheStack(t *testing.T) {
	r := &stepRunner{ps: "abc\n"}
	s := template.Step{Service: "db", Run: []string{"pg_dumpall", "-U", "postgres"}}
	if err := runStep(context.Background(), r, "st", s); err != nil {
		t.Fatal(err)
	}
	ps := strings.Join(r.calls[0], " ")
	if !strings.Contains(ps, "project=st") || !strings.Contains(ps, "service=db") {
		t.Errorf("%s", ps)
	}
	if got := strings.Join(r.calls[1], " "); got != "exec abc pg_dumpall -U postgres" {
		t.Errorf("%s", got)
	}
	r = &stepRunner{}
	if err := runStep(context.Background(), r, "st", s); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Errorf("%v", err)
	}
}

type readyRunner struct{ statuses []string }

func (r *readyRunner) Run(context.Context, string, ...string) ([]byte, error) {
	s := r.statuses[0]
	if len(r.statuses) > 1 {
		r.statuses = r.statuses[1:]
	}
	return []byte(s + "\n"), nil
}

func TestWaitReady(t *testing.T) {
	ctx := context.Background()
	if err := waitReady(ctx, &readyRunner{statuses: []string{"starting", "healthy"}}, []string{"a"}, 10*time.Second); err != nil {
		t.Error(err)
	}
	if err := waitReady(ctx, &readyRunner{statuses: []string{"none"}}, []string{"a"}, time.Second); err != nil {
		t.Error("no health check counts as ready:", err)
	}
	if err := waitReady(ctx, &readyRunner{statuses: []string{"unhealthy"}}, []string{"a"}, time.Millisecond); err == nil {
		t.Error("an unhealthy container must time out")
	}
}

func TestRequireSnapshot(t *testing.T) {
	for _, tc := range []struct {
		backup, rollback string
		want             bool
	}{{"required", "revert", true}, {"none", "restore", true}, {"recommended", "revert", false}} {
		if got := NewMigration(template.Migration{Backup: tc.backup, Rollback: tc.rollback}, nil).RequireSnapshot; got != tc.want {
			t.Errorf("%+v: %v", tc, got)
		}
	}
	var none *guard
	if none.holdsData() || (&guard{}).holdsData() || (&guard{snaps: []*snapshot.Snapshot{{}}}).holdsData() {
		t.Error("an empty snapshot holds no data")
	}
}

const migrateCompose = `services:
  db:
    image: alpine:3.22
    stop_grace_period: 1s
    environment:
      MARK: "%MARK%"
    command: ["sh", "-c", "[ -f /data/version ] || echo v1 > /data/version; exec sleep 600"]
    volumes: ["data:/data"]
volumes:
  data:
`

// With real containers, volumes and snapshots: a migration moves the data, and a
// failed one leaves the old version running on what the rollback says.
func TestMigrationInDocker(t *testing.T) {
	if os.Getenv("NEPFLEET_TEST_DOCKER") == "" {
		t.Skip("set NEPFLEET_TEST_DOCKER to run with Docker")
	}
	dump := template.Step{Name: "dump", Service: "db", Run: []string{"cp", "/data/version", "/data/dump"}}
	deployStep := template.Step{Deploy: true, Wait: "healthy"}
	write := func(s string) template.Step {
		return template.Step{Name: "restore", Service: "db", Run: []string{"sh", "-c", "echo " + s + " > /data/version"}}
	}
	fail := template.Step{Name: "fail", Service: "db", Run: []string{"sh", "-c", "echo broken > /data/version; exit 1"}}
	for _, tc := range []struct {
		name              string
		m                 template.Migration
		wantErr           bool
		wantVersion, mark string
		wantDump          bool
		manifestVersion   string
	}{
		{"works", template.Migration{Rollback: "restore", Steps: []template.Step{dump, deployStep, write("v2")}}, false, "v2", "2", true, "2.0.0"},
		{"restore", template.Migration{Rollback: "restore", Steps: []template.Step{dump, deployStep, fail}}, true, "v1", "1", false, "1.0.0"},
		{"revert", template.Migration{Rollback: "revert", Steps: []template.Step{dump, deployStep, fail}}, true, "broken", "1", true, "1.0.0"},
		{"fails before the deploy", template.Migration{Rollback: "restore", Steps: []template.Step{fail, deployStep}}, true, "v1", "1", false, "1.0.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			stack := "nepfleet-migrate-test-" + strings.ReplaceAll(tc.name, " ", "-")
			dir := t.TempDir()
			t.Cleanup(func() {
				_ = exec.Command("docker", "compose", "-p", stack, "-f", filepath.Join(dir, render.ComposeFile), "down", "-v", "-t", "1").Run()
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
			out := func(mark string) *render.Output {
				return &render.Output{Files: map[string][]byte{
					render.ComposeFile: []byte(strings.ReplaceAll(migrateCompose, "%MARK%", mark)),
					render.EnvFile:     nil,
				}}
			}
			if _, err := Apply(ctx, Docker{}, p, out("1"), Options{Template: "t", TemplateVersion: "1.0.0", WaitTimeout: time.Minute}); err != nil {
				t.Fatal(err)
			}
			_, err = Apply(ctx, Docker{}, p, out("2"), Options{
				Template: "t", TemplateVersion: "2.0.0", WaitTimeout: time.Minute,
				Migration:  NewMigration(tc.m, nil),
				DataSafety: &DataSafety{Snapshots: &snapshot.Snapshotter{Engine: eng, Keep: 3, DockerRoot: root}, Engine: eng},
			})
			if tc.wantErr != (err != nil) {
				t.Fatalf("apply: %v", err)
			}
			if tc.wantErr && !errors.Is(err, ErrRolledBack) {
				t.Errorf("not rolled back: %v", err)
			}
			read := func(args ...string) string {
				o, err := exec.CommandContext(ctx, "docker", append([]string{"compose", "-p", stack, "-f", filepath.Join(dir, render.ComposeFile), "exec", "-T", "db"}, args...)...).CombinedOutput()
				if err != nil {
					return "error: " + strings.TrimSpace(string(o))
				}
				return strings.TrimSpace(string(o))
			}
			if got := read("cat", "/data/version"); got != tc.wantVersion {
				t.Errorf("data: %q, want %q", got, tc.wantVersion)
			}
			if got := read("printenv", "MARK"); got != tc.mark {
				t.Errorf("running version: %q, want %q", got, tc.mark)
			}
			if got := !strings.HasPrefix(read("cat", "/data/dump"), "error"); got != tc.wantDump {
				t.Errorf("dump exists: %v", got)
			}
			man, err := Applied(dir)
			if err != nil || man.TemplateVersion != tc.manifestVersion {
				t.Errorf("manifest: %+v %v", man, err)
			}
		})
	}
}
