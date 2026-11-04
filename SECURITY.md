# Security policy

Please report vulnerabilities privately through [GitHub security advisories](https://github.com/Serraniel/nepfleet/security/advisories/new). Do not open public issues for them.

Expect an acknowledgement within 7 days. Fixes ship as patch releases on the latest minor version. Until 1.0, only the latest release is supported.

Reports of particular interest include anything that lets an unauthenticated party reach the Docker socket, read secrets or write to the fleet repository. The design is described in [docs/security.md](docs/security.md).
