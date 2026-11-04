// SPDX-License-Identifier: AGPL-3.0-or-later

// Package fleet manages the fleet repository: the git repository that holds
// the desired state of every host and stack. See docs/spec/fleet-repo.md.
package fleet

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// skeleton is written by Init. Directories get a .gitkeep so git tracks them.
var skeleton = map[string]string{
	"fleet.yaml": `# NepFleet fleet repository. Every change here is a commit, every commit
# can be rolled back. See https://github.com/Serraniel/nepfleet/blob/main/docs/spec/fleet-repo.md
apiVersion: nepfleet/v1alpha1
kind: Fleet
metadata:
  name: my-fleet
spec:
  templateSources:
    - name: official
      url: https://github.com/Serraniel/nepfleet-templates
      ref: main
  # Secrets are committed encrypted with SOPS + age. Add your age public key.
  secrets:
    age: []
  # Discord, Slack, Telegram, ntfy, webhooks... see docs/notifications.md.
  # Channel URLs hold tokens: put them in fleet.secret.yaml.
  notifications:
    channels: {}
    routes: []
  updates:
    images: notify   # off | notify | auto
    host: off        # opt-in, see docs/host-updates.md
`,
	".sops.yaml": `creation_rules:
  - path_regex: .*\.secret\.ya?ml$
    age: "" # comma separated age public keys
`,
	".gitignore": `*.decrypted.*
.env.local
`,
	hookPath:           preCommitHook,
	"hosts/.gitkeep":   "",
	"stacks/.gitkeep":  "",
	"tunnels/.gitkeep": "",
	"system/.gitkeep":  "",
}

const hookPath = ".githooks/pre-commit"

// preCommitHook refuses commits of secret files that are not SOPS-encrypted.
// Plain sh, so it works on hosts without nepfleet on PATH.
const preCommitHook = `#!/bin/sh
# Installed by nepfleet init: never commit a plaintext secrets file.
# A file passes only if it has SOPS metadata and every top-level value is
# encrypted (ENC[...]); nepfleet secret check applies the same rule in CI.
status=0
# POSIX sh (dash too): one name per line, unquoted so non-ASCII names match.
git -c core.quotePath=false diff --cached --name-only --diff-filter=ACMR |
  { while IFS= read -r f; do
      case "$f" in *.secret.yaml|*.secret.yml) ;; *) continue ;; esac
      content=$(git show ":$f")
      plain=$(printf '%s\n' "$content" | grep -vE '^(sops:|[[:space:]]|#|$)' | grep -vE '^[^:]+: *ENC\[')
      if ! printf '%s\n' "$content" | grep -q '^sops:' || [ -n "$plain" ]; then
        echo "refusing to commit $f: it is not (fully) SOPS-encrypted (use: nepfleet secret set)" >&2
        status=1
      fi
    done
    exit $status; }
`

// ErrExists is returned when Init would overwrite an existing fleet.
var ErrExists = errors.New("a fleet.yaml already exists here")

// Init writes a new fleet repository to dir and makes it a git repository.
// It refuses to touch a directory that already holds a fleet.
func Init(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "fleet.yaml")); err == nil {
		return ErrExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for name, content := range skeleton {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			return err
		}
		mode := os.FileMode(0o600)
		if name == hookPath {
			mode = 0o700
		}
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return fmt.Errorf("refusing to overwrite: %w", err)
		}
		_, werr := f.WriteString(content)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return werr
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); errors.Is(err, fs.ErrNotExist) {
		if err := git(dir, "init", "--quiet", "--initial-branch=main"); err != nil {
			return err
		}
	}
	// Never replace hooks the user already configured.
	if out, err := exec.Command("git", "-C", dir, "config", "--get", "core.hooksPath").Output(); err == nil && len(out) > 0 {
		return nil
	}
	return git(dir, "config", "core.hooksPath", ".githooks")
}

func git(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir // not an argument, so a path starting with "-" cannot become a flag
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w: %s", args[0], err, out)
	}
	return nil
}

// UnencryptedSecrets lists *.secret.yaml files under dir that are not
// SOPS-encrypted. It is what the pre-commit hook enforces, for CI.
func UnencryptedSecrets(dir string, encrypted func([]byte) bool) ([]string, error) {
	// os.Root keeps the walk inside dir: symlinks cannot point it elsewhere.
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	fsys := root.FS()
	var bad []string
	err = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return fs.SkipDir
		}
		name := d.Name()
		if d.IsDir() || (!strings.HasSuffix(name, ".secret.yaml") && !strings.HasSuffix(name, ".secret.yml")) {
			return nil
		}
		raw, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		if !encrypted(raw) {
			bad = append(bad, filepath.FromSlash(p))
		}
		return nil
	})
	return bad, err
}
