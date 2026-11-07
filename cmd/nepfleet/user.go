// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// cmdUser: nepfleet user list | remove <name> — who may sign in. Runs on the
// server: access to its state directory is what recovers a lost passkey.
func cmdUser(args []string, stdout, stderr io.Writer) error {
	const usage = "usage: nepfleet user list | nepfleet user remove NAME [--state DIR]"
	if len(args) == 0 {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet("user "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	state := fs.String("state", envOr("NEPFLEET_STATE", defaultState()), "state directory")
	pos, err := parseInterspersed(fs, args[1:])
	if err != nil {
		return err
	}
	store := &auth.UserStore{Dir: filepath.Join(*state, "api")}
	switch args[0] {
	case "list":
		all, err := store.List()
		if err != nil {
			return err
		}
		if len(all) == 0 {
			fmt.Fprintln(stdout, "no users; with passkeys on, nepfleet serve prints a setup token")
		}
		for _, u := range all {
			fmt.Fprintf(stdout, "%-24s %d passkey(s), since %s\n", u.Name, len(u.Credentials), u.Created.Format("2006-01-02"))
		}
		return nil
	case "remove":
		if len(pos) != 1 {
			return errors.New(usage)
		}
		if err := store.Remove(pos[0]); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s removed; restart nepfleet serve to end their sessions\n", pos[0])
		return nil
	}
	return errors.New(usage)
}
