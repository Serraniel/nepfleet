// SPDX-License-Identifier: AGPL-3.0-or-later

// Package signon registers OpenID Connect clients for stacks in the
// fleet's identity provider (docs/sign-on.md). Pocket ID is the first
// provider; others implement Provider.
package signon

import (
	"context"
	"slices"
)

// Client is the OpenID Connect client NepFleet keeps for one stack.
type Client struct {
	// ID is the client ID, the stack's name.
	ID   string
	Name string
	// Description marks the client as NepFleet's, so it can be told apart
	// in the provider's UI.
	Description string
	// Callbacks and LogoutCallbacks are full URLs.
	Callbacks       []string
	LogoutCallbacks []string
	// Secret is the client secret NepFleet stores encrypted in the stack's
	// secrets; the provider must accept it.
	Secret string
}

// Change is what Ensure would do to bring the provider in line.
type Change struct {
	// Create: the client doesn't exist.
	Create bool
	// Update: its callback URLs differ.
	Update bool
	// Secret: the provider doesn't know the stored secret yet.
	Secret bool
	// Diff lists the differences, one per line ("- old", "+ new"), for
	// the plan; never a secret.
	Diff []string
}

// Empty reports that the provider already has the client as wanted.
func (c Change) Empty() bool {
	return !c.Create && !c.Update && !c.Secret
}

// Provider is an identity provider NepFleet registers clients in.
type Provider interface {
	// Kind names the provider software, e.g. "pocket-id".
	Kind() string
	// Issuer is the issuer URL apps discover the provider at.
	Issuer() string
	// Plan reports what Ensure would change, without changing anything.
	Plan(ctx context.Context, c Client) (Change, error)
	// Ensure creates or updates the client. Settings made in the provider
	// beyond the callbacks (groups, consent, logos) are kept.
	Ensure(ctx context.Context, c Client) error
}

// sameURLs compares lists of URLs as sets; nil and empty are the same.
func sameURLs(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(slices.Compact(a), slices.Compact(b))
}
