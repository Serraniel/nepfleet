// SPDX-License-Identifier: AGPL-3.0-or-later

package fleet

import (
	"encoding/json"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestValueYAML(t *testing.T) {
	var got struct {
		Values map[string]Value `yaml:"values"`
	}
	in := "values:\n  A: x\n  N: 8080\n  E:\n  K: {from: jellyfin, secret: API_KEY}\n  U: {from: ollama, value: URL}\n"
	if err := yaml.Unmarshal([]byte(in), &got); err != nil {
		t.Fatal(err)
	}
	v := got.Values
	if v["A"].Literal != "x" || v["N"].Literal != "8080" || v["E"].Literal != "" || v["A"].IsRef() {
		t.Errorf("literals: %+v", v)
	}
	if k := v["K"]; !k.IsRef() || k.From != "jellyfin" || k.Secret != "API_KEY" {
		t.Errorf("secret ref: %+v", k)
	}
	if u := v["U"]; u.From != "ollama" || u.Value != "URL" {
		t.Errorf("value ref: %+v", u)
	}
	out, err := yaml.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "A: x") || !strings.Contains(string(out), "secret: API_KEY") {
		t.Errorf("round trip:\n%s", out)
	}
	for _, bad := range []string{
		"values: {K: {from: jellyfin}}",
		"values: {K: {from: jellyfin, value: A, secret: B}}",
		"values: {K: {secret: B}}",
		"values: {K: {from: Bad Name, value: A}}",
		"values: {K: {from: a, value: A, extra: 1}}",
		"values: {K: [1]}",
		"values: {K: {from: a, value: ~}}",
		"values: {K: {from: a, value: b, from: c}}",
		"values: {K: {from: a, secret: \"x y\"}}",
	} {
		if err := yaml.Unmarshal([]byte(bad), &got); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
}

func TestValueJSON(t *testing.T) {
	out, err := json.Marshal(map[string]Value{"A": Lit("x"), "K": {From: "jellyfin", Secret: "API_KEY"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"A":"x","K":{"from":"jellyfin","secret":"API_KEY"}}` {
		t.Errorf("json: %s", out)
	}
}

func stack(name string, refs ...string) *Stack {
	s := &Stack{}
	s.Metadata.Name = name
	s.Spec.Values = map[string]Value{}
	for i, r := range refs {
		s.Spec.Values[string(rune('A'+i))] = Value{From: r, Value: "X"}
	}
	return s
}

func TestOrderByRefs(t *testing.T) {
	got, err := OrderByRefs([]*Stack{stack("homepage", "jellyfin", "paperless"), stack("jellyfin"), stack("other"), stack("paperless", "postgres"), stack("postgres")})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range got {
		names = append(names, s.Metadata.Name)
	}
	if strings.Join(names, " ") != "jellyfin postgres paperless homepage other" {
		t.Errorf("order: %v", names)
	}
	// References to stacks outside the list are ignored.
	if _, err := OrderByRefs([]*Stack{stack("a", "elsewhere")}); err != nil {
		t.Error(err)
	}
	_, err = OrderByRefs([]*Stack{stack("a", "b"), stack("b", "c"), stack("c", "a")})
	if err == nil || !strings.Contains(err.Error(), "a → b → c → a") {
		t.Errorf("cycle: %v", err)
	}
}
