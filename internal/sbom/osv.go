// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
)

// OSVURL is the advisory database's API, which covers GitHub's advisories too.
const OSVURL = "https://api.osv.dev"

const (
	osvBatch     = 1000 // the API's limit per request
	osvMaxPages  = 20
	osvMaxDetail = 500
)

// Advisory is one known vulnerability in a package of an image.
type Advisory struct {
	Image    string `json:"image"`
	Package  string `json:"package"`
	ID       string `json:"id"`
	Summary  string `json:"summary,omitempty"`
	Severity string `json:"severity,omitempty"`
	Fixed    string `json:"fixed,omitempty"`
}

// OSV asks the advisory database about packages. It sends package URLs only: not
// image names, hosts or anything else about the fleet.
type OSV struct {
	URL    string
	Client *http.Client
}

func (o *OSV) post(ctx context.Context, path string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.URL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return o.do(req, out)
}

func (o *OSV) do(req *http.Request, out any) error {
	client := o.Client
	if client == nil {
		client = &http.Client{Timeout: time.Minute}
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("the advisory database answered %s", res.Status)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 64<<20)).Decode(out)
}

type osvQuery struct {
	Package struct {
		PURL string `json:"purl"`
	} `json:"package"`
	PageToken string `json:"page_token,omitempty"`
}

// vulnsOf returns the IDs of the known vulnerabilities per package URL.
func (o *OSV) vulnsOf(ctx context.Context, purls []string) (map[string][]string, error) {
	out := map[string][]string{}
	for start := 0; start < len(purls); start += osvBatch {
		batch := purls[start:min(start+osvBatch, len(purls))]
		queries := make([]osvQuery, len(batch))
		for i, p := range batch {
			queries[i].Package.PURL = p
		}
		for page := 0; len(queries) > 0; page++ {
			if page == osvMaxPages {
				return nil, fmt.Errorf("the advisory database still has more after %d pages", osvMaxPages)
			}
			var answer struct {
				Results []struct {
					Vulns []struct {
						ID string `json:"id"`
					} `json:"vulns"`
					Next string `json:"next_page_token"`
				} `json:"results"`
			}
			if err := o.post(ctx, "/v1/querybatch", map[string]any{"queries": queries}, &answer); err != nil {
				return nil, err
			}
			// An answer of another length can't be matched up: never read it as clean.
			if len(answer.Results) != len(queries) {
				return nil, fmt.Errorf("the advisory database answered %d results for %d questions", len(answer.Results), len(queries))
			}
			var more []osvQuery
			for i, r := range answer.Results {
				p := queries[i].Package.PURL
				for _, v := range r.Vulns {
					out[p] = append(out[p], v.ID)
				}
				if r.Next != "" {
					q := queries[i]
					q.PageToken = r.Next
					more = append(more, q)
				}
			}
			queries = more
		}
	}
	return out, nil
}

type osvDetail struct {
	Summary  string `json:"summary"`
	Specific struct {
		Severity string `json:"severity"`
	} `json:"database_specific"`
	Affected []struct {
		Ranges []struct {
			Events []struct {
				Fixed string `json:"fixed"`
			} `json:"events"`
		} `json:"ranges"`
	} `json:"affected"`
}

func (d osvDetail) fixed() string {
	var all []string
	for _, a := range d.Affected {
		for _, r := range a.Ranges {
			for _, e := range r.Events {
				if e.Fixed != "" && !slices.Contains(all, e.Fixed) {
					all = append(all, e.Fixed)
				}
			}
		}
	}
	return strings.Join(all, ", ")
}

func (o *OSV) detail(ctx context.Context, id string) (osvDetail, error) {
	var d osvDetail
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.URL+"/v1/vulns/"+id, nil)
	if err != nil {
		return d, err
	}
	return d, o.do(req, &d)
}

// imagesIn finds the images in a document with what's inside them.
func imagesIn(cs []Component, visit func(image Component)) {
	for _, c := range cs {
		if c.Type == "container" {
			visit(c)
			continue
		}
		imagesIn(c.Components, visit)
	}
}

func packagesIn(cs []Component, into map[string]bool) {
	for _, c := range cs {
		if c.PURL != "" {
			into[c.PURL] = true
		}
		packagesIn(c.Components, into)
	}
}

// Check looks up every package in a document. An error means it doesn't know, which
// is never the same as finding nothing. An image with no contents can't be checked,
// and is named in the second result.
func (o *OSV) Check(ctx context.Context, doc *Document) (found []Advisory, unchecked []string, err error) {
	byImage := map[string]map[string]bool{}
	all := map[string]bool{}
	imagesIn(doc.Components, func(image Component) {
		name := image.Name
		if image.Version != "" {
			name += ":" + image.Version
		}
		if len(image.Components) == 0 {
			unchecked = append(unchecked, name)
			return
		}
		pkgs := map[string]bool{}
		packagesIn(image.Components, pkgs)
		if byImage[name] == nil {
			byImage[name] = map[string]bool{}
		}
		for p := range pkgs {
			byImage[name][p] = true
			all[p] = true
		}
	})
	purls := make([]string, 0, len(all))
	for p := range all {
		purls = append(purls, p)
	}
	sort.Strings(purls)
	vulns, err := o.vulnsOf(ctx, purls)
	if err != nil {
		return nil, unchecked, err
	}
	details := map[string]osvDetail{}
	for image, pkgs := range byImage {
		for p := range pkgs {
			for _, id := range vulns[p] {
				if _, known := details[id]; !known {
					// A summary is a nicety: a list too long to detail still counts.
					if len(details) < osvMaxDetail {
						d, err := o.detail(ctx, id)
						if err != nil {
							return nil, unchecked, err
						}
						details[id] = d
					}
				}
				d := details[id]
				found = append(found, Advisory{Image: image, Package: p, ID: id, Summary: d.Summary, Severity: d.Specific.Severity, Fixed: d.fixed()})
			}
		}
	}
	sort.Slice(found, func(a, b int) bool {
		x, y := found[a], found[b]
		return x.Image+x.Package+x.ID < y.Image+y.Package+y.ID
	})
	slices.Sort(unchecked)
	return found, slices.Compact(unchecked), nil
}
