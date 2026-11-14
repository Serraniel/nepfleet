// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"strings"
	"testing"
)

// The app does everything over a desktop connection, edits included.
func TestMintScriptScope(t *testing.T) {
	if s := mintScript("control-host", "12h"); !strings.Contains(s, "--scope admin ") {
		t.Errorf("scope: %s", s)
	}
}
