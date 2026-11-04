// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/Serraniel/nepfleet/internal/deploy"
	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/render"
	"github.com/Serraniel/nepfleet/internal/secrets"
	"github.com/Serraniel/nepfleet/internal/source"
	"github.com/Serraniel/nepfleet/internal/template"
)

// common holds flags every stack command shares.
type common struct {
	fleetDir, stateDir, host string
	offline                  bool
}

func (c *common) register(fs *flag.FlagSet) {
	fs.StringVar(&c.fleetDir, "fleet", envOr("NEPFLEET_FLEET", "."), "fleet repository")
	fs.StringVar(&c.stateDir, "state", envOr("NEPFLEET_STATE", defaultState()), "state directory (stack working directories)")
	fs.StringVar(&c.host, "host", envOr("NEPFLEET_HOST", hostname()), "this host's name; stacks targeting it or \"local\" are handled")
	fs.BoolVar(&c.offline, "offline", false, "do not fetch template sources")
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func defaultState() string {
	if os.Geteuid() == 0 {
		return "/var/lib/nepfleet"
	}
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "nepfleet")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".local", "state", "nepfleet")
}

// prepared is one stack rendered and planned.
type prepared struct {
	stack   *fleet.Stack
	out     *render.Output
	plan    *deploy.Plan
	secrets map[string]string
}

type env struct {
	common
	fleet    *fleet.Fleet
	store    secrets.SOPS
	resolver *source.Resolver
}

func (c common) open() (*env, error) {
	f, err := fleet.Load(c.fleetDir)
	if err != nil {
		return nil, err
	}
	return &env{
		common:   c,
		fleet:    f,
		store:    secrets.SOPS{Recipients: f.Spec.Secrets.Age},
		resolver: &source.Resolver{Fleet: f, Offline: c.offline},
	}, nil
}

// stacks returns the named stacks, or all stacks for this host.
func (e *env) stacks(names []string) ([]*fleet.Stack, error) {
	if len(names) > 0 {
		var out []*fleet.Stack
		for _, n := range names {
			s, err := e.fleet.LoadStack(n)
			if err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		return out, nil
	}
	all, err := e.fleet.Stacks()
	if err != nil {
		return nil, err
	}
	var mine []*fleet.Stack
	for _, s := range all {
		if s.Spec.Target == e.host || s.Spec.Target == "local" {
			mine = append(mine, s)
		}
	}
	return mine, nil
}

func (e *env) prepare(s *fleet.Stack) (*prepared, error) {
	sec, err := e.store.Load(filepath.Join(s.Dir, fleet.SecretsFile))
	if err != nil {
		return nil, err
	}
	var tpl *template.Template
	if s.Spec.Template != nil {
		if tpl, _, err = e.resolver.Template(*s.Spec.Template); err != nil {
			return nil, err
		}
	}
	out, err := render.Render(render.Input{Fleet: e.fleet, Stack: s, Template: tpl, Secrets: sec})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.Metadata.Name, err)
	}
	p, err := deploy.PlanStack(s.Metadata.Name, s.Spec.Mode, deploy.StackDir(e.stateDir, s.Metadata.Name), out)
	if err != nil {
		return nil, err
	}
	return &prepared{stack: s, out: out, plan: p, secrets: sec}, nil
}

func printPlan(w io.Writer, p *deploy.Plan) {
	if p.Empty() {
		fmt.Fprintf(w, "%s: up to date\n", p.Stack)
		return
	}
	state := "update"
	if p.First {
		state = "new"
	}
	fmt.Fprintf(w, "\n%s (%s, %s)\n", p.Stack, state, p.Mode)
	for _, c := range p.Changes {
		fmt.Fprintf(w, "  %-8s %s\n", c.Kind, c.Path)
		for _, line := range strings.Split(strings.TrimSuffix(c.Diff, "\n"), "\n") {
			if line != "" {
				fmt.Fprintf(w, "           %s\n", line)
			}
		}
	}
	for _, g := range p.Generated {
		fmt.Fprintf(w, "  generate %s (stored encrypted in %s)\n", g, fleet.SecretsFile)
	}
	for _, d := range p.Destructive {
		fmt.Fprintf(w, "  ⚠ DESTRUCTIVE: %s\n", d)
	}
}

