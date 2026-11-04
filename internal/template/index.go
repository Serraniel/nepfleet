// SPDX-License-Identifier: AGPL-3.0-or-later

package template

import (
	"path/filepath"
	"sort"
)

// Catalogue is index.json: what a frontend needs to list, filter and
// install the templates of one source without reading every template.yaml.
type Catalogue struct {
	APIVersion string  `json:"apiVersion"`
	Kind       string  `json:"kind"`
	Templates  []Entry `json:"templates"`
}

// Entry is one template in a Catalogue.
type Entry struct {
	Metadata
	// Path is the template's directory relative to the catalogue root,
	// with forward slashes.
	Path     string   `json:"path"`
	Version  string   `json:"version"`
	Images   []Image  `json:"images"`
	Supports Supports `json:"supports"`
	Inputs   []Input  `json:"inputs"`
	Volumes  []Volume `json:"volumes"`
	// Migrations lists the from → to pairs a template can migrate.
	Migrations []MigrationRef `json:"migrations"`
}

// MigrationRef names one migration without its steps.
type MigrationRef struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Backup string `json:"backup"`
}

// Index builds the catalogue of templates found under root, sorted by tier
// (system first) and name. Templates must already be valid.
func Index(root string, ts []*Template) (*Catalogue, error) {
	c := &Catalogue{APIVersion: APIVersion, Kind: "Catalogue", Templates: []Entry{}}
	for _, t := range ts {
		rel, err := filepath.Rel(root, t.Dir)
		if err != nil {
			return nil, err
		}
		e := Entry{
			Metadata: t.Metadata, Path: filepath.ToSlash(rel), Version: t.Spec.Version,
			Images: nonNil(t.Spec.Images), Supports: t.Spec.Supports,
			Inputs: nonNil(t.Spec.Inputs), Volumes: nonNil(t.Spec.Volumes), Migrations: []MigrationRef{},
		}
		for _, m := range t.Spec.Migrations {
			e.Migrations = append(e.Migrations, MigrationRef{m.From, m.To, m.Backup})
		}
		c.Templates = append(c.Templates, e)
	}
	sort.Slice(c.Templates, func(i, j int) bool {
		a, b := c.Templates[i], c.Templates[j]
		if a.Tier != b.Tier {
			return a.Tier == "system"
		}
		return a.Name < b.Name
	})
	return c, nil
}

// nonNil keeps empty lists as [] in JSON, so frontends need no null checks.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
