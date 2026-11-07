// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	_ "embed" // for the document below
	"net/http"
)

//go:embed openapi.yaml
var openAPISpec []byte

func serveOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(openAPISpec)
}
