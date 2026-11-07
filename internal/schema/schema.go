// SPDX-License-Identifier: AGPL-3.0-or-later

// Package schema generates JSON Schemas for NepFleet's YAML files from the
// Go types that decode them, so the schemas cannot drift from the parser.
//
// Field descriptions come from the Go doc comments. Constraints come from
// `schema` struct tags, separated by semicolons:
//
//	schema:"required;enum=compose|swarm;default=compose;pattern=^[a-z]+$;minimum=1;maximum=9"
package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/template"
)

// BaseURL is the $id prefix of the generated schemas (main; releases attach
// the same files, and written files link to their release's tag).
const BaseURL = "https://raw.githubusercontent.com/Serraniel/nepfleet/main/schemas/"

// File is one generated schema.
type File struct {
	Name  string // e.g. template.schema.json
	Title string
	Type  any
	// Source is the package directory (relative to the module root) whose
	// doc comments describe Type.
	Source string
}

// Files lists every schema NepFleet publishes.
var Files = []File{
	{"fleet.schema.json", "NepFleet fleet.yaml", fleet.Fleet{}, "internal/fleet"},
	{"stack.schema.json", "NepFleet stack.yaml", fleet.Stack{}, "internal/fleet"},
	{"host.schema.json", "NepFleet hosts/<name>.yaml", fleet.Host{}, "internal/fleet"},
	{"template.schema.json", "NepFleet template.yaml", template.Template{}, "internal/template"},
}

// All renders every schema in Files. root is the module root, used to read
// the doc comments.
func All(root string) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, f := range Files {
		docs, err := Comments(filepath.Join(root, f.Source))
		if err != nil {
			return nil, err
		}
		b, err := Generate(f.Type, BaseURL+f.Name, f.Title, docs)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Name, err)
		}
		out[f.Name] = b
	}
	return out, nil
}

// Docs maps "Type" and "Type.Field.SubField" to their doc comments.
type Docs map[string]string

// Comments reads the doc comments of the struct types in a package directory.
func Comments(dir string) (Docs, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	docs := Docs{}
	fset := token.NewFileSet()
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.TYPE {
				continue
			}
			for _, sp := range g.Specs {
				ts := sp.(*ast.TypeSpec)
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				doc := ts.Doc
				if doc == nil && len(g.Specs) == 1 {
					doc = g.Doc
				}
				if t := text(doc, nil); t != "" {
					docs[ts.Name.Name] = t
				}
				fields(docs, ts.Name.Name, st)
			}
		}
	}
	return docs, nil
}

func fields(docs Docs, prefix string, st *ast.StructType) {
	for _, f := range st.Fields.List {
		for _, name := range f.Names {
			key := prefix + "." + name.Name
			if t := text(f.Doc, f.Comment); t != "" {
				docs[key] = t
			}
			if inner, ok := f.Type.(*ast.StructType); ok {
				fields(docs, key, inner)
			}
		}
	}
}

func text(doc, line *ast.CommentGroup) string {
	if t := strings.TrimSpace(doc.Text()); t != "" {
		return strings.Join(strings.Fields(t), " ")
	}
	return strings.Join(strings.Fields(line.Text()), " ")
}

