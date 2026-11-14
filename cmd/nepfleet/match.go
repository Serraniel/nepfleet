// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/neptide/pkg/registry"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/render"
	"github.com/Serraniel/nepfleet/internal/template"
)

// templateMatch is a catalogue template that runs the same images as a
// plain stack.
type templateMatch struct {
	Template *template.Template
	// Same maps an image name to the stack's tag and the template's tag.
	Same map[string][2]string
	// OnlyStack and OnlyTemplate are images on one side only.
	OnlyStack, OnlyTemplate []string
	compose                 []byte
}

// cmdTemplateMatch: nepfleet template match <stack> — tells whether a
// catalogue template covers a plain (e.g. imported) stack and how the two
// differ, as the first step to adopting the template.
func cmdTemplateMatch(args []string, stdout, stderr io.Writer) error {
	var c common
	fs := flag.NewFlagSet("template match", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c.register(fs)
	src := fs.String("source", "official", "template source from fleet.yaml")
	withDiff := fs.Bool("diff", false, "also show how services with the same image differ: environment names (never values), ports and volumes")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: nepfleet template match <stack> [--source NAME] [--diff]")
	}
	e, err := c.open()
	if err != nil {
		return err
	}
	s, err := e.fleet.LoadStack(pos[0])
	if err != nil {
		return err
	}
	if s.Spec.Template != nil {
		fmt.Fprintf(stdout, "%s already uses the template %s/%s\n", s.Metadata.Name, s.Spec.Template.Source, s.Spec.Template.Name)
		return nil
	}
	matches, raw, err := e.templateMatches(s, *src)
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		fmt.Fprintf(stdout, "no template in %s runs the images of %s\n", *src, s.Metadata.Name)
		return nil
	}
	for _, m := range matches {
		printMatch(stdout, s.Metadata.Name, *src, m)
		if *withDiff {
			diffCompose(stdout, raw, m.compose)
		}
	}
	return nil
}

func printMatch(w io.Writer, stack, src string, m templateMatch) {
	t := m.Template
	fmt.Fprintf(w, "%s/%s %s (%s): %d of %d images in common\n", src, t.Metadata.Name, t.Spec.Version, t.Metadata.Title, len(m.Same), len(m.Same)+len(m.OnlyTemplate))
	names := make([]string, 0, len(m.Same))
	for n := range m.Same {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		tags := m.Same[n]
		if tags[0] == tags[1] {
			fmt.Fprintf(w, "  = %s:%s\n", n, tags[0])
		} else {
			fmt.Fprintf(w, "  ≠ %s: yours %s, template %s\n", n, cmpTag(tags[0]), cmpTag(tags[1]))
		}
	}
	for _, n := range m.OnlyTemplate {
		fmt.Fprintf(w, "  + %s (the template also runs this)\n", n)
	}
	for _, n := range m.OnlyStack {
		fmt.Fprintf(w, "  - %s (yours only)\n", n)
	}
	fmt.Fprintf(w, "  adopt it: nepfleet new %s %s-new, compare with nepfleet render, then move data over and remove the old stack\n", t.Metadata.Name, stack)
}

func cmpTag(t string) string {
	if t == "" {
		return "(no tag)"
	}
	return t
}

// matchImages compares image name → tag maps; a match needs at least one
// shared image that is not generic infrastructure (a database, a cache).
func matchImages(stack, tpl map[string]string) (templateMatch, bool) {
	m := templateMatch{Same: map[string][2]string{}}
	for n, tag := range tpl {
		if st, ok := stack[n]; ok {
			m.Same[n] = [2]string{st, tag}
		} else {
			m.OnlyTemplate = append(m.OnlyTemplate, n)
		}
	}
	for n := range stack {
		if _, ok := tpl[n]; !ok {
			m.OnlyStack = append(m.OnlyStack, n)
		}
	}
	slices.Sort(m.OnlyTemplate)
	slices.Sort(m.OnlyStack)
	if len(m.Same) == 0 {
		return m, false
	}
	// Shared infrastructure alone (postgres, redis) is not a match.
	generic := 0
	for n := range m.Same {
		if isGenericImage(n) {
			generic++
		}
	}
	return m, generic < len(m.Same)
}

