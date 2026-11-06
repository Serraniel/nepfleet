// SPDX-License-Identifier: AGPL-3.0-or-later

package lock

import (
	"os"
	"path/filepath"
	"testing"
)

func FuzzLoad(f *testing.F) {
	f.Add([]byte("images:\n  nginx:1.29: sha256:abc\n"))
	f.Add([]byte("images: [1]"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, File), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		_, _ = Load(dir)
	})
}
