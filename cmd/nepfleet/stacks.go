// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"cmp"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"go.yaml.in/yaml/v3"
	"golang.org/x/term"

	nengine "github.com/Serraniel/neptide/pkg/engine"
	"github.com/Serraniel/neptide/pkg/registry"
	"github.com/Serraniel/neptide/pkg/snapshot"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/deploy"
	"github.com/Serraniel/nepfleet/internal/doctor"
	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/importer"
	"github.com/Serraniel/nepfleet/internal/lock"
	"github.com/Serraniel/nepfleet/internal/notify"
	"github.com/Serraniel/nepfleet/internal/render"
	"github.com/Serraniel/nepfleet/internal/secrets"
	"github.com/Serraniel/nepfleet/internal/source"
	"github.com/Serraniel/nepfleet/internal/template"
	"github.com/Serraniel/neptide/pkg/kuma"
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
	// dataSafe: apply snapshots the volumes first (see DataSafety).
	dataSafe bool
	tpl      *template.Template // nil for a plain compose stack
	stack    *fleet.Stack
	out      *render.Output
	plan     *deploy.Plan
	secrets  map[string]string
	// pin is the template source commit this stack renders from; pinMoved
	// means lock.yaml does not record it yet (first apply, --update-lock).
	pin      *lock.TemplatePin
	pinMoved bool
	// lock is lock.yaml as this plan read it: apply pins exactly these
	// digests, also if a pull changes the file meanwhile.
	lock *lock.Lock
}

type env struct {
	common
	noSnapshot bool // apply --no-snapshot
	updateLock bool // plan/apply --update-lock: templates from the source's current commit
	fleet      *fleet.Fleet
	store      secrets.SOPS
	resolver   *source.Resolver
	reg        *registry.Client
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

// stacks returns the named stacks, or those planned on this host, ordered
// so that stacks come after the stacks they take values from.
// A cycle among them is an error; references that loop through stacks not
// selected are caught when the values are resolved.
func (e *env) stacks(names []string) ([]*fleet.Stack, error) {
	mine, err := e.selectStacks(names)
	if err != nil {
		return nil, err
	}
	return fleet.OrderByRefs(mine)
}

// selectStacks returns the named stacks, or those planned on this host.
func (e *env) selectStacks(names []string) ([]*fleet.Stack, error) {
	var mine []*fleet.Stack
	if len(names) > 0 {
		for _, n := range names {
			s, err := e.fleet.LoadStack(n)
			if err != nil {
				return nil, err
			}
			mine = append(mine, s)
		}
		return mine, nil
	}
	all, err := e.fleet.Stacks()
	if err != nil {
		return nil, err
	}
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
	l, err := lock.Load(s.Dir)
	if err != nil {
		return nil, err
	}
	// A template renders from the commit lock.yaml pins, so a stack's
	// revision means the same files later; --update-lock moves the pin.
	var tpl *template.Template
	var pin *lock.TemplatePin
	pinMoved := false
	if ref := s.Spec.Template; ref != nil {
		var res source.Resolved
		old := l.Template
		if old != nil && old.Source == ref.Source && !e.updateLock {
			tpl, res, err = e.resolver.TemplateAt(*ref, old.Commit)
			// stack.yaml asks for another template version than the pinned
			// commit has: an explicit move, shown in the plan.
			if errors.Is(err, source.ErrVersion) {
				tpl, res, err = e.resolver.Template(*ref)
			}
		} else {
			tpl, res, err = e.resolver.Template(*ref)
		}
		if err != nil {
			return nil, err
		}
		if res.Pinnable && res.Commit != "" {
			pin = &lock.TemplatePin{Source: ref.Source, Commit: res.Commit}
			pinMoved = old == nil || *old != *pin
		}
	}
	refs, refSecret, err := e.resolveRefs(s)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.Metadata.Name, err)
	}
	var noBackup []string
	if usesBuiltin(tpl, "NEPFLEET_NO_BACKUP_VOLUMES") {
		if noBackup, err = e.noBackupVolumes(s); err != nil {
			return nil, fmt.Errorf("%s: %w", s.Metadata.Name, err)
		}
	}
	storagePw, err := e.storagePasswords(s)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.Metadata.Name, err)
	}
	var host *fleet.Host
	if s.Spec.Access != nil && s.Spec.Access.Mode == "port" {
		if host, err = e.fleet.LoadHost(e.targetHost(s.Spec.Target)); err != nil {
			return nil, err
		}
		if err := e.checkAccessPort(s); err != nil {
			return nil, err
		}
	}
	out, err := render.Render(render.Input{Fleet: e.fleet, Stack: s, Template: tpl, Secrets: sec, Refs: refs, RefSecret: refSecret,
		NoBackupVolumes: noBackup, StoragePasswords: storagePw, Host: host, RenderGID: doctor.RenderGID("/")})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.Metadata.Name, err)
	}
	p, err := deploy.PlanStack(s.Metadata.Name, s.Spec.Mode, deploy.StackDir(e.stateDir, s.Metadata.Name), out, l.Images)
	if err != nil {
		return nil, err
	}
	// Exact per-service steps from compose; without docker they stay marked approximate.
	_ = p.RefineServices(context.Background(), deploy.Docker{}, out)
	if pin != nil {
		p.Template = pin.Source + "@" + pin.Commit[:12]
		if pinMoved && l.Template != nil {
			p.TemplateFrom = l.Template.Source + "@" + l.Template.Commit[:min(12, len(l.Template.Commit))]
		}
	}
	pr := &prepared{stack: s, tpl: tpl, out: out, plan: p, secrets: sec, pin: pin, pinMoved: pinMoved, lock: l}
	e.finishPlan(pr)
	return pr, nil
}

