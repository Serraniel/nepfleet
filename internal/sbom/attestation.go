// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"encoding/json"
	"sort"
	"strings"
)

// spdxDoc is the part of an SPDX document an SBOM attestation needs.
type spdxDoc struct {
	Packages []struct {
		Name         string `json:"name"`
		VersionInfo  string `json:"versionInfo"`
		ExternalRefs []struct {
			ReferenceType    string `json:"referenceType"`
			ReferenceLocator string `json:"referenceLocator"`
		} `json:"externalRefs"`
	} `json:"packages"`
}

// ParseAttestation reads what's inside an image from the SBOM attestation its
// build attached (`docker buildx imagetools inspect --format '{{json .SBOM}}'`):
// one SPDX document per platform, or a single one. found is false when the
// image has none, or none for this platform. Packages without a package URL
// (the image's own files, the document's root) are left out.
func ParseAttestation(raw []byte, platform string) (components []Component, found bool, err error) {
	var single struct {
		SPDX *spdxDoc `json:"SPDX"`
	}
	if err := json.Unmarshal(raw, &single); err != nil {
		return nil, false, err
	}
	doc := single.SPDX
	if doc == nil {
		var perPlatform map[string]struct {
			SPDX *spdxDoc `json:"SPDX"`
		}
		if err := json.Unmarshal(raw, &perPlatform); err != nil {
			return nil, false, err
		}
		// linux/arm64 may be listed as linux/arm64/v8.
		for p, d := range perPlatform {
			if p == platform || strings.HasPrefix(p, platform+"/") {
				doc = d.SPDX
				break
			}
		}
	}
	if doc == nil {
		return nil, false, nil
	}
	for _, p := range doc.Packages {
		purl := ""
		for _, r := range p.ExternalRefs {
			if r.ReferenceType == "purl" {
				purl = r.ReferenceLocator
				break
			}
		}
		if purl == "" {
			continue
		}
		components = append(components, Component{Type: "library", Name: p.Name, Version: p.VersionInfo, PURL: purl})
	}
	sort.Slice(components, func(a, b int) bool { return components[a].PURL < components[b].PURL })
	return components, true, nil
}
