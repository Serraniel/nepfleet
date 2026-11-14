// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"fmt"
	"log"
	"strings"
)

// logBreaks are what could start a forged line in the server's log.
var logBreaks = strings.NewReplacer("\n", " ", "\r", " ")

// logf logs one line: names of people and tokens, and texts from requests,
// can't add lines of their own.
func logf(format string, args ...any) {
	log.Print(logBreaks.Replace(fmt.Sprintf(format, args...))) //nolint:gosec // this is the sanitiser: line breaks are removed here
}
