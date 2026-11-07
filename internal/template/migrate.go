// SPDX-License-Identifier: AGPL-3.0-or-later

package template

import (
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// ErrDowngrade means the target version is older than the installed one.
var ErrDowngrade = errors.New("template version goes down")

// semver is a parsed version; pre orders below the release, as in Semantic
// Versioning.
type semver struct {
	major, minor, patch int
	pre                 string
}

func parseSemver(s string) (semver, error) {
	if !semverRe.MatchString(s) {
		return semver{}, fmt.Errorf("%q is not a semantic version", s)
	}
	core, pre, _ := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	nums := [3]int{}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return semver{}, fmt.Errorf("%q is not a semantic version", s)
		}
		nums[i] = n
	}
	return semver{nums[0], nums[1], nums[2], pre}, nil
}

func comparePre(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range min(len(as), len(bs)) {
		an, aerr := strconv.Atoi(as[i])
		bn, berr := strconv.Atoi(bs[i])
		var c int
		switch {
		case aerr == nil && berr == nil:
			c = cmp.Compare(an, bn)
		case aerr == nil:
			c = -1
		case berr == nil:
			c = 1
		default:
			c = cmp.Compare(as[i], bs[i])
		}
		if c != 0 {
			return c
		}
	}
	return cmp.Compare(len(as), len(bs))
}

func (v semver) compare(o semver) int {
	return cmp.Or(cmp.Compare(v.major, o.major), cmp.Compare(v.minor, o.minor), cmp.Compare(v.patch, o.patch), comparePre(v.pre, o.pre))
}

// breaking says whether going from v to o may break things by version alone:
// another major, or another minor below 1.0.
func (v semver) breaking(o semver) bool {
	if v.major != o.major {
		return true
	}
	return v.major == 0 && v.minor != o.minor
}

var comparatorRe = regexp.MustCompile(`^(>=|<=|>|<|=)?(.+)$`)

// inRange checks a version against space-separated comparators (">=1.0.0 <2.0.0").
func inRange(v semver, r string) (bool, error) {
	fields := strings.Fields(r)
	if len(fields) == 0 {
		return false, fmt.Errorf("empty version range")
	}
	// Every comparator is read, so a bad one shows when the template is checked.
	all := true
	for _, f := range fields {
		m := comparatorRe.FindStringSubmatch(f)
		if m == nil {
			return false, fmt.Errorf("bad version range %q", r)
		}
		o, err := parseSemver(m[2])
		if err != nil {
			return false, fmt.Errorf("bad version range %q: %w", r, err)
		}
		c := v.compare(o)
		ok := false
		switch m[1] {
		case ">=":
			ok = c >= 0
		case "<=":
			ok = c <= 0
		case ">":
			ok = c > 0
		case "<":
			ok = c < 0
		default:
			ok = c == 0
		}
		all = all && ok
	}
	return all, nil
}

// UpgradePath is the migrations that take a stack from one template version to
// another, in order. It is empty when the upgrade needs none.
//
// Every migration on the way has to run: one that applies can't be skipped, as
// it is there because the data needs it. An upgrade to another major version
// with no migrations leading to it is refused, as nothing says it is safe. Going
// down is ErrDowngrade: it has no path, and only the caller knows if it's wanted.
func UpgradePath(ms []Migration, from, to string) ([]Migration, error) {
	start, err := parseSemver(from)
	if err != nil {
		return nil, err
	}
	target, err := parseSemver(to)
	if err != nil {
		return nil, err
	}
	switch c := start.compare(target); {
	case c == 0:
		return nil, nil
	case c > 0:
		return nil, fmt.Errorf("%w: %s is older than the installed %s", ErrDowngrade, to, from)
	}
	seen := map[string]bool{}
	var walk func(v semver, name string) ([]Migration, bool, error)
	walk = func(v semver, name string) ([]Migration, bool, error) {
		if seen[name] {
			return nil, false, nil
		}
		seen[name] = true
		type edge struct {
			m  Migration
			to semver
		}
		var edges []edge
		for _, m := range ms {
			mTo, err := parseSemver(m.To)
			if err != nil {
				return nil, false, err
			}
			in, err := inRange(v, m.From)
			if err != nil {
				return nil, false, err
			}
			if in && v.compare(mTo) < 0 && mTo.compare(target) <= 0 {
				edges = append(edges, edge{m, mTo})
			}
		}
		if len(edges) == 0 {
			// A migration still ahead that this version can't take would be
			// skipped, and the new version would start on data it expects migrated.
			for _, m := range ms {
				mTo, err := parseSemver(m.To)
				if err != nil {
					return nil, false, err
				}
				if v.compare(mTo) < 0 && mTo.compare(target) <= 0 {
					return nil, false, nil
				}
			}
			return nil, !v.breaking(target), nil
		}
		// The furthest jump first: the fewest steps.
		slices.SortStableFunc(edges, func(a, b edge) int { return b.to.compare(a.to) })
		for _, e := range edges {
			rest, ok, err := walk(e.to, e.m.To)
			if err != nil {
				return nil, false, err
			}
			if ok {
				return append([]Migration{e.m}, rest...), true, nil
			}
		}
		return nil, false, nil
	}
	path, ok, err := walk(start, from)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("no migration path from %s to %s", from, to)
	}
	return path, nil
}

// checkMigration reports what is wrong with one migration's steps and versions.
func checkMigration(m Migration) []string {
	var bad []string
	if _, err := inRange(semver{}, m.From); err != nil {
		bad = append(bad, "from: "+err.Error())
	}
	deploys := 0
	for i, st := range m.Steps {
		switch {
		case st.Deploy && (len(st.Run) > 0 || st.Service != ""):
			bad = append(bad, fmt.Sprintf("steps[%d]: deploy and run are separate steps", i))
		case st.Deploy:
			deploys++
		case len(st.Run) == 0 || st.Service == "":
			bad = append(bad, fmt.Sprintf("steps[%d]: needs service and run, or deploy", i))
		}
		if st.Wait != "" && (st.Wait != "healthy" || !st.Deploy) {
			bad = append(bad, fmt.Sprintf("steps[%d]: wait is healthy, on a deploy step", i))
		}
	}
	if deploys > 1 {
		bad = append(bad, "only one step can deploy")
	}
	return bad
}
