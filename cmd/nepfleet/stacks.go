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

	"github.com/Serraniel/neptide/pkg/registry"

	"github.com/Serraniel/nepfleet/internal/deploy"
	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/importer"
	"github.com/Serraniel/nepfleet/internal/lock"
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
	reg      *registry.Client
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
// resolverOnce builds the registry resolver with Docker's credentials.
func (e *env) resolverOnce() lock.Resolver {
	if e.reg == nil {
		c := registry.New()
		c.UserAgent = "nepfleet/" + version
		cfgPath := filepath.Join(os.Getenv("DOCKER_CONFIG"), "config.json")
		if os.Getenv("DOCKER_CONFIG") == "" {
			h, _ := os.UserHomeDir()
			cfgPath = filepath.Join(h, ".docker", "config.json")
		}
		if creds, err := registry.LoadDockerConfig(cfgPath); err == nil {
			c.Creds = creds.Lookup
		}
		e.reg = c
	}
	return lock.Registry{Client: e.reg}
}

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
	l, err := lock.Load(s.Dir)
	if err != nil {
		return nil, err
	}
	p, err := deploy.PlanStack(s.Metadata.Name, s.Spec.Mode, deploy.StackDir(e.stateDir, s.Metadata.Name), out, l.Images)
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
	noLock := fs.Bool("no-lock", false, "do not pin images to digests (lock.yaml)")
	updateLock := fs.Bool("update-lock", false, "re-resolve every image tag to its current digest")
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
		if p.plan.Empty() && !*updateLock {
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
		opts := deploy.Options{
			WaitTimeout: *wait, Commit: commit, Dirty: dirty, IngressNetwork: e.fleet.Spec.IngressNetwork,
			UpdateLock: *updateLock,
		}
		if !*noLock {
			if opts.Lock, err = lock.Load(s.Dir); err != nil {
				errs = append(errs, err)
				continue
			}
			opts.Resolver = e.resolverOnce()
		}
		var res deploy.Result
		res, err = deploy.Apply(ctx, deploy.Docker{}, p.plan, p.out, opts)
		for _, u := range res.Unpinned {
			fmt.Fprintf(stdout, "%s: ⚠ not pinned, runs by tag: %s\n", name, u)
		}
		if res.LockChanged {
			if serr := opts.Lock.Save(s.Dir); serr != nil {
				errs = append(errs, serr)
			} else {
				fmt.Fprintf(stdout, "%s: image digests pinned; commit %s\n", name, filepath.Join(s.Dir, lock.File))
			}
		}
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
	if len(args) >= 1 && args[0] == "check" {
		fs := flag.NewFlagSet("secret check", flag.ContinueOnError)
		fs.SetOutput(stderr)
		dir := fs.String("fleet", envOr("NEPFLEET_FLEET", "."), "fleet repository")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		bad, err := fleet.UnencryptedSecrets(*dir, secrets.IsEncrypted)
		if err != nil {
			return err
		}
		for _, b := range bad {
			fmt.Fprintf(stdout, "not encrypted: %s\n", b)
		}
		if len(bad) > 0 {
			return fmt.Errorf("%d secret file(s) are not SOPS-encrypted", len(bad))
		}
		fmt.Fprintln(stdout, "all secret files are encrypted")
		return nil
	}
	if len(args) < 1 || args[0] != "set" {
		return errors.New("usage: nepfleet secret set <stack> <KEY> (value from stdin) | nepfleet secret check")
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

func cmdImport(args []string, stdout, stderr io.Writer) error {
	if len(args) < 1 || args[0] != "compose" {
		return errors.New("usage: nepfleet import compose [project...] [--all] [--target host]")
	}
	var c common
	fs := flag.NewFlagSet("import compose", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c.register(fs)
	all := fs.Bool("all", false, "import every project found")
	target := fs.String("target", "", "spec.target for the imported stacks (default: --host)")
	encryptAll := fs.Bool("encrypt-all", false, "store every env value encrypted, not only secret-looking names")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *target == "" {
		*target = c.host
	}
	e, err := c.open()
	if err != nil {
		return err
	}
	ctx := context.Background()
	projects, err := importer.ListCompose(ctx, deploy.Docker{})
	if err != nil {
		return err
	}
	if fs.NArg() == 0 && !*all {
		fmt.Fprintln(stdout, "Compose projects on this host (import with `nepfleet import compose <name>` or --all):")
		for _, p := range projects {
			fmt.Fprintf(stdout, "  %-24s %-14s %s\n", p.Name, p.Status, strings.Join(p.ConfigFiles, ", "))
		}
		return nil
	}
	want := map[string]bool{}
	for _, n := range fs.Args() {
		want[n] = true
	}
	var errs []error
	for _, p := range projects {
		if !*all && !want[p.Name] {
			continue
		}
		delete(want, p.Name)
		res, err := importer.ImportCompose(ctx, deploy.Docker{}, e.fleet, e.store, p, importer.Options{Target: *target, EncryptAll: *encryptAll})
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.Name, err))
			continue
		}
		fmt.Fprintf(stdout, "imported %s → %s\n  values: %s\n  secrets (encrypted): %s\n  volumes (kept as they are): %s\n",
			p.Name, relTo(e.fleet.Dir, res.Dir), orNone(res.Values), orNone(res.Secrets), orNone(res.Volumes))
		if *target == c.host || *target == "local" {
			// The running containers are the applied state: record it, so the
			// first plan is empty and apply has nothing to change.
			s, err := e.fleet.LoadStack(p.Name)
			if err == nil {
				var prep *prepared
				if prep, err = e.prepare(s); err == nil {
					err = deploy.Adopt(prep.plan.Dir, prep.out)
				}
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: imported, but recording the running state failed: %w", p.Name, err))
				continue
			}
			fmt.Fprintf(stdout, "  adopted: the running containers are now the applied state (plan is empty)\n")
		}
		for _, w := range res.Warnings {
			fmt.Fprintf(stdout, "  ⚠ %s\n", w)
		}
	}
	for n := range want {
		errs = append(errs, fmt.Errorf("no compose project %q on this host", n))
	}
	if len(errs) == 0 {
		fmt.Fprintln(stdout, "\nNext: review and commit the stack. `nepfleet plan` shows nothing to do; manage these projects only through NepFleet from now on.")
	}
	return errors.Join(errs...)
}

func orNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ", ")
}
