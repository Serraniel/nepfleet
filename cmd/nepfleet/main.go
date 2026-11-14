// SPDX-License-Identifier: AGPL-3.0-or-later

// Command nepfleet manages Linux hosts and Docker deployments from a git
// repository. Run `nepfleet help` for usage.
package main

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/auth"
	"github.com/Serraniel/nepfleet/internal/deploy"
	"github.com/Serraniel/nepfleet/internal/doctor"
	"github.com/Serraniel/nepfleet/internal/events"
	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/remote"
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
  nepfleet adopt [stack...]             a new installation takes over what runs, restarting nothing
  nepfleet apply [stack...]             apply, health-gated, rollsback on failure
  nepfleet render <stack>               print rendered files (secrets masked)
  nepfleet egress suggest <stack>       an allowlist from what a stack in egress mode log did
  nepfleet sbom [stack...] [--out DIR]what the fleet runs, as CycloneDX (--contents: inside each image, with Syft)
  nepfleet updates apply [stack...]what a timer runs: stacks with updates: auto take rebuilt images
  nepfleet updates [stack...] [--json]image updates within each track, beyond it, and rebuilt tags (read-only)
  nepfleet secret set <stack> <KEY>     store a secret (encrypted, read from stdin)
  nepfleet secret set --global <KEY>    store a fleet-wide secret (fleet.secret.yaml)
  nepfleet secret check                 fail if any *.secret.yaml is not encrypted
  nepfleet notify test <channel>        send a test message to a notification channel
  nepfleet remote probe <ssh://host>    look at a machine over SSH without changing it: OS, Docker, swarm, fleet
  nepfleet remote add|list|refresh|connect|disconnect|remove  machines the local control daemon knows
  nepfleet host bootstrap <ssh://host>  prepare a machine over SSH: Docker, Compose, ingress network (shows a plan first)
  nepfleet host install <ssh://host>    install NepFleet on a prepared machine, as a container (shows a plan first)
  nepfleet host apply <name>            make a host's network mounts match hosts/<name>.yaml (shows a plan first)
  nepfleet user list|remove             who may sign in with a passkey
  nepfleet tui                          terminal client: machines, stacks, plans, apply
  nepfleet control [status|token|http]  the local control daemon (started on demand; keeps running)
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
	// ssh-add runs SSH_ASKPASS with its prompt as the only argument, so the
	// askpass helper is told by its environment, not by a command name.
	if os.Getenv(remote.AskpassOnceEnv) != "" {
		return cmdAskpass(stdout)
	}
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
		case len(args) >= 3 && args[1] == "generate":
			return cmdTemplateGenerate(args[2:], stdout, stderr)
		}
		return errors.New("usage: nepfleet template validate <path>... | nepfleet template index <root> | nepfleet template match <stack> [--diff] | nepfleet template generate <compose.yaml> --name NAME [--out DIR]")
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
	case "user":
		return cmdUser(args[1:], stdout, stderr)
	case "tui":
		return cmdTUI(stdout)
	case "control":
		return cmdControl(args[1:], stdout, stderr)
	case "host":
		return cmdHost(args[1:], stdout, stderr)
	case "remote":
		return cmdRemote(args[1:], stdout, stderr)
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
	case "adopt":
		return cmdAdopt(args[1:], stdout, stderr)
	case "sbom":
		return cmdSBOM(args[1:], stdout, stderr)
	case "egress":
		return cmdEgress(args[1:], stdout, stderr)
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
	syncEvery := fs.Duration("sync", 0, "pull the fleet repository from its origin this often (e.g. 5m) and apply changed stacks set to apply: auto; 0: only on webhooks, if NEPFLEET_SYNC_SECRET is set")
	fleetDir := fs.String("fleet", envOr("NEPFLEET_FLEET", "."), "fleet repository")
	stateDir := fs.String("state", envOr("NEPFLEET_STATE", defaultState()), "state directory (API tokens live in <state>/api)")
	name := fs.String("name", hostname(), "name shown to frontends")
	host := fs.String("host", envOr("NEPFLEET_HOST", hostname()), "this host's name, as for plan and apply")
	origins := fs.String("allowed-origins", "", "comma separated frontend origins allowed via CORS")
	proxies := fs.String("trusted-proxies", "", "comma separated CIDRs of reverse proxies whose X-Forwarded-For is trusted, e.g. 172.16.0.0/12")
	tlsCert := fs.String("tls-cert", "", "TLS certificate file (with --tls-key; renewals are picked up without a restart)")
	tlsKey := fs.String("tls-key", "", "TLS private key file")
	tlsDomain := fs.String("tls-domain", "", "get a certificate for this domain from Let's Encrypt, accepting its terms (the domain must reach this server on port 443)")
	rpID := fs.String("passkey-domain", "", "turn on passkey sign-in for this domain, e.g. fleet.example.org")
	rpOrigins := fs.String("passkey-origins", "", "comma separated exact origins of the web UI, for passkeys (default https://<passkey-domain>)")
	behindProxy := fs.Bool("behind-proxy", false, "listen on plain HTTP beyond loopback: a reverse proxy in front terminates TLS")
	oidcIssuer := fs.String("oidc-issuer", "", "turn on sign-in through this OpenID Connect provider; its client secret comes from NEPFLEET_OIDC_CLIENT_SECRET")
	oidcClient := fs.String("oidc-client-id", "", "this server's client ID at the provider")
	oidcRedirect := fs.String("oidc-redirect-url", "", "this server's public /api/v1/auth/oidc/callback URL")
	oidcAllow := fs.String("oidc-allow", "", "comma separated: who may sign in, as verified emails, sub:<subject> or group:<name>; each can do everything")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var oidcConfig *api.OIDC
	if *oidcIssuer != "" {
		oidcConfig = &api.OIDC{Issuer: *oidcIssuer, ClientID: *oidcClient, ClientSecret: os.Getenv("NEPFLEET_OIDC_CLIENT_SECRET"),
			RedirectURL: *oidcRedirect, Allow: splitCSV(*oidcAllow)}
		// Not for child processes: stacks are rendered and run from here.
		_ = os.Unsetenv("NEPFLEET_OIDC_CLIENT_SECRET")
		if err := oidcConfig.CheckOIDC(); err != nil {
			return fmt.Errorf("--oidc-issuer: %w", err)
		}
		// A session could read but not change anything, and there'd be nowhere to return to.
		if strings.TrimSpace(*origins) == "" && *rpID == "" {
			return errors.New("--oidc-issuer needs --allowed-origins: where the web UI runs")
		}
	}
	tlsConfig, err := serveTLS(*tlsCert, *tlsKey, *tlsDomain, *stateDir)
	if err != nil {
		return err
	}
	if err := api.CheckListen(*addr, tlsConfig != nil, *behindProxy); err != nil {
		return err
	}
	if *behindProxy && *proxies == "" {
		fmt.Fprintln(stderr, "warning: --behind-proxy without --trusted-proxies: every client counts as the proxy, so one client's failed attempts lock out all token users")
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
			if err := api.CheckOrigin(o); err != nil {
				return fmt.Errorf("--allowed-origins: %w", err)
			}
			allowed = append(allowed, o)
		}
	}
	c := common{fleetDir: *fleetDir, stateDir: *stateDir, host: *host}
	// Git sync: on a timer, on forges' webhooks, or both.
	var gitSync *syncer
	var syncStatus func() api.SyncStatus
	var syncNow func()
	if *syncEvery > 0 || os.Getenv("NEPFLEET_SYNC_SECRET") != "" {
		gitSync = newSyncer(c, *syncEvery, stderr)
		syncStatus, syncNow = gitSync.Status, gitSync.Now
	}
	accounts, err := serveAccounts(*rpID, *rpOrigins, *stateDir, stderr)
	if err != nil {
		return err
	}
	if oidcConfig != nil {
		if accounts == nil {
			accounts = &api.Accounts{Users: &auth.UserStore{Dir: filepath.Join(*stateDir, "api")}, Sessions: &auth.Sessions{}}
		}
		accounts.OIDC = oidcConfig
	}
	if accounts != nil {
		accounts.Alert = func(_ context.Context, a api.Alert) error { return sendAlert(c, stderr, a) }
	}
	// A stop signal does not cancel an apply: stopped halfway, a stack stays
	// half updated. Only a second signal does.
	applyCtx, abortApply := context.WithCancel(context.Background())
	defer abortApply()
	srv := api.Server(*addr, api.Options{
		Info:           api.Info{Name: *name, Version: version, APIVersion: "v1"},
		AllowedOrigins: allowed,
		Tokens:         tokenStore(*stateDir),
		TrustedProxies: trustedNets,
		Accounts:       accounts,
		SyncStatus:     syncStatus,
		SyncNow:        syncNow,
		SyncSecret:     os.Getenv("NEPFLEET_SYNC_SECRET"),
		Stacks: func() ([]api.StackInfo, error) {
			// Without Docker, stacks are listed without a state.
			cs, _ := composeContainers(context.Background())
			return stackInfos(*fleetDir, *host, stackStates(cs))
		},
		Plan: func(ctx context.Context, stack string, updateLock bool) (any, error) {
			if !apiMu.TryLock() {
				return nil, api.ErrBusy
			}
			defer apiMu.Unlock()
			return withStack(c, stack, func(e *env, s *fleet.Stack) (any, error) {
				p, err := e.prepareAPI(ctx, s, updateLock)
				if err != nil {
					return nil, err
				}
				return apiPlanOf(p.plan)
			})
		},
		Templates: func(context.Context) (any, error) {
			if !apiMu.TryLock() {
				return nil, api.ErrBusy
			}
			defer apiMu.Unlock()
			return sourceCatalogues(c)
		},
		Events: func(after int64, limit int, allow func(string) bool) (any, error) {
			return eventStore(*stateDir).After(after, limit, func(e events.Event) bool { return allow(e.Stack) })
		},
		Alert:       func(_ context.Context, a api.Alert) error { return sendAlert(c, stderr, a) },
		Maintenance: func(ctx context.Context) ([]api.KumaMaintenance, error) { return fleetMaintenance(ctx, c) },
		NewStack: func(_ context.Context, req api.NewStack, by string) (api.NewStackResult, error) {
			return newStackAPI(c, req, by)
		},
		Features: func() ([]api.Feature, error) { return featureList(c) },
		Channels: func() ([]string, error) { return channelNames(c) },
		Doctor:   func(ctx context.Context) (api.DoctorReport, error) { return doctorAPI(ctx, c) },
		TestChannel: func(_ context.Context, name string) error {
			return testChannel(c, name)
		},
		SecretKeys: func(_ context.Context, stack string) ([]string, error) { return secretKeys(c, stack) },
		SetSecret: func(_ context.Context, stack, key string, value *string, by string) (string, error) {
			return setSecretAPI(c, stack, key, value, by)
		},
		StackFiles: func(_ context.Context, stack string) ([]api.StackFile, error) {
			out, err := withStack(c, stack, func(_ *env, s *fleet.Stack) (any, error) { return stackFiles(s) })
			if err != nil {
				return nil, err
			}
			return out.([]api.StackFile), nil
		},
		EditStack: func(_ context.Context, stack string, e api.StackEdit, by string, stacks []string) (api.EditResult, error) {
			return editStack(c, stack, e, by, stacks)
		},
		EgressAllow: func(_ context.Context, stack string, add []string, by string) (api.EgressAllowResult, error) {
			return egressAllowAPI(c, stack, add, by)
		},
		Egress: func(ctx context.Context, stack string) (*api.StackEgress, error) {
			if !apiMu.TryLock() {
				return nil, api.ErrBusy
			}
			defer apiMu.Unlock()
			out, err := withStack(c, stack, func(e *env, s *fleet.Stack) (any, error) {
				ctx, cancel := context.WithTimeout(ctx, time.Minute)
				defer cancel()
				mode, r, found, err := e.stackEgress(ctx, s)
				if err != nil {
					return nil, err
				}
				nonNil := func(v []string) []string { return append([]string{}, v...) }
				return &api.StackEgress{Mode: cmp.Or(mode, fleet.EgressOff), Sidecars: found,
					Hosts: nonNil(r.Hosts), Addresses: nonNil(r.Addresses), LookedUp: nonNil(r.LookedUp)}, nil
			})
			if err != nil {
				return nil, err
			}
			return out.(*api.StackEgress), nil
		},
		Updates: func(ctx context.Context, stack string) ([]api.ImageUpdate, error) {
			if !apiMu.TryLock() {
				return nil, api.ErrBusy
			}
			defer apiMu.Unlock()
			u, err := withStack(c, stack, func(e *env, s *fleet.Stack) (any, error) {
				e.resolverOnce()
				return e.stackUpdates(ctx, s)
			})
			if err != nil {
				return nil, err
			}
			return u.([]api.ImageUpdate), nil
		},
		StartRollback: func(_ context.Context, stack, commit string, confirm bool, by string) (func(io.Writer) error, error) {
			return startRollback(applyCtx, c, stack, commit, confirm, by)
		},
		StartApply: func(_ context.Context, stack, planID string, confirm, updateLock bool, by string) (func(io.Writer) error, error) {
			return startApply(applyCtx, c, stack, planID, confirm, updateLock, by)
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
	if gitSync != nil {
		go gitSync.run(ctx)
	}
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
	// After a reboot, emulators are gone until the binfmt stack runs again.
	go func() {
		if e, err := c.open(); err == nil {
			reregisterBinfmt(ctx, e.fleet, *host, doctor.Host(), startProject, stderr)
		}
	}()

	var serveErr error
	select {
	case serveErr = <-errc:
	case <-ctx.Done():
		// A second signal aborts a running apply, also one during the shutdown.
		again := make(chan os.Signal, 1)
		signal.Notify(again, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(again)
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// A client holding its connection open must not cut an apply short.
			fmt.Fprintf(stderr, "closing connections: %v\n", err)
			_ = srv.Close()
		}
		return waitForApply(again, abortApply, stderr)
	}
	// The listener failed: nothing new comes in, but an apply still finishes.
	if err := waitForApply(make(chan os.Signal), abortApply, stderr); err != nil {
		return errors.Join(serveErr, err)
	}
	return serveErr
}

// waitForApply lets a running apply finish; its health gate and rollback bound
// it. A signal on again aborts it.
func waitForApply(again <-chan os.Signal, abort func(), stderr io.Writer) error {
	if apiMu.TryLock() {
		return nil
	}
	fmt.Fprintln(stderr, "waiting for the running apply to finish (stop again to abort it)")
	done := make(chan struct{})
	go func() { apiMu.Lock(); close(done) }()
	select {
	case <-done:
		return nil
	case <-again:
		abort()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
		}
		return errors.New("stopped during an apply; check the stack with nepfleet plan")
	}
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "nepfleet"
	}
	return h
}

