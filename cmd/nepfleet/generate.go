// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/nepfleet/internal/template"
)

// cmdTemplateGenerate drafts a template from a compose file, in a new folder.
func cmdTemplateGenerate(args []string, stdout, stderr io.Writer) error {
	const usage = "usage: nepfleet template generate <compose.yaml> --name NAME [--out DIR]"
	fs := flag.NewFlagSet("template generate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "the template's name, and its folder's")
	out := fs.String("out", ".", "where to create the folder")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *name == "" {
		return errors.New(usage)
	}
	raw, err := os.ReadFile(pos[0])
	if err != nil {
		return err
	}
	d, err := template.DraftFrom(raw, *name)
	if err != nil {
		return err
	}
	dir := filepath.Join(*out, *name)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("%s already exists", dir)
	}
	manifest, err := yaml.Marshal(d.Template)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "template.yaml"), manifest, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), d.Compose, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "draft template in %s. Check these:\n", dir)
	for _, n := range d.Notes {
		fmt.Fprintf(stdout, "  - %s\n", n)
	}
	loaded, err := template.Load(dir)
	if err == nil {
		err = loaded.Validate()
	}
	if err != nil {
		return fmt.Errorf("the draft doesn't validate yet, so finish it by hand: %w", err)
	}
	fmt.Fprintln(stdout, "It validates, but it's still a draft: nothing has run it.")
	return nil
}
