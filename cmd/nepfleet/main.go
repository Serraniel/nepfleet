// SPDX-License-Identifier: AGPL-3.0-or-later

// Command nepfleet manages Linux hosts and Docker deployments from a git
// repository. Run `nepfleet help` for usage.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/deploy"
	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/template"
)

// Set at build time with -ldflags "-X main.version=... -X main.commit=...".
var (
	version = "dev"
	commit  = "unknown"
)

const usage = `nepfleet - manage Linux hosts and Docker deployments from git

Usage:
  nepfleet init [dir]                   create a fleet repository
  nepfleet doctor                       check that this host can run what is planned for it
  nepfleet new <template> [name]        create a stack from a template (asks for its inputs)
  nepfleet enable [feature]             add an opt-in feature (ingress, logs, uptime, dashboard, …) as a system stack; lists them without args
  nepfleet plan [stack...] [--json]     show what apply would change
  nepfleet apply [stack...]             apply, health-gated, rolls back on failure
  nepfleet render <stack>               print rendered files (secrets masked)
  nepfleet updates [stack...] [--json]  image updates within each track, beyond it, and rebuilt tags (read-only)
  nepfleet secret set <stack> <KEY>     store a secret (encrypted, read from stdin)
  nepfleet secret set --global <KEY>    store a fleet-wide secret (fleet.secret.yaml)
  nepfleet secret check                 fail if any *.secret.yaml is not encrypted
  nepfleet notify test <channel>        send a test message to a notification channel
  nepfleet watch                        report OOM kills, crashes, crash loops and failed health checks
  nepfleet key backup --out FILE        copy the age key, encrypted with a passphrase (or --recipient)
  nepfleet mirror                       push origin's branches and tags to fleet.yaml's spec.mirrors (no force, no deletes)
  nepfleet history <stack>              versions of a stack in the fleet repository
  nepfleet restore <stack> --archive F  put a stack's volumes back from a volume-backup archive
  nepfleet rollback <stack> [commit]    restore an earlier version, commit and apply
  nepfleet import compose [project...]  adopt running Compose projects (lists them without args)
  nepfleet import portainer --url URL [stack...]  adopt Portainer stacks (token in PORTAINER_TOKEN or prompted)
  nepfleet template validate <path>...  validate templates (searches recursively)
  nepfleet template index <root>        print index.json, the catalogue of the templates under root
  nepfleet template match <stack> [--diff]  which catalogue template covers a plain stack, and how they differ
  nepfleet serve [flags]                run the optional HTTP API
  nepfleet token create|list|revoke     API tokens for scripts and CI (scoped, expiring, stored hashed)
  nepfleet version                      print the version

Stack commands take --fleet <dir> (default .), --state <dir> and --host <name>.
`

// releaseTag matches the versions release builds are stamped with.
var releaseTag = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