// applyWait is the health gate of an apply over the API.
const applyWait = 3 * time.Minute

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
	// marks: the ID covers which secrets change, and ignores every mark. A
	// secret that was already changing and changes again keeps the ID.
	sum := sha256.Sum256(secretMark.ReplaceAll(raw, []byte("••••••")))
	return &apiPlan{Plan: &pl, ID: hex.EncodeToString(sum[:16])}, nil
}

// checkReviewed refuses a plan other than the one the client reviewed, and
// destructive changes it did not confirm.
func checkReviewed(p *deploy.Plan, planID string, confirm bool) error {
	ap, err := apiPlanOf(p)
	if err != nil {
		return err
	}
	switch {
	case ap.ID != planID:
		return api.ErrPlanChanged
	case len(p.Destructive) > 0 && !confirm:
		return api.ErrNeedsConfirm
	}
	return nil
}

// startApply answers at once whether the plan is still the reviewed one, and
// returns the apply to run as a job. The job checks again, on the very plan
// it applies: the repository can change in between (a pull, the CLI). The API
// lock is held until the job ends; ctx ends only when serve is told to abort.
func startApply(ctx context.Context, c common, stack, planID string, confirm, updateLock bool, by string) (func(io.Writer) error, error) {
	if !apiMu.TryLock() {
		return nil, api.ErrBusy
	}
	handedOver := false
	defer func() {
		if !handedOver {
			apiMu.Unlock() // also on a panic, which net/http would swallow
		}
	}()
	if _, err := withStack(c, stack, func(e *env, s *fleet.Stack) (any, error) {
		p, err := e.prepareAPI(ctx, s, updateLock)
		if err != nil {
			return nil, err
		}
		return nil, checkReviewed(p.plan, planID, confirm)
	}); err != nil {
		return nil, err
	}
	handedOver = true
	return func(log io.Writer) error {
		defer apiMu.Unlock()
		fmt.Fprintf(log, "apply %s, requested by %s over the API\n", stack, by)
		o := applyOptions{yes: true, wait: applyWait, updateLock: updateLock, check: func(p *deploy.Plan) error { return checkReviewed(p, planID, confirm) }}
		if confirm {
			o.confirm = []string{stack} // checked above: only what the client saw
		}
		err := runApply(ctx, c, []string{stack}, o, log, log)
		commitAPIApply(c, stack, by, log)
		return err
	}, nil
}

