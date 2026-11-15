// SPDX-License-Identifier: AGPL-3.0-or-later

package render

import (
	"strings"
	"testing"
)

// docker compose config names the project at the top; docker stack deploy
// refuses it.
func TestForSwarmDropsProjectName(t *testing.T) {
	out, err := ForSwarm([]byte("name: hb\nservices:\n  web:\n    image: nginx:1.29\n"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "name: hb") || !strings.Contains(string(out), "services:") {
		t.Errorf("%s", out)
	}
}
