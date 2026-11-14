// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
		// The signature names the workflow, not the version: an older signed
		// image under a newer tag would pass. The version label (in the
		// config, which the digest covers) must be the tag's.
		if err := checkVersion(ctx, reg, ref); err != nil {
			return "", err
		}
		return digest, nil
	}
}

// checkVersion refuses an image whose version label isn't its tag's, for
// version tags (latest has nothing to compare).
func checkVersion(ctx context.Context, reg *registry.Client, ref registry.Ref) error {
	if !releaseRe.MatchString(ref.Tag) {
		return nil
	}
	got, err := imageVersion(ctx, reg, ref)
	if err != nil {
		return fmt.Errorf("reading %s's version: %w", ref.String(), err)
	}
	if want := strings.TrimPrefix(ref.Tag, "v"); strings.TrimPrefix(got, "v") != want {
		return fmt.Errorf("%s is version %q inside, not %s: refusing a different (perhaps older) image under this tag", ref.String(), got, want)
	}
	return nil
}

// manifestTypes are the manifests an image digest may name.
const manifestTypes = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

// imageVersion reads org.opencontainers.image.version from the image's
// config: the index, a Linux manifest and the config, each checked against
// the digest that names it, starting from ref's (signed) digest.
func imageVersion(ctx context.Context, reg *registry.Client, ref registry.Ref) (string, error) {
	type manifest struct {
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform struct {
				OS string `json:"os"`
			} `json:"platform"`
		} `json:"manifests"`
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	fetch := func(digest string) (manifest, error) {
		var m manifest
		r := ref
		r.Digest = digest
		raw, err := reg.Manifest(ctx, r, manifestTypes)
		if err != nil {
			return m, err
		}
		if sum := sha256.Sum256(raw); "sha256:"+hex.EncodeToString(sum[:]) != digest {
			return m, fmt.Errorf("manifest %s doesn't match its digest", digest)
		}
		return m, json.Unmarshal(raw, &m)
	}
	m, err := fetch(ref.Digest)
	if err != nil {
		return "", err
	}
	if len(m.Manifests) > 0 { // an index: any Linux image of it (not an attestation)
		next := ""
		for _, d := range m.Manifests {
			if d.Platform.OS == "linux" {
				next = d.Digest
				break
			}
		}
		if next == "" {
			return "", errors.New("the index has no Linux image")
		}
		if m, err = fetch(next); err != nil {
			return "", err
		}
	}
	if m.Config.Digest == "" {
		return "", errors.New("no image config")
	}
	raw, err := reg.Blob(ctx, ref, m.Config.Digest, 1<<20)
	if err != nil {
		return "", err
	}
	var cfg struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"config"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", err
	}
	v := cfg.Config.Labels["org.opencontainers.image.version"]
	if v == "" {
		return "", errors.New("the image has no org.opencontainers.image.version label")
	}
	return v, nil
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

// pinnedImageRe is NepFleet's image line pinned to a digest: the image
// itself, not a comment after it.
var pinnedImageRe = regexp.MustCompile(`(?m)^\s*image:\s*ghcr\.io/serraniel/nepfleet:[^\s@]+@sha256:[0-9a-f]{64}(\s|$)`)

// Verified says whether compose pins NepFleet's image to a digest, for the
// plans' wording.
func Verified(compose []byte) bool { return pinnedImageRe.Match(compose) }
