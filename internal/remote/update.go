// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/mod/semver"
)

// imageTagRe finds a release version in NepFleet's image reference.
var imageTagRe = regexp.MustCompile(`:v?([0-9]+\.[0-9]+\.[0-9]+)(@|$)`)

// RunningVersion is the NepFleet release the machine's container runs, from
// its image tag, or "".
func (r *Report) RunningVersion() string {
	if m := imageTagRe.FindStringSubmatch(r.NepFleetImage); m != nil {
		return m[1]
	}
	return ""
}

// UpdatePlan moves a NepFleet that host install set up (the container in
// InstallDir) to version, compose being that version's compose.yaml: the old
// compose.yaml is kept as compose.yaml.previous, .env stays, and a version
// that doesn't come up healthy is replaced by the previous one again. It
// changes nothing itself.
func UpdatePlan(r *Report, compose []byte, version string) ([]Step, error) {
	target := strings.TrimPrefix(version, "v")
	current := r.RunningVersion()
	switch {
	case r.NepFleetImage == "":
		return nil, errors.New("no NepFleet container runs here to update")
	case !releaseRe.MatchString(version):
		return nil, fmt.Errorf("this app is a development build (%s): it updates machines to releases only", version)
	case current == "":
		return nil, fmt.Errorf("can't tell which version runs here from its image (%s)", r.NepFleetImage)
	case semver.Compare("v"+current, "v"+target) >= 0:
		return nil, fmt.Errorf("NepFleet %s runs here already, as new as this app's %s", current, target)
	case strings.Contains(string(compose), composeEOF):
		return nil, errors.New("compose.yaml holds the heredoc marker")
	}
	return []Step{
		{Text: "keep the current compose.yaml as compose.yaml.previous (" + current + ")",
			Script: "cd " + InstallDir + "\ntest -f compose.yaml || { echo 'no compose.yaml in " + InstallDir + ": this NepFleet was not set up by host install' >&2; exit 1; }\n" +
				"cp -p compose.yaml compose.yaml.previous\n"},
		{Text: "write compose.yaml for NepFleet " + target + " (.env, the fleet repository and the data stay)",
			Script: "cd " + InstallDir + "\ncat > compose.yaml <<'" + composeEOF + "'\n" + string(compose) + composeEOF + "\n"},
		{Text: pullText(compose),
			Script: "cd " + InstallDir + "\ndocker compose pull -q\n"},
		{Text: "restart it and wait until it's healthy; if it isn't, start " + current + " again",
			Script: "cd " + InstallDir + "\n" +
				"if ! docker compose up -d --wait; then\n" +
				"  echo 'NepFleet " + target + " did not become healthy: starting " + current + " again' >&2\n" +
				"  cp -p compose.yaml.previous compose.yaml\n" +
				"  docker compose up -d --wait\n" +
				"  exit 1\n" +
				"fi\n"},
	}, nil
}

// StartPlan starts a NepFleet that host install set up and that has stopped
// (the doctor's "its container is stopped"), and waits until it's healthy.
func StartPlan(r *Report) ([]Step, error) {
	if r.NepFleetRunning || r.NepFleetImage != "" {
		return nil, errors.New("NepFleet runs here already")
	}
	return []Step{
		{Text: "start NepFleet in " + InstallDir + " and wait until it's healthy",
			Script: "cd " + InstallDir + "\ntest -f compose.yaml || { echo 'no compose.yaml in " + InstallDir + ": NepFleet was not set up here by host install' >&2; exit 1; }\n" +
				"docker compose up -d --wait\n"},
	}, nil
}
