// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

var (
	testNow    = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	testDigest = "sha256:" + strings.Repeat("a", 64)
)

func TestImageComponent(t *testing.T) {
	c := ImageComponent(Image{Repository: "ghcr.io/Acme/App", Tag: "1.2", Digest: testDigest})
	if c.Type != "container" || c.Version != "1.2" || len(c.Hashes) != 1 || c.Hashes[0].Alg != "SHA-256" || c.Hashes[0].Content != strings.Repeat("a", 64) {
		t.Errorf("%+v", c)
	}
	if !strings.HasPrefix(c.PURL, "pkg:oci/app@sha256%3A") || !strings.Contains(c.PURL, "repository_url=ghcr.io%2FAcme%2FApp") || !strings.Contains(c.PURL, "tag=1.2") {
		t.Errorf("%s", c.PURL)
	}
	if c := ImageComponent(Image{Repository: "nginx", Tag: "1"}); len(c.Hashes) != 0 || strings.Contains(c.PURL, "@") {
		t.Errorf("unpinned: %+v", c)
	}
}

func TestReference(t *testing.T) {
	for want, i := range map[string]Image{
		"a@" + testDigest: {Repository: "a", Tag: "1", Digest: testDigest},
		"a:1":             {Repository: "a", Tag: "1"},
		"a":               {Repository: "a"},
	} {
		if got := i.Reference(); got != want {
			t.Errorf("%s: %s", want, got)
		}
	}
}

func TestForStackIsStableAndValid(t *testing.T) {
	images := []Image{{Repository: "b", Tag: "2"}, {Repository: "a", Tag: "1", Digest: testDigest}}
	doc, err := ForStack(context.Background(), "web", images, nil, testNow)
	if err != nil {
		t.Fatal(err)
	}
	reversed, err := ForStack(context.Background(), "web", []Image{images[1], images[0]}, nil, testNow)
	if err != nil {
		t.Fatal(err)
	}
	one, _ := json.Marshal(doc)
	two, _ := json.Marshal(reversed)
	if string(one) != string(two) {
		t.Error("the same stack must give the same document")
	}
	if doc.BOMFormat != "CycloneDX" || doc.SpecVersion != "1.6" || doc.Metadata.Component.Name != "web" || doc.Components[0].Name != "a" {
		t.Errorf("%+v", doc)
	}
}

func TestForStackKeepsImagesThatCantBeRead(t *testing.T) {
	contents := func(_ context.Context, ref string) ([]Component, error) {
		if ref == "bad:1" {
			return nil, errors.New("no such image")
		}
		return []Component{{Type: "library", Name: "openssl", Version: "3"}}, nil
	}
	doc, err := ForStack(context.Background(), "web", []Image{{Repository: "bad", Tag: "1"}, {Repository: "good", Tag: "1"}}, contents, testNow)
	if err == nil || !strings.Contains(err.Error(), "bad:1") {
		t.Errorf("%v", err)
	}
	if len(doc.Components) != 2 || len(doc.Components[0].Components) != 0 || doc.Components[1].Components[0].Name != "openssl" {
		t.Errorf("%+v", doc.Components)
	}
}

func TestAggregate(t *testing.T) {
	web, _ := ForStack(context.Background(), "web", []Image{{Repository: "a", Tag: "1"}}, nil, testNow)
	db, _ := ForStack(context.Background(), "db", []Image{{Repository: "b", Tag: "1"}}, nil, testNow)
	doc := Aggregate("fleet", map[string]*Document{"web": web, "db": db}, testNow)
	if len(doc.Components) != 2 || doc.Components[0].Name != "db" || doc.Components[1].Components[0].Name != "a" {
		t.Errorf("%+v", doc.Components)
	}
}

func collectRefs(cs []Component, into map[string]int) {
	for _, c := range cs {
		if c.BOMRef != "" {
			into[c.BOMRef]++
		}
		collectRefs(c.Components, into)
	}
}

// CycloneDX requires bom-refs to be unique in a document.
func TestBOMRefsAreUnique(t *testing.T) {
	contents := func(context.Context, string) ([]Component, error) {
		return []Component{{Type: "library", Name: "openssl", BOMRef: "pkg:apk/openssl"}}, nil
	}
	shared := []Image{{Repository: "a", Tag: "1"}, {Repository: "b", Tag: "1"}}
	web, _ := ForStack(context.Background(), "web", shared, contents, testNow)
	db, _ := ForStack(context.Background(), "db", shared, contents, testNow)
	refs := map[string]int{}
	collectRefs(Aggregate("fleet", map[string]*Document{"web": web, "db": db}, testNow).Components, refs)
	for ref, n := range refs {
		if n != 1 {
			t.Errorf("%s appears %d times", ref, n)
		}
	}
	// The stacks' own documents stay as they were.
	if !strings.HasPrefix(web.Components[0].BOMRef, "pkg:oci/") {
		t.Errorf("%s", web.Components[0].BOMRef)
	}
}

func TestCachedContents(t *testing.T) {
	calls := 0
	inner := func(context.Context, string) ([]Component, error) {
		calls++
		return []Component{{Type: "library", Name: "x"}}, nil
	}
	c := CachedContents(t.TempDir(), inner)
	ctx := context.Background()
	pinned := "a@" + testDigest
	for range 2 {
		got, err := c(ctx, pinned)
		if err != nil || len(got) != 1 || got[0].Name != "x" {
			t.Fatalf("%v %v", got, err)
		}
	}
	if calls != 1 {
		t.Errorf("a digest was read %d times", calls)
	}
	// A tag can move, and an odd digest mustn't become a path.
	for _, ref := range []string{"a:1", "a:1", "a@sha256:../../etc/passwd"} {
		if _, err := c(ctx, ref); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 4 {
		t.Errorf("%d calls", calls)
	}
}

func TestParseComponents(t *testing.T) {
	got, err := ParseComponents([]byte(`{"bomFormat":"CycloneDX","components":[{"type":"library","name":"openssl","version":"3","purl":"pkg:apk/openssl@3","licenses":[{}]}]}`))
	if err != nil || len(got) != 1 || got[0].PURL != "pkg:apk/openssl@3" {
		t.Errorf("%+v %v", got, err)
	}
	if _, err := ParseComponents([]byte("not json")); err == nil {
		t.Error("garbage must not read as empty")
	}
}
