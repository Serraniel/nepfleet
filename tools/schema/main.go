// SPDX-License-Identifier: AGPL-3.0-or-later

// Command schema writes the JSON Schemas to schemas/. Run from the module
// root: go run ./tools/schema
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Serraniel/nepfleet/internal/schema"
)

func main() {
	files, err := schema.All(".")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.MkdirAll("schemas", 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join("schemas", name), b, 0o644); err != nil { //nolint:gosec // public files
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}
