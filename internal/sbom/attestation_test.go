// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import "testing"

const spdx = `{"SPDXID": "SPDXRef-DOCUMENT", "packages": [
  {"name": "sbom", "SPDXID": "SPDXRef-DocumentRoot"},
  {"name": "musl", "versionInfo": "1.2.5-r10", "externalRefs": [{"referenceType": "cpe23Type", "referenceLocator": "cpe:2.3:a:musl:musl"}, {"referenceType": "purl", "referenceLocator": "pkg:apk/alpine/musl@1.2.5-r10"}]},
  {"name": "busybox", "versionInfo": "1.37.0-r20", "externalRefs": [{"referenceType": "purl", "referenceLocator": "pkg:apk/alpine/busybox@1.37.0-r20"}]}
]}`

func TestParseAttestation(t *testing.T) {
	multi := `{"linux/amd64": {"SPDX": ` + spdx + `}, "linux/arm64/v8": {"SPDX": ` + spdx + `}}`
	for name, c := range map[string]struct {
		raw, platform string
		found         bool
		n             int
	}{
		"per platform":            {multi, "linux/amd64", true, 2},
		"a variant of a platform": {multi, "linux/arm64", true, 2},
		"another platform":        {multi, "linux/riscv64", false, 0},
		"one document":            {`{"SPDX": ` + spdx + `}`, "linux/amd64", true, 2},
		"no attestation":          {`{}`, "linux/amd64", false, 0},
	} {
		got, found, err := ParseAttestation([]byte(c.raw), c.platform)
		if err != nil || found != c.found || len(got) != c.n {
			t.Errorf("%s: %v %v %+v", name, err, found, got)
			continue
		}
		if c.n > 0 && (got[0].Name != "busybox" || got[0].PURL != "pkg:apk/alpine/busybox@1.37.0-r20" || got[1].Version != "1.2.5-r10" || got[0].Type != "library") {
			t.Errorf("%s: %+v", name, got)
		}
	}
	if _, _, err := ParseAttestation([]byte(`not json`), "linux/amd64"); err == nil {
		t.Error("not JSON")
	}
}
