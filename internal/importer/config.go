// SPDX-License-Identifier: AGPL-3.0-or-later

package importer

import (
	"bytes"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/nepfleet/internal/render"
)

// bindMount is a host path a service mounts.
type bindMount struct {
	service  string
	source   *yaml.Node
	readOnly bool
}

// bindMounts lists the long-form bind mounts of a normalised compose file.
func bindMounts(doc *yaml.Node) []bindMount {
	if len(doc.Content) == 0 {
		return nil
	}
	services := child(doc.Content[0], "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil
	}
	var out []bindMount
	for i := 0; i+1 < len(services.Content); i += 2 {
		vols := child(services.Content[i+1], "volumes")
		if vols == nil || vols.Kind != yaml.SequenceNode {
			continue
		}
		for _, v := range vols.Content {
			typ, src := child(v, "type"), child(v, "source")
			if typ == nil || src == nil || typ.Value != "bind" {
				continue
			}
			ro := child(v, "read_only")
			out = append(out, bindMount{service: services.Content[i].Value, source: src, readOnly: ro != nil && ro.Value == "true"})
		}
	}
	return out
}

// readConfig reads a file or a folder for the fleet repository, under the limits
// stacks/<name>/files/ has. What doesn't fit is data, and stays where it is.
func readConfig(root string) (map[string][]byte, error) {
	fi, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	var total int64
	add := func(rel, path string, size int64) error {
		total += size
		switch {
		case size > render.MaxStackFile:
			return fmt.Errorf("%s is %d KiB; config files may be at most %d KiB", path, size>>10, render.MaxStackFile>>10)
		case total > render.MaxStackFiles || len(out) >= render.MaxFileCount:
			return fmt.Errorf("%s holds more than %d files or %d MiB, which is data, not config", root, render.MaxFileCount, render.MaxStackFiles>>20)
		}
		raw, err := os.ReadFile(path) //nolint:gosec // a path named on the command line
		if err != nil {
			return err
		}
		if bytes.IndexByte(raw, 0) >= 0 {
			return fmt.Errorf("%s is binary, which doesn't belong in the fleet repository", path)
		}
		out[rel] = raw
		return nil
	}
	if fi.Mode().IsRegular() {
		return out, add("", root, fi.Size())
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s is neither a file nor a folder", root)
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is a link or special file, which can't be copied", path)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		return add(filepath.ToSlash(rel), path, info.Size())
	})
	return out, err
}

// A line like `api_key: abc123` in YAML, INI or env style.
var configSecretLine = regexp.MustCompile(`^(\s*(?:export\s+)?)([A-Za-z0-9_.-]+)(\s*[:=]\s*)(["']?)([^\s"'#\\]{8,})(["']?)(\s*)$`)

var notSecretValue = regexp.MustCompile(`(?i)^(true|false|enabled|disabled|https?://[^@]*|/.*|[0-9.]+)$`)

var notEnvName = regexp.MustCompile(`[^A-Z0-9]+`)

// moveConfigSecrets replaces values that look secret with ${NAME}, adding them to
// secrets. It errs on the side of moving: they come back the same when rendered.
func moveConfigSecrets(raw []byte, secrets map[string]string) (out []byte, moved []string) {
	lines := strings.Split(string(raw), "\n")
	for i, line := range lines {
		m := configSecretLine.FindStringSubmatch(line)
		if m == nil || !secretName.MatchString(m[2]) || notSecretValue.MatchString(m[5]) || m[4] != m[6] || strings.Contains(m[5], "${") {
			continue
		}
		name := "CONFIG_" + strings.Trim(notEnvName.ReplaceAllString(strings.ToUpper(m[2]), "_"), "_")
		base := name
		for n := 2; ; n++ {
			have, taken := secrets[name]
			if !taken || have == m[5] {
				break
			}
			name = fmt.Sprintf("%s_%d", base, n)
		}
		secrets[name] = m[5]
		// Marked for now: every other ${ in the file gets escaped next.
		lines[i] = m[1] + m[2] + m[3] + m[4] + "\x00" + name + "\x01" + m[6] + m[7]
		moved = append(moved, name)
	}
	return []byte(strings.Join(lines, "\n")), moved
}

// escapeForRender makes a file render to exactly what it is now: a ${X} of its own
// must stay, whatever values the stack has. The marks become the moved secrets.
func escapeForRender(raw []byte) []byte {
	s := strings.ReplaceAll(string(raw), "${", "$${")
	s = strings.ReplaceAll(s, "\x00", "${")
	s = strings.ReplaceAll(s, "\x01", "}")
	return []byte(s)
}

// copyConfig moves the named bind mounts into the stack's files/ folder, and points
// the compose file at the rendered copies.
func copyConfig(cfg []byte, paths []string, secrets map[string]string) (out []byte, files map[string][]byte, warnings []string, err error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(cfg, &doc); err != nil {
		return nil, nil, nil, err
	}
	mounts := bindMounts(&doc)
	files = map[string][]byte{}
	taken := map[string]bool{}
	for _, p := range paths {
		clean := filepath.Clean(p)
		var users []bindMount
		for _, m := range mounts {
			if filepath.Clean(m.source.Value) == clean {
				users = append(users, m)
			}
		}
		if len(users) == 0 {
			return nil, nil, nil, fmt.Errorf("--copy-config %s: no service mounts that path", p)
		}
		read, err := readConfig(clean)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("--copy-config %s: %w", p, err)
		}
		dest := "config/" + filepath.Base(clean)
		for n := 2; taken[dest]; n++ {
			dest = fmt.Sprintf("config/%s-%d", filepath.Base(clean), n)
		}
		taken[dest] = true
		var moved []string
		for _, rel := range slices.Sorted(maps.Keys(read)) { // sorted: secret names stay the same
			changed, names := moveConfigSecrets(read[rel], secrets)
			moved = append(moved, names...)
			target := dest
			if rel != "" {
				target = dest + "/" + rel
			}
			files[target] = escapeForRender(changed)
		}
		sort.Strings(moved)
		for _, name := range slices.Compact(moved) {
			warnings = append(warnings, fmt.Sprintf("%s: a value is now stored encrypted as ${%s}; check that it's a secret", p, name))
		}
		for _, m := range users {
			m.source.Value = "./" + dest
			if !m.readOnly {
				warnings = append(warnings, fmt.Sprintf("service %s can write to %s: what it writes there is overwritten by each apply, so mount it read-only, or leave it out if the app keeps changing it", m.service, p))
			}
		}
	}
	rendered, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, nil, nil, err
	}
	return rendered, files, warnings, nil
}

// System paths are never offered, whatever their size.
var systemPath = regexp.MustCompile(`^/(etc|proc|sys|dev|run|var/run|var/lib/docker|usr|lib|bin|sbin|boot)(/|$)`)

// configCandidates names the bind mounts that could move into the repository.
func configCandidates(cfg []byte, except []string) []string {
	var doc yaml.Node
	if yaml.Unmarshal(cfg, &doc) != nil {
		return nil
	}
	var out []string
	for _, m := range bindMounts(&doc) {
		p := filepath.Clean(m.source.Value)
		if systemPath.MatchString(p) || slices.Contains(except, p) || slices.Contains(out, p) {
			continue
		}
		if read, err := readConfig(p); err == nil && len(read) > 0 {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// writeConfigFiles writes files/ into a stack's folder.
func writeConfigFiles(dir string, files map[string][]byte) error {
	for rel, raw := range files {
		path := filepath.Join(dir, render.StackFilesDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			return err
		}
	}
	return nil
}
