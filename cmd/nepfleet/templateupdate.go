// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/template"
)

// templateUpdate is a newer version of a stack's template in its source.
type templateUpdate struct {
	from, to  string
	sameMajor bool
}

// newerTemplate finds a newer version of the stack's template in its
// source, as the source is now. None (nil) for a plain stack, an unpinned
// one, or one already at the newest.
func (e *env) newerTemplate(s *fleet.Stack) (*templateUpdate, error) {
	ref := s.Spec.Template
	if ref == nil || ref.Version == "" {
		return nil, nil
	}
	now := *ref
	now.Version = "" // whatever the source has
	tpl, _, err := e.resolver.Template(now)
	if err != nil {
		return nil, err
	}
	newer, same, err := template.NewerWithinMajor(ref.Version, tpl.Spec.Version)
	if err != nil || !newer {
		return nil, err
	}
	return &templateUpdate{from: ref.Version, to: tpl.Spec.Version, sameMajor: same}, nil
}

// moveTemplate pins the stack to template version to in its stack.yaml,
// keeping its comments, and commits that as author. Applying it is the
// caller's: with the template's image updates, which a new template version
// may need (an image rebuilt under the same tag with what it uses).
func (e *env) moveTemplate(s *fleet.Stack, u *templateUpdate, author string) error {
	path := filepath.Join(s.Dir, "stack.yaml")
	raw, err := os.ReadFile(path) //nolint:gosec // the fleet repository's own file
	if err != nil {
		return err
	}
	out, err := setTemplateVersion(raw, u.to)
	if err != nil {
		return fmt.Errorf("%s: %w", s.Metadata.Name, err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return err
	}
	rel := relTo(e.fleet.Dir, path)
	if _, err := gitOut(e.fleet.Dir, "add", "--", rel); err != nil {
		return err
	}
	msg := fmt.Sprintf("chore(%s): template %s %s → %s", s.Metadata.Name, s.Spec.Template.Name, u.from, u.to)
	return commitAs(e.fleet.Dir, apiAuthor(author), msg, rel)
}

// setTemplateVersion sets spec.template.version in a stack.yaml, keeping its
// comments.
func setTemplateVersion(raw []byte, version string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, errors.New("stack.yaml is empty")
	}
	spec, err := mappingChild(doc.Content[0], "spec", yaml.MappingNode)
	if err != nil {
		return nil, err
	}
	tpl, err := mappingChild(spec, "template", yaml.MappingNode)
	if err != nil {
		return nil, err
	}
	v := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: version}
	set := false
	for i := 0; i+1 < len(tpl.Content); i += 2 {
		if tpl.Content[i].Value == "version" {
			tpl.Content[i+1] = v
			set = true
		}
	}
	if !set {
		tpl.Content = append(tpl.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "version"}, v)
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	return b.Bytes(), enc.Close()
}
