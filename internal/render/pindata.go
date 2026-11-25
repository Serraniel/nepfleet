// SPDX-License-Identifier: AGPL-3.0-or-later

package render

import (
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// DataLabel is the node label that holds a swarm stack's node-local volumes:
// services that mount one run only where it is set (PinData).
func DataLabel(stack string) string { return "nepfleet.data." + stack }

// PinData keeps a swarm stack's services with node-local volumes on one node:
// such a volume exists only where it was created, so a task scheduled
// elsewhere would start on an empty one. Services that mount a local named
// volume get the constraint node.labels.<DataLabel>==true, unless they set
// placement constraints of their own or run in global mode. Volumes on
// network shares (nfs, cifs driver options) and external ones follow the
// service anywhere and don't pin. It reports the services it pinned.
func PinData(compose []byte, stack string) ([]byte, []string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(compose, &doc); err != nil {
		return nil, nil, err
	}
	if len(doc.Content) == 0 {
		return nil, nil, fmt.Errorf("empty compose document")
	}
	root := doc.Content[0]
	services := mapGet(root, "services")
	if services == nil {
		return compose, nil, nil
	}
	local := localVolumes(mapGet(root, "volumes"))
	constraint := "node.labels." + DataLabel(stack) + "==true"
	var pinned []string
	for i := 0; i+1 < len(services.Content); i += 2 {
		name, svc := services.Content[i].Value, services.Content[i+1]
		if !mountsLocal(svc, local) {
			continue
		}
		deploy := mapGet(svc, "deploy")
		if deploy != nil {
			if m := mapGet(deploy, "mode"); m != nil && m.Value == "global" {
				continue
			}
		} else {
			deploy = &yaml.Node{Kind: yaml.MappingNode}
			mapSet(svc, "deploy", deploy)
		}
		placement := mapGet(deploy, "placement")
		if placement == nil {
			placement = &yaml.Node{Kind: yaml.MappingNode}
			mapSet(deploy, "placement", placement)
		}
		constraints := mapGet(placement, "constraints")
		if constraints == nil || constraints.Kind != yaml.SequenceNode {
			constraints = &yaml.Node{Kind: yaml.SequenceNode}
			mapSet(placement, "constraints", constraints)
		}
		if tiedToANode(constraints) {
			continue // the template keeps it on one node itself
		}
		constraints.Content = append(constraints.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: constraint})
		pinned = append(pinned, name)
	}
	if len(pinned) == 0 {
		return compose, nil, nil
	}
	out, err := encodeYAML(&doc, 4)
	return out, pinned, err
}

// shareTypes are driver_opts types whose volumes live on another machine.
var shareTypes = []string{"nfs", "nfs4", "cifs", "smb", "smb3"}

// localVolumes is the set of top-level volumes that live on the node:
// not external, the local driver, and not a network share.
func localVolumes(vols *yaml.Node) map[string]bool {
	out := map[string]bool{}
	if vols == nil || vols.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i+1 < len(vols.Content); i += 2 {
		key, v := vols.Content[i].Value, vols.Content[i+1]
		if v.Kind == yaml.MappingNode {
			if e := mapGet(v, "external"); e != nil && e.Value == "true" {
				continue
			}
			if d := mapGet(v, "driver"); d != nil && d.Value != "" && d.Value != "local" {
				continue // a volume plugin: where its data is is the plugin's business
			}
			if opts := mapGet(v, "driver_opts"); opts != nil {
				if t := mapGet(opts, "type"); t != nil && slices.Contains(shareTypes, strings.ToLower(t.Value)) {
					continue
				}
			}
		}
		out[key] = true
	}
	return out
}

// mountsLocal reports whether a service mounts one of the local volumes, in
// the short ("data:/data") or the long ({type: volume, source: data}) form.
func mountsLocal(svc *yaml.Node, local map[string]bool) bool {
	vols := mapGet(svc, "volumes")
	if vols == nil || vols.Kind != yaml.SequenceNode {
		return false
	}
	for _, v := range vols.Content {
		var source string
		switch v.Kind {
		case yaml.ScalarNode:
			source, _, _ = strings.Cut(v.Value, ":")
		case yaml.MappingNode:
			if t := mapGet(v, "type"); t != nil && t.Value != "volume" {
				continue
			}
			if s := mapGet(v, "source"); s != nil {
				source = s.Value
			}
		}
		if local[source] {
			return true
		}
	}
	return false
}

// tiedToANode reports whether placement constraints keep a service on one
// node already: by its hostname, its ID, or an equality on a node label.
// Broader ones (node.role==manager, node.platform.os==linux) don't.
func tiedToANode(constraints *yaml.Node) bool {
	for _, c := range constraints.Content {
		v := strings.ReplaceAll(c.Value, " ", "")
		for _, p := range []string{"node.hostname==", "node.id==", "node.labels."} {
			if strings.HasPrefix(v, p) && strings.Contains(v, "==") {
				return true
			}
		}
	}
	return false
}
