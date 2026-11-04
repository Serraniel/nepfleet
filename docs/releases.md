# Releases

All NepFleet repositories release the same way.

- **Edge**: every push to `main` publishes `:edge` and `:sha-<7-char commit>`.
- **Releases** happen on demand. release-please keeps an open *release PR* that collects [Conventional Commits](https://www.conventionalcommits.org/). **Merging it is the release.** The merge tags `vX.Y.Z`, writes `CHANGELOG.md`, and publishes these image tags:
  - `:X.Y.Z`, `:X.Y`, `:latest` and `:sha-<commit>`;
  - `:X` as well, from 1.0 on.
- **Versioning:** `feat` bumps the minor version and `fix`, `security` and `deps` bump the patch version. Before 1.0, a breaking change (`feat!:`) bumps the minor version.

## Changelog sections

| Commit type | Section | Release? |
|---|---|---|
| `security` | Security | patch |
| `feat` | Features | minor |
| `fix` | Bug Fixes | patch |
| `perf` | Performance | patch |
| `deps` | Dependencies | patch |
| `docs`, `chore`, `ci`, `build`, `test`, `refactor` | hidden | no |

## Dependencies

Dependabot runs in every repository. PRs are squash-merged and the PR title becomes the commit. The [Dependabot workflow](../.github/workflows/dependabot.yml) sets the title type as follows:

1. The update fixes a published advisory (checked against the GitHub Advisory Database): `security(deps): … (GHSA-…)`.
2. A production dependency or base image changed: `deps: …`.
3. Development tooling changed: `chore(deps-dev): …`. GitHub Actions changed: `ci: …`. Neither causes a release.

Base images are pinned as `tag@sha256:digest`. When upstream rebuilds a base image under the same tag, Dependabot opens a PR, and merging it releases an image built on the new base.

Supply-chain artefacts are described in [supply chain](supply-chain.md).
