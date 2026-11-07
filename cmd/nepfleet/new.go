// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
	"golang.org/x/term"

	"github.com/Serraniel/nepfleet/internal/doctor"
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
	access := fs.String("access", "domain", "how the web UI is reached: domain (Caddy) or port (a LAN port of the host)")
	port := fs.Int("port", 0, "with --access port: the host port (default: the lowest free one in the host's range)")
	set := setFlags{}
	fs.Var(set, "set", "input value KEY=VALUE (repeatable); missing inputs are asked for. Prefer the prompt for secrets: --set ends up in shell history")
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

	if *access != "domain" && *access != "port" {
		return errors.New("--access is domain or port")
	}
	if *access == "port" {
		if tpl.Spec.Web == nil {
			return fmt.Errorf("%s declares no web UI (spec.web), so it can only run behind Caddy", tpl.Metadata.Name)
		}
		if *port == 0 {
			if *port, err = e.pickPort(e.targetHost(*target), *target, name); err != nil {
				return err
			}
		} else {
			h, err := e.fleet.LoadHost(e.targetHost(*target))
			if err != nil {
				return err
			}
			if r := h.PortRange(); *port < r.From || *port > r.To {
				return fmt.Errorf("--port %d is outside %s's range %d-%d (hosts/%s.yaml spec.ports)", *port, *target, r.From, r.To, e.targetHost(*target))
			}
			if err := e.portFree(*target, *port, name); err != nil {
				return err
			}
			if owner, taken := e.renderedPorts(*target, name)[*port]; taken {
				return fmt.Errorf("port %d is already published by %s on %s", *port, owner, *target)
			}
		}
	}
	interactive := term.IsTerminal(int(os.Stdin.Fd())) //nolint:gosec // file descriptors fit in int
	in := bufio.NewReader(os.Stdin)
	fmt.Fprintf(stdout, "%s: %s\n\n", tpl.Metadata.Title, tpl.Metadata.Description)
	values, secretVals := map[string]string{}, map[string]string{}
	for _, inp := range tpl.Spec.Inputs {
		if *access == "port" && inp.Name == "DOMAIN" {
			continue // reached on the LAN port; NepFleet fills DOMAIN with the host's address
		}
		v, given := set[inp.Name]
		if given && v != "" {
			if err := checkValue(e.fleet, inp, v); err != nil {
				return fmt.Errorf("%s: %w", inp.Name, err)
			}
		}
		for !given && interactive {
			if v, err = askInput(stdout, in, inp, *target == "local" || *target == c.host, e.fleet.Spec.Paths); err != nil {
				return err
			}
			if v == "" {
				break
			}
			if err := checkValue(e.fleet, inp, v); err != nil {
				fmt.Fprintf(stdout, "  ✗ %v, try again\n", err)
				continue
			}
			break
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
	if *access == "port" {
		stack["spec"].(map[string]any)["access"] = map[string]any{"mode": "port", "port": *port}
	}
	raw, err := yaml.Marshal(stack)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if err := os.WriteFile(filepath.Join(dir, "stack.yaml"), append([]byte(fleet.Modeline("stack.schema.json")), raw...), 0o600); err != nil {
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
		var host *fleet.Host
		if *access == "port" {
			if host, err = e.fleet.LoadHost(e.targetHost(*target)); err != nil {
				return err
			}
		}
		_, err = render.Render(render.Input{Fleet: e.fleet, Stack: s, Template: tpl, Secrets: secretVals, Host: host})
	}
	if err != nil {
		cleanup()
		return fmt.Errorf("the values do not render (nothing was written): %w", err)
	}
	fmt.Fprintf(stdout, "\nwrote %s\n", relTo(e.fleet.Dir, dir))
	if *access == "port" {
		fmt.Fprintf(stdout, "reached on port %d of %s\n", *port, e.targetHost(*target))
	}
	if len(secretVals) > 0 {
		fmt.Fprintf(stdout, "secrets stored encrypted in %s\n", filepath.Join(relTo(e.fleet.Dir, dir), fleet.SecretsFile))
	}
	fmt.Fprintf(stdout, "next: nepfleet plan %s, then commit\n", name)
	return nil
}

// askInput asks for one value. local: the stack runs on this machine, so
// its hardware can be offered; paths are the fleet's shared folders.
func askInput(w io.Writer, in *bufio.Reader, inp template.Input, local bool, paths map[string]string) (string, error) {
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
	case "device":
		// Offer what this host has; stable by-id paths survive replugging.
		if !local {
			fmt.Fprintln(w, "  the stack runs on another host: give a device path there (/dev/serial/by-id/… is stable)")
		} else if hw := doctor.HostHardware("/"); len(hw.Serial) > 0 {
			fmt.Fprintln(w, "  devices on this host:")
			for _, d := range hw.Serial {
				fmt.Fprintf(w, "    %s\n", d)
			}
		} else {
			fmt.Fprintln(w, "  no USB serial devices found under /dev/serial/by-id")
		}
	case "gpu":
		hint = " [none/intel/amd/nvidia]"
		if found := doctor.HostHardware("/").GPUs; local && len(found) > 0 {
			hint += " (this host: " + strings.Join(found, ", ") + ")"
		}
	case "path":
		if len(paths) > 0 {
			fmt.Fprintln(w, "  shared folders (fleet.yaml spec.paths), use as @name:")
			for _, n := range slices.Sorted(maps.Keys(paths)) {
				fmt.Fprintf(w, "    @%-12s %s\n", n, paths[n])
			}
		}
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

// checkValue validates an input value, and that an @name is defined.
func checkValue(f *fleet.Fleet, inp template.Input, v string) error {
	if err := render.CheckInput(inp, v); err != nil {
		return err
	}
	if inp.Type == "path" && strings.HasPrefix(v, "@") {
		_, err := f.ResolvePath(v)
		return err
	}
	return nil
}
