// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/api"
)

func TestAddEgressAllow(t *testing.T) {
	for name, c := range map[string]struct{ in, want string }{
		"no egress yet": {
			"apiVersion: nepfleet/v1alpha1\nkind: Stack\nmetadata: {name: web}\n# the host\nspec:\n  target: nas # here\n",
			"  egress:\n    allow:\n      - api.example.org\n",
		},
		"a list already": {
			"spec:\n  egress:\n    mode: enforce\n    allow:\n      - old.example.org # keep\n",
			"      - old.example.org # keep\n      - api.example.org\n",
		},
		"an empty allow": {
			"spec:\n  egress:\n    allow:\n",
			"    allow:\n      - api.example.org\n",
		},
	} {
		out, added, err := addEgressAllow([]byte(c.in), []string{"api.example.org", "api.example.org"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(string(out), c.want) || !slices.Equal(added, []string{"api.example.org"}) {
			t.Errorf("%s:\n%s\nadded %v", name, out, added)
		}
		if strings.Contains(c.in, "# the host") && (!strings.Contains(string(out), "# the host") || !strings.Contains(string(out), "# here")) {
			t.Errorf("%s: comments lost:\n%s", name, out)
		}
	}
	if _, _, err := addEgressAllow([]byte("spec:\n  egress: [1]\n"), []string{"x"}); err == nil {
		t.Error("egress as a list")
	}
}

func TestEgressAllowAPI(t *testing.T) {
	c, dir, _ := rollbackFleet(t)
	res, err := egressAllowAPI(c, "web", []string{"api.example.org", "203.0.113.0/24"}, "ci")
	if err != nil || res.Commit == "" || len(res.Added) != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	if log := tgit(t, dir, "log", "-1", "--format=%an %s"); !strings.Contains(log, "ci chore(web): allow egress to api.example.org, 203.0.113.0/24") {
		t.Errorf("commit: %s", log)
	}
	again, err := egressAllowAPI(c, "web", []string{"api.example.org"}, "ci")
	if err != nil || again.Commit != "" || len(again.Added) != 0 {
		t.Errorf("nothing new: %+v %v", again, err)
	}
	var invalid api.InvalidError
	if _, err := egressAllowAPI(c, "web", []string{"*.example.org"}, "ci"); !errors.As(err, &invalid) {
		t.Errorf("a wildcard: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "stacks/web/stack.yaml"))
	if strings.Contains(string(raw), "*.example.org") {
		t.Error("the refused entry stayed")
	}
	if _, err := egressAllowAPI(c, "ghost", []string{"x.org"}, "ci"); !errors.Is(err, api.ErrNotFound) {
		t.Errorf("unknown stack: %v", err)
	}
}

func TestRenderAPI(t *testing.T) {
	c, _, _ := rollbackFleet(t)
	files, err := renderAPI(c, "web")
	if err != nil || len(files) == 0 {
		t.Fatalf("%v %v", files, err)
	}
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	if !slices.IsSorted(paths) || !slices.ContainsFunc(files, func(f api.RenderedFile) bool { return strings.Contains(f.Content, "nginx") }) {
		t.Errorf("files: %v", paths)
	}
	if _, err := renderAPI(c, "ghost"); !errors.Is(err, api.ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
}
