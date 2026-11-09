// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// tokenStore is where nepfleet serve keeps API tokens.
func tokenStore(stateDir string) *auth.Store {
	return &auth.Store{Dir: filepath.Join(stateDir, "api")}
}

// cmdToken: nepfleet token create|list|revoke
func cmdToken(args []string, stdout, stderr io.Writer) error {
	usage := errors.New("usage: nepfleet token create --name NAME --scope read|apply|edit|admin|notify [--stack a,b] [--expires 90d] | token list | token revoke <id>")
	if len(args) == 0 {
		return usage
	}
	var c common
	fs := flag.NewFlagSet("token "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	c.register(fs)
	name := fs.String("name", "", "what the token is for, e.g. ci or homepage")
	scope := fs.String("scope", auth.ScopeRead, "read, apply, edit (also change stack files) or admin; notify only sends alerts")
	stacks := fs.String("stack", "", "limit the token to these stacks (comma separated)")
	expires := fs.String("expires", "90d", "lifetime, e.g. 30d, 12h")
	pos, err := parseInterspersed(fs, args[1:])
	if err != nil {
		return err
	}
	store := tokenStore(c.stateDir)
	switch args[0] {
	case "create":
		ttl, err := parseLifetime(*expires)
		if err != nil {
			return err
		}
		var list []string
		for _, s := range strings.Split(*stacks, ",") {
			if s = strings.TrimSpace(s); s != "" {
				list = append(list, s)
			}
		}
		secret, t, err := store.Create(*name, *scope, list, ttl)
		if err != nil {
			return err
		}
		fmt.Fprintf(stderr, "token %s (%s, %s) valid until %s. It is shown only now:\n", t.ID, t.Name, t.Scope, t.Expires.Format("2006-01-02"))
		fmt.Fprintln(stdout, secret)
		return nil
	case "list":
		all, err := store.List()
		if err != nil {
			return err
		}
		if len(all) == 0 {
			fmt.Fprintln(stdout, "no tokens")
		}
		for _, t := range all {
			state := "valid until " + t.Expires.Format("2006-01-02")
			if !time.Now().Before(t.Expires) {
				state = "expired"
			}
			limit := "all stacks"
			if len(t.Stacks) > 0 {
				limit = strings.Join(t.Stacks, ",")
			}
			fmt.Fprintf(stdout, "%s  %-16s %-6s %-20s %s\n", t.ID, t.Name, t.Scope, limit, state)
		}
		return nil
	case "revoke":
		if len(pos) != 1 {
			return usage
		}
		if err := store.Revoke(pos[0]); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "token %s revoked\n", pos[0])
		return nil
	}
	return usage
}

// parseLifetime reads 90d, 12h, 30m (days are not a Go duration unit).
func parseLifetime(s string) (time.Duration, error) {
	if d, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(d)
		if err != nil || n <= 0 || n > 3650 {
			return 0, fmt.Errorf("--expires %q: want e.g. 90d", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("--expires %q: want e.g. 90d or 12h", s)
	}
	return d, nil
}
