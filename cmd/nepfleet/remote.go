// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Serraniel/nepfleet/internal/remote"
)

// cmdRemote: nepfleet remote probe <address>.
func cmdRemote(args []string, stdout, stderr io.Writer) error {
	const usage = "usage: nepfleet remote probe ssh://[user@]host[:port] [--json] [--ssh-config FILE] | nepfleet remote add|list|refresh|remove"
	if len(args) > 0 && (args[0] == "add" || args[0] == "list" || args[0] == "refresh" || args[0] == "remove") {
		return cmdRemoteManage(args[0], args[1:], stdout, stderr)
	}
	if len(args) == 0 || args[0] != "probe" {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet("remote probe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print the result as JSON")
	config := fs.String("ssh-config", "", "ssh config file instead of ~/.ssh/config")
	pos, err := parseInterspersed(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New(usage)
	}
	t, err := remote.ParseTarget(pos[0])
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	r, err := remote.Probe(ctx, remote.SSH{ConfigFile: *config}.Run, t)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			*remote.Report
			Next string `json:"next"`
		}{r, r.Next()})
	}
	printReport(stdout, r)
	return nil
}

func printReport(w io.Writer, r *remote.Report) {
	line := func(k, v string) {
		if v != "" {
			fmt.Fprintf(w, "  %-10s %s\n", k, v)
		}
	}
	fmt.Fprintln(w, r.Target)
	line("system", fmt.Sprintf("%s (%s, %s)", r.OS, r.Kernel, r.Arch))
	who := r.User
	switch {
	case r.Root:
		who += " (root)"
	case r.Sudo:
		who += " (sudo without a password)"
	}
	line("user", who)
	switch {
	case !r.Docker:
		line("docker", "not installed")
	case !r.DockerRunning:
		line("docker", "installed, not reachable by this user")
	default:
		line("docker", fmt.Sprintf("%s, compose %s, %d running containers in %d projects", r.DockerVersion, noneIfEmpty(r.Compose), r.Containers, r.Projects))
	}
	if r.Swarm != "" {
		line("swarm", r.Swarm+" "+r.SwarmCluster)
	}
	line("nepfleet", r.NepFleet)
	line("container", r.NepFleetImage)
	if r.NepFleetRunning {
		line("server", "answers on port 7480")
	}
	line("fleet", r.Fleet)
	fmt.Fprintf(w, "\n%s\n", r.Next())
}

func noneIfEmpty(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
