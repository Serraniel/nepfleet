// SPDX-License-Identifier: AGPL-3.0-or-later

// Command nepfleet manages Linux hosts and Docker deployments from a git
// repository. Run `nepfleet help` for usage.
package main

import (
	"context"
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
	"syscall"
	"time"

	"github.com/Serraniel/nepfleet/internal/api"
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
  nepfleet plan [stack...] [--json]     show what apply would change
  nepfleet apply [stack...]             apply, health-gated, rolls back on failure
  nepfleet render <stack>               print rendered files (secrets masked)
  nepfleet secret set <stack> <KEY>     store a secret (encrypted, read from stdin)
  nepfleet secret set --global <KEY>    store a fleet-wide secret (fleet.secret.yaml)
  nepfleet secret check                 fail if any *.secret.yaml is not encrypted
  nepfleet history <stack>              versions of a stack in the fleet repository
  nepfleet rollback <stack> [commit]    restore an earlier version, commit and apply
  nepfleet import compose [project...]  adopt running Compose projects (lists them without args)
  nepfleet import portainer --url URL [stack...]  adopt Portainer stacks (token in PORTAINER_TOKEN or prompted)
  nepfleet template validate <path>...  validate templates (searches recursively)
  nepfleet template index <root>        print index.json, the catalogue of the templates under root
  nepfleet serve [flags]                run the optional HTTP API
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
		}
		return errors.New("usage: nepfleet template validate <path>... | nepfleet template index <root>")
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
	name := fs.String("name", hostname(), "name shown to frontends")
	origins := fs.String("allowed-origins", "", "comma separated frontend origins allowed via CORS")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if host, _, err := net.SplitHostPort(*addr); err == nil {
		if ip := net.ParseIP(host); host == "" || (ip != nil && !ip.IsLoopback()) {
			fmt.Fprintln(stderr, "warning: listening beyond loopback without TLS; put a TLS proxy in front (see docs/security.md)")
		}
	}

	var allowed []string
	for _, o := range strings.Split(*origins, ",") {
		if o = strings.TrimSpace(o); o != "" {
			allowed = append(allowed, o)
		}
	}
	srv := api.Server(*addr, api.Options{
		Info:           api.Info{Name: *name, Version: version, APIVersion: "v1"},
		AllowedOrigins: allowed,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
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
