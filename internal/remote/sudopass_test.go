// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A root step with a password runs through a real shell: sudo gets the password
// as the first line of its stdin, and runs the step's script as its command.
func TestStepScriptSudoPassword(t *testing.T) {
	dir := t.TempDir()
	// A stand-in sudo: checks the options, compares the password, runs the rest.
	fake := `#!/bin/sh
[ "$1" = -S ] && [ "$2" = -p ] && [ "$3" = "" ] || { echo "unexpected options: $*" >&2; exit 2; }
shift 3
IFS= read -r pw
[ "$pw" = "$WANT" ] || { echo "Sorry, try again." >&2; echo "sudo: 1 incorrect password attempt" >&2; exit 1; }
exec "$@"
`
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte(fake), 0o700); err != nil { //nolint:gosec // a test helper
		t.Fatal(err)
	}
	step := Step{Root: true, Script: "echo \"ran as step: $0\"\n"}
	for _, pw := range []string{"simple", `it's "quoted" $HOME; rm -rf /`, " spaced  "} {
		script := stepScript(step, false, pw)
		if strings.Contains(script, "<<") {
			t.Errorf("the script must not use a heredoc with a password: %q", script)
		}
		cmd := exec.Command("sh", "-s")
		cmd.Stdin = strings.NewReader(script)
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "WANT="+pw)
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "ran as step") {
			t.Errorf("%q: %v: %s", pw, err, out)
		}
	}
	// A wrong password: sudo says so, and the error says it plainly.
	cmd := exec.Command("sh", "-s")
	cmd.Stdin = strings.NewReader(stepScript(step, false, "wrong"))
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "WANT=right")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("a wrong password ran the step")
	}
	if got := sudoError(errors.New(string(out)), "wrong"); !errors.Is(got, ErrSudoPassword) {
		t.Errorf("wrong password: %v", got)
	}
}

func TestSudoErrors(t *testing.T) {
	for msg, want := range map[string]error{
		"sudo: 3 incorrect password attempts":               ErrSudoPassword,
		"ada is not in the sudoers file.":                   ErrNoSudo,
		"sudo: a password is required":                      ErrPasswordNeeded,
		"sudo: a terminal is required to read the password": ErrPasswordNeeded,
	} {
		if got := sudoError(errors.New(msg), "pw"); !errors.Is(got, want) {
			t.Errorf("%q: %v", msg, got)
		}
	}
	// Any other error never repeats the password.
	if got := sudoError(errors.New("bash: line 1: s3cr3t-pw: command not found"), "s3cr3t-pw"); strings.Contains(got.Error(), "s3cr3t-pw") {
		t.Errorf("the password leaked: %v", got)
	}
}

func TestRunBootstrapPassword(t *testing.T) {
	r := &Report{User: "ada", SudoPassword: true}
	steps := []Step{{Text: "root", Root: true, Script: "true\n"}}
	var scripts []string
	run := func(_ context.Context, s string) ([]byte, error) { scripts = append(scripts, s); return nil, nil }
	var log bytes.Buffer
	if err := RunBootstrap(context.Background(), run, r, steps, &log, ""); !errors.Is(err, ErrPasswordNeeded) || len(scripts) != 0 {
		t.Errorf("no password: %v, ran %d", err, len(scripts))
	}
	if err := RunBootstrap(context.Background(), run, r, steps, &log, "a\nb"); err == nil {
		t.Error("a password with a line break was accepted")
	}
	if err := RunBootstrap(context.Background(), run, r, steps, &log, "pw"); err != nil || len(scripts) != 1 || !strings.Contains(scripts[0], "sudo -S") {
		t.Errorf("with a password: %v %q", err, scripts)
	}
	if strings.Contains(log.String(), "pw") && !strings.Contains(log.String(), "1/1 root") {
		t.Errorf("log: %q", log.String())
	}
	// Plans no longer refuse a user whose sudo asks for a password.
	if _, err := BootstrapPlan(&Report{Kernel: "Linux", OSID: "debian", User: "ada", SudoPassword: true}, "nepfleet_ingress"); err != nil {
		t.Errorf("plan with a sudo password: %v", err)
	}
	if _, err := BootstrapPlan(&Report{Kernel: "Linux", OSID: "debian", User: "ada"}, "nepfleet_ingress"); err == nil || !strings.Contains(err.Error(), "needs root") {
		t.Error("a user without sudo must be refused")
	}
}
