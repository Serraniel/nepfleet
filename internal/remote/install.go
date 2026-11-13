// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// InstallDir is where the container install lives: compose.yaml mounts it at
// the same path inside, so it can't move.
const InstallDir = "/srv/nepfleet"

// composeImageRe is the image line of the shipped compose.yaml.
var composeImageRe = regexp.MustCompile(`(?m)^(\s*image:\s*)ghcr\.io/serraniel/nepfleet:\S+(.*)$`)

// releaseRe is a released version, which has an image of its own.
var releaseRe = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+$`)

// PinCompose sets the shipped compose.yaml's image to this NepFleet's version, so
// the machine runs what installed it; a development build takes the latest
// release.
func PinCompose(compose []byte, version string) ([]byte, error) {
	if !composeImageRe.Match(compose) {
		return nil, errors.New("compose.yaml has no NepFleet image line")
	}
	tag := "latest"
	if releaseRe.MatchString(version) {
		tag = strings.TrimPrefix(version, "v")
	}
	return composeImageRe.ReplaceAll(compose, []byte("${1}ghcr.io/serraniel/nepfleet:"+tag+"${2}")), nil
}

// Heredoc markers: the scripts and the compose file never hold them.
const (
	composeEOF = "NEPFLEET_COMPOSE_EOF"
)

// InstallPlan installs NepFleet as a container on a probed machine, as
// docs/install.md does by hand: compose.yaml and .env in /srv/nepfleet, an age
// key generated there (it never leaves the machine), a fleet repository that
// encrypts to it, and the API on the machine's loopback. It changes nothing.
// skipDir: /srv/nepfleet exists already, owned by the user, so no root is needed.
func InstallPlan(r *Report, name string, compose []byte, skipDir bool) ([]Step, error) {
	switch {
	case r.Kernel != "Linux":
		return nil, fmt.Errorf("this is %s, and NepFleet's container runs on Linux", r.Kernel)
	case r.Arch != "x86_64" && r.Arch != "aarch64":
		return nil, fmt.Errorf("this is %s, and NepFleet's image is for x86_64 and aarch64", r.Arch)
	case r.NepFleetRunning || r.NepFleetImage != "":
		return nil, errors.New("a NepFleet already runs here: connect to it with nepfleet remote add")
	case r.Fleet != "":
		return nil, fmt.Errorf("a fleet repository is already here (%s): adopt it instead of installing over it", r.Fleet)
	case !r.Docker || r.Compose == "":
		return nil, errors.New("no Docker or no Compose plugin here: run nepfleet host bootstrap first")
	case !r.DockerRunning:
		return nil, errors.New("this user can't reach Docker: if host bootstrap just added it to the docker group, that counts from the next login, so run this again")
	case !unixRe.MatchString(r.User):
		return nil, fmt.Errorf("%q is not a user name this can give the folder to", r.User)
	case !hostNameRe.MatchString(name):
		return nil, fmt.Errorf("%q cannot be a host's name", name)
	case strings.Contains(string(compose), composeEOF):
		return nil, errors.New("compose.yaml holds the heredoc marker")
	}
	steps := []Step{
		{Text: "create " + InstallDir + ", owned by " + r.User, Root: !r.Root,
			Script: "mkdir -p " + InstallDir + "\nchown " + r.User + ": " + InstallDir + "\n"},
		{Text: "write compose.yaml and .env there (the API listens on this machine's loopback only)",
			Script: "cd " + InstallDir + "\nmkdir -p fleet state\n" +
				"cat > compose.yaml <<'" + composeEOF + "'\n" + string(compose) + composeEOF + "\n" +
				"umask 077\n" +
				"printf 'NEPFLEET_UID=%s\\nNEPFLEET_GID=%s\\nDOCKER_GID=%s\\nNEPFLEET_HOST=%s\\n' \"$(id -u)\" \"$(id -g)\" \"$(getent group docker | cut -d: -f3)\" " + name + " > .env\n"},
		{Text: "pull the image",
			Script: "cd " + InstallDir + "\ndocker compose pull -q\n"},
		{Text: "generate the fleet's age key on this machine (keep a copy elsewhere: without it, no secret can be read)",
			Script: "cd " + InstallDir + "\n" +
				"if [ ! -s age.key ]; then docker compose run --rm -T --entrypoint age-keygen nepfleet -o " + InstallDir + "/age.key 2>/dev/null; fi\n" +
				"chmod 600 age.key\n"},
		{Text: "create the fleet repository, encrypting to that key",
			Script: "cd " + InstallDir + "\n" +
				"if [ ! -f fleet/fleet.yaml ]; then docker compose run --rm -T nepfleet init . >/dev/null; fi\n" +
				"pub=\"$(docker compose run --rm -T --entrypoint age-keygen nepfleet -y " + InstallDir + "/age.key)\"\n" +
				"case \"$pub\" in age1*) ;; *) echo \"no public key: $pub\" >&2; exit 1;; esac\n" +
				"grep -q \"$pub\" fleet/fleet.yaml || sed -i \"s|^    age: \\[\\]\\$|    age: [\\\"$pub\\\"]|\" fleet/fleet.yaml\n" +
				"grep -q \"$pub\" fleet/fleet.yaml\n"},
		{Text: "start the API and wait until it's healthy",
			Script: "cd " + InstallDir + "\ndocker compose up -d --wait\n"},
	}
	if skipDir {
		return steps[1:], nil
	}
	if steps[0].Root && !r.Root && !r.Sudo && !r.SudoPassword {
		return steps, fmt.Errorf("creating %s needs root: log in as root or as a user with sudo, or create it yourself, owned by %s, and use --skip-dir", InstallDir, r.User)
	}
	return steps, nil
}

var hostNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9_-]{0,61}[a-z0-9])?$`)
