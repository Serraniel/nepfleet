// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/deploy"
	"github.com/Serraniel/nepfleet/internal/fleet"
)

var slugRe = regexp.MustCompile(`[^a-z0-9-]+`)

// apiAuthor is who the API's commits name: the token that asked. git's own
// identity stays the committer, and signing follows git's own config.
func apiAuthor(by string) string {
	name := strings.NewReplacer("<", "", ">", "", "\n", " ", "\r", " ").Replace(by)
	if strings.TrimSpace(name) == "" {
		name = "token"
	}
	slug := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if slug == "" {
		// A name with no Latin letters, such as 花子: two people mustn't share an address.
		sum := sha256.Sum256([]byte(name))
		slug = "u-" + hex.EncodeToString(sum[:6])
	}
	return fmt.Sprintf("%s <%s@tokens.nepfleet.invalid>", name, slug)
}

// commitTimeout bounds a commit over the API, which holds the API's lock:
// a signing key waiting for its passphrase would block every change.
var commitTimeout = 60 * time.Second

// commitWaitDelay is how long a stopped commit gets to exit before it is
// killed and its output abandoned.
var commitWaitDelay = 10 * time.Second

// commitAs commits paths in the fleet repository as the given author. Nothing
// staged elsewhere goes along. A commit that outlasts commitTimeout is
// stopped with SIGTERM, on which git removes its lock files (a kill would
// leave index.lock and block later commits); only one that ignores it is
// killed.
func commitAs(dir, author, msg string, paths ...string) error {
	return committer{author: author}.commit(dir, msg, paths...)
}

// committer is who a commit names. An empty author is git's own identity.
type committer struct {
	author string
	// env is added to git's environment: the committer, when git has none.
	env []string
}

// The identity of the server's own commits, for a CLI where git has none.
// compose.yaml gives the server's container the same committer.
const (
	serverCommitterName  = "NepFleet"
	serverCommitterEmail = "nepfleet@localhost"
)

// cliCommitter is whoever runs the CLI, as git's config names them. Without
// one (inside the container, say), the commit names the server's identity
// instead of failing.
func cliCommitter(dir string) committer {
	_, aerr := gitOut(dir, "var", "GIT_AUTHOR_IDENT")
	_, cerr := gitOut(dir, "var", "GIT_COMMITTER_IDENT")
	if aerr == nil && cerr == nil {
		return committer{}
	}
	return committer{
		author: apiAuthor(reconcileAuthor),
		env:    []string{"GIT_COMMITTER_NAME=" + serverCommitterName, "GIT_COMMITTER_EMAIL=" + serverCommitterEmail},
	}
}

// as is " as <author>" for messages, empty for git's own identity.
func (w committer) as() string {
	if w.author == "" {
		return ""
	}
	return " as " + w.author
}

