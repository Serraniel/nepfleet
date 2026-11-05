// SPDX-License-Identifier: AGPL-3.0-or-later

package render

import (
	"testing"

	"github.com/Serraniel/nepfleet/internal/template"
)

// Every value the env file accepts must come back unchanged: a lossy
// round trip would deploy a different secret than the one stored.
func FuzzDotenvRoundTrip(f *testing.F) {
	for _, s := range []string{"plain", "$HOME #not a comment", `"quoted" \ back`, "=a=b=", "", "ünïcode"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		out, err := dotenv(map[string]string{"KEY": v})
		if err != nil {
			return // refused values (quotes, newlines) are reported to the user
		}
		if got := EnvValues(out)["KEY"]; got != v {
			t.Fatalf("round trip of %q gave %q", v, got)
		}
	})
}

func FuzzForSwarm(f *testing.F) {
	f.Add([]byte("services:\n  web:\n    image: nginx\n    labels: {caddy: x, other: y}\n"))
	f.Add([]byte("services:\n  web:\n    labels: [\"caddy=x\", \"kuma.web.http.name=w\"]\n    deploy: {labels: {a: b}}\n"))
	f.Add([]byte("x: [}"))
	f.Fuzz(func(_ *testing.T, compose []byte) {
		_, _ = ForSwarm(compose)
		_, _ = PinImages(compose, map[string]string{"nginx": "sha256:00"})
	})
}

func FuzzCheckInput(f *testing.F) {
	for _, typ := range []string{"string", "text", "secret", "hostname", "url", "email", "int", "bool", "enum", "path"} {
		f.Add(typ, "example.org", "")
	}
	f.Add("string", "abc", "^[a-z]+$")
	f.Fuzz(func(_ *testing.T, typ, v, pattern string) {
		_ = CheckInput(template.Input{Name: "X", Type: typ, Pattern: pattern, Options: []string{"a", "b"}}, v)
	})
}

func FuzzSubstitute(f *testing.F) {
	f.Add([]byte("a: ${A} b: $${B} c: ${C:-x} ${"))
	f.Fuzz(func(_ *testing.T, raw []byte) {
		_ = substitute(raw, map[string]string{"A": "1", "C": "3"})
	})
}