// sourceCatalogue is one template source's catalogue, for the API.
type sourceCatalogue struct {
	Source    string           `json:"source"`
	Commit    string           `json:"commit,omitempty"`
	Templates []template.Entry `json:"templates"`
	// Invalid lists templates that do not validate (not offered).
	Invalid []string `json:"invalid,omitempty"`
}

// sourceCatalogues indexes every template source of the fleet, fetching
// them like plan does.
func sourceCatalogues(c common) ([]sourceCatalogue, error) {
	e, err := c.open()
	if err != nil {
		return nil, err
	}
	out := []sourceCatalogue{}
	for _, src := range e.fleet.Spec.TemplateSources {
		res, err := e.resolver.Resolve(src.Name)
		if err != nil {
			return nil, err
		}
		dirs, err := findTemplates([]string{res.Dir})
		if err != nil {
			return nil, err
		}
		sc := sourceCatalogue{Source: src.Name, Commit: res.Commit}
		var ts []*template.Template
		for _, dir := range dirs {
			t, err := template.Load(dir)
			if err == nil {
				err = t.Validate()
			}
			if err != nil {
				sc.Invalid = append(sc.Invalid, relTo(res.Dir, dir))
				continue
			}
			ts = append(ts, t)
		}
		cat, err := template.Index(res.Dir, ts)
		if err != nil {
			return nil, err
		}
		sc.Templates = cat.Templates
		out = append(out, sc)
	}
	return out, nil
}

