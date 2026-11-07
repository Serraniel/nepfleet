// SPDX-License-Identifier: AGPL-3.0-or-later

package doctor

import (
	"errors"
	"os"
)

// Hosts NepFleet manages run Linux; Windows only runs its client side.
func fileGID(os.FileInfo) string { return "" }

func freeBytes(string) (uint64, error) {
	return 0, errors.New("not available on Windows")
}
