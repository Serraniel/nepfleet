// SPDX-License-Identifier: AGPL-3.0-or-later

package render

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/nepfleet/internal/fleet"
)

func TestWithLogging(t *testing.T) {
	in := []byte("services:\n  web:\n    image: nginx # pinned\n  db:\n    image: postgres\n    logging: {driver: syslog}\n")
	out, err := WithLogging(in, &fleet.DefaultLogging)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Services map[string]struct {
			Logging struct {
				Driver  string            `yaml:"driver"`
				Options map[string]string `yaml:"options"`
			} `yaml:"logging"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if web := doc.Services["web"].Logging; web.Driver != "json-file" || web.Options["max-size"] != "20m" {
		t.Errorf("web logging = %+v", web)
	}
	if db := doc.Services["db"].Logging; db.Driver != "syslog" || db.Options != nil {
		t.Errorf("a service's own logging must win: %+v", db)
	}
	if !strings.Contains(string(out), "# pinned") {
		t.Error("comments lost")
	}
}

func TestEffectiveLogging(t *testing.T) {
	off := false
	f := &fleet.Fleet{}
	s := &fleet.Stack{}
	if l := fleet.EffectiveLogging(f, s); l == nil || l.Driver != "json-file" {
		t.Errorf("default: %+v", l)
	}
	f.Spec.Logging = &fleet.Logging{Driver: "local"}
	if l := fleet.EffectiveLogging(f, s); l.Driver != "local" {
		t.Errorf("fleet setting: %+v", l)
	}
	s.Spec.Logging = &fleet.Logging{Enabled: &off}
	if l := fleet.EffectiveLogging(f, s); l != nil {
		t.Errorf("stack opt-out: %+v", l)
	}
}
