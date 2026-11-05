// SPDX-License-Identifier: AGPL-3.0-or-later

package importer

import (
	"os"
	"path/filepath"
	"testing"
)

// .env files of projects being imported are arbitrary.
func FuzzParseEnvFile(f *testing.F) {
	f.Add([]byte("A=1\nexport B='two words'\nC=\"x\\n${A}\" # c\nD=${NOPE:-def}\n#x\n"))
	f.Add([]byte("A='unterminated\n"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		path := filepath.Join(t.TempDir(), ".env")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, _ = parseEnvFile(path, map[string]string{"HOME": "/root"})
	})
}

func FuzzInterpolate(f *testing.F) {
	f.Add("${A} $B $$C ${D:-x} ${E:?msg} ${F:+y} ${")
	f.Fuzz(func(_ *testing.T, s string) {
		_, _ = interpolate(s, func(n string) (string, bool) { return "v", n == "A" })
	})
}

// Secrets written into a compose file are moved to the encrypted values.
func FuzzExtractLiteralSecrets(f *testing.F) {
	f.Add([]byte("services:\n  db:\n    environment: {POSTGRES_PASSWORD: hunter2, A: b}\n"), false)
	f.Add([]byte("services:\n  db:\n    environment: [\"API_TOKEN=x\", \"B\"]\n"), true)
	f.Fuzz(func(_ *testing.T, cfg []byte, all bool) {
		_, _, _, _ = extractLiteralSecrets(cfg, map[string]string{}, all)
		_ = inspect(cfg, map[string]string{})
		_ = volumes(cfg)
	})
}
