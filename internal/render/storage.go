// SPDX-License-Identifier: AGPL-3.0-or-later

package render

import (
	"bytes"
	"fmt"
	"maps"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/nepfleet/internal/fleet"
)

// StoragePasswordVar is the env variable holding a storage's password.
func StoragePasswordVar(storage string) string {
	return "NEPFLEET_STORAGE_" + strings.ToUpper(strings.NewReplacer("-", "_").Replace(storage)) + "_PASSWORD"
}

// WithStorage replaces the compose volumes a stack puts on network storage
// (spec.volumes) with local-driver volumes that mount the share; passwords
// go into env, which is masked, never into the compose file.
func WithStorage(compose []byte, f *fleet.Fleet, s *fleet.Stack, passwords map[string]string, env map[string]string, out *Output) ([]byte, error) {
	if len(s.Spec.Volumes) == 0 {
		return compose, nil
	}
	if err := fleet.CheckVolumeMounts(f, s); err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(compose, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, fmt.Errorf("empty compose file")
	}
	vols := mapGet(doc.Content[0], "volumes")
	for _, key := range slices.Sorted(maps.Keys(s.Spec.Volumes)) {
		m := s.Spec.Volumes[key]
		if vols == nil || vols.Kind != yaml.MappingNode || mapGet(vols, key) == nil {
			return nil, fmt.Errorf("spec.volumes.%s: the compose file has no volume %q", key, key)
		}
		st := f.Spec.Storage[m.Storage]
		pwVar := ""
		if st.PasswordSecret != "" {
			pw, ok := passwords[m.Storage]
			if !ok || pw == "" {
				return nil, fmt.Errorf("spec.volumes.%s: the password of storage %s (%s in fleet.secret.yaml) is not set", key, m.Storage, st.PasswordSecret)
			}
			// Mount options are comma separated; a comma would end the password.
			if strings.ContainsAny(pw, ",\n") {
				return nil, fmt.Errorf("storage %s: the password contains a comma, which mount options cannot hold; choose another", m.Storage)
			}
			pwVar = StoragePasswordVar(m.Storage)
			env[pwVar] = pw
			out.Secret[pwVar] = true
		}
		var node yaml.Node
		if err := node.Encode(fleet.StorageVolume(s.Metadata.Name, key, st, m, pwVar)); err != nil {
			return nil, err
		}
		mapSet(vols, key, &node)
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
