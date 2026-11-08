// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"slices"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// cmdUser: nepfleet user list | remove <name> — who may sign in. Runs on the
// server: access to its state directory is what recovers a lost passkey.
func cmdUser(args []string, stdout, stderr io.Writer) error {
	const usage = "usage: nepfleet user list | remove NAME | passkeys NAME | remove-passkey NAME ID [--state DIR]"
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
			codes := ""
			if u.TOTP != nil {
				codes = ", sign-in codes on"
			}
			fmt.Fprintf(stdout, "%-24s %d passkey(s)%s, since %s\n", u.Name, len(u.Credentials), codes, u.Created.Format("2006-01-02"))
		}
		return nil
	case "remove":
		if len(pos) != 1 {
			return errors.New(usage)
		}
		if err := store.Remove(pos[0]); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s removed, and their sessions ended; if no users are left, restart nepfleet serve for a new setup token\n", pos[0])
		return nil
	case "passkeys":
		if len(pos) != 1 {
			return errors.New(usage)
		}
		all, err := store.List()
		if err != nil {
			return err
		}
		i := slices.IndexFunc(all, func(u auth.User) bool { return u.Name == pos[0] })
		if i < 0 {
			return fmt.Errorf("no user %q", pos[0])
		}
		u := all[i]
		if len(u.Credentials) == 0 {
			fmt.Fprintf(stdout, "%s has no passkeys\n", u.Name)
		}
		for _, c := range u.Credentials {
			state := ""
			if u.IsLocked(c.ID) {
				state = "  LOCKED: may have been copied"
			}
			kind := "security key or phone"
			if c.Flags.BackupEligible {
				kind = "synced"
			}
			fmt.Fprintf(stdout, "%-12s %-22s used %d time(s)%s\n", auth.CredentialName(c.ID), kind, c.Authenticator.SignCount, state)
		}
		return nil
	case "remove-passkey":
		if len(pos) != 2 {
			return errors.New(usage)
		}
		if err := store.RemoveCredential(pos[0], pos[1]); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "passkey %s of %s removed; sessions signed in with it last until they expire (12 hours) or the server restarts\n", pos[1], pos[0])
		return nil
	}
	return errors.New(usage)
}