// withStack opens the fleet fresh (edits and pulls show without a restart)
// and runs fn on one of its stacks; an unknown stack is api.ErrNotFound. It
// takes no lock: history only reads git and may run during an apply.
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

// serveAccounts turns on passkey sign-in if asked. With no users yet, it prints a
// one-time setup token to the log: whoever can read the log may create the first
// user, the same trust as whoever runs the server.
func serveAccounts(rpID, origins, stateDir string, stderr io.Writer) (*api.Accounts, error) {
	if rpID == "" {
		return nil, nil
	}
	list := splitCSV(origins)
	if len(list) == 0 {
		list = []string{"https://" + rpID}
	}
	for _, o := range list {
		if err := api.CheckOrigin(o); err != nil {
			return nil, fmt.Errorf("--passkey-origins: %w", err)
		}
	}
	w, err := api.NewWebAuthn(rpID, list)
	if err != nil {
		return nil, fmt.Errorf("passkeys: %w", err)
	}
	a := &api.Accounts{Users: &auth.UserStore{Dir: filepath.Join(stateDir, "api")}, Sessions: &auth.Sessions{}, WebAuthn: w}
	if !a.Users.Any() {
		raw := make([]byte, 24)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		a.SetupToken = base64.RawURLEncoding.EncodeToString(raw)
		fmt.Fprintf(stderr, "no users yet; create the first with this one-time setup token: %s\n", a.SetupToken)
	}
	return a, nil
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
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
func stackInfos(dir, host string, states map[string]string) ([]api.StackInfo, error) {
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
		si.URL = stackAddress(f, s)
		si.Links = stackLinks(f, all, s)
		si.Uses = stackUses(s)
		if s.Spec.Mode != "swarm" && (s.Spec.Target == host || s.Spec.Target == "local") {
			si.State = states[s.Metadata.Name]
		}
		out = append(out, si)
	}
	return out, nil
}

