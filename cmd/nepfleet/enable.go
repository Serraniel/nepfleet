// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
)

// feature is an opt-in part of NepFleet, run as a system stack from the
// official catalogue.
type feature struct {
	Name, Template, What string
}

var features = []feature{
	{"ingress", "caddy", "reverse proxy with automatic TLS for every stack's domain"},
	{"logs", "dozzle", "container logs in the browser"},
	{"uptime", "uptime-kuma", "monitors and status pages, created from the stacks' labels"},
	{"dashboard", "homepage", "a start page with a tile per stack"},
	{"updates", "neptide", "health-gated image updates with rollback"},
	{"backups", "volume-backup", "scheduled, encrypted backups of the volumes on a host"},
	{"tunnel", "frpc", "publish services at home through a public host"},
	{"tunnel-server", "frps", "the public end of tunnels, behind Caddy"},
	{"emulation", "binfmt", "run images built for another CPU"},
}

// cmdEnable adds a feature's system stack (nepfleet new <template>); without
// a feature it lists them and the stacks that already provide them. The
// stacks are ordinary stacks: versioned, planned and rolled back as usual.
func cmdEnable(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return listFeatures(args, stdout, stderr)
	}
	i := slices.IndexFunc(features, func(f feature) bool { return f.Name == args[0] })
	if i < 0 {
		var names []string
		for _, f := range features {
			names = append(names, f.Name)
		}
		return fmt.Errorf("unknown feature %q (one of %s)", args[0], strings.Join(names, ", "))
	}
	f := features[i]
	// Refuse a second stack for the same feature unless it gets its own name
	// (e.g. dozzle on another host): nepfleet new <template> <name>.
	if pos := positional(args[1:]); len(pos) == 0 {
		if used, err := stacksUsing(args[1:], stderr); err == nil && len(used[f.Template]) > 0 {
			return fmt.Errorf("%s is already enabled (%s); to add another, give a stack name: nepfleet enable %s <name> --target <host>", f.Name, strings.Join(used[f.Template], ", "), f.Name)
		}
	}
	return cmdNew(append([]string{f.Template}, args[1:]...), stdout, stderr)
}

func listFeatures(args []string, stdout, stderr io.Writer) error {
	used, err := stacksUsing(args, stderr)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Features (nepfleet enable <feature>):")
	for _, f := range features {
		state := "off"
		if s := used[f.Template]; len(s) > 0 {
			state = "on: " + strings.Join(s, ", ")
		}
		fmt.Fprintf(stdout, "  %-14s %-14s %s (%s)\n", f.Name, f.Template, f.What, state)
	}
	return nil
}

// stacksUsing maps official template names to the stacks (name@target) that
// use them, across the whole fleet.
func stacksUsing(args []string, stderr io.Writer) (map[string][]string, error) {
	var c common
	fs := flag.NewFlagSet("enable", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	c.register(fs)
	fs.String("source", "", "")
	fs.String("target", "", "")
	fs.Var(setFlags{}, "set", "")
	if _, err := parseInterspersed(fs, args); err != nil {
		fs.SetOutput(stderr)
		return nil, err
	}
	e, err := c.open()
	if err != nil {
		return nil, err
	}
	all, err := e.fleet.Stacks()
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, s := range all {
		if t := s.Spec.Template; t != nil && t.Source == "official" {
			out[t.Name] = append(out[t.Name], s.Metadata.Name+"@"+s.Spec.Target)
		}
	}
	return out, nil
}

// positional returns the arguments that are not flags or flag values.
func positional(args []string) []string {
	fs := flag.NewFlagSet("enable", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var c common
	c.register(fs)
	fs.String("source", "", "")
	fs.String("target", "", "")
	fs.Var(setFlags{}, "set", "")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return nil
	}
	return pos
}
