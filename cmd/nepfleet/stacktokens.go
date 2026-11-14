// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"maps"
	"time"

	"github.com/Serraniel/nepfleet/internal/render"
	"github.com/Serraniel/nepfleet/internal/template"
)

// A stack's API token (generate kind notify-token) lasts a year and is made
// again on the first apply in its last two months.
const (
	stackTokenTTL   = 365 * 24 * time.Hour
	stackTokenRenew = 60 * 24 * time.Hour
)

// servedAPIURL is NepFleet's API as stacks on render.APINetwork reach it,
// set by serve from its listen port; empty means render.DefaultAPIURL.
var servedAPIURL string

// stackTokenName names a stack's token in the token store and on Access.
func stackTokenName(stack, key string) string { return "stack:" + stack + "/" + key }

// staleTokens drops the stack's generated tokens that this machine's token
// store doesn't know, or that expire soon, so render makes new ones. Where
// the store can't be read (another machine plans), they stay as they are.
func (e *env) staleTokens(tpl *template.Template, sec map[string]string) map[string]string {
	if tpl == nil {
		return sec
	}
	store := tokenStore(e.stateDir)
	out, cloned := sec, false
	for _, g := range tpl.Spec.Generate {
		v, ok := sec[g.Name]
		if g.Kind != template.KindNotifyToken || !ok {
			continue
		}
		if valid, known := store.ValidFor(v, stackTokenRenew); known && !valid {
			if !cloned {
				// The caller's map stays as it was; only this render forgets.
				out, cloned = maps.Clone(sec), true
			}
			delete(out, g.Name)
		}
	}
	return out
}

// registerTokens makes the tokens render generated valid, before they are
// saved with the stack's secrets: an unregistered one would only be refused.
func (e *env) registerTokens(stack string, out *render.Output) error {
	store := tokenStore(e.stateDir)
	for k, scope := range out.Tokens {
		if _, err := store.Register(stackTokenName(stack, k), scope, out.Generated[k], stackTokenTTL); err != nil {
			return err
		}
	}
	return nil
}
