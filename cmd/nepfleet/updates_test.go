// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"strings"
	"testing"
)

func TestPrintUpdates(t *testing.T) {
	var b strings.Builder
	printUpdates(&b, []imageUpdate{
		{Stack: "jf", Image: "jellyfin/jellyfin", Track: "minor", Current: "10.11.11", Next: "10.12.0", Beyond: "11.0.0"},
		{Stack: "jf", Image: "postgres", Track: "minor", Current: "18-alpine", Rebuilt: true},
		{Stack: "m", Image: "mealie", Track: "minor", Current: "v3.30.0"},
	})
	out := b.String()
	for _, want := range []string{"10.11.11 → 10.12.0 within track minor", "11.0.0 is out, beyond track minor", ":18-alpine was rebuilt upstream (nepfleet apply --update-lock jf)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "mealie") {
		t.Errorf("a current image is listed:\n%s", out)
	}
	b.Reset()
	printUpdates(&b, nil)
	if b.String() != "all images are current\n" {
		t.Errorf("empty: %q", b.String())
	}
}
