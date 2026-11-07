// SPDX-License-Identifier: AGPL-3.0-or-later

package render

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Limits for stacks/<name>/files/: config belongs in git, data does not.
const (
	maxStackFile  = 1 << 20 // per file
	maxStackFiles = 8 << 20 // all files of a stack
	maxFileCount  = 500
)

// StackFilesDir is the stack's folder of config files in the fleet repo.
const StackFilesDir = "files"

// stackFiles renders stacks/<name>/files/<path> to <path> in the stack's
// working directory, with ${VAR} substituted like template files. A stack's
// file replaces a template file with the same destination: the template
// ships defaults, the fleet repository owns what it overrides.
func stackFiles(dir string, env map[string]string, out *Output) error {
	root := filepath.Join(dir, StackFilesDir)
	if fi, err := os.Lstat(root); err != nil || !fi.IsDir() {
		return nil //nolint:nilerr // no files folder: nothing to render
	}
	var total, count int
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			// A link could point anywhere on the machine running NepFleet.
			return fmt.Errorf("%s/%s: symbolic links are not rendered", StackFilesDir, rel)
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			return fmt.Errorf("%s/%s: not a regular file", StackFilesDir, rel)
		case rel == ComposeFile || rel == EnvFile || strings.HasPrefix(rel, ".nepfleet/"):
			return fmt.Errorf("%s/%s: this name belongs to NepFleet", StackFilesDir, rel)
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		count++
		total += int(fi.Size())
		switch {
		case fi.Size() > maxStackFile:
			return fmt.Errorf("%s/%s is %d KiB; config files may be at most %d KiB (keep data in volumes)", StackFilesDir, rel, fi.Size()>>10, maxStackFile>>10)
		case total > maxStackFiles || count > maxFileCount:
			return fmt.Errorf("%s/ holds more than %d files or %d MiB; keep data in volumes", StackFilesDir, maxFileCount, maxStackFiles>>20)
		}
		raw, err := os.ReadFile(path) //nolint:gosec // inside the stack's folder, links refused above
		if err != nil {
			return err
		}
		out.Files[rel] = substitute(raw, env)
		return nil
	})
}

// MaskSecrets hides the values of secrets in a rendered file other than the
// env file (a config file may carry ${API_KEY}), for plans and output.
func (o *Output) MaskSecrets(content []byte) []byte {
	if len(o.secretValues) == 0 {
		return content
	}
	s := string(content)
	for _, v := range o.secretValues {
		s = strings.ReplaceAll(s, v, "••••••")
	}
	return []byte(s)
}

// collectSecretValues remembers secret values to mask, longest first so a
// value containing another is masked whole.
func (o *Output) collectSecretValues(env map[string]string) {
	for k := range o.Secret {
		if v := env[k]; len(v) >= 4 {
			o.secretValues = append(o.secretValues, v)
		}
	}
	sort.Slice(o.secretValues, func(i, j int) bool { return len(o.secretValues[i]) > len(o.secretValues[j]) })
}