// stackAddress is where a stack's web UI is, as far as its settings say, without
// rendering it: domain values that come from another stack aren't resolved.
func stackAddress(f *fleet.Fleet, s *fleet.Stack) string {
	if a := s.Spec.Access; a != nil && a.Mode == "port" && a.Port > 0 {
		addr := s.Spec.Target
		if h, err := f.LoadHost(addr); err == nil && h.Spec.Address != "" {
			addr = h.Spec.Address
		}
		return fmt.Sprintf("http://%s:%d", addr, a.Port)
	}
	if d := s.Spec.Values["DOMAIN"]; !d.IsRef() && d.Literal != "" {
		return "https://" + d.Literal
	}
	return ""
}

// stackUses lists the values a stack takes from others, sorted: names, not values.
func stackUses(s *fleet.Stack) []api.StackRef {
	var out []api.StackRef
	for key, v := range s.Spec.Values {
		if v.IsRef() {
			out = append(out, api.StackRef{Key: key, From: v.From, Value: v.Value, Secret: v.Secret})
		}
	}
	slices.SortFunc(out, func(a, b api.StackRef) int { return strings.Compare(a.Key, b.Key) })
	return out
}

// stackLinks point to where else a stack can be looked at: its logs in the fleet's
// Dozzle on the same host, and the fleet's Uptime Kuma instances.
func stackLinks(f *fleet.Fleet, all []*fleet.Stack, s *fleet.Stack) []api.StackLink {
	var links []api.StackLink
	for _, other := range all {
		if t := other.Spec.Template; t != nil && t.Name == "dozzle" && other.Spec.Target == s.Spec.Target {
			if u := stackAddress(f, other); u != "" {
				links = append(links, api.StackLink{Name: "Logs in Dozzle", URL: u + "/stack/" + url.PathEscape(s.Metadata.Name)})
				break
			}
		}
	}
	for _, k := range f.Spec.UptimeKuma {
		links = append(links, api.StackLink{Name: "Monitors in " + k.Name, URL: strings.TrimRight(k.URL, "/") + "/dashboard"})
	}
	return links
}

// prepareAPI plans a stack as the API shows it: with updateLock, as apply
// --update-lock would (the template source's current commit, and image tags
// resolved to their current digests), so the reviewed plan is what applies.
func (e *env) prepareAPI(ctx context.Context, s *fleet.Stack, updateLock bool) (*prepared, error) {
	e.updateLock = updateLock
	p, err := e.prepare(s)
	if err != nil || !updateLock {
		return p, err
	}
	if _, err := e.previewUpdate(ctx, p, io.Discard); err != nil {
		return nil, err
	}
	return p, nil
}