func cmdPlan(args []string, stdout, stderr io.Writer) error {
	var c common
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c.register(fs)
	asJSON := fs.Bool("json", false, "print the plan as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := c.open()
	if err != nil {
		return err
	}
	stacks, err := e.stacks(fs.Args())
	if err != nil {
		return err
	}
	var plans []*deploy.Plan
	var errs []error
	for _, s := range stacks {
		p, err := e.prepare(s)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		plans = append(plans, p.plan)
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(plans); err != nil {
			return err
		}
	} else {
		for _, p := range plans {
			printPlan(stdout, p)
		}
	}
	return errors.Join(errs...)
}

func cmdApply(args []string, stdout, stderr io.Writer) error {
	var c common
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c.register(fs)
	yes := fs.Bool("yes", false, "apply stacks with apply: manual without asking")
	var confirm multiFlag
	fs.Var(&confirm, "confirm", "allow destructive changes for this stack (repeatable)")
	wait := fs.Duration("wait", 3*time.Minute, "health gate timeout per stack")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := c.open()
	if err != nil {
		return err
	}
	stacks, err := e.stacks(fs.Args())
	if err != nil {
		return err
	}
	commit, dirty := gitRevision(e.fleet.Dir)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var errs []error
	for _, s := range stacks {
		name := s.Metadata.Name
		p, err := e.prepare(s)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		printPlan(stdout, p.plan)
		if p.plan.Empty() {
			continue
		}
		if len(p.plan.Destructive) > 0 && !slices.Contains(confirm, name) {
			if !ask(stdout, fmt.Sprintf("%s has destructive changes. Type the stack name to continue: ", name), name) {
				errs = append(errs, fmt.Errorf("%s: destructive changes not confirmed (use --confirm %s)", name, name))
				continue
			}
		} else if s.Spec.Apply == "manual" && !*yes && !ask(stdout, fmt.Sprintf("Apply %s? [y/N] ", name), "y") {
			fmt.Fprintf(stdout, "%s: skipped\n", name)
			continue
		}
		if len(p.out.Generated) > 0 {
			for k, v := range p.out.Generated {
				p.secrets[k] = v
			}
			if err := e.store.Save(filepath.Join(s.Dir, fleet.SecretsFile), p.secrets); err != nil {
				errs = append(errs, fmt.Errorf("%s: saving generated secrets: %w", name, err))
				continue
			}
			fmt.Fprintf(stdout, "%s: generated secrets saved; commit %s\n", name, filepath.Join(s.Dir, fleet.SecretsFile))
		}
		err = deploy.Apply(ctx, deploy.Docker{}, p.plan, p.out, deploy.Options{
			WaitTimeout: *wait, Commit: commit, Dirty: dirty, IngressNetwork: e.fleet.Spec.IngressNetwork,
		})
		switch {
		case errors.Is(err, deploy.ErrRolledBack):
			fmt.Fprintf(stdout, "%s: ✗ %v\n", name, err)
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		case err != nil:
			fmt.Fprintf(stdout, "%s: ✗ %v\n", name, err)
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		default:
			fmt.Fprintf(stdout, "%s: ✓ applied and healthy\n", name)
		}
	}
	return errors.Join(errs...)
}

func cmdRender(args []string, stdout, stderr io.Writer) error {
	var c common
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: nepfleet render <stack>")
	}
	e, err := c.open()
	if err != nil {
		return err
	}
	s, err := e.fleet.LoadStack(fs.Arg(0))
	if err != nil {
		return err
	}
	p, err := e.prepare(s)
	if err != nil {
		return err
	}
	var paths []string
	for path := range p.out.Files {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	for _, path := range paths {
		fmt.Fprintf(stdout, "# ---- %s\n%s", path, p.out.Masked(path))
	}
	return nil
}

func cmdSecret(args []string, stdout, stderr io.Writer) error {
	if len(args) < 1 || args[0] != "set" {
		return errors.New("usage: nepfleet secret set <stack> <KEY>   (value is read from stdin)")
	}
	var c common
	fs := flag.NewFlagSet("secret set", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c.register(fs)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return errors.New("usage: nepfleet secret set <stack> <KEY>")
	}
	e, err := c.open()
	if err != nil {
		return err
	}
	s, err := e.fleet.LoadStack(fs.Arg(0))
	if err != nil {
		return err
	}
	value, err := readSecret(stdout, fs.Arg(1))
	if err != nil {
		return err
	}
	path := filepath.Join(s.Dir, fleet.SecretsFile)
	values, err := e.store.Load(path)
	if err != nil {
		return err
	}
	values[fs.Arg(1)] = value
	if err := e.store.Save(path, values); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "saved %s in %s (encrypted)\n", fs.Arg(1), path)
	return nil
}

