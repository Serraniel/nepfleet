// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import "flag"

// parseInterspersed parses flags anywhere in args (`new caddy --set A=b`),
// which flag.Parse alone does not: it stops at the first positional.
// It returns the positional arguments in order; "--" ends flag parsing.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	// Everything after a literal "--" is positional, whatever it looks like.
	var rest []string
	for i, a := range args {
		if a == "--" {
			args, rest = args[:i], args[i+1:]
			break
		}
	}
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return append(pos, rest...), nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}
