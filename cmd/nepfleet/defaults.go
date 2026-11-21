// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/nepfleet/internal/fleet"
)

// Stacks made from a template were created with apply: manual written out,
// before apply: auto became the default. That line was NepFleet's choice,
// not the person's: a stack whose apply line no commit has touched since the
// one that created it takes the default now. A manual someone set stays.

// writtenManual says whether s carries the apply: manual its creation wrote
// and nothing changed since.
func writtenManual(e *env, s *fleet.Stack) (bool, error) {
	if s.Spec.Apply != "manual" || s.Spec.Template == nil {
		return false, nil
	}
	rel := relTo(e.fleet.Dir, filepath.Join(s.Dir, "stack.yaml"))
	out, err := gitOut(e.fleet.Dir, "log", "--format=%s", "-G", `^[[:space:]]*apply:`, "--", rel)
	if err != nil {
		return false, err
	}
	subjects := strings.Split(strings.TrimSpace(out), "\n")
	return len(subjects) == 1 && strings.HasPrefix(subjects[0], "feat("+s.Metadata.Name+"): new stack from "), nil
}

// adoptDefaults moves stacks with a written-out apply: manual to the
// default, one commit each, and says which. A fleet that syncs from origin
// is left alone: its stack files change there.
func adoptDefaults(e *env, log io.Writer) []string {
	if _, err := gitOut(e.fleet.Dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); err == nil {
		return nil
	}
	all, err := e.fleet.Stacks()
	if err != nil {
		fmt.Fprintf(log, "defaults: %v\n", err)
		return nil
	}
	var moved []string
	for _, s := range all {
		ok, err := writtenManual(e, s)
		if err != nil || !ok {
			continue
		}
		if err := setApplyAuto(e, s); err != nil {
			fmt.Fprintf(log, "defaults: %s: %v\n", s.Metadata.Name, err)
			continue
		}
		fmt.Fprintf(log, "defaults: %s applies by itself now, as new stacks do\n", s.Metadata.Name)
		moved = append(moved, s.Metadata.Name)
	}
	return moved
}

// setApplyAuto removes the written-out apply line, so the stack takes the
// default (auto), and commits that as NepFleet.
func setApplyAuto(e *env, s *fleet.Stack) error {
	path := filepath.Join(s.Dir, "stack.yaml")
	raw, err := os.ReadFile(path) //nolint:gosec // the fleet repository's own file
	if err != nil {
		return err
	}
	out, err := dropSpecKey(raw, "apply")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return err
	}
	rel := relTo(e.fleet.Dir, path)
	if _, err := gitOut(e.fleet.Dir, "add", "--", rel); err != nil {
		return err
	}
	msg := fmt.Sprintf("chore(%s): apply changes by itself, as new stacks do\n\nNepFleet wrote apply: manual when it created this stack. Set it again\nto review every change before it applies.", s.Metadata.Name)
	return commitAs(e.fleet.Dir, apiAuthor(reconcileAuthor), msg, rel)
}

// dropSpecKey removes spec.<key> from a stack.yaml, keeping its comments.
func dropSpecKey(raw []byte, key string) ([]byte, error) {
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
	for i := 0; i+1 < len(spec.Content); i += 2 {
		if spec.Content[i].Value == key {
			spec.Content = append(spec.Content[:i], spec.Content[i+2:]...)
			break
		}
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	return b.Bytes(), enc.Close()
}
