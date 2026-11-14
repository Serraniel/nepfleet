// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

const goodDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestVerifiedCompose(t *testing.T) {
	var asked string
	verify := func(_ context.Context, image string) (string, error) {
		asked = image
		return goodDigest, nil
	}
	out, err := VerifiedCompose(context.Background(), []byte(testCompose), "v0.4.0", verify)
	if err != nil {
		t.Fatal(err)
	}
	if asked != "ghcr.io/serraniel/nepfleet:0.4.0" || !strings.Contains(string(out), "image: ghcr.io/serraniel/nepfleet:0.4.0@"+goodDigest) || !Verified(out) {
		t.Errorf("asked %s, got:\n%s", asked, out)
	}
	if strings.Contains(pullText(out), "not verified") || !strings.Contains(pullText([]byte(testCompose)), "not verified") {
		t.Error("the pull step doesn't say whether the image was checked")
	}
	if _, err := VerifiedCompose(context.Background(), []byte(testCompose), "dev", verify); err != nil || asked != "ghcr.io/serraniel/nepfleet:latest" {
		t.Errorf("a development build: %s, %v", asked, err)
	}

	// Fails closed: nothing is written when the signature doesn't check out.
	refuse := func(context.Context, string) (string, error) { return "", errors.New("no valid signature") }
	if out, err := VerifiedCompose(context.Background(), []byte(testCompose), "0.4.0", refuse); err == nil || out != nil || !strings.Contains(err.Error(), "isn't verified") {
		t.Errorf("unverified: %s %v", out, err)
	}
	odd := func(context.Context, string) (string, error) { return "latest", nil }
	if _, err := VerifiedCompose(context.Background(), []byte(testCompose), "0.4.0", odd); err == nil {
		t.Error("accepted a digest that isn't one")
	}
}

// TestSigstoreVerifierLive checks a real release image against ghcr.io and
// Sigstore: NEPFLEET_TEST_NETWORK=1 go test ./internal/remote -run Live
func TestSigstoreVerifierLive(t *testing.T) {
	if os.Getenv("NEPFLEET_TEST_NETWORK") == "" {
		t.Skip("set NEPFLEET_TEST_NETWORK to verify against ghcr.io and Sigstore")
	}
	d, err := SigstoreVerifier()(context.Background(), "ghcr.io/serraniel/nepfleet:0.3.4")
	if err != nil || !digestRe.MatchString(d) {
		t.Fatalf("0.3.4: %q %v", d, err)
	}
	if _, err := SigstoreVerifier()(context.Background(), "ghcr.io/serraniel/nepfleet-caddy:2.11"); err == nil {
		t.Error("an image signed by another workflow passed as NepFleet's release")
	}
}
