// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import "testing"

func TestProgressFrom(t *testing.T) {
	log := "pulling\nweb: step 1/4: write the files\nweb: step 3/4: pull, start and wait until healthy\nsome compose output\n"
	p := ProgressFrom(log)
	if p == nil || p.Step != 3 || p.Steps != 4 || p.Text != "pull, start and wait until healthy" {
		t.Errorf("progress = %+v", p)
	}
	for _, bad := range []string{"", "web: step 5/4: nonsense\n", "web: step 0/4: none\n", "a line saying step 1/2: inside\n"} {
		if p := ProgressFrom(bad); p != nil {
			t.Errorf("%q: %+v", bad, p)
		}
	}
	// The updates job counts stacks; the applies it runs print no steps.
	u := ProgressFrom("updates: step 1/3: checking web\nweb: 1 update; updating\nupdates: step 2/3: checking db\n")
	if u == nil || u.Step != 2 || u.Steps != 3 || u.Text != "checking db" {
		t.Errorf("updates progress = %+v", u)
	}
}
