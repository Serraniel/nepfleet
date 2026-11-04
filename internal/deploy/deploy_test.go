// SPDX-License-Identifier: AGPL-3.0-or-later

package deploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/render"
)

// fakeRunner fails `compose up` while failUp is set and records calls.
type fakeRunner struct {
	failUp bool
	calls  []string
}

func (f *fakeRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	call := strings.Join(args, " ")
	f.calls = append(f.calls, call)
	if f.failUp && strings.Contains(call, " up ") {
		f.failUp = false // the rollback's up succeeds
		return nil, errors.New("container web is unhealthy")
	}
	return nil, nil
}

func output(compose, env string) *render.Output {
	return &render.Output{
		Files:  map[string][]byte{render.ComposeFile: []byte(compose), render.EnvFile: []byte(env), "conf/a.conf": []byte("a")},
		Secret: map[string]bool{"PASSWORD": true},
	}
}

func TestPlanApplyRollback(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{}
	ctx := context.Background()

	v1 := output("services:\n  web: {image: 'nginx:1'}\nvolumes:\n  data: {}\n", "PASSWORD='one'\nX='1'\n")
	p, err := PlanStack("app", "compose", dir, v1)
	if err != nil || !p.First || len(p.Changes) != 3 {
		t.Fatalf("first plan = %+v, %v", p, err)
	}
	if strings.Contains(p.Changes[0].Diff+p.Changes[1].Diff+p.Changes[2].Diff, "one") {
		t.Fatal("secret value leaked into the plan")
	}
	if err := Apply(ctx, r, p, v1, Options{Commit: "c1"}); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(filepath.Join(dir, ".env")); info.Mode().Perm() != 0o600 {
		t.Errorf(".env mode = %v", info.Mode().Perm())
	}
	if p, _ := PlanStack("app", "compose", dir, v1); !p.Empty() {
		t.Fatalf("re-plan not empty: %+v", p.Changes)
	}

	// v2 drops the volume (destructive) and fails its health gate.
	v2 := output("services:\n  web: {image: 'nginx:2'}\n", "PASSWORD='two'\nX='1'\n")
	delete(v2.Files, "conf/a.conf")
	p, err = PlanStack("app", "compose", dir, v2)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Destructive) != 1 || !strings.Contains(p.Destructive[0], `volume "data"`) {
		t.Errorf("destructive = %v", p.Destructive)
	}
	var kinds []string
	for _, c := range p.Changes {
		kinds = append(kinds, c.Path+":"+string(c.Kind))
	}
	if got := strings.Join(kinds, " "); got != ".env:changed compose.yaml:changed conf/a.conf:removed" {
		t.Errorf("changes = %s", got)
	}
	r.failUp = true
	err = Apply(ctx, r, p, v2, Options{Commit: "c2"})
	if !errors.Is(err, ErrRolledBack) {
		t.Fatalf("expected rollback, got %v", err)
	}
	for path, want := range map[string]string{"compose.yaml": "nginx:1", ".env": "one", "conf/a.conf": "a"} {
		if raw, _ := os.ReadFile(filepath.Join(dir, path)); !strings.Contains(string(raw), want) {
			t.Errorf("%s not restored: %q", path, raw)
		}
	}
	if m, _ := Applied(dir); m.Commit != "c1" {
		t.Errorf("manifest commit = %q, want c1 (failed apply must not be recorded)", m.Commit)
	}
	ups := 0
	for _, c := range r.calls {
		if strings.Contains(c, " up ") {
			ups++
		}
	}
	if ups != 3 {
		t.Errorf("expected 3 compose up calls (v1, failed v2, rollback), got %d: %v", ups, r.calls)
	}
}

func TestDiff(t *testing.T) {
	d := diff([]byte("a\nb\nc\nd\ne\nf\ng\n"), []byte("a\nb\nc\nX\ne\nf\ng\n"))
	if !strings.Contains(d, "- d\n+ X\n") || strings.Contains(d, "  a\n") {
		t.Errorf("diff:\n%s", d)
	}
}
