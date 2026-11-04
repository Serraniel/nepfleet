// SPDX-License-Identifier: AGPL-3.0-or-later

package lock

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fake map[string]string

func (f fake) Digest(_ context.Context, ref string) (string, error) {
	if d, ok := f[ref]; ok {
		return d, nil
	}
	return "", errors.New("not found")
}

func digest(c string) string { return "sha256:" + strings.Repeat(c, 64) }

func TestSync(t *testing.T) {
	ctx := context.Background()
	reg := fake{"nginx:1": digest("a"), "redis:8": digest("b")}
	l := &Lock{Images: map[string]string{"old:1": digest("c")}}

	changed, skipped, err := l.Sync(ctx, reg, []string{"nginx:1", "redis:8", "local/built:dev", "pinned@sha256:x"}, false)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if l.Images["nginx:1"] != digest("a") || l.Images["redis:8"] != digest("b") || len(l.Images) != 2 {
		t.Errorf("images = %v", l.Images)
	}
	if len(skipped) != 1 || !strings.HasPrefix(skipped[0], "local/built:dev") {
		t.Errorf("skipped = %v", skipped)
	}

	// Existing pins are kept even if the tag moved, unless update is set.
	reg["nginx:1"] = digest("d")
	if changed, _, _ := l.Sync(ctx, reg, []string{"nginx:1", "redis:8"}, false); changed || l.Images["nginx:1"] != digest("a") {
		t.Errorf("pin moved without update: %v", l.Images)
	}
	if changed, _, _ := l.Sync(ctx, reg, []string{"nginx:1", "redis:8"}, true); !changed || l.Images["nginx:1"] != digest("d") {
		t.Errorf("update did not move the pin: %v", l.Images)
	}
}

func TestLoadSave(t *testing.T) {
	dir := t.TempDir()
	l, err := Load(dir)
	if err != nil || len(l.Images) != 0 {
		t.Fatalf("empty load: %v %v", l, err)
	}
	l.Images["nginx:1"] = digest("a")
	if err := l.Save(dir); err != nil {
		t.Fatal(err)
	}
	back, err := Load(dir)
	if err != nil || back.Images["nginx:1"] != digest("a") {
		t.Fatalf("round trip: %v %v", back, err)
	}
}
