// SPDX-License-Identifier: AGPL-3.0-or-later

package deploy

import (
	"context"
	"errors"
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
	// startErr fails the first `up` (no --wait); hang makes it block until
	// its context ends, failing with startErr.
	startErr error
	hang     bool
}

func (f *onceRunner) Run(ctx context.Context, _ string, args ...string) ([]byte, error) {
	call := strings.Join(args, " ")
	f.calls = append(f.calls, call)
	switch {
	case strings.HasSuffix(call, " up --detach --remove-orphans"):
		if f.hang {
			<-ctx.Done()
		}
		return nil, f.startErr
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

func noMask(s string) string { return s }

func withFastPoll(t *testing.T) {
	old := runOncePoll
	runOncePoll = time.Millisecond
	t.Cleanup(func() { runOncePoll = old })
}

func TestUpWaitsForRunOnceService(t *testing.T) {
	withFastPoll(t)
	r := &onceRunner{config: onceConfig, states: []string{ps("running", 0), ps("exited", 0)}}
	if err := upCompose(context.Background(), r, "st", t.TempDir(), time.Minute, noMask); err != nil {
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
	err := upCompose(context.Background(), r, "st", t.TempDir(), time.Minute, noMask)
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
	err := upCompose(context.Background(), r, "st", t.TempDir(), 20*time.Millisecond, noMask)
	if err == nil || !strings.Contains(err.Error(), "run-once service setup did not finish within") {
		t.Fatalf("err = %v", err)
	}
}

func TestUpWithoutRunOnceIsOneCall(t *testing.T) {
	// No explicit restart: "no" (db leaves restart unset): compose's own wait.
	r := &onceRunner{config: `{"services":{"app":{"restart":"always"},"db":{}}}`}
	if err := upCompose(context.Background(), r, "st", t.TempDir(), time.Minute, noMask); err != nil {
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
	if err := upCompose(context.Background(), r, "st", t.TempDir(), time.Minute, noMask); err != nil {
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

func TestUpRunOnceLogIsMasked(t *testing.T) {
	withFastPoll(t)
	r := &onceRunner{config: onceConfig, states: []string{ps("exited", 1)}, logs: "login as admin / hunter2-secret failed\n"}
	mask := func(s string) string { return strings.ReplaceAll(s, "hunter2-secret", "••••••") }
	err := upCompose(context.Background(), r, "st", t.TempDir(), time.Minute, mask)
	if err == nil || strings.Contains(err.Error(), "hunter2") || !strings.Contains(err.Error(), "admin / ••••••") {
		t.Fatalf("err = %v", err)
	}
}

func TestUpRunOnceWithoutContainer(t *testing.T) {
	withFastPoll(t)
	r := &onceRunner{config: onceConfig, states: []string{""}}
	err := upCompose(context.Background(), r, "st", t.TempDir(), time.Minute, noMask)
	if err == nil || !strings.Contains(err.Error(), "run-once service setup has no container") {
		t.Fatalf("err = %v", err)
	}
}

func TestUpStartTimeoutShowsCompose(t *testing.T) {
	r := &onceRunner{config: onceConfig, hang: true, startErr: errors.New("signal: killed: Container st-app-1 Waiting")}
	err := upCompose(context.Background(), r, "st", t.TempDir(), 20*time.Millisecond, noMask)
	if err == nil || !strings.Contains(err.Error(), "st did not start within") || !strings.Contains(err.Error(), "Container st-app-1 Waiting") {
		t.Fatalf("err = %v", err)
	}
}

// A setup that app waits for with service_completed_successfully counts as
// run-once even without restart: "no", and when it fails compose's first up
// fails: the error carries the setup's log.
func TestUpCompletedDependencyFailureShowsLog(t *testing.T) {
	cfg := `{"services":{
	  "app":{"depends_on":{"setup":{"condition":"service_completed_successfully","required":true}}},
	  "setup":{}}}`
	r := &onceRunner{config: cfg, states: []string{ps("exited", 2)}, logs: "seed: table missing\n",
		startErr: errors.New("service \"setup\" didn't complete successfully: exit 2")}
	once, rest, err := runOnceServices(context.Background(), r, "st", t.TempDir())
	if err != nil || !slices.Equal(once, []string{"setup"}) || !slices.Equal(rest, []string{"app"}) {
		t.Fatalf("once %v, rest %v, %v", once, rest, err)
	}
	err = upCompose(context.Background(), r, "st", t.TempDir(), time.Minute, noMask)
	for _, want := range []string{"didn't complete successfully", "run-once service setup exited with code 2", "seed: table missing"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, lacks %q", err, want)
		}
	}
}
