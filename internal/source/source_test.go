// SPDX-License-Identifier: AGPL-3.0-or-later

package source

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/fleet"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false", "-c", "user.name=t", "-c", "user.email=t@example.org", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// writeDemo puts the template test fixture at apps/demo with a version.
func writeDemo(t *testing.T, repo, version string) {
	t.Helper()
	dir := filepath.Join(repo, "apps", "demo")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"template.yaml", "compose.yaml"} {
		raw, err := os.ReadFile(filepath.Join("..", "template", "testdata", "demo", f))
		if err != nil {
			t.Fatal(err)
		}
		if f == "template.yaml" {
			lines := strings.Split(string(raw), "\n")
			for i, l := range lines {
				if strings.HasPrefix(l, "  version: ") {
					lines[i] = "  version: " + version
				}
			}
			raw = []byte(strings.Join(lines, "\n"))
		}
		if err := os.WriteFile(filepath.Join(dir, f), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTemplateAtPinnedCommit(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "catalogue")
	if err := os.MkdirAll(repo, 0o750); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "init", "-q")
	writeDemo(t, repo, "1.0.0")
	run(t, repo, "add", "-A")
	run(t, repo, "commit", "-qm", "one")
	first := run(t, repo, "rev-parse", "HEAD")
	writeDemo(t, repo, "1.1.0")
	run(t, repo, "commit", "-qam", "two")

	f := &fleet.Fleet{Dir: root}
	f.Spec.TemplateSources = []fleet.Source{{Name: "official", URL: repo}}
	r := &Resolver{Fleet: f, Cache: filepath.Join(root, "cache")}
	ref := fleet.TemplateRef{Source: "official", Name: "demo"}

	tpl, res, err := r.Template(ref)
	if err != nil {
		t.Fatal(err)
	}
	if tpl.Spec.Version != "1.1.0" || !res.Pinnable || res.Commit == first {
		t.Fatalf("current: version %s, %+v", tpl.Spec.Version, res)
	}
	tpl, res, err = r.TemplateAt(ref, first)
	if err != nil {
		t.Fatal(err)
	}
	if tpl.Spec.Version != "1.0.0" || res.Commit != first {
		t.Fatalf("pinned: version %s, %+v", tpl.Spec.Version, res)
	}
	// The pinned checkout is reused, and the clone itself stays current.
	if _, _, err := r.TemplateAt(ref, first); err != nil {
		t.Fatal(err)
	}
	if tpl, _, _ := r.Template(ref); tpl.Spec.Version != "1.1.0" {
		t.Errorf("the clone moved to the pin: %s", tpl.Spec.Version)
	}
	if _, _, err := r.TemplateAt(ref, strings.Repeat("ab", 20)); err == nil || !strings.Contains(err.Error(), "--update-lock") {
		t.Errorf("unknown commit: %v", err)
	}
}
