// SPDX-License-Identifier: AGPL-3.0-or-later

package fleet

import (
	"os"
	"path/filepath"
	"testing"
)

// fleet.yaml and stack.yaml come from a git repository others may write
// to: parsing them must fail cleanly, never panic.
func FuzzLoad(f *testing.F) {
	f.Add([]byte(skeleton["fleet.yaml"]))
	f.Add([]byte("apiVersion: nepfleet/v1alpha1\nkind: Fleet\nmetadata: {name: x}\nspec: {templateSources: [{name: a, url: u, path: p}]}\n"))
	f.Add([]byte("kind: [\n"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "fleet.yaml"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		fl, err := Load(dir)
		if err != nil {
			return
		}
		_, _ = fl.Stacks()
	})
}

func FuzzLoadStack(f *testing.F) {
	f.Add([]byte("apiVersion: nepfleet/v1alpha1\nkind: Stack\nmetadata: {name: web}\nspec: {target: local, mode: swarm, template: {source: s, name: n}, values: {A: b}}\n"))
	f.Add([]byte("spec: {values: [1, 2]}"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		dir := filepath.Join(t.TempDir(), "web")
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "stack.yaml"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		_, _ = loadStack(dir)
	})
}
