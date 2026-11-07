# Supply chain and vulnerability handling

NepFleet is not legally required to comply with the EU Cyber Resilience Act (CRA). It follows the CRA's practices anyway, because the users of an infrastructure tool depend on them.

## Every release ships with

| Artefact | Where | Format |
|---|---|---|
| Image SBOM | Signed attestation on the image; release asset `*_image.cdx.json` | SPDX (BuildKit) and CycloneDX |
| Source SBOM | Release assets `*.spdx.json` and `*.cdx.json` | SPDX and CycloneDX |
| Build provenance | Image attestation | SLSA (BuildKit, `mode=max`) |
| Signatures | Image and `SHA256SUMS` | Sigstore cosign, keyless |

To verify an image and read its SBOM:

```sh
cosign verify ghcr.io/serraniel/nepfleet:latest \
  --certificate-identity-regexp '^https://github.com/Serraniel/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation --type cyclonedx ghcr.io/serraniel/nepfleet:latest \
  --certificate-identity-regexp '^https://github.com/Serraniel/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  | jq -r .payload | base64 -d | jq .predicate
```

## Keeping dependencies fresh

Dependabot updates code dependencies, actions and digest-pinned base images, and security fixes are released as patch versions (see [releases](releases.md)). `govulncheck` and `npm audit` run on every PR.

## Vulnerability handling

- **Reporting**: privately, through GitHub security advisories ([SECURITY.md](../SECURITY.md)).
- **Response**: acknowledgement within 7 days and a fix in a patch release. The advisory is published with a CVE once the fix is out.
- **Support period**: until 1.0, the latest release. From 1.0 on, the latest minor version of each supported major version, with the support period stated per major version.
- **VEX**: when a dependency reports a vulnerability that NepFleet does not reach (`govulncheck` shows the call graph), we publish an OpenVEX statement with the release (planned).

## Infrastructure SBOMs

NepFleet knows which images your stacks run, so it can produce an SBOM of your *infrastructure*:

```sh
nepfleet sbom                       # the stacks on this host, as one CycloneDX document
nepfleet sbom web db --out sboms/   # one <stack>.cdx.json each, plus fleet.cdx.json
nepfleet sbom --contents            # also what's inside each image, with Syft
```

- **Images are named by digest,** from `lock.yaml`. One without a pinned digest is listed by tag with a warning, as the tag can move.
- **`--contents`** needs [Syft](https://github.com/anchore/syft) installed, and results are cached by digest. An image it can't read is still listed, and the command reports it and fails.
- **It reports what the fleet says should run,** from the rendered stacks and pins, not by asking Docker what's running.
- **To keep a history,** commit the documents to the fleet repository.

Planned: using an image's SBOM attestation before falling back to Syft, swarm targets, and checking against advisories, with alerts through [notifications](notifications.md).