func readSecret(w io.Writer, key string) (string, error) {
	fd := int(os.Stdin.Fd()) //nolint:gosec // file descriptors fit in int
	if term.IsTerminal(fd) {
		fmt.Fprintf(w, "%s: ", key)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(w)
		return string(b), err
	}
	b, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	return strings.TrimRight(string(b), "\r\n"), err
}

func cmdHistory(args []string, stdout, stderr io.Writer) error {
	var c common
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c.register(fs)
	n := fs.Int("n", 20, "number of entries")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: nepfleet history <stack>")
	}
	e, err := c.open()
	if err != nil {
		return err
	}
	s, err := e.fleet.LoadStack(fs.Arg(0))
	if err != nil {
		return err
	}
	applied, _ := deploy.Applied(deploy.StackDir(e.stateDir, s.Metadata.Name))
	out, err := gitOut(e.fleet.Dir, "log", fmt.Sprintf("-n%d", *n), "--date=short", "--format=%h %ad %an  %s", "--", relTo(e.fleet.Dir, s.Dir))
	if err != nil {
		return err
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		mark := "  "
		if applied != nil && applied.Commit != "" && strings.HasPrefix(applied.Commit, strings.Fields(line + " x")[0]) {
			mark = "▶ " // currently applied
		}
		fmt.Fprintln(stdout, mark+line)
	}
	return nil
}

func cmdRollback(args []string, stdout, stderr io.Writer) error {
	var c common
	fs := flag.NewFlagSet("rollback", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c.register(fs)
	noCommit := fs.Bool("no-commit", false, "restore the files but do not commit")
	noApply := fs.Bool("no-apply", false, "do not apply after restoring")
	yes := fs.Bool("yes", false, "apply without asking")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 || fs.NArg() > 2 {
		return errors.New("usage: nepfleet rollback <stack> [commit]")
	}
	e, err := c.open()
	if err != nil {
		return err
	}
	s, err := e.fleet.LoadStack(fs.Arg(0))
	if err != nil {
		return err
	}
	rel := relTo(e.fleet.Dir, s.Dir)
	if out, _ := gitOut(e.fleet.Dir, "status", "--porcelain", "--", rel); out != "" {
		return fmt.Errorf("%s has uncommitted changes; commit or discard them first", rel)
	}
	target := fs.Arg(1)
	if target == "" {
		log, err := gitOut(e.fleet.Dir, "log", "-n2", "--format=%H", "--", rel)
		if err != nil {
			return err
		}
		commits := strings.Fields(log)
		if len(commits) < 2 {
			return fmt.Errorf("%s has no earlier version to roll back to", s.Metadata.Name)
		}
		target = commits[1]
	}
	short, err := gitOut(e.fleet.Dir, "rev-parse", "--short", target+"^{commit}")
	if err != nil {
		return err
	}
	short = strings.TrimSpace(short)
	// Replace the directory with its content at target (files added since are removed).
	if _, err := gitOut(e.fleet.Dir, "rm", "-r", "-q", "--", rel); err != nil {
		return err
	}
	if _, err := gitOut(e.fleet.Dir, "checkout", target, "--", rel); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s restored to %s\n", rel, short)
	if !*noCommit {
		msg := fmt.Sprintf("revert(%s): roll back to %s", s.Metadata.Name, short)
		cmd := exec.Command("git", "commit", "-m", msg, "--", rel)
		cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = e.fleet.Dir, os.Stdin, stdout, stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("git commit: %w", err)
		}
	}
	if *noApply {
		return nil
	}
	applyArgs := []string{"--fleet", c.fleetDir, "--state", c.stateDir, "--host", c.host, s.Metadata.Name}
	if *yes {
		applyArgs = append([]string{"--yes"}, applyArgs...)
	}
	return cmdApply(applyArgs, stdout, stderr)
}

func ask(w io.Writer, prompt, want string) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) { //nolint:gosec // file descriptors fit in int
		return false
	}
	fmt.Fprint(w, prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(line) == want
}

func gitRevision(dir string) (commit string, dirty bool) {
	c, err := gitOut(dir, "rev-parse", "HEAD")
	if err != nil {
		return "", true
	}
	st, _ := gitOut(dir, "status", "--porcelain")
	return strings.TrimSpace(c), strings.TrimSpace(st) != ""
}

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func relTo(base, path string) string {
	if r, err := filepath.Rel(base, path); err == nil {
		return r
	}
	return path
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }
