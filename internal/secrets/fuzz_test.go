// SPDX-License-Identifier: AGPL-3.0-or-later

package secrets

import "testing"

// The pre-commit hook and `secret check` decide with IsEncrypted whether a
// file may be committed: arbitrary content must never make it panic.
func FuzzIsEncrypted(f *testing.F) {
	f.Add([]byte("A: ENC[AES256_GCM,data:x]\nsops: {version: 3}\n"))
	f.Add([]byte("A: plain\n"))
	f.Fuzz(func(_ *testing.T, raw []byte) {
		_ = IsEncrypted(raw)
	})
}