// commit commits paths in the repository at dir; see commitAs.
func (w committer) commit(dir, msg string, paths ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), commitTimeout)
	defer cancel()
	args := []string{"commit", "--quiet"}
	if w.author != "" {
		args = append(args, "--author", w.author)
	}
	args = append(append(args, "-m", msg, "--"), paths...)
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // fixed arguments
	cmd.Dir = dir
	if len(w.env) > 0 {
		cmd.Env = append(os.Environ(), w.env...)
	}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = commitWaitDelay
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return fmt.Errorf("git commit didn't finish within %s and was stopped; is git waiting for a signing key's passphrase? %s", commitTimeout, strings.TrimSpace(string(out)))
	}
	if err != nil {
		return fmt.Errorf("git commit: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// restoreStack puts a stack's directory back to target and commits that, once
// check agrees with the plan it leads to. On any failure the directory is as
// it was: the caller found it clean, so nothing else can be lost.
func restoreStack(c common, name, target, author string, check func(*deploy.Plan) error, log io.Writer) (err error) {
	e, err := c.open()
	if err != nil {
		return err
	}
	s, err := e.fleet.LoadStack(name)
	if err != nil {
		return err
	}
	rel := relTo(e.fleet.Dir, s.Dir)
	if out, _ := gitOut(e.fleet.Dir, "status", "--porcelain", "--", rel); out != "" {
		return fmt.Errorf("%s has uncommitted changes; commit or discard them first", rel)
	}
	before, err := gitOut(e.fleet.Dir, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	defer func() {
		if err == nil {
			return
		}
		// Back to HEAD: the index, the files, and what the old version added.
		for _, args := range [][]string{{"reset", "-q", "HEAD", "--", rel}, {"checkout", "-q", "HEAD", "--", rel}, {"clean", "-fdq", "--", rel}} {
			if _, uerr := gitOut(e.fleet.Dir, args...); uerr != nil {
				err = errors.Join(err, uerr)
			}
		}
		if after, _ := gitOut(e.fleet.Dir, "rev-parse", "HEAD"); after != before {
			err = errors.Join(err, errors.New("the repository moved during the rollback"))
		}
	}()
	short, err := gitOut(e.fleet.Dir, "rev-parse", "--short", target+"^{commit}")
	if err != nil {
		return err
	}
	short = strings.TrimSpace(short)
	if _, err := gitOut(e.fleet.Dir, "rm", "-r", "-q", "--", rel); err != nil {
		return err
	}
	if _, err := gitOut(e.fleet.Dir, "checkout", target, "--", rel); err != nil {
		return err
	}
	// Plan what's restored, before anything is recorded.
	e2, err := c.open()
	if err != nil {
		return err
	}
	s2, err := e2.fleet.LoadStack(name)
	if err != nil {
		return fmt.Errorf("the version at %s can't be loaded: %w", short, err)
	}
	p, err := e2.prepare(s2)
	if err != nil {
		return fmt.Errorf("the version at %s can't be planned: %w", short, err)
	}
	if err := check(p.plan); err != nil {
		return err
	}
	if err := commitAs(e.fleet.Dir, author, fmt.Sprintf("revert(%s): roll back to %s", name, short), rel); err != nil {
		return err
	}
	fmt.Fprintf(log, "%s restored to %s and committed as %s\n", rel, short, author)
	return nil
}

// startRollback checks the request at once and returns the rollback to run as
// a job, holding the API lock as apply does.
func startRollback(ctx context.Context, c common, stack, commit string, confirm bool, by string) (func(io.Writer) error, error) {
	if !apiMu.TryLock() {
		return nil, api.ErrBusy
	}
	handedOver := false
	defer func() {
		if !handedOver {
			apiMu.Unlock()
		}
	}()
	// Only this stack's own versions: not any commit that happens to exist.
	if _, err := withStack(c, stack, func(e *env, s *fleet.Stack) (any, error) {
		h, err := e.history(s, 200)
		if err != nil {
			return nil, err
		}
		for _, entry := range h {
			if entry.Commit == commit {
				return nil, nil
			}
		}
		return nil, api.ErrNotInHistory
	}); err != nil {
		return nil, err
	}
	handedOver = true
	return func(log io.Writer) error {
		defer apiMu.Unlock()
		fmt.Fprintf(log, "roll back %s to %s, requested by %s over the API\n", stack, commit, by)
		check := func(p *deploy.Plan) error {
			if len(p.Destructive) > 0 && !confirm {
				return fmt.Errorf("%w: %s", api.ErrNeedsConfirm, strings.Join(p.Destructive, ", "))
			}
			return nil
		}
		if err := restoreStack(c, stack, commit, apiAuthor(by), check, log); err != nil {
			return err
		}
		o := applyOptions{yes: true, wait: applyWait, check: check}
		if confirm {
			o.confirm = []string{stack}
		}
		err := runApply(ctx, c, []string{stack}, o, log, log)
		commitAPIApply(c, stack, by, log)
		return err
	}, nil
}

// commitAPIApply records what an apply wrote into the repository (image digests,
// generated secrets), so an API change is always a commit. A problem only warns:
// the stack is applied, and the files are there to commit by hand.
func commitAPIApply(c common, stack, by string, log io.Writer) {
	w := committer{author: apiAuthor(by)}
	recordApply(c, stack, func(string) committer { return w }, log)
}

// recordApply commits a stack's lock.yaml and secrets when an apply changed
// them, and nothing else, as who names for the fleet repository. Outside a
// git repository there is nothing to do: the apply already said what to commit.
func recordApply(c common, stack string, who func(dir string) committer, log io.Writer) {
	e, err := c.open()
	if err != nil {
		fmt.Fprintf(log, "⚠ not committed: %v\n", err)
		return
	}
	s, err := e.fleet.LoadStack(stack)
	if err != nil {
		fmt.Fprintf(log, "⚠ not committed: %v\n", err)
		return
	}
	rel := relTo(e.fleet.Dir, s.Dir)
	var paths []string
	for _, f := range []string{"lock.yaml", fleet.SecretsFile} {
		p := rel + "/" + f
		if out, _ := gitOut(e.fleet.Dir, "status", "--porcelain", "--", p); strings.TrimSpace(out) != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return
	}
	if _, err := gitOut(e.fleet.Dir, append([]string{"add", "--"}, paths...)...); err != nil {
		fmt.Fprintf(log, "⚠ not committed: %v\n", err)
		return
	}
	w := who(e.fleet.Dir)
	if err := w.commit(e.fleet.Dir, fmt.Sprintf("chore(%s): record the apply", stack), paths...); err != nil {
		// Unstaged again: the files stay as they are, to commit by hand.
		_, _ = gitOut(e.fleet.Dir, append([]string{"reset", "-q", "--"}, paths...)...)
		fmt.Fprintf(log, "⚠ not committed: %v\n", err)
		return
	}
	fmt.Fprintf(log, "committed %s%s\n", strings.Join(paths, ", "), w.as())
}

// inGitRepo reports whether dir is inside a git working tree.
func inGitRepo(dir string) bool {
	out, err := gitOut(dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

// stackDirDirty reports whether git has uncommitted changes under rel.
func stackDirDirty(dir, rel string) bool {
	st, _ := gitOut(dir, "status", "--porcelain", "--", rel)
	return strings.TrimSpace(st) != ""
}

// commitNewStack commits a stack directory just written, and only it. On a
// failure nothing stays staged; the files are the caller's to keep or remove.
func commitNewStack(dir, rel, name, tplName string, w committer) error {
	if _, err := gitOut(dir, "add", "--", rel); err != nil {
		return err
	}
	if err := w.commit(dir, "feat("+name+"): new stack from "+tplName, rel); err != nil {
		_, _ = gitOut(dir, "reset", "-q", "--", rel)
		return err
	}
	return nil
}
