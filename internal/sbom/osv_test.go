// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeOSV(t *testing.T, handle func(queries []osvQuery) any) (*OSV, *[]string) {
	t.Helper()
	var bodies []string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/querybatch", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		var in struct {
			Queries []osvQuery `json:"queries"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(handle(in.Queries))
	})
	mux.HandleFunc("/v1/vulns/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"summary":"Command Injection","database_specific":{"severity":"HIGH"},"affected":[{"ranges":[{"events":[{"introduced":"0"},{"fixed":"4.17.21"}]}]}]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &OSV{URL: srv.URL, Client: srv.Client()}, &bodies
}

func docWith(images ...Component) *Document {
	d := newDocument("fleet", testNow)
	d.Components = []Component{{Type: "application", Name: "web", Components: images}}
	return d
}

func TestCheckFindsAndNamesAdvisories(t *testing.T) {
	o, bodies := fakeOSV(t, func(q []osvQuery) any {
		results := make([]map[string]any, len(q))
		for i, x := range q {
			results[i] = map[string]any{}
			if strings.Contains(x.Package.PURL, "lodash") {
				results[i]["vulns"] = []map[string]string{{"id": "GHSA-1"}}
			}
		}
		return map[string]any{"results": results}
	})
	doc := docWith(
		Component{Type: "container", Name: "ghcr.io/secret-org/app", Version: "1", Components: []Component{
			{Type: "library", Name: "lodash", PURL: "pkg:npm/lodash@4.17.15"},
			{Type: "library", Name: "safe", PURL: "pkg:npm/safe@1"},
			{Type: "library", Name: "no-purl"},
		}},
		Component{Type: "container", Name: "nginx", Version: "1"},
	)
	found, unchecked, err := o.Check(context.Background(), doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Fatalf("%+v", found)
	}
	f := found[0]
	if f.Image != "ghcr.io/secret-org/app:1" || f.ID != "GHSA-1" || f.Severity != "HIGH" || f.Fixed != "4.17.21" || f.Summary != "Command Injection" {
		t.Errorf("%+v", f)
	}
	if len(unchecked) != 1 || unchecked[0] != "nginx:1" {
		t.Errorf("an image with no contents wasn't checked: %v", unchecked)
	}
	// Only package URLs leave: no image names.
	if strings.Contains(strings.Join(*bodies, ""), "secret-org") {
		t.Errorf("sent: %v", *bodies)
	}
}

func TestCheckFollowsPages(t *testing.T) {
	calls := 0
	o, _ := fakeOSV(t, func(q []osvQuery) any {
		calls++
		if q[0].PageToken == "" {
			return map[string]any{"results": []map[string]any{{"vulns": []map[string]string{{"id": "A"}}, "next_page_token": "more"}}}
		}
		return map[string]any{"results": []map[string]any{{"vulns": []map[string]string{{"id": "B"}}}}}
	})
	doc := docWith(Component{Type: "container", Name: "a", Components: []Component{{Type: "library", PURL: "pkg:npm/x@1"}}})
	found, _, err := o.Check(context.Background(), doc)
	if err != nil || len(found) != 2 || calls != 2 {
		t.Errorf("%+v %v, %d calls", found, err, calls)
	}
}

// Not knowing must never read as clean.
func TestCheckFailsRatherThanSaysClean(t *testing.T) {
	doc := docWith(Component{Type: "container", Name: "a", Components: []Component{{Type: "library", PURL: "pkg:npm/x@1"}}})
	short, _ := fakeOSV(t, func([]osvQuery) any { return map[string]any{"results": []any{}} })
	if _, _, err := short.Check(context.Background(), doc); err == nil {
		t.Error("a short answer read as clean")
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer down.Close()
	if _, _, err := (&OSV{URL: down.URL, Client: down.Client()}).Check(context.Background(), doc); err == nil {
		t.Error("an error read as clean")
	}
	endless, _ := fakeOSV(t, func([]osvQuery) any {
		return map[string]any{"results": []map[string]any{{"next_page_token": "again"}}}
	})
	if _, _, err := endless.Check(context.Background(), doc); err == nil {
		t.Error("endless pages must stop")
	}
}
