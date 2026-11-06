// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/Serraniel/neptide/pkg/kuma"

	"github.com/Serraniel/nepfleet/internal/render"
)

// kumaFirstRun sets up a freshly deployed Uptime Kuma (the uptime-kuma
// template): its database and the admin from KUMA_USERNAME/KUMA_PASSWORD,
// right after the first apply, so its first-run screen is never left open
// on a public domain. It talks to the container directly, not through the
// domain. Problems only warn: the stack itself is deployed.
func (e *env) kumaFirstRun(ctx context.Context, stdout io.Writer, p *prepared) {
	if p.tpl == nil || p.tpl.Metadata.Name != "uptime-kuma" || p.plan.Mode == "swarm" {
		return
	}
	name := p.stack.Metadata.Name
	warn := func(err error) {
		fmt.Fprintf(stdout, "%s: ⚠ Uptime Kuma setup: %v; finish it in Kuma's first-run screen now\n", name, err)
	}
	env := render.EnvValues(p.out.Files[render.EnvFile])
	user, pass := env["KUMA_USERNAME"], env["KUMA_PASSWORD"]
	if user == "" || pass == "" {
		warn(fmt.Errorf("KUMA_USERNAME or KUMA_PASSWORD is empty"))
		return
	}
	base, err := containerURL(ctx, name, "uptime-kuma", 3001)
	if err != nil {
		warn(err)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := kumaDatabase(ctx, base); err != nil {
		warn(err)
		return
	}
	switch err := kuma.Setup(ctx, base, user, pass, nil); {
	case err == nil:
		fmt.Fprintf(stdout, "%s: Uptime Kuma admin %s created\n", name, user)
	case strings.Contains(strings.ToLower(err.Error()), "initialized") || strings.Contains(strings.ToLower(err.Error()), "already"):
		// Set up before: check that the credentials still work.
		k, lerr := kuma.Login(ctx, base, user, pass, nil)
		if lerr != nil {
			warn(fmt.Errorf("already set up, and KUMA_USERNAME/KUMA_PASSWORD do not log in: %w", lerr))
			return
		}
		k.Close()
	default:
		warn(err)
		return
	}
	fmt.Fprintf(stdout, "%s: to have NepFleet put monitors into maintenance during applies, add to fleet.yaml:\n"+
		"  uptimeKuma: [{name: %s, url: \"https://%s\", username: %s, passwordSecret: KUMA_PASSWORD}]\n"+
		"  and store the password there too: nepfleet secret set --global KUMA_PASSWORD\n", name, name, env["DOMAIN"], user)
}

// containerURL is http://<ip>:<port> of a compose service's container on
// this host.
func containerURL(ctx context.Context, project, service string, port int) (string, error) {
	id, err := exec.CommandContext(ctx, "docker", "compose", "--project-name", project, "ps", "--quiet", service).Output() //nolint:gosec // validated names
	if err != nil || strings.TrimSpace(string(id)) == "" {
		return "", fmt.Errorf("no running %s container", service)
	}
	ips, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}", strings.Fields(string(id))[0]).Output() //nolint:gosec // a container ID
	if err != nil {
		return "", err
	}
	f := strings.Fields(string(ips))
	if len(f) == 0 {
		return "", fmt.Errorf("the %s container has no IP address", service)
	}
	return fmt.Sprintf("http://%s:%d", f[0], port), nil
}

// kumaDatabase completes Kuma 2's database step (SQLite) and waits until
// the server answers, which takes a while on the first start.
func kumaDatabase(ctx context.Context, base string) error {
	type info struct {
		NeedSetup bool `json:"needSetup"`
	}
	get := func() (*info, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/setup-database-info", nil)
		if err != nil {
			return nil, err
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer func() { _ = res.Body.Close() }()
		var i info
		if err := json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&i); err != nil {
			return nil, err
		}
		return &i, nil
	}
	posted := false
	for {
		i, err := get()
		switch {
		case err == nil && !i.NeedSetup:
			// The database is ready; socket.io answers shortly after.
			if res, err := http.Get(base + "/socket.io/?EIO=4&transport=polling"); err == nil { //nolint:gosec,noctx // the container's own address
				_ = res.Body.Close()
				if res.StatusCode == http.StatusOK {
					return nil
				}
			}
		case err == nil && i.NeedSetup && !posted:
			body := bytes.NewReader([]byte(`{"dbConfig":{"type":"sqlite"}}`))
			req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, base+"/setup-database", body)
			if rerr != nil {
				return rerr
			}
			req.Header.Set("Content-Type", "application/json")
			if res, perr := http.DefaultClient.Do(req); perr == nil {
				_ = res.Body.Close()
				posted = true
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("uptime Kuma did not get ready: %w", ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}
