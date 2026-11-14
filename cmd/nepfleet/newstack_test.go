// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/api"
)

// A fleet with a local template source holding one template, "hello".
func newStackFleet(t *testing.T) (common, string) {
	t.Helper()
	c, dir, _ := rollbackFleet(t)
	src := t.TempDir()
	tpl := filepath.Join(src, "apps", "hello")
	if err := os.MkdirAll(tpl, 0o750); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"template.yaml": `apiVersion: nepfleet/v1alpha1
kind: Template
metadata:
  name: hello
  title: Hello
  description: Says hello.
  tier: app
  category: web
  homepage: https://example.org
spec:
  version: 0.1.0
  compose: compose.yaml
  images:
    - name: nginx
      track: minor
  inputs:
    - name: GREETING
      type: string
      title: Greeting
      required: true
    - name: LEVEL
      type: int
      title: Level
      default: "1"
`,
		"compose.yaml": "services:\n  web:\n    image: nginx:1.29-alpine\n    environment:\n      GREETING: ${GREETING}\n      LEVEL: ${LEVEL}\n",
	}
	for n, content := range files {
		if err := os.WriteFile(filepath.Join(tpl, n), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fy := filepath.Join(dir, "fleet.yaml")
	raw, err := os.ReadFile(fy) //nolint:gosec // the test's fleet
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fy, []byte(strings.Replace(string(raw), "  templateSources:\n", "  templateSources:\n    - name: local\n      path: "+src+"\n", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	tgit(t, dir, "add", "-A")
	tgit(t, dir, "commit", "-qm", "templates", "--no-verify")
	return c, dir
}

func TestNewStackAPI(t *testing.T) {
	c, dir := newStackFleet(t)
	res, err := newStackAPI(c, api.NewStack{Template: "hello", Source: "local", Name: "greeter", Values: map[string]string{"GREETING": "hi"}}, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "greeter" || res.Commit == "" {
		t.Errorf("result: %+v", res)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "stacks", "greeter", "stack.yaml")) //nolint:gosec // the test's fleet
	if err != nil || !strings.Contains(string(raw), "GREETING: hi") || !strings.Contains(string(raw), "name: hello") {
		t.Errorf("stack.yaml: %s %v", raw, err)
	}
	if got := tgit(t, dir, "log", "-1", "--format=%an|%s"); got != "Ada|feat(greeter): new stack from hello" {
		t.Errorf("commit: %s", got)
	}
	if s := tgit(t, dir, "status", "--porcelain"); s != "" {
		t.Errorf("left dirty: %s", s)
	}
	var invalid api.InvalidError
	for _, c2 := range []struct {
		req  api.NewStack
		want string
	}{
		{api.NewStack{Template: "hello", Source: "local", Name: "greeter", Values: map[string]string{"GREETING": "x"}}, "exists"},
		{api.NewStack{Template: "hello", Source: "local", Name: "two"}, "GREETING is required"},
		{api.NewStack{Template: "hello", Source: "local", Name: "two", Values: map[string]string{"GREETING": "x", "OTHER": "y"}}, "not an input"},
		{api.NewStack{Template: "hello", Source: "local", Name: "Bad Name", Values: map[string]string{"GREETING": "x"}}, "not a valid stack name"},
		{api.NewStack{Template: "../etc", Source: "local"}, "not a template name"},
	} {
		_, err := newStackAPI(c, c2.req, "Ada")
		if c2.want == "exists" {
			if !errors.Is(err, api.ErrStackExists) {
				t.Errorf("an existing stack: %v", err)
			}
			continue
		}
		if !errors.As(err, &invalid) || !strings.Contains(invalid.Reason, c2.want) {
			t.Errorf("%+v: %v, want %q", c2.req, err, c2.want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "stacks", "two")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a refused stack left files")
	}
}
