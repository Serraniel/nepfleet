// SPDX-License-Identifier: AGPL-3.0-or-later

package render

import (
	"bytes"

	"go.yaml.in/yaml/v3"
)

// encodeYAML writes a compose document the render steps changed. indent
// keeps each step's output as it was (2, or yaml.Marshal's 4).
func encodeYAML(doc *yaml.Node, indent int) ([]byte, error) {
	keepNulls(doc, false)
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(indent)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// keepNulls spells out empty nulls inside flow collections. The encoder
// can't write an empty plain scalar there and writes an empty quoted
// string, so environment: {TZ: } set TZ to "" instead of taking it from
// .env. In block style an empty null stays as it was.
func keepNulls(n *yaml.Node, flow bool) {
	if n == nil {
		return
	}
	flow = flow || n.Style&yaml.FlowStyle != 0
	if flow && n.Kind == yaml.ScalarNode && n.Tag == "!!null" && n.Value == "" {
		n.Value = "null"
	}
	for _, c := range n.Content {
		keepNulls(c, flow)
	}
}