// Generate renders the schema for v's type.
func Generate(v any, id, title string, docs Docs) ([]byte, error) {
	t := reflect.TypeOf(v)
	g := gen{docs: docs}
	s, err := g.schema(t, t.Name())
	if err != nil {
		return nil, err
	}
	s["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	s["$id"] = id
	s["title"] = title
	if d := docs[t.Name()]; d != "" {
		s["description"] = d
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type gen struct{ docs Docs }

type obj = map[string]any

// schema describes t. path is the doc key of t ("Type" or "Type.Field").
func (g gen) schema(t reflect.Type, path string) (obj, error) {
	switch t.Kind() {
	case reflect.Pointer:
		return g.schema(t.Elem(), path)
	case reflect.String:
		return obj{"type": "string"}, nil
	case reflect.Bool:
		return obj{"type": "boolean"}, nil
	case reflect.Int, reflect.Int32, reflect.Int64:
		return obj{"type": "integer"}, nil
	case reflect.Interface:
		return obj{}, nil
	case reflect.Slice:
		items, err := g.schema(t.Elem(), elemPath(t.Elem(), path))
		if err != nil {
			return nil, err
		}
		return obj{"type": "array", "items": items}, nil
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("%s: map keys must be strings", path)
		}
		vals, err := g.schema(t.Elem(), elemPath(t.Elem(), path))
		if err != nil {
			return nil, err
		}
		return obj{"type": "object", "additionalProperties": vals}, nil
	case reflect.Struct:
		return g.object(t, path)
	}
	return nil, fmt.Errorf("%s: unsupported kind %s", path, t.Kind())
}

// elemPath keeps describing a named element type by its own doc comments.
func elemPath(t reflect.Type, path string) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Name() != "" && t.Kind() == reflect.Struct {
		return t.Name()
	}
	return path
}

func (g gen) object(t reflect.Type, path string) (obj, error) {
	props := obj{}
	var required []string
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "-" || !f.IsExported() {
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		own := path + "." + f.Name
		// Anonymous structs are described under the field; named types
		// under their own name.
		fpath := elemPath(f.Type, own)
		s, err := g.schema(f.Type, fpath)
		if err != nil {
			return nil, err
		}
		// A field's own comment wins over its type's.
		if d := g.docs[own]; d != "" {
			s["description"] = d
		} else if d := g.docs[fpath]; d != "" && f.Type.Kind() != reflect.Slice {
			s["description"] = d
		}
		req, err := constrain(s, f.Tag.Get("schema"))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", fpath, err)
		}
		loosen(s, req)
		if req {
			required = append(required, name)
		}
		props[name] = s
	}
	o := obj{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		sort.Strings(required)
		o["required"] = required
	}
	if x, ok := reflect.New(t).Interface().(interface{ SchemaExtend(map[string]any) }); ok {
		x.SchemaExtend(o)
	}
	return o, nil
}

// loosen makes a field's schema accept what yaml.v3 accepts for it, so
// editors do not report errors the parser would not:
//   - a required string must not be empty (the validators check values);
//   - an optional enum may be empty, the parser then applies the default;
//   - a free-form string may be written as a number or boolean, which
//     yaml.v3 turns into a string;
//   - an empty list or map may be written as null (key with no value).
func loosen(s obj, required bool) {
	scalars := []any{"string", "number", "boolean"}
	switch s["type"] {
	case "string":
		switch {
		case s["enum"] != nil:
			if !required {
				s["enum"] = append([]string{""}, s["enum"].([]string)...)
			}
		case s["const"] != nil || s["pattern"] != nil:
		case required:
			s["minLength"] = 1
		default:
			s["type"] = scalars
		}
	case "array", "object":
		s["type"] = []any{s["type"], "null"}
		if items, ok := s["items"].(obj); ok && items["type"] == "string" && items["enum"] == nil && items["pattern"] == nil {
			items["type"] = scalars
		}
		if vals, ok := s["additionalProperties"].(obj); ok && vals["type"] == "string" {
			vals["type"] = append(scalars, "null")
		}
	}
}

// constrain applies a schema tag and reports whether the field is required.
func constrain(s obj, tag string) (required bool, err error) {
	if tag == "" {
		return false, nil
	}
	for part := range strings.SplitSeq(tag, ";") {
		k, v, _ := strings.Cut(part, "=")
		target := s
		if items, ok := s["items"].(obj); ok && k != "required" {
			target = items // constraints on a list apply to its elements
		}
		switch k {
		case "required":
			required = true
		case "enum":
			target["enum"] = strings.Split(v, "|")
		case "const":
			target["const"] = v
		case "default":
			if target["type"] == "boolean" {
				target["default"] = v == "true"
			} else {
				target["default"] = v
			}
		case "pattern":
			target["pattern"] = v
		case "minItems":
			n, err := strconv.Atoi(v)
			if err != nil {
				return false, fmt.Errorf("%s: %w", k, err)
			}
			s[k] = n
		case "minimum", "maximum":
			n, err := strconv.Atoi(v)
			if err != nil {
				return false, fmt.Errorf("%s: %w", k, err)
			}
			target[k] = n
		default:
			return false, fmt.Errorf("unknown schema tag %q", k)
		}
	}
	return required, nil
}
