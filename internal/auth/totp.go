// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238's default, the only one every authenticator app supports
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"time"
)

// TOTP is a user's fallback for when no passkey is at hand. Unlike a passkey it
// is a shared secret, so users.json is a secret too.
type TOTP struct {
	Secret string `json:"secret"`
	// LastStep is the last time step used: a code works once.
	LastStep int64 `json:"lastStep"`
}

const (
	totpPeriod = 30
	totpDigits = 1000000
	// One step either way, for clocks that differ a little.
	totpSkew = 1
)

var totpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret makes a secret, in the form authenticator apps take.
func NewTOTPSecret() (string, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return totpEncoding.EncodeToString(raw), nil
}

// TOTPURL is what an authenticator app scans.
func TOTPURL(name, secret string) string {
	q := url.Values{"secret": {secret}, "issuer": {"NepFleet"}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
	return "otpauth://totp/" + url.PathEscape("NepFleet:"+name) + "?" + q.Encode()
}

func totpStep(t time.Time) int64 { return t.Unix() / totpPeriod }

func totpCode(secret string, step int64) (string, error) {
	key, err := totpEncoding.DecodeString(secret)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha1.New, key)
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step)) //nolint:gosec // time steps are positive
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	n := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", n%totpDigits), nil
}

// TOTPCode is the code a secret gives at a moment, as an authenticator app shows it.
func TOTPCode(secret string, t time.Time) (string, error) {
	return totpCode(secret, totpStep(t))
}

// matchTOTP returns the step a code belongs to, if it's newer than after.
func matchTOTP(secret, code string, now time.Time, after int64) (int64, bool) {
	step := totpStep(now)
	var found int64
	ok := false
	// All of them, so the time taken doesn't say which one matched.
	for s := step - totpSkew; s <= step+totpSkew; s++ {
		want, err := totpCode(secret, s)
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 && s > after {
			found, ok = s, true
		}
	}
	return found, ok
}

// CheckTOTPCode checks a code against a secret that isn't stored yet.
func CheckTOTPCode(secret, code string, now time.Time) (int64, bool) {
	return matchTOTP(secret, code, now, 0)
}

// SetTOTP turns the fallback on for a user, or off with nil.
func (s *UserStore) SetTOTP(id []byte, t *TOTP) error {
	return s.update(id, func(u *User) { u.TOTP = t })
}

// VerifyTOTP checks a code for a user and spends it, under the file's lock: two
// requests can't both use one code.
func (s *UserStore) VerifyTOTP(name, code string, now time.Time) (*User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	all, err := s.load()
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(all, func(u User) bool { return u.Name == name })
	if i < 0 || all[i].TOTP == nil {
		return nil, ErrUnauthorized
	}
	step, ok := matchTOTP(all[i].TOTP.Secret, code, now, all[i].TOTP.LastStep)
	if !ok {
		return nil, ErrUnauthorized
	}
	all[i].TOTP.LastStep = step
	if err := s.save(all); err != nil {
		return nil, errors.Join(ErrUnauthorized, err)
	}
	return &all[i], nil
}
