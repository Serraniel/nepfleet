// SPDX-License-Identifier: AGPL-3.0-or-later

package fleet

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Value is one entry of a stack's values: a literal, or a reference to a
// value or secret of another stack, resolved when the stack is rendered:
//
//	values:
//	  DOMAIN: media.example.org
//	  JELLYFIN_KEY: {from: jellyfin, secret: API_KEY}
//	  OLLAMA_URL: {from: ollama, value: URL}
//
// A referenced secret stays in the other stack's encrypted file; it is only
// written into the rendered env file on the host.
type Value struct {
	// Literal is the value written directly.
	Literal string `yaml:"-"`
	// From names the stack whose value or secret is used.
	From string `yaml:"from"`
	// Value is the name of a value (or template input default) of that stack.
	Value string `yaml:"value"`
	// Secret is the name of a secret of that stack, generated ones included.
	Secret string `yaml:"secret"`
}

// Lit is a literal value.
func Lit(s string) Value { return Value{Literal: s} }

// Literals turns plain strings into values.
func Literals(m map[string]string) map[string]Value {
	out := make(map[string]Value, len(m))
	for k, v := range m {
		out[k] = Lit(v)
	}
	return out
}

// IsRef reports a reference to another stack.
func (v Value) IsRef() bool { return v.From != "" }

func (v Value) String() string {
	switch {
	case v.Secret != "":
		return "secret " + v.Secret + " of " + v.From
	case v.IsRef():
		return "value " + v.Value + " of " + v.From
	}
	return v.Literal
}

// UnmarshalYAML accepts a scalar (literal; numbers and booleans as written)
// or a reference mapping with from and exactly one of value or secret.
func (v *Value) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*v = Value{}
		if n.Tag != "!!null" {
			v.Literal = n.Value
		}
		return nil
	case yaml.MappingNode:
		*v = Value{}
		seen := map[string]bool{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, val := n.Content[i], n.Content[i+1]
			if seen[k.Value] {
				return fmt.Errorf("line %d: %s is given twice", k.Line, k.Value)
			}
			seen[k.Value] = true
			if val.Kind != yaml.ScalarNode || val.Tag == "!!null" {
				return fmt.Errorf("line %d: %s must be a name", val.Line, k.Value)
			}
			switch k.Value {
			case "from":
				v.From = val.Value
			case "value":
				v.Value = val.Value
			case "secret":
				v.Secret = val.Value
			default:
				return fmt.Errorf("line %d: a reference has from and value or secret, not %q", k.Line, k.Value)
			}
		}
		switch {
		case !nameRe.MatchString(v.From):
			return fmt.Errorf("line %d: a reference needs from: <stack name>", n.Line)
		case (v.Value == "") == (v.Secret == ""):
			return fmt.Errorf("line %d: a reference to %s needs exactly one of value or secret", n.Line, v.From)
		case !keyRe.MatchString(v.Value + v.Secret):
			return fmt.Errorf("line %d: %q is not a value name (letters, digits and _)", n.Line, v.Value+v.Secret)
		}
		return nil
	}
	return fmt.Errorf("line %d: a value is a scalar or {from, value|secret}", n.Line)
}

var keyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// MarshalJSON uses the stack.yaml shape: a string, or the reference object.
func (v Value) MarshalJSON() ([]byte, error) {
	m, err := v.MarshalYAML()
	if err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

// MarshalYAML writes literals as plain scalars.
func (v Value) MarshalYAML() (any, error) {
	if !v.IsRef() {
		return v.Literal, nil
	}
	m := map[string]string{"from": v.From}
	if v.Secret != "" {
		m["secret"] = v.Secret
	} else {
		m["value"] = v.Value
	}
	return m, nil
}

// SchemaExtend makes the schema accept a scalar or a reference.
func (*Value) SchemaExtend(s map[string]any) {
	ref := map[string]any{}
	for k, v := range s {
		ref[k] = v
		delete(s, k)
	}
	ref["required"] = []string{"from"}
	if props, ok := ref["properties"].(map[string]any); ok {
		props["from"] = map[string]any{"type": "string", "pattern": nameRe.String(), "description": "The stack whose value or secret is used."}
	}
	ref["oneOf"] = []any{
		map[string]any{"required": []string{"value"}},
		map[string]any{"required": []string{"secret"}},
	}
	s["anyOf"] = []any{map[string]any{"type": []any{"string", "number", "boolean", "null"}}, ref}
}

// Refs lists the stacks a stack takes values from, sorted.
func (s *Stack) Refs() []string {
	var out []string
	for _, v := range s.Spec.Values {
		if v.IsRef() && !slices.Contains(out, v.From) {
			out = append(out, v.From)
		}
	}
	slices.Sort(out)
	return out
}

// OrderByRefs sorts stacks so that every stack comes after the stacks it
// takes values from (otherwise in the given order), and refuses cycles. References to
// stacks not in the list are ignored here.
func OrderByRefs(stacks []*Stack) ([]*Stack, error) {
	byName := map[string]*Stack{}
	for _, s := range stacks {
		byName[s.Metadata.Name] = s
	}
	const (
		visiting = 1
		done     = 2
	)
	state := map[string]int{}
	var out []*Stack
	var visit func(s *Stack, path []string) error
	visit = func(s *Stack, path []string) error {
		name := s.Metadata.Name
		switch state[name] {
		case done:
			return nil
		case visiting:
			i := slices.Index(path, name)
			return fmt.Errorf("stacks take values from each other in a cycle: %s", strings.Join(append(path[i:], name), " → "))
		}
		state[name] = visiting
		for _, dep := range s.Refs() {
			if dep == name {
				return fmt.Errorf("%s: a stack cannot take values from itself", name)
			}
			if d := byName[dep]; d != nil {
				if err := visit(d, append(path, name)); err != nil {
					return err
				}
			}
		}
		state[name] = done
		out = append(out, s)
		return nil
	}
	for _, s := range stacks {
		if err := visit(s, nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}
