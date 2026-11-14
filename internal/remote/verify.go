// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/Serraniel/neptide/pkg/registry"
	"github.com/Serraniel/neptide/pkg/sigcheck"
)

// ReleaseIdentity is who signs NepFleet's release images: the release
// workflow of its repository, run from main or a version tag (cosign keyless,
// GitHub's OIDC issuer).
const ReleaseIdentity = `^https://github\.com/Serraniel/nepfleet/\.github/workflows/release\.yml@refs/(heads/main|tags/v[0-9.]+)$`

// digestRe is an image digest as registries give it.
var digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// releaseImage is the repository NepFleet's release images are pushed to.
const releaseImage = "ghcr.io/serraniel/nepfleet"

// ImageVerifier resolves an image reference to its digest and checks that
// it is signed by NepFleet's release workflow.
type ImageVerifier func(ctx context.Context, image string) (digest string, err error)

// SigstoreVerifier checks signatures against Sigstore's public trusted root,
// fetched over TUF on first use, with the image's digest from its registry.
func SigstoreVerifier() ImageVerifier {
	reg := registry.New()
	chk := &sigcheck.Checker{Registry: reg}
	return func(ctx context.Context, image string) (string, error) {
		ref, err := registry.ParseRef(image)
		if err != nil {
			return "", err
		}
		digest, err := reg.Digest(ctx, ref)
		if err != nil {
			return "", fmt.Errorf("resolving %s: %w", image, err)
		}
		ref.Digest = digest
		if err := chk.Verify(ctx, ref, sigcheck.Policy{Identity: ReleaseIdentity, Issuer: sigcheck.GitHubActions}); err != nil {
			return "", err
		}
		return digest, nil
	}
}

// VerifiedCompose sets compose.yaml's image to this NepFleet's release (the
// latest one for a development build) pinned to its digest, after checking
// that the release workflow signed that digest. It fails closed: an image
// that can't be verified is never written.
func VerifiedCompose(ctx context.Context, compose []byte, version string, verify ImageVerifier) ([]byte, error) {
	if !composeImageRe.Match(compose) {
		return nil, errors.New("compose.yaml has no NepFleet image line")
	}
	tag := "latest"
	if releaseRe.MatchString(version) {
		tag = strings.TrimPrefix(version, "v")
	}
	image := releaseImage + ":" + tag
	digest, err := verify(ctx, image)
	if err != nil {
		return nil, fmt.Errorf("the NepFleet image %s isn't verified, so it isn't installed: %w", image, err)
	}
	if !digestRe.MatchString(digest) {
		return nil, fmt.Errorf("%s: %q is not a sha256 digest", image, digest)
	}
	return composeImageRe.ReplaceAll(compose, []byte("${1}"+image+"@"+digest+"${2}")), nil
}

// Verified says whether compose pins NepFleet's image to a digest, for the
// plans' wording.
func Verified(compose []byte) bool {
	m := composeImageRe.Find(compose)
	return m != nil && strings.Contains(string(m), "@sha256:")
}