// storagePasswords reads the passwords of the network storage a stack's
// volumes use from fleet.secret.yaml (only when it uses any).
func (e *env) storagePasswords(s *fleet.Stack) (map[string]string, error) {
	var keys []string
	for _, m := range s.Spec.Volumes {
		if st, ok := e.fleet.Spec.Storage[m.Storage]; ok && st.PasswordSecret != "" {
			keys = append(keys, m.Storage)
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	sec, err := e.store.Load(filepath.Join(e.fleet.Dir, fleet.FleetSecretsFile))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, k := range keys {
		out[k] = sec[e.fleet.Spec.Storage[k].PasswordSecret]
	}
	return out, nil
}

// targetHost is the host a target names ("local" is this host).
func (e *env) targetHost(target string) string {
	if target == "local" {
		return e.host
	}
	return target
}

// targetNames are the targets that mean the same host as target: on the
// host itself, "local" and its name.
func (e *env) targetNames(target string) []string {
	if strings.EqualFold(target, "local") || strings.EqualFold(target, e.host) {
		return []string{"local", e.host}
	}
	return []string{target}
}

// checkAccessPort refuses a LAN port that another stack on the same host
// already uses, or one outside the host's range.
func (e *env) checkAccessPort(s *fleet.Stack) error {
	a := s.Spec.Access
	if a == nil || a.Mode != "port" {
		return nil
	}
	return e.portFree(s.Spec.Target, a.Port, s.Metadata.Name)
}

func (e *env) portFree(target string, port int, self string) error {
	used, err := e.fleet.AccessPorts(e.targetNames(target)...)
	if err != nil {
		return err
	}
	for _, n := range used[port] {
		if n != self {
			return fmt.Errorf("port %d is already used by %s on %s", port, n, target)
		}
	}
	return nil
}

// usesBuiltin reports whether a template's compose file or files use ${name}.
func usesBuiltin(tpl *template.Template, name string) bool {
	if tpl == nil {
		return false
	}
	paths := []string{tpl.Spec.Compose}
	for _, f := range tpl.Spec.Files {
		paths = append(paths, f.Src)
	}
	for _, p := range paths {
		if raw, err := os.ReadFile(filepath.Join(tpl.Dir, p)); err == nil && strings.Contains(string(raw), "${"+name+"}") {
			return true
		}
	}
	return false
}

// noBackupVolumes lists the Docker names (<stack>_<volume>) of volumes that
// templates of stacks on the same host as s mark backup: false. Plain
// stacks' volumes are not known, so they stay in the backup.
func (e *env) noBackupVolumes(s *fleet.Stack) ([]string, error) {
	all, err := e.fleet.Stacks()
	if err != nil {
		return nil, err
	}
	host := func(t string) string {
		if t == "local" {
			return e.host
		}
		return t
	}
	var out []string
	for _, o := range all {
		if o.Spec.Template == nil || host(o.Spec.Target) != host(s.Spec.Target) {
			continue
		}
		tpl, err := e.pinnedTemplate(o)
		if err != nil {
			continue // that stack's own plan reports it
		}
		for _, v := range tpl.Spec.Volumes {
			if !v.Backup {
				out = append(out, o.Metadata.Name+"_"+v.Name)
			}
		}
	}
	return out, nil
}

// finishPlan adds what apply does around the compose steps. Called again
// whenever the plan is recomputed (--update-lock).
func (e *env) finishPlan(pr *prepared) {
	p := pr.plan
	// Data-safe rollback: an apply that recreates running services copies
	// the stack's volumes first (compose stacks; swarm rolls back by itself).
	pr.dataSafe = !e.noSnapshot && pr.stack.Spec.Snapshot != "off" && !p.First && p.Mode != "swarm" && p.Recreates()
	if pr.dataSafe {
		p.UseSnapshots()
	}
	e.addMaintenanceStep(pr)
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
	if p.TemplateFrom != "" {
		fmt.Fprintf(w, "  template %s → %s\n", p.TemplateFrom, p.Template)
	}
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
	if len(p.Steps) > 0 {
		if p.Approximate {
			fmt.Fprintln(w, "  steps (approximate: docker was not reachable to compute them exactly):")
		} else {
			fmt.Fprintln(w, "  steps:")
		}
		for i, s := range p.Steps {
			mark := ""
			if s.Destructive {
				mark = " ⚠"
			}
			fmt.Fprintf(w, "    %d. %s%s\n", i+1, s.Text, mark)
		}
	}
}

func cmdPlan(args []string, stdout, stderr io.Writer) error {
	var c common
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c.register(fs)
	asJSON := fs.Bool("json", false, "print the plan as JSON")
	updateLock := fs.Bool("update-lock", false, "include what apply --update-lock would change: newer image digests and the template source's current commit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := c.open()
	if err != nil {
		return err
	}
	e.updateLock = *updateLock
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
		if *updateLock {
			if _, err := e.previewUpdate(context.Background(), p, stderr); err != nil {
				errs = append(errs, err)
				continue
			}
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
	noLock := fs.Bool("no-lock", false, "do not write lock.yaml (image digests, template commit)")
	noSnapshot := fs.Bool("no-snapshot", false, "do not copy volumes before recreating services (a failed apply then rolls back files and images, not data)")
	updateLock := fs.Bool("update-lock", false, "re-resolve every image tag to its current digest and move the template pin to the source's current commit (shown in the plan before confirming)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *noLock && *updateLock {
		return errors.New("--update-lock and --no-lock contradict each other")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runApply(ctx, c, fs.Args(), applyOptions{yes: *yes, confirm: confirm, wait: *wait, noLock: *noLock, noSnapshot: *noSnapshot, updateLock: *updateLock}, stdout, stderr)
}

// applyOptions are the flags of nepfleet apply, for the CLI and the API.
type applyOptions struct {
	yes                            bool
	confirm                        []string
	wait                           time.Duration
	noLock, noSnapshot, updateLock bool
	// check, if set, sees each stack's plan before anything happens; an error
	// refuses it. What it saw is exactly what is applied.
	check func(*deploy.Plan) error
}

// lockStack keeps one apply per stack on this host, whether from the CLI or
// the API. The kernel drops the lock when the process ends.
func lockStack(stateDir, name string) (func(), error) {
	dir := filepath.Join(stateDir, "locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, name+".lock"), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // the state directory, a checked stack name
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil { //nolint:gosec // a file descriptor fits an int
		_ = f.Close()
		return nil, fmt.Errorf("%s: another apply of this stack is running", name)
	}
	return func() { _ = f.Close() }, nil
}

func runApply(ctx context.Context, c common, names []string, o applyOptions, stdout, stderr io.Writer) error {
	yes, wait, noLock, noSnapshot, updateLock, confirm := &o.yes, &o.wait, &o.noLock, &o.noSnapshot, &o.updateLock, o.confirm
	e, err := c.open()
	if err != nil {
		return err
	}
	e.noSnapshot = *noSnapshot
	e.updateLock = *updateLock
	stacks, err := e.stacks(names)
	if err != nil {
		return err
	}
	commit, dirty := gitRevision(e.fleet.Dir)

	notifier, nerr := e.notifier()
	if nerr != nil {
		fmt.Fprintf(stdout, "⚠ notifications: %v\n", nerr) // never blocks an apply
	}
	var errs []error
	unlock := func() {}
	defer func() { unlock() }()
	for _, s := range stacks {
		name := s.Metadata.Name
		unlock()
		unlock = func() {}
		release, err := lockStack(e.stateDir, name)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		unlock = release
		p, err := e.prepare(s)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if o.check != nil {
			if err := o.check(p.plan); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		var updated *lock.Lock
		if *updateLock {
			// Resolve before confirming, so the plan shows the image changes.
			if updated, err = e.previewUpdate(ctx, p, stderr); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		printPlan(stdout, p.plan)
		if p.plan.Empty() {
			// Same files from another commit (or a stack from before pins):
			// only the pin moves.
			if p.pinMoved && !*noLock {
				if err := savePin(s.Dir, p.pin); err != nil {
					errs = append(errs, err)
				} else {
					fmt.Fprintf(stdout, "%s: template pinned to %s; commit %s\n", name, p.plan.Template, filepath.Join(s.Dir, lock.File))
				}
			}
			continue
		}
		pins := updated
		if pins == nil && !*noLock {
			pins = p.lock
		}
		if err := archGate(ctx, stdout, name, p, pins); err != nil {
			errs = append(errs, err)
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
		}
		if p.dataSafe {
			if opts.DataSafety, err = e.dataSafety(ctx, p, stdout); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				continue
			}
		}
		if !*noLock {
			if updated != nil {
				opts.Lock = updated
			} else {
				opts.Lock = p.lock
			}
			opts.Resolver = e.resolverOnce()
		}
		// Monitors of a stack whose containers change go into maintenance in
		// every configured Uptime Kuma; problems only warn.
		// Scheduled to expire after the health gate plus a margin, so it ends
		// even if this process dies; closed as soon as the apply is done.
		windows := e.openMaintenance(ctx, stdout, p, *wait+10*time.Minute)
		var res deploy.Result
		res, err = func() (deploy.Result, error) {
			defer func() {
				for _, window := range windows {
					for _, w := range window.Close(context.WithoutCancel(ctx), nil) {
						fmt.Fprintf(stdout, "%s: ⚠ %s\n", name, w)
					}
				}
			}()
			return deploy.Apply(ctx, deploy.Docker{}, p.plan, p.out, opts)
		}()
		for _, u := range res.Unpinned {
			fmt.Fprintf(stdout, "%s: ⚠ not pinned, runs by tag: %s\n", name, u)
		}
		if err == nil && updated != nil {
			// The preview already moved the pins; persist them now that they ran.
			if before, lerr := lock.Load(s.Dir); lerr == nil && !maps.Equal(before.Images, updated.Images) {
				res.LockChanged = true
			}
		}
		if res.LockChanged {
			if serr := opts.Lock.Save(s.Dir); serr != nil {
				errs = append(errs, serr)
			} else {
				fmt.Fprintf(stdout, "%s: image digests pinned; commit %s\n", name, filepath.Join(s.Dir, lock.File))
			}
		}
		if err == nil && p.pinMoved && !*noLock {
			if perr := savePin(s.Dir, p.pin); perr != nil {
				errs = append(errs, perr)
			} else {
				fmt.Fprintf(stdout, "%s: template pinned to %s; commit %s\n", name, p.plan.Template, filepath.Join(s.Dir, lock.File))
			}
		}
		switch {
		case errors.Is(err, deploy.ErrRolledBack):
			fmt.Fprintf(stdout, "%s: ✗ %v\n", name, err)
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			e.emit(notifier, stdout, notify.Event{Name: "apply.rolled_back", Severity: notify.Warning, Stack: name,
				Title: name + " rolled back", Message: fmt.Sprintf("The new version of %s was not healthy; the previous one runs again.\n%v", name, err)})
		case err != nil:
			fmt.Fprintf(stdout, "%s: ✗ %v\n", name, err)
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			e.emit(notifier, stdout, notify.Event{Name: "apply.failed", Severity: notify.Error, Stack: name,
				Title: "applying " + name + " failed", Message: err.Error()})
		default:
			fmt.Fprintf(stdout, "%s: ✓ applied and healthy\n", name)
			if p.plan.First {
				e.kumaFirstRun(ctx, stdout, p)
			}
			e.emit(notifier, stdout, notify.Event{Name: "apply.succeeded", Severity: notify.Info, Stack: name,
				Title: name + " applied", Message: fmt.Sprintf("%s is updated and healthy.%s", name, changeSummary(p.plan))})
		}
	}
	return errors.Join(errs...)
}

// pinnedTemplate loads a stack's template as the stack renders it: at the
// commit its lock.yaml pins (when that is still valid), else current.
func (e *env) pinnedTemplate(s *fleet.Stack) (*template.Template, error) {
	ref := s.Spec.Template
	if ref == nil {
		return nil, nil
	}
	if l, err := lock.Load(s.Dir); err == nil && l.Template != nil && l.Template.Source == ref.Source {
		if tpl, _, err := e.resolver.TemplateAt(*ref, l.Template.Commit); err == nil {
			return tpl, nil
		}
	}
	tpl, _, err := e.resolver.Template(*ref)
	return tpl, err
}

// changeSummary lists what an apply changed, for notifications.
func changeSummary(p *deploy.Plan) string {
	var parts []string
	for _, c := range p.Changes {
		parts = append(parts, string(c.Kind)+" "+c.Path)
	}
	if len(parts) == 0 {
		return ""
	}
	if len(parts) > 8 {
		parts = append(parts[:8], fmt.Sprintf("and %d more", len(parts)-8))
	}
	return "\nChanged: " + strings.Join(parts, ", ")
}

// savePin records the template commit in lock.yaml, keeping the image pins.
func savePin(dir string, pin *lock.TemplatePin) error {
	l, err := lock.Load(dir)
	if err != nil {
		return err
	}
	l.Template = pin
	return l.Save(dir)
}

// previewUpdate resolves current digests for a prepared stack and re-plans
// with them, so the plan lists every image that would move.
func (e *env) previewUpdate(ctx context.Context, p *prepared, stderr io.Writer) (*lock.Lock, error) {
	current, err := lock.Load(p.stack.Dir)
	if err != nil {
		return nil, err
	}
	updated, skipped, err := deploy.ResolveUpdate(ctx, deploy.Docker{}, p.stack.Metadata.Name, p.out, current, e.resolverOnce())
	if err != nil {
		return nil, fmt.Errorf("%s: resolving image updates: %w", p.stack.Metadata.Name, err)
	}
	for _, s := range skipped {
		fmt.Fprintf(stderr, "%s: ⚠ cannot resolve %s, its pin stays as is\n", p.stack.Metadata.Name, s)
	}
	tplNow, tplFrom := p.plan.Template, p.plan.TemplateFrom
	p.plan, err = deploy.PlanStack(p.stack.Metadata.Name, p.stack.Spec.Mode, deploy.StackDir(e.stateDir, p.stack.Metadata.Name), p.out, updated.Images)
	if err == nil {
		p.plan.Template, p.plan.TemplateFrom = tplNow, tplFrom
		_ = p.plan.RefineServices(ctx, deploy.Docker{}, p.out)
		e.finishPlan(p)
	}
	return updated, err
}

// addMaintenanceStep lists the Uptime Kuma window apply will open, before
// the first step that touches containers.
func (e *env) addMaintenanceStep(p *prepared) {
	if len(e.fleet.Spec.UptimeKuma) == 0 || !touchesRuntime(p.plan) {
		return
	}
	refs := kuma.MonitorRefs(p.out.Files[render.ComposeFile], render.EnvValues(p.out.Files[render.EnvFile]))
	if len(refs) == 0 {
		return
	}
	var windows, incidents []string
	w, inc := e.kumaSplit(p)
	for _, k := range w {
		windows = append(windows, k.Name)
	}
	for _, k := range inc {
		incidents = append(incidents, k.Name)
	}
	var texts []string
	if len(windows) > 0 {
		texts = append(texts, fmt.Sprintf("put %d monitor(s) into maintenance in Uptime Kuma %s until the stack is healthy", len(refs), strings.Join(windows, ", ")))
	}
	if len(incidents) > 0 {
		texts = append(texts, fmt.Sprintf("rolling update: post a warning incident on the status pages of %d monitor(s) in Uptime Kuma %s until it is done", len(refs), strings.Join(incidents, ", ")))
	}
	if len(texts) == 0 {
		return
	}
	step := deploy.Step{Kind: "maintenance", Text: strings.Join(texts, "; ")}
	for i, s := range p.plan.Steps {
		if s.Kind == "image" || s.Kind == "service" || s.Kind == "health" {
			p.plan.Steps = append(p.plan.Steps[:i], append([]deploy.Step{step}, p.plan.Steps[i:]...)...)
			return
		}
	}
}

func touchesRuntime(p *deploy.Plan) bool {
	for _, c := range p.Changes {
		if c.Path == render.ComposeFile || c.Path == render.EnvFile || strings.HasPrefix(c.Path, "image ") {
			return true
		}
	}
	return false
}

// kumaCloser is an open maintenance window or incident.
type kumaCloser interface {
	Close(ctx context.Context, hc *http.Client) []string
}

// openMaintenance opens Uptime Kuma maintenance windows when the plan
// changes what runs (compose file, env file or image pins). A swarm rolling
// update, where every monitored service keeps running replicas, gets what
// each Kuma's rollingUpdates says instead (an incident by default).
func (e *env) openMaintenance(ctx context.Context, stdout io.Writer, p *prepared, maxDuration time.Duration) []kumaCloser {
	if len(e.fleet.Spec.UptimeKuma) == 0 {
		return nil
	}
	runtime := touchesRuntime(p.plan)
	compose := p.out.Files[render.ComposeFile]
	refs := kuma.MonitorRefs(compose, render.EnvValues(p.out.Files[render.EnvFile]))
	if !runtime || len(refs) == 0 {
		return nil
	}
	fleetSecrets, err := e.store.Load(filepath.Join(e.fleet.Dir, fleet.FleetSecretsFile))
	if err != nil {
		fmt.Fprintf(stdout, "%s: ⚠ Uptime Kuma: cannot read %s: %v\n", p.stack.Metadata.Name, fleet.FleetSecretsFile, err)
		return nil
	}
	target := func(k fleet.KumaInstance) kuma.Target {
		return kuma.Target{Name: k.Name, URL: k.URL, Username: k.Username, Password: fleetSecrets[k.PasswordSecret]}
	}
	var windows, incidents []kuma.Target
	w, inc := e.kumaSplit(p)
	for _, k := range w {
		windows = append(windows, target(k))
	}
	for _, k := range inc {
		incidents = append(incidents, target(k))
	}
	what := "update"
	if p.plan.First {
		what = "first deployment"
	}
	title := fmt.Sprintf("NepFleet: %s of %s", what, p.stack.Metadata.Name)
	desc := fmt.Sprintf("Started by nepfleet apply on %s. Ends automatically when the stack is healthy or rolled back.", e.host)
	var open []kumaCloser
	report := func(warnings []string) {
		for _, msg := range warnings {
			fmt.Fprintf(stdout, "%s: %s\n", p.stack.Metadata.Name, msg)
		}
	}
	if len(windows) > 0 {
		w, warnings := kuma.Open(ctx, windows, refs, title, desc, maxDuration, nil)
		report(warnings)
		open = append(open, w)
	}
	if len(incidents) > 0 {
		inc, warnings := kuma.OpenIncident(ctx, incidents, refs, "Rolling update of "+p.stack.Metadata.Name,
			"Replicas are replaced one at a time: the service stays up, responses may be slower meanwhile. Resolved automatically when the update is done.", nil)
		report(warnings)
		open = append(open, inc)
	}
	return open
}

// kumaSplit sorts the fleet's Kuma instances into those that get a
// maintenance window and those that get an incident (a swarm rolling update,
// rollingUpdates: incident); rollingUpdates: none gets neither.
func (e *env) kumaSplit(p *prepared) (windows, incidents []fleet.KumaInstance) {
	rolling := p.plan.Mode == "swarm" && !p.plan.First && rollingUpdate(p.out.Files[render.ComposeFile])
	for _, k := range e.fleet.Spec.UptimeKuma {
		switch {
		case !rolling || k.RollingUpdates == "maintenance":
			windows = append(windows, k)
		case k.RollingUpdates == "incident":
			incidents = append(incidents, k)
		}
	}
	return windows, incidents
}

// rollingUpdate: every service with Uptime Kuma monitors runs at least two
// replicas (or globally), so a swarm update replaces them one by one
// instead of stopping the service.
func rollingUpdate(compose []byte) bool {
	var doc struct {
		Services map[string]struct {
			Labels any `yaml:"labels"`
			Deploy struct {
				Mode         string `yaml:"mode"`
				Replicas     any    `yaml:"replicas"`
				Labels       any    `yaml:"labels"`
				UpdateConfig struct {
					Parallelism any    `yaml:"parallelism"`
					Order       string `yaml:"order"`
				} `yaml:"update_config"`
			} `yaml:"deploy"`
		} `yaml:"services"`
	}
	if yaml.Unmarshal(compose, &doc) != nil {
		return false
	}
	monitored := 0
	for _, svc := range doc.Services {
		if len(kuma.RefsFromLabels(labelStrings(svc.Labels), nil)) == 0 && len(kuma.RefsFromLabels(labelStrings(svc.Deploy.Labels), nil)) == 0 {
			continue
		}
		monitored++
		// Parallelism 0 replaces all replicas at once; with stop-first,
		// parallelism >= replicas stops them all too: downtime either way.
		par, parSet := intValue(svc.Deploy.UpdateConfig.Parallelism)
		startFirst := svc.Deploy.UpdateConfig.Order == "start-first"
		if svc.Deploy.Mode == "global" {
			if parSet && par == 0 && !startFirst {
				return false
			}
			continue
		}
		replicas, ok := intValue(svc.Deploy.Replicas)
		if !ok || replicas < 2 { // unset is 1; a ${VAR} cannot be judged
			return false
		}
		if parSet && !startFirst && (par == 0 || par >= replicas) {
			return false
		}
	}
	return monitored > 0
}

// intValue reads a YAML number (not an interpolated string).
func intValue(v any) (int, bool) {
	n, ok := v.(int)
	return n, ok
}

// labelStrings reads both compose label forms.
func labelStrings(labels any) map[string]string {
	out := map[string]string{}
	switch l := labels.(type) {
	case map[string]any:
		for k, v := range l {
			out[k] = fmt.Sprint(v)
		}
	case []any:
		for _, item := range l {
			if s, ok := item.(string); ok {
				k, v, _ := strings.Cut(s, "=")
				out[k] = v
			}
		}
	}
	return out
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
	global := fs.Bool("global", false, "store in fleet.secret.yaml (fleet-wide, e.g. Uptime Kuma passwords): nepfleet secret set --global KEY")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if (*global && fs.NArg() != 1) || (!*global && fs.NArg() != 2) {
		return errors.New("usage: nepfleet secret set <stack> <KEY> | nepfleet secret set --global <KEY>")
	}
	e, err := c.open()
	if err != nil {
		return err
	}
	key := fs.Arg(fs.NArg() - 1)
	path := filepath.Join(e.fleet.Dir, fleet.FleetSecretsFile)
	if !*global {
		s, err := e.fleet.LoadStack(fs.Arg(0))
		if err != nil {
			return err
		}
		path = filepath.Join(s.Dir, fleet.SecretsFile)
	}
	value, err := readSecret(stdout, key)
	if err != nil {
		return err
	}
	values, err := e.store.Load(path)
	if err != nil {
		return err
	}
	values[key] = value
	if err := e.store.Save(path, values); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "saved %s in %s (encrypted)\n", key, path)
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
	entries, err := e.history(s, *n)
	if err != nil {
		return err
	}
	for _, h := range entries {
		mark := "  "
		if h.Applied {
			mark = "▶ " // currently applied
		}
		fmt.Fprintf(stdout, "%s%s %s %s  %s\n", mark, h.Commit[:min(7, len(h.Commit))], h.Date[:min(10, len(h.Date))], h.Author, h.Subject)
	}
	return nil
}

// history lists the last n commits that touched a stack, newest first.
func (e *env) history(s *fleet.Stack, n int) ([]api.HistoryEntry, error) {
	applied, _ := deploy.Applied(deploy.StackDir(e.stateDir, s.Metadata.Name))
	out, err := gitOut(e.fleet.Dir, "log", fmt.Sprintf("-n%d", n), "--format=%H%x1f%aI%x1f%an%x1f%s", "--", relTo(e.fleet.Dir, s.Dir))
	if err != nil {
		return nil, err
	}
	var entries []api.HistoryEntry
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "\x1f")
		if len(f) != 4 {
			continue
		}
		entries = append(entries, api.HistoryEntry{Commit: f[0], Date: f[1], Author: f[2], Subject: f[3],
			Applied: applied != nil && applied.Commit != "" && applied.Commit == f[0]})
	}
	return entries, nil
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
	if len(args) >= 1 && args[0] == "portainer" {
		return cmdImportPortainer(args[1:], stdout, stderr)
	}
	if len(args) < 1 || args[0] != "compose" {
		return errors.New("usage: nepfleet import compose [project...] [--all] [--target host] | nepfleet import portainer --url URL [stack...] [--all]")
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
		if err := e.importOne(ctx, stdout, p, importer.Options{Target: *target, EncryptAll: *encryptAll}, c.host, nil); err != nil {
			errs = append(errs, err)
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

// importOne imports one project and, when it runs on this host, records the
// running containers as the applied state, so the first plan is empty.
func (e *env) importOne(ctx context.Context, stdout io.Writer, p importer.Project, o importer.Options, host string, extra []string) error {
	res, err := importer.ImportCompose(ctx, deploy.Docker{}, e.fleet, e.store, p, o)
	if err != nil {
		return fmt.Errorf("%s: %w", p.Name, err)
	}
	fmt.Fprintf(stdout, "imported %s → %s\n  values: %s\n  secrets (encrypted): %s\n  volumes (kept as they are): %s\n",
		p.Name, relTo(e.fleet.Dir, res.Dir), orNone(res.Values), orNone(res.Secrets), orNone(res.Volumes))
	if (o.Target == host || o.Target == "local") && o.Mode != "swarm" && !o.NoAdopt {
		s, err := e.fleet.LoadStack(p.Name)
		if err == nil {
			var prep *prepared
			if prep, err = e.prepare(s); err == nil {
				err = deploy.Adopt(prep.plan.Dir, prep.out)
			}
		}
		if err != nil {
			return fmt.Errorf("%s: imported, but recording the running state failed: %w", p.Name, err)
		}
		fmt.Fprintf(stdout, "  adopted: the running containers are now the applied state (plan is empty)\n")
		if fleet.EffectiveLogging(e.fleet, nil) != nil {
			fmt.Fprintf(stdout, "  note: the running containers do not have NepFleet's log rotation yet; the next apply of %s recreates all its services\n", p.Name)
		}
	}
	for _, w := range append(extra, res.Warnings...) {
		fmt.Fprintf(stdout, "  ⚠ %s\n", w)
	}
	return nil
}

func cmdImportPortainer(args []string, stdout, stderr io.Writer) error {
	var c common
	fs := flag.NewFlagSet("import portainer", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c.register(fs)
	u := fs.String("url", envOr("NEPFLEET_PORTAINER_URL", ""), "Portainer URL, e.g. https://portainer.example.org:9443")
	all := fs.Bool("all", false, "import every compose and swarm stack")
	target := fs.String("target", "", "spec.target for the imported stacks (default: --host)")
	encryptAll := fs.Bool("encrypt-all", false, "store every env value encrypted, not only secret-looking names")
	endpoint := fs.Int("endpoint", 0, "Portainer environment (endpoint) ID of this host; required when stacks of several environments are listed")
	caFile := fs.String("ca-file", "", "CA certificate for Portainer's TLS certificate (its default one is self-signed)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := importer.ValidPortainerURL(*u); err != nil {
		return err
	}
	if *target == "" {
		*target = c.host
	}
	// Never a flag: it would end up in the shell history.
	token := os.Getenv("PORTAINER_TOKEN")
	if token == "" {
		var err error
		if token, err = readSecret(stderr, "Portainer access token"); err != nil {
			return err
		}
	}
	var pool *x509.CertPool
	if *caFile != "" {
		pem, err := os.ReadFile(*caFile)
		if err != nil {
			return err
		}
		pool = x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return fmt.Errorf("%s: no PEM certificate", *caFile)
		}
	}
	pt := importer.Portainer{URL: *u, Token: token, HTTP: importer.NewPortainerHTTP(pool)}
	ctx := context.Background()
	stacks, err := pt.Stacks(ctx)
	if err != nil {
		return err
	}
	if fs.NArg() == 0 && !*all {
		fmt.Fprintln(stdout, "Portainer stacks (import with `nepfleet import portainer <name>` or --all):")
		for _, s := range stacks {
			state := "running"
			if s.Status != 1 {
				state = "stopped"
			}
			fmt.Fprintf(stdout, "  %-24s %-11s %-8s environment %d\n", s.Name, s.Kind(), state, s.EndpointID)
		}
		return nil
	}
	e, err := c.open()
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, n := range fs.Args() {
		want[n] = true
	}
	// Portainer lists the stacks of every environment; each runs on its own
	// Docker host, so import only this host's.
	envs := map[int]bool{}
	for _, s := range stacks {
		if *all || want[s.Name] {
			envs[s.EndpointID] = true
		}
	}
	if *endpoint == 0 && len(envs) > 1 {
		return errors.New("the stacks belong to several Portainer environments: pass --endpoint with this host's environment ID (see the listing)")
	}
	var errs []error
	for _, s := range stacks {
		if (!*all && !want[s.Name]) || (*endpoint != 0 && s.EndpointID != *endpoint) {
			continue
		}
		delete(want, s.Name)
		if err := s.Supported(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Name, err))
			continue
		}
		if err := e.importPortainerStack(ctx, stdout, pt, s, importer.Options{Target: *target, EncryptAll: *encryptAll}, c.host); err != nil {
			errs = append(errs, err)
		}
	}
	for n := range want {
		errs = append(errs, fmt.Errorf("no Portainer stack %q", n))
	}
	if len(errs) == 0 {
		fmt.Fprintln(stdout, "\nNext: review and commit the stacks; `nepfleet plan` shows nothing to do. Do not touch them in Portainer any more (deleting a stack there removes its containers); once every stack is imported, Portainer can be stopped.")
	}
	return errors.Join(errs...)
}

func (e *env) importPortainerStack(ctx context.Context, stdout io.Writer, pt importer.Portainer, s importer.PortainerStack, o importer.Options, host string) error {
	content, err := pt.File(ctx, s.ID)
	if err != nil {
		return fmt.Errorf("%s: %w", s.Name, err)
	}
	dir, err := os.MkdirTemp("", "nepfleet-portainer-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	p, so, warnings, err := importer.StageProject(dir, s, content)
	if err != nil {
		return fmt.Errorf("%s: %w", s.Name, err)
	}
	so.Target, so.EncryptAll = o.Target, o.EncryptAll
	// Adopt only what runs here: otherwise the plan would claim the stack is
	// deployed on this host.
	if so.Mode != "swarm" {
		local, err := importer.ListCompose(ctx, deploy.Docker{})
		if err != nil {
			return err
		}
		// Same name and Portainer's working directory: a local project that
		// only shares the name was not deployed by this stack.
		if !slices.ContainsFunc(local, func(lp importer.Project) bool {
			return lp.Name == s.Name && (s.ProjectPath == "" || lp.WorkingDir == s.ProjectPath)
		}) {
			so.NoAdopt = true
			warnings = append(warnings, "no compose project of this name runs on this host, so it was not adopted: check --endpoint and the target before applying")
		}
	}
	return e.importOne(ctx, stdout, p, so, host, warnings)
}

func orNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ", ")
}

func cmdDoctor(args []string, stdout, stderr io.Writer) error {
	var c common
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := c.open()
	if err != nil {
		return err
	}
	stacks, err := e.selectStacks(nil)
	if err != nil {
		return err
	}
	var cycleErr error
	if ordered, err := fleet.OrderByRefs(stacks); err != nil {
		cycleErr = err // reported with the other stack problems
	} else {
		stacks = ordered
	}
	in := doctor.Input{Recipients: e.fleet.Spec.Secrets.Age, AgeKeyFile: ageKeyFile(), LogRotation: fleet.EffectiveLogging(e.fleet, nil).Rotates()}
	for _, k := range []string{"SOPS_AGE_KEY", "SOPS_AGE_KEY_CMD"} {
		if os.Getenv(k) != "" {
			in.KeyFromEnv = k
		}
	}
	seen := map[string]bool{}
	var renderErrs []error
	if cycleErr != nil {
		renderErrs = append(renderErrs, cycleErr)
	}
	for _, s := range stacks {
		p, err := e.prepare(s)
		if err != nil {
			renderErrs = append(renderErrs, err)
			continue
		}
		imgs, publishes := composeImages(p.out)
		in.Ingress = in.Ingress || publishes
		for _, img := range imgs {
			if !seen[img] {
				seen[img] = true
				in.Images = append(in.Images, img)
			}
		}
	}
	checks := doctor.Run(context.Background(), doctor.Host(), in)
	// Hardware and shared folders that the stacks' inputs use must be there:
	// a stick that is unplugged, a GPU this host lacks, an unmounted share.
	hw := doctor.HostHardware("/")
	used := map[string][]string{}
	for _, s := range stacks {
		if s.Spec.Template == nil {
			continue // plain stacks: values pass through
		}
		tpl, err := e.pinnedTemplate(s)
		if err != nil {
			continue // reported with the stack's render error
		}
		for _, inp := range tpl.Spec.Inputs {
			raw := s.Spec.Values[inp.Name]
			if raw.IsRef() {
				continue // another stack's value: checked where it is defined
			}
			v := cmp.Or(raw.Literal, inp.Default)
			switch {
			case v == "" || v == "none":
			case inp.Type == "device":
				if _, err := os.Stat(v); err != nil {
					checks = append(checks, doctor.Check{Name: "Device", Status: doctor.Fail, Detail: fmt.Sprintf("%s: %s=%s is missing", s.Metadata.Name, inp.Name, v), Fix: "plug it in, or choose another device (nepfleet new lists them)"})
				} else {
					checks = append(checks, doctor.Check{Name: "Device", Status: doctor.OK, Detail: fmt.Sprintf("%s: %s present", s.Metadata.Name, v), Fix: ""})
				}
			case inp.Type == "gpu":
				if slices.Contains(hw.GPUs, v) {
					checks = append(checks, doctor.Check{Name: "GPU", Status: doctor.OK, Detail: fmt.Sprintf("%s: %s GPU present", s.Metadata.Name, v), Fix: ""})
				} else {
					checks = append(checks, doctor.Check{Name: "GPU", Status: doctor.Fail, Detail: fmt.Sprintf("%s: %s=%s, but this host has %s", s.Metadata.Name, inp.Name, v, cmp.Or(strings.Join(hw.GPUs, ", "), "no GPU NepFleet can use")), Fix: "choose the GPU this host has, or none"})
				}
			case inp.Type == "path" && strings.HasPrefix(v, "@"):
				name := strings.TrimPrefix(v, "@")
				if _, defined := e.fleet.Spec.Paths[name]; defined { // undefined: a render error already
					used[name] = append(used[name], s.Metadata.Name)
				}
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(used)) {
		p, _ := e.fleet.ResolvePath("@" + name)
		if _, serr := os.Stat(p); serr != nil {
			checks = append(checks, doctor.Check{Name: "Shared folder", Status: doctor.Fail, Detail: fmt.Sprintf("@%s (%s, used by %s) is missing", name, p, strings.Join(used[name], ", ")), Fix: "create it, or mount the share it lives on"})
		} else {
			checks = append(checks, doctor.Check{Name: "Shared folder", Status: doctor.OK, Detail: fmt.Sprintf("@%s %s (used by %s)", name, p, strings.Join(used[name], ", ")), Fix: ""})
		}
	}
	checks = append(checks, backupChecks(e, c.stateDir, in.KeyFromEnv)...)
	for _, err := range renderErrs {
		checks = append(checks, doctor.Check{Name: "Stacks", Status: doctor.Fail, Detail: err.Error(), Fix: "fix the stack, then run doctor again"})
	}
	fmt.Fprintf(stdout, "Host %s, %d stack(s) planned here\n\n", c.host, len(stacks))
	for _, ch := range checks {
		fmt.Fprintf(stdout, " %s %-20s %s\n", ch.Status, ch.Name, ch.Detail)
		if ch.Fix != "" && ch.Status != doctor.OK {
			fmt.Fprintf(stdout, "   %-20s → %s\n", "", ch.Fix)
		}
	}
	if doctor.Failed(checks) {
		return errors.New("some checks failed")
	}
	return nil
}

// hostPort returns the published host port of a compose ports entry, in
// short ("[ip:]host:container[/proto]") or long ({published: …}) syntax.
func hostPort(port any, env map[string]string) string {
	if m, ok := port.(map[string]any); ok {
		return os.Expand(fmt.Sprint(m["published"]), func(k string) string { return env[k] })
	}
	s := os.Expand(fmt.Sprint(port), func(k string) string { return env[k] })
	s, _, _ = strings.Cut(s, "/")
	parts := strings.Split(s, ":")
	if len(parts) < 2 {
		return "" // container port only: Docker picks a random host port
	}
	return parts[len(parts)-2]
}

func ageKeyFile() string {
	if f := os.Getenv("SOPS_AGE_KEY_FILE"); f != "" {
		return f
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "sops", "age", "keys.txt")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "sops", "age", "keys.txt")
}

// composeImages lists the image references of a rendered stack (env values
// substituted) and whether it publishes port 80 or 443.
func composeImages(out *render.Output) (images []string, publishesWeb bool) {
	env := render.EnvValues(out.Files[render.EnvFile])
	var doc struct {
		Services map[string]struct {
			Image string `yaml:"image"`
			Ports []any  `yaml:"ports"`
		} `yaml:"services"`
	}
	if yaml.Unmarshal(out.Files[render.ComposeFile], &doc) != nil {
		return nil, false
	}
	for _, s := range doc.Services {
		if s.Image != "" {
			images = append(images, os.Expand(s.Image, func(k string) string { return env[k] }))
		}
		for _, port := range s.Ports {
			if h := hostPort(port, env); h == "80" || h == "443" {
				publishesWeb = true
			}
		}
	}
	sort.Strings(images)
	return images, publishesWeb
}

// archGate refuses a stack with an image that cannot run on this host,
// before anything is pulled or stopped. Images that only run under binfmt
// emulation, or that could not be checked, are reported and allowed.
func archGate(ctx context.Context, stdout io.Writer, name string, p *prepared, pins *lock.Lock) error {
	imgs, _ := composeImages(p.out)
	// Check what apply deploys: the pinned digest, not what the tag (local
	// or in the registry) points to today.
	refs := make([]string, len(imgs))
	shown := map[string]string{}
	for i, img := range imgs {
		refs[i] = img
		if pins != nil && pins.Images[img] != "" {
			refs[i] = img + "@" + pins.Images[img] // as deploy pins it
		}
		shown[refs[i]] = img
	}
	var bad []string
	for _, a := range doctor.Architectures(ctx, doctor.Host(), refs) {
		a.Image = shown[a.Image]
		switch a.Status {
		case doctor.Fail:
			bad = append(bad, a.Image+": "+a.Detail)
		case doctor.Warn:
			fmt.Fprintf(stdout, "%s: %s: %s\n", name, a.Image, a.Detail)
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("%s: refusing to deploy images this host cannot run:\n  %s\n  use other images, or install emulation with: nepfleet new binfmt", name, strings.Join(bad, "\n  "))
	}
	return nil
}

// dataSafety connects the snapshotter for one stack's apply.
func (e *env) dataSafety(ctx context.Context, p *prepared, stdout io.Writer) (*deploy.DataSafety, error) {
	eng, err := nengine.New("")
	if err != nil {
		return nil, fmt.Errorf("docker API for snapshots: %w", err)
	}
	root, err := eng.DockerRoot(ctx)
	if err != nil || root == "" {
		root = "/var/lib/docker"
	}
	d := &deploy.DataSafety{
		Snapshots: &snapshot.Snapshotter{Engine: eng, Keep: 3, DockerRoot: root},
		Engine:    eng,
		Exclude:   p.stack.Spec.SnapshotExclude,
		Log:       func(msg string) { fmt.Fprintf(stdout, "%s: %s\n", p.stack.Metadata.Name, msg) },
	}
	if p.tpl != nil {
		for _, v := range p.tpl.Spec.Volumes {
			if v.Snapshot != nil && !*v.Snapshot {
				d.ExcludeVolumes = append(d.ExcludeVolumes, v.Name)
			}
		}
	}
	return d, nil
}
