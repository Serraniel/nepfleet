// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The ASCII key of RFC 6238's test vectors.
var rfcSecret = totpEncoding.EncodeToString([]byte("12345678901234567890"))

func TestTOTPVectors(t *testing.T) {
	for unix, want := range map[int64]string{
		59:          "287082",
		1111111109:  "081804",
		1111111111:  "050471",
		1234567890:  "005924",
		2000000000:  "279037",
		20000000000: "353130",
	} {
		got, err := totpCode(rfcSecret, unix/totpPeriod)
		if err != nil || got != want {
			t.Errorf("%d: %s, want %s (%v)", unix, got, want, err)
		}
	}
}

func TestMatchTOTP(t *testing.T) {
	now := time.Unix(1111111109, 0)
	step := totpStep(now)
	code := func(s int64) string {
		c, err := totpCode(rfcSecret, s)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	for s, want := range map[int64]bool{step - 2: false, step - 1: true, step: true, step + 1: true, step + 2: false} {
		if _, ok := matchTOTP(rfcSecret, code(s), now, 0); ok != want {
			t.Errorf("step %+d: %v", s-step, ok)
		}
	}
	if _, ok := matchTOTP(rfcSecret, code(step), now, step); ok {
		t.Error("a used code worked again")
	}
	if _, ok := matchTOTP(rfcSecret, code(step-1), now, step); ok {
		t.Error("an older code worked after a newer one")
	}
	for _, bad := range []string{"", "12345", "abcdef", code(step) + "0"} {
		if _, ok := matchTOTP(rfcSecret, bad, now, 0); ok {
			t.Errorf("%q passed", bad)
		}
	}
	if _, ok := matchTOTP("not base32!", "000000", now, 0); ok {
		t.Error("a damaged secret passed")
	}
}

func TestVerifyTOTPSpendsCodes(t *testing.T) {
	s := &UserStore{Dir: t.TempDir()}
	u, err := NewUser("Ada")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Add(u, true); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1111111109, 0)
	code, err := totpCode(rfcSecret, totpStep(now))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyTOTP("Ada", code, now); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("not turned on: %v", err)
	}
	if err := s.SetTOTP(u.ID, &TOTP{Secret: rfcSecret}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyTOTP("Nobody", code, now); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("unknown user: %v", err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if got, err := s.VerifyTOTP("Ada", code, now); err == nil && got.Name == "Ada" {
				wins.Add(1)
			}
		})
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Errorf("one code signed in %d times", wins.Load())
	}
	if err := s.SetTOTP(u.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyTOTP("Ada", code, now.Add(time.Hour)); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("turned off: %v", err)
	}
}

func TestTOTPURL(t *testing.T) {
	got := TOTPURL("Ada L", "ABC")
	for _, want := range []string{"otpauth://totp/NepFleet:Ada%20L?", "secret=ABC", "issuer=NepFleet", "digits=6"} {
		if !strings.Contains(got, want) {
			t.Errorf("%s lacks %s", got, want)
		}
	}
	a, _ := NewTOTPSecret()
	b, _ := NewTOTPSecret()
	if a == b || len(a) != 32 {
		t.Errorf("%s %s", a, b)
	}
}
