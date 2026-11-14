// SPDX-License-Identifier: AGPL-3.0-or-later

package deploy

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// noNetworks has no networks yet: inspect fails, create is recorded.
type noNetworks struct{ created []string }

func (n *noNetworks) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	if len(args) > 1 && args[1] == "inspect" {
		return nil, errors.New("no such network")
	}
	n.created = append(n.created, strings.Join(args, " "))
	return nil, nil
}

func TestEnsureNetwork(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		swarm, internal bool
		want            string
	}{
		{false, false, "network create nepfleet_ingress"},
		{false, true, "network create --internal nepfleet_api"},
		{true, true, "network create --driver overlay --attachable --internal nepfleet_api"},
	} {
		r := &noNetworks{}
		name := "nepfleet_ingress"
		if c.internal {
			name = "nepfleet_api"
		}
		if err := ensureNetwork(ctx, r, name, c.swarm, c.internal); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(r.created, []string{c.want}) {
			t.Errorf("swarm %v internal %v: %v, want %q", c.swarm, c.internal, r.created, c.want)
		}
	}
}
