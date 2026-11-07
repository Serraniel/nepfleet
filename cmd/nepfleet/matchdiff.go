// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/neptide/pkg/registry"
)

// composeService is what the comparison looks at in a service.
type composeService struct {
	Name    string
	Image   string // normalized name, without the tag
	Env     map[string]bool
	Ports   []string
	Volumes []string
}

// composeServices reads the services of a compose file. Only environment
// names are kept: values may be secrets, and the comparison never prints them.
func composeServices(raw []byte) []composeService {
	var doc struct {
		Services map[string]struct {
			Image       string `yaml:"image"`
			Environment any    `yaml:"environment"`
			Ports       []any  `yaml:"ports"`
			Volumes     []any  `yaml:"volumes"`
		} `yaml:"services"`
	}
	if yaml.Unmarshal(raw, &doc) != nil {
		return nil
	}
	out := make([]composeService, 0, len(doc.Services))
	for name, s := range doc.Services {
		c := composeService{Name: name, Env: envNames(s.Environment), Ports: shortForms(s.Ports), Volumes: shortForms(s.Volumes)}
		if r, err := registry.ParseRef(s.Image); err == nil && !strings.Contains(s.Image, "${") {
			c.Image = r.Name()
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// envNames takes both forms of environment: a map (with merge keys resolved
// by the decoder), or a list of NAME=value.
func envNames(env any) map[string]bool {
	out := map[string]bool{}
	switch v := env.(type) {
	case map[string]any:
		for k := range v {
			out[k] = true
		}
	case []any:
		for _, e := range v {
			k, _, _ := strings.Cut(fmt.Sprint(e), "=")
			out[k] = true
		}
	}
	return out
}

// shortForms renders ports and volumes for reading; the long form becomes
// its source and target.
func shortForms(items []any) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		switch v := it.(type) {
		case map[string]any:
			if src, ok := v["source"]; ok {
				out = append(out, fmt.Sprintf("%v:%v", src, v["target"]))
			} else if pub, ok := v["published"]; ok {
				out = append(out, fmt.Sprintf("%v:%v", pub, v["target"]))
			} else {
				out = append(out, fmt.Sprintf("%v", v["target"]))
			}
		default:
			out = append(out, fmt.Sprint(v))
		}
	}
	return out
}

// diffCompose prints how the services both files run with the same image
// differ: what adopting the template would change.
func diffCompose(w io.Writer, stackRaw, tplRaw []byte) {
	tpl := composeServices(tplRaw)
	for _, s := range composeServices(stackRaw) {
		i := slices.IndexFunc(tpl, func(t composeService) bool { return s.Image != "" && t.Image == s.Image })
		if i < 0 {
			continue
		}
		t := tpl[i]
		fmt.Fprintf(w, "  service %s (template: %s)\n", s.Name, t.Name)
		onlyYours := sortedDiff(slices.Collect(maps.Keys(s.Env)), slices.Collect(maps.Keys(t.Env)))
		onlyTpl := sortedDiff(slices.Collect(maps.Keys(t.Env)), slices.Collect(maps.Keys(s.Env)))
		printSide(w, "environment, yours only", onlyYours)
		printSide(w, "environment, template only", onlyTpl)
		printSide(w, "ports, yours only", sortedDiff(s.Ports, t.Ports))
		printSide(w, "ports, template only", sortedDiff(t.Ports, s.Ports))
		printSide(w, "volumes, yours only", sortedDiff(s.Volumes, t.Volumes))
		printSide(w, "volumes, template only", sortedDiff(t.Volumes, s.Volumes))
	}
}

func printSide(w io.Writer, what string, items []string) {
	if len(items) > 0 {
		fmt.Fprintf(w, "    %s: %s\n", what, strings.Join(items, ", "))
	}
}

// sortedDiff is a without b, sorted.
func sortedDiff(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	slices.Sort(out)
	return out
}
