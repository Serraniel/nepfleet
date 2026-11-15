// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"strings"
	"testing"
)

func TestSSHHardenPlan(t *testing.T) {
	steps, err := SSHHardenPlan(&Report{Kernel: "Linux", Sudo: true, User: "me"})
	if err != nil || len(steps) != 2 || !steps[0].Root || steps[1].Root {
		t.Fatalf("%+v %v", steps, err)
	}
	s := steps[0].Script
	for _, want := range []string{"PasswordAuthentication no", "PermitRootLogin no", "sshd -t", "sshd_config.d/\\*\\.conf", "reload"} {
		if !strings.Contains(s, want) && !strings.Contains(s, strings.ReplaceAll(want, `\`, "")) {
			t.Errorf("no %q in %s", want, s)
		}
	}
	// Root's own login with a key must stay possible.
	rootSteps, _ := SSHHardenPlan(&Report{Kernel: "Linux", Root: true, User: "root"})
	if !strings.Contains(rootSteps[0].Script, "PermitRootLogin prohibit-password") || strings.Contains(rootSteps[0].Script, "PermitRootLogin no") {
		t.Errorf("root: %s", rootSteps[0].Script)
	}
	if _, err := SSHHardenPlan(&Report{Kernel: "Linux"}); err == nil {
		t.Error("planned without root or sudo")
	}
}
