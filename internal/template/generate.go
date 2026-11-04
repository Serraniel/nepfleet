// SPDX-License-Identifier: AGPL-3.0-or-later

package template

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
)

const alnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// Value produces the generated value. Length is the length of the result in
// characters, not of the entropy behind it.
func (g Generate) Value() (string, error) {
	switch g.Encoding {
	case "hex":
		b := make([]byte, (g.Length+1)/2)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		return hex.EncodeToString(b)[:g.Length], nil
	case "", "base64url":
		b := make([]byte, g.Length)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(b)[:g.Length], nil
	case "alnum":
		out := make([]byte, g.Length)
		size := big.NewInt(int64(len(alnum)))
		for i := range out {
			n, err := rand.Int(rand.Reader, size)
			if err != nil {
				return "", err
			}
			out[i] = alnum[n.Int64()]
		}
		return string(out), nil
	}
	return "", fmt.Errorf("unknown encoding %q", g.Encoding)
}
