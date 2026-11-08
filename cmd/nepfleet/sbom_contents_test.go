// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/sbom"
)

func TestImageContents(t *testing.T) {
	var scanned []string
	attested := func(_ context.Context, ref string) ([]sbom.Component, error) {
		if ref == "attested" {
			return []sbom.Component{{Name: "from-attestation"}}, nil
		}
		return nil, errNoAttestation
	}
	scan := func(_ context.Context, ref string) ([]sbom.Component, error) {
		scanned = append(scanned, ref)
		return []sbom.Component{{Name: "from-syft"}}, nil
	}
	look := imageContents(attested, scan, true)
	if got, _ := look(context.Background(), "attested"); len(got) != 1 || got[0].Name != "from-attestation" || len(scanned) != 0 {
		t.Errorf("an attestation is used, without scanning: %+v %v", got, scanned)
	}
	if got, _ := look(context.Background(), "plain"); len(got) != 1 || got[0].Name != "from-syft" {
		t.Errorf("no attestation: Syft looks: %+v", got)
	}
	_, err := imageContents(attested, scan, false)(context.Background(), "plain")
	if !errors.Is(err, errNoAttestation) || !strings.Contains(err.Error(), "Syft isn't installed") {
		t.Errorf("without Syft: %v", err)
	}
}

// A Docker Official Image attests its SBOM; read it as the real command does.
func TestAttestedContentsReal(t *testing.T) {
	if os.Getenv("NEPFLEET_TEST_DOCKER") == "" {
		t.Skip("set NEPFLEET_TEST_DOCKER to run with Docker and internet access")
	}
	got, err := attestedContents(context.Background(), "docker.io/library/alpine:3.24")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(got, func(c sbom.Component) bool {
		return c.Name == "musl" && strings.HasPrefix(c.PURL, "pkg:apk/alpine/musl@")
	}) {
		t.Errorf("no musl in %d components", len(got))
	}
}
