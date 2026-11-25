// SPDX-License-Identifier: AGPL-3.0-or-later

package deploy

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// onceRunner answers compose config with its services and compose ps with
// the next of its states for the run-once service.
type onceRunner struct {
	config string
	states []string // ps output per poll; the last one repeats
	logs   string
	calls  []string
}

func (f *onceRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	call := strings.Join(args, " ")
	f.calls = append(f.calls, call)
	switch {
	case strings.Contains(call, " config --format json"):
		return []byte(f.config), nil
	case strings.Contains(call, " ps --all --format json"):
		s := f.states[0]
		if len(f.states) > 1 {
			f.states = f.states[1:]
		}
		return []byte(s), nil
	case strings.Contains(call, " logs "):
		return []byte(f.logs), nil
	}
	return nil, nil
}

func (f *onceRunner) composeCalls() []string {
	var out []string
	for _, c := range f.calls {
		_, rest, _ := strings.Cut(c, ".env ")
		out = append(out, rest)
	}
	return out
}

const onceConfig = `{"name":"st","services":{
  "app":{"image":"app:1","restart":"unless-stopped"},
  "db":{"image":"db:1"},
  "setup":{"image":"app:1","restart":"no"}}}`

func ps(state string, code int) string {
	return `{"Name":"st-setup-1","Service":"setup","State":"` + state + `","ExitCode":` + strconv.Itoa(code) + "}\n"
}

func withFastPoll(t *testing.T) {
	old := runOncePoll
	runOncePoll = time.Millisecond
	t.Cleanup(func() { runOncePoll = old })
}

func TestUpWaitsForRunOnceService(t *testing.T) {
	withFastPoll(t)
	r := &onceRunner{config: onceConfig, states: []string{ps("running", 0), ps("exited", 0)}}
	if err := upCompose(context.Background(), r, "st", t.TempDir(), time.Minute); err != nil {
		t.Fatal(err)
	}
	got := r.composeCalls()
	want := []string{
		"config --format json",
		"up --detach --remove-orphans",
		"up --detach --no-deps --wait --wait-timeout 60 app db",
		"ps --all --format json setup",
		"ps --all --format json setup",
	}
	if len(got) != len(want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	// The wait timeout is what is left of the minute: 59 or 60.
	got[2] = strings.Replace(got[2], "--wait-timeout 59", "--wait-timeout 60", 1)
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestUpRunOnceFailureShowsLog(t *testing.T) {
	withFastPoll(t)
	r := &onceRunner{config: onceConfig, states: []string{ps("exited", 3)}, logs: "connecting\nadmin: password too short\n"}
	err := upCompose(context.Background(), r, "st", t.TempDir(), time.Minute)
	if err == nil {
		t.Fatal("a run-once service that exited 3 must fail the apply")
	}
	for _, want := range []string{"run-once service setup exited with code 3", "admin: password too short"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if !slices.Contains(r.composeCalls(), "logs --no-log-prefix --tail 10 setup") {
		t.Errorf("no logs call: %q", r.composeCalls())
	}
}

func TestUpRunOnceTimeout(t *testing.T) {
	withFastPoll(t)
	r := &onceRunner{config: onceConfig, states: []string{ps("running", 0)}}
	err := upCompose(context.Background(), r, "st", t.TempDir(), 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "run-once service setup did not finish within") {
		t.Fatalf("err = %v", err)
	}
}

func TestUpWithoutRunOnceIsOneCall(t *testing.T) {
	// No explicit restart: "no" (db leaves restart unset): compose's own wait.
	r := &onceRunner{config: `{"services":{"app":{"restart":"always"},"db":{}}}`}
	if err := upCompose(context.Background(), r, "st", t.TempDir(), time.Minute); err != nil {
		t.Fatal(err)
	}
	want := []string{"config --format json", "up --detach --remove-orphans --wait --wait-timeout 60"}
	if got := r.composeCalls(); !slices.Equal(got, want) {
		t.Errorf("calls = %q, want %q", got, want)
	}
}

func TestUpOnlyRunOnce(t *testing.T) {
	// A stack of only run-once services (binfmt): nothing for compose to wait on.
	withFastPoll(t)
	r := &onceRunner{config: `{"services":{"setup":{"restart":"no"}}}`, states: []string{ps("exited", 0)}}
	if err := upCompose(context.Background(), r, "st", t.TempDir(), time.Minute); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.composeCalls() {
		if strings.Contains(c, "--wait") {
			t.Errorf("unexpected wait: %q", c)
		}
	}
}

func TestParsePS(t *testing.T) {
	for _, in := range []string{
		`[{"Name":"a","Service":"s","State":"exited","ExitCode":1}]`,
		`{"Name":"a","Service":"s","State":"exited","ExitCode":1}` + "\n",
	} {
		got, err := parsePS([]byte(in))
		if err != nil || len(got) != 1 || got[0].Service != "s" || got[0].ExitCode != 1 {
			t.Errorf("parsePS(%s) = %+v, %v", in, got, err)
		}
	}
	if got, err := parsePS(nil); err != nil || len(got) != 0 {
		t.Errorf("parsePS(empty) = %+v, %v", got, err)
	}
}