// isGenericImage: databases and caches many apps share.
func isGenericImage(name string) bool {
	base := name[strings.LastIndex(name, "/")+1:]
	return slices.Contains([]string{"postgres", "mariadb", "mysql", "redis", "valkey", "mongo", "memcached", "socket-proxy", "busybox", "alpine"}, base)
}

// imageTags maps the images of a compose file (normalized names) to their
// tags; ${VAR} in an image is left out of the comparison.
func imageTags(compose []byte) map[string]string {
	var doc struct {
		Services map[string]struct {
			Image string `yaml:"image"`
		} `yaml:"services"`
	}
	out := map[string]string{}
	if yaml.Unmarshal(compose, &doc) != nil {
		return out
	}
	for _, svc := range doc.Services {
		if svc.Image == "" || strings.Contains(svc.Image, "${") {
			continue
		}
		r, err := registry.ParseRef(svc.Image)
		if err != nil {
			continue
		}
		out[r.Name()] = r.Tag
	}
	return out
}

// templateMatches finds the templates of source src that run a plain stack's
// images, best first, with the stack's compose file.
func (e *env) templateMatches(s *fleet.Stack, src string) ([]templateMatch, []byte, error) {
	raw, err := os.ReadFile(filepath.Join(s.Dir, render.ComposeFile))
	if err != nil {
		return nil, nil, err
	}
	stackImages := imageTags(raw)
	if len(stackImages) == 0 {
		return nil, nil, fmt.Errorf("%s: no images found in its compose file", s.Metadata.Name)
	}
	res, err := e.resolver.Resolve(src)
	if err != nil {
		return nil, nil, err
	}
	// A source may have apps only, or system stacks only.
	var roots []string
	for _, r := range []string{filepath.Join(res.Dir, "system"), filepath.Join(res.Dir, "apps")} {
		if fi, err := os.Stat(r); err == nil && fi.IsDir() {
			roots = append(roots, r)
		}
	}
	dirs, err := findTemplates(roots)
	if err != nil {
		return nil, nil, err
	}
	var matches []templateMatch
	for _, d := range dirs {
		t, err := template.Load(d)
		if err != nil {
			continue
		}
		tplRaw, err := os.ReadFile(filepath.Join(d, t.Spec.Compose))
		if err != nil {
			continue
		}
		if m, ok := matchImages(stackImages, imageTags(tplRaw)); ok {
			m.Template, m.compose = t, tplRaw
			matches = append(matches, m)
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return len(matches[i].Same) > len(matches[j].Same) })
	return matches, raw, nil
}

// matchAPI is GET /api/v1/stacks/{name}/match.
func matchAPI(c common, stack, src string) (api.TemplateMatches, error) {
	if !apiMu.TryLock() {
		return api.TemplateMatches{}, api.ErrBusy
	}
	defer apiMu.Unlock()
	e, err := c.open()
	if err != nil {
		return api.TemplateMatches{}, err
	}
	s, err := e.fleet.LoadStack(stack)
	if err != nil {
		return api.TemplateMatches{}, api.ErrNotFound
	}
	if t := s.Spec.Template; t != nil {
		return api.TemplateMatches{Template: t.Source + "/" + t.Name}, nil
	}
	if !slices.ContainsFunc(e.fleet.Spec.TemplateSources, func(ts fleet.Source) bool { return ts.Name == src }) {
		return api.TemplateMatches{}, api.InvalidError{Reason: "fleet.yaml has no template source " + src}
	}
	matches, _, err := e.templateMatches(s, src)
	if err != nil {
		return api.TemplateMatches{}, api.InvalidError{Reason: cleanReason(err, e.fleet.Dir)}
	}
	out := api.TemplateMatches{Matches: []api.TemplateMatch{}}
	for _, m := range matches {
		am := api.TemplateMatch{Source: src, Name: m.Template.Metadata.Name, Title: m.Template.Metadata.Title, Version: m.Template.Spec.Version,
			Same: []api.MatchedImage{}, OnlyStack: append([]string{}, m.OnlyStack...), OnlyTemplate: append([]string{}, m.OnlyTemplate...)}
		for _, n := range slices.Sorted(maps.Keys(m.Same)) {
			am.Same = append(am.Same, api.MatchedImage{Image: n, Stack: m.Same[n][0], Template: m.Same[n][1]})
		}
		out.Matches = append(out.Matches, am)
	}
	return out, nil
}
