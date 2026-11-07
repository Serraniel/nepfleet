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
	"strings"
	"syscall"
	"time"

	"github.com/Serraniel/nepfleet/internal/notify"
)

// dockerEvent is the part of `docker events --format '{{json .}}'` used.
type dockerEvent struct {
	Action string `json:"Action"`
	Actor  struct {
		ID         string            `json:"ID"`
		Attributes map[string]string `json:"Attributes"`
	} `json:"Actor"`
	TimeNano int64 `json:"timeNano"`
}

// watcher turns container events into notifications: OOM kills, crashes,
// crash loops and failed health checks. Stops NepFleet, Docker or a person
// asked for (a kill event shortly before, or exit codes 0 and 143) are not
// crashes. Each kind of problem is reported at most once per container per
// quiet period.
type watcher struct {
	quiet   time.Duration
	window  time.Duration // crash loop: loopAfter crashes within window
	loop    int
	logs    func(id string) string
	sent    map[string]time.Time
	crashes map[string][]time.Time
	killed  map[string]time.Time
	oomed   map[string]time.Time
}

func newWatcher(logs func(string) string) *watcher {
	return &watcher{quiet: 30 * time.Minute, window: 10 * time.Minute, loop: 3, logs: logs,
		sent: map[string]time.Time{}, crashes: map[string][]time.Time{}, killed: map[string]time.Time{}, oomed: map[string]time.Time{}}
}

func (w *watcher) process(ev dockerEvent, now time.Time) []notify.Event {
	id := ev.Actor.ID
	attrs := ev.Actor.Attributes
	name := strings.TrimPrefix(attrs["name"], "/")
	stack := attrs["com.docker.compose.project"]
	if stack == "" {
		stack = attrs["com.docker.stack.namespace"]
	}
	mk := func(kind, severity, title, msg string) []notify.Event {
		key := id + "/" + kind
		if t, ok := w.sent[key]; ok && now.Sub(t) < w.quiet {
			return nil
		}
		w.sent[key] = now
		if w.logs != nil {
			if l := strings.TrimSpace(w.logs(id)); l != "" {
				msg += "\n\nLast log lines:\n" + l
			}
		}
		return []notify.Event{{Name: "container." + kind, Severity: severity, Stack: stack, Title: title, Message: msg}}
	}
	switch {
	case ev.Action == "kill":
		w.killed[id] = now
	case ev.Action == "oom":
		w.oomed[id] = now
		return mk("oom", notify.Error, name+" ran out of memory", fmt.Sprintf("The kernel killed %s for using too much memory. Raise its memory limit or find the leak.", name))
	case strings.HasPrefix(ev.Action, "health_status: unhealthy"):
		return mk("unhealthy", notify.Warning, name+" is unhealthy", fmt.Sprintf("The health check of %s fails.", name))
	case ev.Action == "die":
		code := attrs["exitCode"]
		if t, ok := w.oomed[id]; ok && now.Sub(t) < time.Minute {
			return nil // reported as OOM already
		}
		if t, ok := w.killed[id]; ok && now.Sub(t) < time.Minute {
			return nil // stopped on purpose (docker stop, apply, a restart)
		}
		if code == "0" || code == "143" {
			return nil
		}
		recent := []time.Time{now}
		for _, t := range w.crashes[id] {
			if now.Sub(t) < w.window {
				recent = append(recent, t)
			}
		}
		w.crashes[id] = recent
		if len(recent) >= w.loop {
			return mk("crash_loop", notify.Error, name+" keeps crashing", fmt.Sprintf("%s exited with code %s, %d times in %s.", name, code, len(recent), w.window))
		}
		return mk("crashed", notify.Warning, name+" crashed", fmt.Sprintf("%s exited with code %s.", name, code))
	}
	return nil
}

// cmdWatch: nepfleet watch — follows Docker's events on this host and sends
// container problems to the fleet's notification channels until stopped.
func cmdWatch(args []string, stdout, stderr io.Writer) error {
	var c common
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := c.open()
	if err != nil {
		return err
	}
	n, err := e.notifier()
	if err != nil {
		return err
	}
	if n == nil {
		fmt.Fprintln(stderr, "no notification channels in fleet.yaml: problems are only printed")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logs := func(id string) string {
		out, _ := exec.CommandContext(ctx, "docker", "logs", "--tail", "15", id).CombinedOutput() //nolint:gosec // a container ID from Docker
		const maxLen = 1500
		if len(out) > maxLen {
			out = out[len(out)-maxLen:]
		}
		return string(out)
	}
	w := newWatcher(logs)
	cmd := exec.CommandContext(ctx, "docker", "events", "--format", "{{json .}}", "--filter", "type=container",
		"--filter", "event=die", "--filter", "event=oom", "--filter", "event=kill", "--filter", "event=health_status")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("docker events: %w", err)
	}
	fmt.Fprintf(stderr, "watching containers on %s\n", e.host)
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var ev dockerEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		for _, msg := range w.process(ev, time.Now()) {
			fmt.Fprintf(stdout, "%s %s: %s\n", time.Now().Format(time.RFC3339), msg.Name, msg.Title)
			e.emit(n, stdout, msg)
		}
	}
	if err := cmd.Wait(); err != nil && !errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Errorf("docker events: %w", err)
	}
	return nil
}
