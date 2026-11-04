// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
	"golang.org/x/term"

	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/render"
	"github.com/Serraniel/nepfleet/internal/template"
)

type setFlags map[string]string

func (s setFlags) String() string { return fmt.Sprint(map[string]string(s)) }
func (s setFlags) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok || k == "" {
		return errors.New("use --set KEY=VALUE")
	}
	s[k] = val
	return nil
}

// cmdNew is the CLI wizard: it asks for a template's inputs and writes the
// stack. Secret inputs are stored encrypted, never in stack.yaml.
func cmdNew(args []string, stdout, stderr io.Writer) error {
	var c common
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c.register(fs)
	source := fs.String("source", "official", "template source from fleet.yaml")
	target := fs.String("target", "local", "spec.target of the new stack")
	set := setFlags{}
	fs.Var(set, "set", "input value KEY=VALUE (repeatable); missing inputs are asked for")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 || len(pos) > 2 {
		return errors.New("usage: nepfleet new <template> [stack-name] [--set KEY=VALUE]")
	}
	if !fleet.ValidName(pos[0]) {
		return fmt.Errorf("%q is not a template name", pos[0])
	}
	e, err := c.open()
	if err != nil {
		return err
	}
	tpl, _, err := e.resolver.Template(fleet.TemplateRef{Source: *source, Name: pos[0]})
	if err != nil {
		return err
	}
	name := tpl.Metadata.Name
	if len(pos) == 2 {
		name = pos[1]
	}
	// Before anything touches the disk: the name becomes a directory.
	if !fleet.ValidName(name) {
		return fmt.Errorf("%q is not a valid stack name (lowercase letters, digits, - and _)", name)
	}
	known := map[string]bool{}
	for _, inp := range tpl.Spec.Inputs {
		known[inp.Name] = true
	}
	for k := range set {
		if !known[k] {
			return fmt.Errorf("%s is not an input of %s", k, tpl.Metadata.Name)
		}
	}
	dirName := "stacks"
	if tpl.Metadata.Tier == "system" {
		dirName = "system"
	}
	dir := filepath.Join(e.fleet.Dir, dirName, name)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("%s already exists", dir)
	}
	if _, err := e.fleet.LoadStack(name); err == nil {
		return fmt.Errorf("a stack named %s already exists", name)
	}

	interactive := term.IsTerminal(int(os.Stdin.Fd())) //nolint:gosec // file descriptors fit in int
	in := bufio.NewReader(os.Stdin)
	fmt.Fprintf(stdout, "%s: %s\n\n", tpl.Metadata.Title, tpl.Metadata.Description)
	values, secretVals := map[string]string{}, map[string]string{}
	for _, inp := range tpl.Spec.Inputs {
		v, given := set[inp.Name]
		delete(set, inp.Name)
		if !given && interactive {
			if v, err = askInput(stdout, in, inp); err != nil {
				return err
			}
		}
		if v == "" {
			if inp.Required && inp.Default == "" {
				return fmt.Errorf("%s is required (--set %s=…)", inp.Name, inp.Name)
			}
			continue // the default applies at render time and stays visible in the template
		}
		if inp.Type == "secret" {
			secretVals[inp.Name] = v
		} else {
			values[inp.Name] = v
		}
	}

	stack := map[string]any{
		"apiVersion": "nepfleet/v1alpha1",
		"kind":       "Stack",
		"metadata":   map[string]string{"name": name},
		"spec": map[string]any{
			"target":   *target,
			"apply":    "manual",
			"template": map[string]string{"source": *source, "name": tpl.Metadata.Name, "version": tpl.Spec.Version},
		},
	}
	if len(values) > 0 {
		stack["spec"].(map[string]any)["values"] = values
	}
	raw, err := yaml.Marshal(stack)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if err := os.WriteFile(filepath.Join(dir, "stack.yaml"), raw, 0o600); err != nil {
		cleanup()
		return err
	}
	if len(secretVals) > 0 {
		if err := e.store.Save(filepath.Join(dir, fleet.SecretsFile), secretVals); err != nil {
			cleanup()
			return fmt.Errorf("storing secrets (nothing was written): %w", err)
		}
	}
	// Validate now, so a typo surfaces here and not at apply time.
	s, err := e.fleet.LoadStack(name)
	if err == nil {
		_, err = render.Render(render.Input{Fleet: e.fleet, Stack: s, Template: tpl, Secrets: secretVals})
	}
	if err != nil {
		cleanup()
		return fmt.Errorf("the values do not render (nothing was written): %w", err)
	}
	fmt.Fprintf(stdout, "\nwrote %s\n", relTo(e.fleet.Dir, dir))
	if len(secretVals) > 0 {
		fmt.Fprintf(stdout, "secrets stored encrypted in %s\n", filepath.Join(relTo(e.fleet.Dir, dir), fleet.SecretsFile))
	}
	fmt.Fprintf(stdout, "next: nepfleet plan %s, then commit\n", name)
	return nil
}

func askInput(w io.Writer, in *bufio.Reader, inp template.Input) (string, error) {
	label := inp.Title
	if label == "" {
		label = inp.Name
	}
	if inp.Description != "" {
		fmt.Fprintf(w, "  %s\n", inp.Description)
	}
	hint := ""
	switch inp.Type {
	case "enum":
		hint = " [" + strings.Join(inp.Options, "/") + "]"
	case "bool":
		hint = " [true/false]"
	}
	if inp.Default != "" {
		hint += " (default " + inp.Default + ")"
	} else if !inp.Required {
		hint += " (optional)"
	}
	fmt.Fprintf(w, "%s%s: ", label, hint)
	if inp.Type == "secret" {
		b, err := term.ReadPassword(int(os.Stdin.Fd())) //nolint:gosec // file descriptors fit in int
		fmt.Fprintln(w)
		return string(b), err
	}
	line, err := in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
