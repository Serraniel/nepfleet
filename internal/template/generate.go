// SPDX-License-Identifier: AGPL-3.0-or-later

package template

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"github.com/Serraniel/nepfleet/internal/auth"
	"golang.org/x/crypto/bcrypt"
	"math/big"
)

const alnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// Hash is a bcrypt kind's value for from, the value it's made from. A stored
// hash that still matches from is kept: the hash stays the same across
// renders, and is made again only when from changes.
func (g Generate) Hash(from, stored string) (string, error) {
	if stored != "" && bcrypt.CompareHashAndPassword([]byte(stored), []byte(from)) == nil {
		return stored, nil
	}
	h, err := bcrypt.GenerateFromPassword([]byte(from), bcrypt.DefaultCost)
	return string(h), err
}

// Value produces the generated value. Length is the length of the result in
// characters, not of the entropy behind it. A bcrypt kind uses Hash.
func (g Generate) Value() (string, error) {
	if g.Kind == KindBcrypt {
		return "", fmt.Errorf("%s: a bcrypt hash is made from %s", g.Name, g.From)
	}
	if g.Kind == KindNotifyToken {
		// The shape of an API token: NepFleet registers it when the stack
		// is applied (auth.Store.Register).
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		return auth.Prefix + base64.RawURLEncoding.EncodeToString(b), nil
	}
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
