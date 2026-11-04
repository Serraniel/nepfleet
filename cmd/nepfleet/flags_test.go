// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"flag"
	"strings"
	"testing"
)

func TestParseInterspersed(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	set := setFlags{}
	fs.Var(set, "set", "")
	target := fs.String("target", "local", "")
	pos, err := parseInterspersed(fs, []string{"caddy", "--set", "A=1", "edge", "--target", "nas", "--set=B=2", "--", "--literal"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(pos, " ") != "caddy edge --literal" || *target != "nas" || set["A"] != "1" || set["B"] != "2" {
		t.Fatalf("pos=%v target=%s set=%v", pos, *target, set)
	}
}
