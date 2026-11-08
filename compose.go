// SPDX-License-Identifier: AGPL-3.0-or-later

// Package nepfleet holds what the repository ships besides code, for the
// commands that need it.
package nepfleet

import _ "embed"

// Compose is compose.yaml, the container install of docs/install.md, so `host
// install` puts exactly that on a machine.
//
//go:embed compose.yaml
var Compose []byte
