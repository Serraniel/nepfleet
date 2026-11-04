// SPDX-License-Identifier: AGPL-3.0-or-later

package template

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"testing"
)

func TestIndex(t *testing.T) {
	demo, err := Load("testdata/demo")
	if err != nil {
		t.Fatal(err)
	}
	sys := &Template{Metadata: Metadata{Name: "zz-system", Tier: "system"}, Dir: filepath.Join("testdata", "x", "zz-system")}
	c, err := Index("testdata", []*Template{demo, sys})
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != "Catalogue" || len(c.Templates) != 2 {
		t.Fatalf("catalogue = %+v", c)
	}
	if c.Templates[0].Name != "zz-system" || c.Templates[1].Path != "demo" {
		t.Errorf("want system first and paths relative to the root: %+v", c.Templates)
	}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	// Frontends read these keys: no Go field names, no nulls for lists.
	if m := regexp.MustCompile(`"[A-Z][A-Za-z0-9]*":`).Find(raw); m != nil {
		t.Errorf("Go field name in JSON: %s", m)
	}
	if regexp.MustCompile(`:null`).Match(raw) {
		t.Errorf("null in JSON: %s", raw)
	}
	twin := &Template{Metadata: Metadata{Name: "zz-system", Tier: "app"}, Dir: filepath.Join("testdata", "y", "zz-system")}
	if _, err := Index("testdata", []*Template{sys, twin}); err == nil {
		t.Error("two templates with the same name must be refused")
	}
}
