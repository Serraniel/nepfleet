// SPDX-License-Identifier: AGPL-3.0-or-later

package template

import (
	"os"
	"path/filepath"
	"testing"
)

// Templates come from third-party catalogues: loading and validating one
// must fail cleanly, never panic.
func FuzzTemplate(f *testing.F) {
	demo, err := os.ReadFile("testdata/demo/template.yaml")
	if err != nil {
		f.Fatal(err)
	}
	f.Add(demo, []byte("services:\n  web:\n    image: nginx:1.29\n"))
	f.Add([]byte("spec: {compose: ../outside.yaml, files: [{src: ../x, dest: y}]}"), []byte(""))
	f.Fuzz(func(t *testing.T, tpl, compose []byte) {
		dir := filepath.Join(t.TempDir(), "demo")
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(dir, "template.yaml"), tpl, 0o600)
		_ = os.WriteFile(filepath.Join(dir, "compose.yaml"), compose, 0o600)
		tp, err := Load(dir)
		if err != nil {
			return
		}
		_ = tp.Validate()
		_, _ = Index(filepath.Dir(dir), []*Template{tp})
	})
}
