// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type openAPIDoc struct {
	OpenAPI string                    `yaml:"openapi"`
	Paths   map[string]map[string]any `yaml:"paths"`
	Comps   struct {
		Schemas map[string]struct {
			Required   []string       `yaml:"required"`
			Properties map[string]any `yaml:"properties"`
		} `yaml:"schemas"`
	} `yaml:"components"`
}

func loadOpenAPI(t *testing.T) openAPIDoc {
	t.Helper()
	var d openAPIDoc
	if err := yaml.Unmarshal(openAPISpec, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

var routeRe = regexp.MustCompile(`"((?:GET|POST|PUT|PATCH|DELETE) /[^"]*)"`)

// The document and the code's routes are the same two sets.
func TestOpenAPICoversEveryRoute(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var inCode []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f) //nolint:gosec // this package's own sources
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range routeRe.FindAllStringSubmatch(string(raw), -1) {
			inCode = append(inCode, m[1])
		}
	}
	if len(inCode) < 10 {
		t.Fatalf("found only %d routes in the code: %v", len(inCode), inCode)
	}
	var inDoc []string
	for path, ops := range loadOpenAPI(t).Paths {
		for method := range ops {
			inDoc = append(inDoc, strings.ToUpper(method)+" "+path)
		}
	}
	slices.Sort(inCode)
	slices.Sort(inDoc)
	inCode = slices.Compact(inCode)
	for _, r := range inCode {
		if !slices.Contains(inDoc, r) {
			t.Errorf("%s isn't in openapi.yaml", r)
		}
	}
	for _, r := range inDoc {
		if !slices.Contains(inCode, r) {
			t.Errorf("%s is in openapi.yaml but not the code", r)
		}
	}
}

func jsonFields(typ reflect.Type) (all, required []string) {
	for f := range typ.Fields() {
		name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		all = append(all, name)
		if !strings.Contains(opts, "omit") {
			required = append(required, name)
		}
	}
	return all, required
}

// The answers the document describes have the fields the code sends.
func TestOpenAPISchemasMatchTypes(t *testing.T) {
	schemas := loadOpenAPI(t).Comps.Schemas
	for name, typ := range map[string]reflect.Type{
		"Info":              reflect.TypeFor[Info](),
		"StackInfo":         reflect.TypeFor[StackInfo](),
		"HistoryEntry":      reflect.TypeFor[HistoryEntry](),
		"Job":               reflect.TypeFor[Job](),
		"Alert":             reflect.TypeFor[Alert](),
		"ImageUpdate":       reflect.TypeFor[ImageUpdate](),
		"KumaMaintenance":   reflect.TypeFor[KumaMaintenance](),
		"MaintenanceWindow": reflect.TypeFor[MaintenanceWindow](),
		"StackEgress":       reflect.TypeFor[StackEgress](),
		"StackFile":         reflect.TypeFor[StackFile](),
		"EditResult":        reflect.TypeFor[EditResult](),
		"SyncStatus":        reflect.TypeFor[SyncStatus](),
		"SecretKeys":        reflect.TypeFor[SecretKeys](),
		"Feature":           reflect.TypeFor[Feature](),
		"Channels":          reflect.TypeFor[Channels](),
		"TokenInfo":         reflect.TypeFor[TokenInfo](),
		"NewToken":          reflect.TypeFor[NewToken](),
		"CreatedToken":      reflect.TypeFor[CreatedToken](),
		"DoctorCheck":       reflect.TypeFor[DoctorCheck](),
		"SecurityFinding":   reflect.TypeFor[SecurityFinding](),
		"DoctorReport":      reflect.TypeFor[DoctorReport](),
		"UserInfo":          reflect.TypeFor[UserInfo](),
		"EgressAllow":       reflect.TypeFor[EgressAllow](),
		"RenderedFile":      reflect.TypeFor[RenderedFile](),
		"KeyBackupRequest":  reflect.TypeFor[KeyBackupRequest](),
		"MirrorStatus":      reflect.TypeFor[MirrorStatus](),
		"EgressAllowResult": reflect.TypeFor[EgressAllowResult](),
		"PasskeyInfo":       reflect.TypeFor[PasskeyInfo](),
		"SecretValue":       reflect.TypeFor[SecretValue](),
		"SecretResult":      reflect.TypeFor[SecretResult](),
		"NewStackResult":    reflect.TypeFor[NewStackResult](),
	} {
		s, ok := schemas[name]
		if !ok {
			t.Errorf("no schema %s", name)
			continue
		}
		all, required := jsonFields(typ)
		for _, f := range all {
			if _, ok := s.Properties[f]; !ok {
				t.Errorf("%s.%s isn't in openapi.yaml", name, f)
			}
		}
		for f := range s.Properties {
			if !slices.Contains(all, f) {
				t.Errorf("%s.%s is in openapi.yaml but not the code", name, f)
			}
		}
		got := slices.Clone(s.Required)
		slices.Sort(got)
		slices.Sort(required)
		if !slices.Equal(got, required) {
			t.Errorf("%s required: %v, the code always sends %v", name, got, required)
		}
	}
}

func TestOpenAPIIsServed(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler(Options{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/openapi.yaml", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "openapi: 3.1.0") {
		t.Errorf("%d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/yaml" {
		t.Errorf("content type: %s", got)
	}
}
