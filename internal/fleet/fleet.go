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
	"hosts/.gitkeep":   "",
	"stacks/.gitkeep":  "",
	"tunnels/.gitkeep": "",
	"system/.gitkeep":  "",
}

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
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
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
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return nil
	}
	cmd := exec.Command("git", "init", "--quiet", "--initial-branch=main")
	cmd.Dir = dir // not an argument, so a path starting with "-" cannot become a flag
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git init: %w: %s", err, out)
	}
	return nil
}