func main() {
	if releaseTag.MatchString(version) {
		fleet.SchemaRef = version // schema links in written files match this release
	}
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "nepfleet:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return errors.New("no command given")
	}
	switch args[0] {
	case "version", "--version":
		fmt.Fprintf(stdout, "nepfleet %s (%s)\n", version, commit)
		return nil
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	case "init":
		dir := "."
		if len(args) > 1 {
			dir = args[1]
		}
		if err := fleet.Init(dir); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "fleet repository created in %s\n", dir)
		return nil
	case "template":
		switch {
		case len(args) >= 3 && args[1] == "validate":
			return validateTemplates(args[2:], stdout)
		case len(args) == 3 && args[1] == "index":
			return indexTemplates(args[2], stdout, stderr)
		case len(args) >= 3 && args[1] == "match":
			return cmdTemplateMatch(args[2:], stdout, stderr)
		}
		return errors.New("usage: nepfleet template validate <path>... | nepfleet template index <root> | nepfleet template match <stack> [--diff]")
	case "serve":
		return serve(args[1:], stderr)
	case "doctor":
		return cmdDoctor(args[1:], stdout, stderr)
	case "new":
		return cmdNew(args[1:], stdout, stderr)
	case "plan":
		return cmdPlan(args[1:], stdout, stderr)
	case "apply":
		return cmdApply(args[1:], stdout, stderr)
	case "render":
		return cmdRender(args[1:], stdout, stderr)
	case "secret":
		return cmdSecret(args[1:], stdout, stderr)
	case "history":
		return cmdHistory(args[1:], stdout, stderr)
	case "watch":
		return cmdWatch(args[1:], stdout, stderr)
	case "token":
		return cmdToken(args[1:], stdout, stderr)
	case "notify":
		return cmdNotify(args[1:], stdout, stderr)
	case "restore":
		return cmdRestore(args[1:], stdout, stderr)
	case "enable":
		return cmdEnable(args[1:], stdout, stderr)
	case "updates":
		return cmdUpdates(args[1:], stdout, stderr)
	case "key":
		return cmdKey(args[1:], stdout, stderr)
	case "mirror":
		return cmdMirror(args[1:], stdout, stderr)
	case "rollback":
		return cmdRollback(args[1:], stdout, stderr)
	case "import":
		return cmdImport(args[1:], stdout, stderr)
	}
	fmt.Fprint(stderr, usage)
	return fmt.Errorf("unknown command %q", args[0])
}

// indexTemplates prints the catalogue of every template under root. It
// refuses to index an invalid template.
func indexTemplates(root string, stdout, stderr io.Writer) error {
	dirs, err := findTemplates([]string{root})
	if err != nil {
		return err
	}
	var ts []*template.Template
	for _, dir := range dirs {
		t, err := template.Load(dir)
		if err == nil {
			err = t.Validate()
		}
		if err != nil {
			fmt.Fprintf(stderr, "FAIL %s\n  %s\n", dir, strings.ReplaceAll(err.Error(), "\n", "\n  "))
			return errors.New("not indexing invalid templates; see nepfleet template validate")
		}
		ts = append(ts, t)
	}
	c, err := template.Index(root, ts)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(c)
}

