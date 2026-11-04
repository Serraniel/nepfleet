// SPDX-License-Identifier: AGPL-3.0-or-later

package fleet

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInit(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"fleet.yaml", ".sops.yaml", "stacks/.gitkeep", ".git"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
	if err := Init(dir); !errors.Is(err, ErrExists) {
		t.Fatalf("second Init = %v, want ErrExists", err)
	}
}
