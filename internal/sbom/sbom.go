// SPDX-License-Identifier: AGPL-3.0-or-later

// Package sbom describes what a fleet runs, as CycloneDX: one document per stack,
// listing its images by digest, and one for all of them.
package sbom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Image is an image a stack runs.
type Image struct {
	// Repository is its name without a tag, e.g. ghcr.io/acme/app.
	Repository string
	Tag        string
	// Digest is what's pinned (sha256:…); empty if nothing is, so it can move.
	Digest string
}

// Component is a CycloneDX component, with what's inside an image as children.
type Component struct {
	Type       string      `json:"type"`
	BOMRef     string      `json:"bom-ref,omitempty"`
	Name       string      `json:"name"`
	Version    string      `json:"version,omitempty"`
	PURL       string      `json:"purl,omitempty"`
	Hashes     []Hash      `json:"hashes,omitempty"`
	Components []Component `json:"components,omitempty"`
}

// Hash is a CycloneDX hash.
type Hash struct {
	Alg     string `json:"alg"`
	Content string `json:"content"`
}

// Document is a CycloneDX BOM.
type Document struct {
	BOMFormat   string      `json:"bomFormat"`
	SpecVersion string      `json:"specVersion"`
	Version     int         `json:"version"`
	Metadata    Metadata    `json:"metadata"`
	Components  []Component `json:"components"`
}

// Metadata says what a document is about, and when it was made.
type Metadata struct {
	Timestamp string    `json:"timestamp"`
	Component Component `json:"component"`
}

// Contents lists what's inside an image; nil means images are listed alone.
type Contents func(ctx context.Context, ref string) ([]Component, error)

var digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func purl(i Image) string {
	base := i.Repository
	if j := strings.LastIndex(base, "/"); j >= 0 {
		base = base[j+1:]
	}
	q := url.Values{"repository_url": {i.Repository}}
	if i.Tag != "" {
		q.Set("tag", i.Tag)
	}
	version := ""
	if i.Digest != "" {
		version = "@" + strings.ReplaceAll(i.Digest, ":", "%3A") // as the purl spec writes OCI digests
	}
	return "pkg:oci/" + url.PathEscape(strings.ToLower(base)) + version + "?" + q.Encode()
}

// ImageComponent is one image as a component, referred to by its digest.
func ImageComponent(i Image) Component {
	c := Component{Type: "container", Name: i.Repository, Version: i.Tag, PURL: purl(i), BOMRef: purl(i)}
	if hex, ok := strings.CutPrefix(i.Digest, "sha256:"); ok {
		c.Hashes = []Hash{{Alg: "SHA-256", Content: hex}}
	}
	return c
}

// Reference is how to name an image to a tool: exactly, if it can be.
func (i Image) Reference() string {
	if i.Digest != "" {
		return i.Repository + "@" + i.Digest
	}
	if i.Tag != "" {
		return i.Repository + ":" + i.Tag
	}
	return i.Repository
}

// ForStack makes a stack's document, sorted so a repeat differs only if the stack
// does. An image that can't be looked into is still listed, and reported.
func ForStack(ctx context.Context, stack string, images []Image, contents Contents, now time.Time) (*Document, error) {
	sorted := append([]Image(nil), images...)
	sort.Slice(sorted, func(a, b int) bool { return sorted[a].Reference() < sorted[b].Reference() })
	doc := newDocument(stack, now)
	var errs []error
	for _, i := range sorted {
		c := ImageComponent(i)
		if contents != nil {
			inside, err := contents(ctx, i.Reference())
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", i.Reference(), err))
			}
			c.Components = withoutRefs(inside)
		}
		doc.Components = append(doc.Components, c)
	}
	return doc, errors.Join(errs...)
}

func newDocument(name string, now time.Time) *Document {
	return &Document{
		BOMFormat: "CycloneDX", SpecVersion: "1.6", Version: 1,
		Metadata:   Metadata{Timestamp: now.UTC().Format(time.RFC3339), Component: Component{Type: "application", Name: name}},
		Components: []Component{},
	}
}

// Aggregate is one document for many: each stack, with its images in it.
func Aggregate(name string, stacks map[string]*Document, now time.Time) *Document {
	doc := newDocument(name, now)
	names := make([]string, 0, len(stacks))
	for n := range stacks {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		// bom-refs are unique per document, and stacks can share an image.
		images := make([]Component, 0, len(stacks[n].Components))
		for _, c := range stacks[n].Components {
			c.BOMRef = n + ":" + c.BOMRef
			images = append(images, c)
		}
		doc.Components = append(doc.Components, Component{Type: "application", Name: n, BOMRef: "stack:" + n, Components: images})
	}
	return doc
}

// withoutRefs drops bom-refs from what's inside an image: images can hold the same
// package, refs must be unique per document, and this document doesn't use them.
func withoutRefs(in []Component) []Component {
	out := make([]Component, 0, len(in))
	for _, c := range in {
		c.BOMRef = ""
		c.Components = withoutRefs(c.Components)
		out = append(out, c)
	}
	return out
}

// CachedContents keeps what's inside an image per digest: a digest's contents don't
// change. A reference without a digest isn't cached, as what it means can.
func CachedContents(dir string, inner Contents) Contents {
	return func(ctx context.Context, ref string) ([]Component, error) {
		_, digest, pinned := strings.Cut(ref, "@")
		if !pinned || !digestRe.MatchString(digest) {
			return inner(ctx, ref)
		}
		path := filepath.Join(dir, strings.ReplaceAll(digest, ":", "-")+".json")
		if raw, err := os.ReadFile(path); err == nil { //nolint:gosec // a name made from a checked digest
			var cached []Component
			if json.Unmarshal(raw, &cached) == nil {
				return cached, nil
			}
		}
		found, err := inner(ctx, ref)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(found)
		if err != nil {
			return found, nil //nolint:nilerr // only the cache is lost
		}
		if os.MkdirAll(dir, 0o700) == nil {
			_ = os.WriteFile(path, raw, 0o600)
		}
		return found, nil
	}
}

// ParseComponents reads the components out of a CycloneDX document, as Syft writes
// them. Text from a tool: fields this doesn't know are dropped.
func ParseComponents(raw []byte) ([]Component, error) {
	var doc struct {
		Components []Component `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	return doc.Components, nil
}