func findTemplates(roots []string) ([]string, error) {
	var dirs []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && d.Name() == "template.yaml" {
				dirs = append(dirs, filepath.Dir(p))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if len(dirs) == 0 {
		return nil, errors.New("no template.yaml found")
	}
	return dirs, nil
}

func validateTemplates(roots []string, out io.Writer) error {
	dirs, err := findTemplates(roots)
	if err != nil {
		return err
	}
	failed := 0
	for _, dir := range dirs {
		t, err := template.Load(dir)
		if err == nil {
			err = t.Validate()
		}
		if err != nil {
			failed++
			fmt.Fprintf(out, "FAIL %s\n  %s\n", dir, strings.ReplaceAll(err.Error(), "\n", "\n  "))
			continue
		}
		fmt.Fprintf(out, "ok   %s\n", dir)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d templates invalid", failed, len(dirs))
	}
	return nil
}

func serve(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("listen", "127.0.0.1:7480", "address to listen on")
	fleetDir := fs.String("fleet", envOr("NEPFLEET_FLEET", "."), "fleet repository")
	stateDir := fs.String("state", envOr("NEPFLEET_STATE", defaultState()), "state directory (API tokens live in <state>/api)")
	name := fs.String("name", hostname(), "name shown to frontends")
	host := fs.String("host", envOr("NEPFLEET_HOST", hostname()), "this host's name, as for plan and apply")
	origins := fs.String("allowed-origins", "", "comma separated frontend origins allowed via CORS")
	proxies := fs.String("trusted-proxies", "", "comma separated CIDRs of reverse proxies whose X-Forwarded-For is trusted, e.g. 172.16.0.0/12")
	tlsCert := fs.String("tls-cert", "", "TLS certificate file (with --tls-key; renewals are picked up without a restart)")
	tlsKey := fs.String("tls-key", "", "TLS private key file")
	tlsDomain := fs.String("tls-domain", "", "get a certificate for this domain from Let's Encrypt, accepting its terms (the domain must reach this server on port 443)")
	behindProxy := fs.Bool("behind-proxy", false, "listen on plain HTTP beyond loopback: a reverse proxy in front terminates TLS")
	if err := fs.Parse(args); err != nil {
		return err
	}
	tlsConfig, err := serveTLS(*tlsCert, *tlsKey, *tlsDomain, *stateDir)
	if err != nil {
		return err
	}
	if err := api.CheckListen(*addr, tlsConfig != nil, *behindProxy); err != nil {
		return err
	}

	var trustedNets []*net.IPNet
	for _, c := range strings.Split(*proxies, ",") {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			return fmt.Errorf("--trusted-proxies: %q is not a CIDR", c)
		}
		trustedNets = append(trustedNets, n)
	}
	var allowed []string
	for _, o := range strings.Split(*origins, ",") {
		if o = strings.TrimSpace(o); o != "" {
			allowed = append(allowed, o)
		}
	}
	c := common{fleetDir: *fleetDir, stateDir: *stateDir, host: *host}
	srv := api.Server(*addr, api.Options{
		Info:           api.Info{Name: *name, Version: version, APIVersion: "v1"},
		AllowedOrigins: allowed,
		Tokens:         tokenStore(*stateDir),
		TrustedProxies: trustedNets,
		Stacks:         func() ([]api.StackInfo, error) { return stackInfos(*fleetDir) },
		Plan: func(_ context.Context, stack string) (any, error) {
			if !apiMu.TryLock() {
				return nil, api.ErrBusy
			}
			defer apiMu.Unlock()
			return withStack(c, stack, func(e *env, s *fleet.Stack) (any, error) {
				p, err := e.prepare(s)
				if err != nil {
					return nil, err
				}
				return apiPlanOf(p.plan)
			})
		},
		StartApply: func(_ context.Context, stack, planID string, confirm bool, by string) (func(io.Writer) error, error) {
			return startApply(c, stack, planID, confirm, by)
		},
		History: func(_ context.Context, stack string, n int) ([]api.HistoryEntry, error) {
			h, err := withStack(c, stack, func(e *env, s *fleet.Stack) (any, error) { return e.history(s, n) })
			if err != nil {
				return nil, err
			}
			return h.([]api.HistoryEntry), nil
		},
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	srv.TLSConfig = tlsConfig
	go func() {
		if tlsConfig != nil {
			errc <- srv.ListenAndServeTLS("", "")
			return
		}
		errc <- srv.ListenAndServe()
	}()
	fmt.Fprintf(stderr, "nepfleet API listening on %s\n", *addr)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "nepfleet"
	}
	return h
}

// apiMu runs one fleet operation of the API at a time: plans fetch template
// sources and render into shared directories, applies change the host.
// Requests that find it taken are refused at once (busy), not queued.
var apiMu sync.Mutex

// apiPlan is a plan as the API returns it: with an ID the client sends back
// to apply exactly what it reviewed.
type apiPlan struct {
	*deploy.Plan
	ID string `json:"id"`
}

// secretMark is a masked secret's keyed mark in a plan diff.
var secretMark = regexp.MustCompile(`•••••• \([0-9a-f]+\)`)

// apiPlanOf hides the server's paths and adds the plan's ID: a hash of
// everything the client was shown (secret marks are keyed per process, so
// a restart changes the ID and asks for a fresh review).
func apiPlanOf(p *deploy.Plan) (*apiPlan, error) {
	pl := *p
	pl.Dir = "" // the server's paths are none of the client's business
	raw, err := json.Marshal(&pl)
	if err != nil {
		return nil, err
	}
	// Secrets NepFleet generates are new on every plan, and so are their
	// marks: the ID covers which secrets change, not the marks.
	sum := sha256.Sum256(secretMark.ReplaceAll(raw, []byte("••••••")))
	return &apiPlan{Plan: &pl, ID: hex.EncodeToString(sum[:16])}, nil
}

// startApply plans the stack again and, if the plan is still the reviewed
// one, returns the apply to run as a job. The API lock is held from the
// check to the end of the apply, so nothing changes in between.
func startApply(c common, stack, planID string, confirm bool, by string) (func(io.Writer) error, error) {
	if !apiMu.TryLock() {
		return nil, api.ErrBusy
	}
	_, err := withStack(c, stack, func(e *env, s *fleet.Stack) (any, error) {
		p, err := e.prepare(s)
		if err != nil {
			return nil, err
		}
		ap, err := apiPlanOf(p.plan)
		if err != nil {
			return nil, err
		}
		switch {
		case ap.ID != planID:
			return nil, api.ErrPlanChanged
		case len(p.plan.Destructive) > 0 && !confirm:
			return nil, api.ErrNeedsConfirm
		}
		return nil, nil
	})
	if err != nil {
		apiMu.Unlock()
		return nil, err
	}
	return func(log io.Writer) error {
		defer apiMu.Unlock()
		fmt.Fprintf(log, "apply %s, requested by %s over the API\n", stack, by)
		// --yes: the client reviewed and asked; --confirm: destructive
		// changes were confirmed above. So apply never prompts.
		args := []string{"--fleet", c.fleetDir, "--state", c.stateDir, "--host", c.host, "--yes"}
		if confirm {
			args = append(args, "--confirm", stack)
		}
		return cmdApply(append(args, stack), log, log)
	}, nil
}

// withStack opens the fleet fresh (edits and pulls show without a restart)
// and runs fn on one of its stacks; an unknown stack is api.ErrNotFound.
func withStack(c common, name string, fn func(*env, *fleet.Stack) (any, error)) (any, error) {
	e, err := c.open()
	if err != nil {
		return nil, err
	}
	all, err := e.fleet.Stacks()
	if err != nil {
		return nil, err
	}
	for _, s := range all {
		if s.Metadata.Name == name {
			return fn(e, s)
		}
	}
	return nil, api.ErrNotFound
}

// serveTLS builds the server's TLS configuration from its flags; nil means
// plain HTTP.
func serveTLS(cert, key, domain, stateDir string) (*tls.Config, error) {
	switch {
	case domain != "" && (cert != "" || key != ""):
		return nil, errors.New("--tls-domain and --tls-cert/--tls-key exclude each other")
	case (cert == "") != (key == ""):
		return nil, errors.New("--tls-cert and --tls-key belong together")
	case domain != "":
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(domain),
			Cache:      autocert.DirCache(filepath.Join(stateDir, "api", "acme")),
		}
		c := m.TLSConfig()
		c.MinVersion = tls.VersionTLS12
		return c, nil
	case cert != "":
		r := &api.CertReloader{CertFile: cert, KeyFile: key}
		if err := r.Load(); err != nil {
			return nil, fmt.Errorf("TLS certificate: %w", err)
		}
		return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: r.GetCertificate}, nil
	}
	return nil, nil
}

// stackInfos reads the fleet's stacks for the API, fresh on every request,
// so edits and pulls show without a restart.
func stackInfos(dir string) ([]api.StackInfo, error) {
	f, err := fleet.Load(dir)
	if err != nil {
		return nil, err
	}
	all, err := f.Stacks()
	if err != nil {
		return nil, err
	}
	out := make([]api.StackInfo, 0, len(all))
	for _, s := range all {
		si := api.StackInfo{Name: s.Metadata.Name, Target: s.Spec.Target, Mode: s.Spec.Mode, Apply: s.Spec.Apply, Updates: s.Spec.Updates}
		if t := s.Spec.Template; t != nil {
			si.Template = t.Source + "/" + t.Name + "@" + t.Version
		}
		out = append(out, si)
	}
	return out, nil
}
