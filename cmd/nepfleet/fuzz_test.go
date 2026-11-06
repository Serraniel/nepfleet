// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"flag"
	"io"
	"slices"
	"strings"
	"testing"
)

// Flags may come before, between or after positional arguments; whatever
// follows "--" is positional, verbatim.
func FuzzParseInterspersed(f *testing.F) {
	f.Add("apply\x00--yes\x00web\x00--\x00--yes")
	f.Add("-host=a\x00-host\x00\x00x")
	f.Add("--\x00--")
	f.Add("-\x00--=\x00---x")
	f.Fuzz(func(t *testing.T, joined string) {
		args := strings.Split(joined, "\x00")
		fs := flag.NewFlagSet("x", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		fs.Bool("yes", false, "")
		fs.String("host", "", "")
		pos, err := parseInterspersed(fs, args)
		if err != nil {
			return
		}
		if i := slices.Index(args, "--"); i >= 0 {
			tail := args[i+1:]
			if len(pos) < len(tail) || !slices.Equal(pos[len(pos)-len(tail):], tail) {
				t.Fatalf("args after -- changed: %q gave %q", args, pos)
			}
		}
	})
}
