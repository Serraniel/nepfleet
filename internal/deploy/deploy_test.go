// SPDX-License-Identifier: AGPL-3.0-or-later

package deploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/lock"
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
	p, err := PlanStack("app", "compose", dir, v1, nil)
	if err != nil || !p.First || len(p.Changes) != 3 {
		t.Fatalf("first plan = %+v, %v", p, err)
	}
	if strings.Contains(p.Changes[0].Diff+p.Changes[1].Diff+p.Changes[2].Diff, "one") {
		t.Fatal("secret value leaked into the plan")
	}
	if _, err := Apply(ctx, r, p, v1, Options{Commit: "c1"}); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(filepath.Join(dir, ".env")); info.Mode().Perm() != 0o600 {
		t.Errorf(".env mode = %v", info.Mode().Perm())
	}
	if p, _ := PlanStack("app", "compose", dir, v1, nil); !p.Empty() {
		t.Fatalf("re-plan not empty: %+v", p.Changes)
	}

	// v2 drops the volume (destructive) and fails its health gate.
	v2 := output("services:\n  web: {image: 'nginx:2'}\n", "PASSWORD='two'\nX='1'\n")
	delete(v2.Files, "conf/a.conf")
	p, err = PlanStack("app", "compose", dir, v2, nil)
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
	_, err = Apply(ctx, r, p, v2, Options{Commit: "c2"})
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

// tagRunner simulates local image tags for pinning and rollback.
type tagRunner struct {
	tags    map[string]string // ref -> image ID
	byDig   map[string]string // name@digest -> image ID (once pulled)
	failUp  bool
	tagCmds []string
}

func (f *tagRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	call := strings.Join(args, " ")
	switch {
	case strings.Contains(call, "config --images"):
		return []byte("app:1\n"), nil
	case strings.HasPrefix(call, "image inspect --format {{.Id}} "):
		ref := args[len(args)-1]
		if id, ok := f.byDig[ref]; ok {
			return []byte(id), nil
		}
		if id, ok := f.tags[ref]; ok && !strings.Contains(ref, "@") {
			return []byte(id), nil
		}
		return nil, errors.New("no such image")
	case strings.HasPrefix(call, "image inspect"), strings.HasPrefix(call, "ps "), strings.HasPrefix(call, "network"):
		return []byte("[]"), nil
	case strings.HasPrefix(call, "pull "):
		ref := args[len(args)-1]
		f.byDig[ref] = "id-" + ref[strings.Index(ref, "@")+1:]
		return nil, nil
	case strings.HasPrefix(call, "tag "):
		src, dst := args[1], args[2]
		if id, ok := f.byDig[src]; ok {
			src = id
		}
		f.tags[dst] = src
		f.tagCmds = append(f.tagCmds, call)
		return nil, nil
	case strings.Contains(call, " up "):
		if f.failUp {
			f.failUp = false
			return nil, errors.New("unhealthy")
		}
	}
	return nil, nil
}

type fixed map[string]string

func (f fixed) Digest(_ context.Context, ref string) (string, error) { return f[ref], nil }

// TestRollbackRestoresTagWithoutRecordedDigests covers a previous version
// applied without pins (older manifest, or --no-lock): the failed attempt
// moved app:1 to the new digest; rollback must point it back.
func TestRollbackRestoresTagWithoutRecordedDigests(t *testing.T) {
	dir := t.TempDir()
	r := &tagRunner{tags: map[string]string{"app:1": "id-old"}, byDig: map[string]string{}}
	ctx := context.Background()
	v1 := output("services:\n  a: {image: 'app:1'}\n", "X='1'\n")
	p, _ := PlanStack("app", "compose", dir, v1, nil)
	if _, err := Apply(ctx, r, p, v1, Options{}); err != nil { // no lock: manifest without Images
		t.Fatal(err)
	}
	newDigest := "sha256:" + strings.Repeat("b", 64)
	l := &lock.Lock{Images: map[string]string{}}
	v2 := output("services:\n  a: {image: 'app:1', environment: {Y: '2'}}\n", "X='1'\n")
	p, _ = PlanStack("app", "compose", dir, v2, nil)
	r.failUp = true
	res, err := Apply(ctx, r, p, v2, Options{Lock: l, Resolver: fixed{"app:1": newDigest}, UpdateLock: true})
	if !errors.Is(err, ErrRolledBack) {
		t.Fatalf("expected rollback, got %v", err)
	}
	if r.tags["app:1"] != "id-old" {
		t.Fatalf("after rollback app:1 -> %s, want id-old (tag commands: %v)", r.tags["app:1"], r.tagCmds)
	}
	if res.LockChanged {
		t.Error("a failed apply must not ask to commit the lock")
	}
}

// swarmRunner reports a running service digest and fails the first deploy.
type swarmRunner struct {
	deploys []string
	failed  bool
}

func (f *swarmRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	call := strings.Join(args, " ")
	switch {
	case strings.Contains(call, "config --images"):
		return []byte("app:1\n"), nil
	case strings.Contains(call, "config --format yaml"):
		return []byte("services:\n  a:\n    image: app:1\n"), nil
	case strings.HasPrefix(call, "service ls"):
		return []byte("st_a\n"), nil
	case strings.HasPrefix(call, "service inspect"):
		return []byte("app:1@sha256:" + strings.Repeat("a", 64) + "\n"), nil
	case strings.HasPrefix(call, "stack deploy"):
		raw, _ := os.ReadFile(args[len(args)-2])
		f.deploys = append(f.deploys, string(raw))
		if !f.failed && len(f.deploys) == 2 {
			f.failed = true
			return nil, errors.New("update failed")
		}
	}
	return nil, nil
}

// TestSwarmRollbackPinsRunningDigest: a swarm stack applied without pins,
// then a failing --update-lock: the rollback deploy must pin the digest the
// service ran before, not the moved tag.
func TestSwarmRollbackPinsRunningDigest(t *testing.T) {
	dir := t.TempDir()
	r := &swarmRunner{}
	ctx := context.Background()
	v1 := output("services:\n  a: {image: 'app:1'}\n", "X='1'\n")
	p, _ := PlanStack("st", "swarm", dir, v1, nil)
	if _, err := Apply(ctx, r, p, v1, Options{}); err != nil {
		t.Fatal(err)
	}
	v2 := output("services:\n  a: {image: 'app:1', environment: {Y: '2'}}\n", "X='1'\n")
	p, _ = PlanStack("st", "swarm", dir, v2, nil)
	newDigest := "sha256:" + strings.Repeat("b", 64)
	_, err := Apply(ctx, r, p, v2, Options{Lock: &lock.Lock{Images: map[string]string{}}, Resolver: fixed{"app:1": newDigest}, UpdateLock: true})
	if !errors.Is(err, ErrRolledBack) {
		t.Fatalf("expected rollback, got %v", err)
	}
	last := r.deploys[len(r.deploys)-1]
	if !strings.Contains(last, "app:1@sha256:"+strings.Repeat("a", 64)) {
		t.Fatalf("rollback deploy does not pin the previously running digest:\n%s", last)
	}
}
