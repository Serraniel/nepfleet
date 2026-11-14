// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Serraniel/neptide/pkg/registry"
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
	if Verified([]byte("services:\n  nepfleet:\n    image: ghcr.io/serraniel/nepfleet:0.4.0 # @sha256:" + strings.Repeat("a", 64) + "\n")) {
		t.Error("a digest in a comment counted as pinned")
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

// fakeRegistry serves one image: an index naming an attestation and a Linux
// manifest, whose config carries version. tamper changes the manifest it
// serves without changing the digests the index names.
func fakeRegistry(t *testing.T, version string, tamper bool) (*registry.Client, registry.Ref) {
	t.Helper()
	sum := func(b []byte) string { s := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(s[:]) }
	config := []byte(`{"config":{"Labels":{"org.opencontainers.image.version":"` + version + `"}}}`)
	manifest := []byte(`{"config":{"digest":"` + sum(config) + `"}}`)
	attestation := []byte(`{"config":{"digest":"sha256:` + strings.Repeat("0", 64) + `"}}`)
	index := []byte(`{"manifests":[{"digest":"` + sum(attestation) + `","platform":{"os":"unknown"}},{"digest":"` + sum(manifest) + `","platform":{"os":"linux"}}]}`)
	blobs := map[string][]byte{sum(index): index, sum(manifest): manifest, sum(attestation): attestation, sum(config): config}
	if tamper {
		blobs[sum(manifest)] = []byte(`{"config":{"digest":"sha256:` + strings.Repeat("1", 64) + `"}}`)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		b, ok := blobs[d]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	return registry.New(), registry.Ref{Host: host, Repo: "serraniel/nepfleet", Tag: "0.4.0", Digest: sum(index)}
}

func TestImageVersion(t *testing.T) {
	reg, ref := fakeRegistry(t, "0.4.0", false)
	if v, err := imageVersion(context.Background(), reg, ref); err != nil || v != "0.4.0" {
		t.Errorf("version = %q, %v", v, err)
	}
	if err := checkVersion(context.Background(), reg, ref); err != nil {
		t.Errorf("matching version refused: %v", err)
	}
	// An older image under the tag is refused.
	reg, ref = fakeRegistry(t, "0.3.1", false)
	if err := checkVersion(context.Background(), reg, ref); err == nil || !strings.Contains(err.Error(), `version "0.3.1" inside, not 0.4.0`) {
		t.Errorf("older image under the tag: %v", err)
	}
	ref.Tag = "latest"
	if err := checkVersion(context.Background(), reg, ref); err != nil {
		t.Errorf("latest has no version to compare: %v", err)
	}
	// A manifest that doesn't match its digest is refused.
	reg, ref = fakeRegistry(t, "0.4.0", true)
	if _, err := imageVersion(context.Background(), reg, ref); err == nil || !strings.Contains(err.Error(), "doesn't match its digest") {
		t.Errorf("tampered manifest: %v", err)
	}
}
