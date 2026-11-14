// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestKeepGoingWhenOutputCloses(t *testing.T) {
	if keepGoingWhenOutputCloses([]string{"plan"}) || keepGoingWhenOutputCloses(nil) {
		t.Error("read-only commands must still end on a closed pipe")
	}
}

// A changing command whose output pipe closes runs on to its end.
func TestChangingCommandOutlivesClosedPipe(t *testing.T) {
	if os.Getenv("NF_SIGPIPE_CHILD") == "1" {
		keepGoingWhenOutputCloses([]string{"apply"})
		for range 2000 {
			_, _ = os.Stdout.WriteString(strings.Repeat("x", 1024) + "\n")
		}
		_ = os.WriteFile(os.Getenv("NF_SIGPIPE_DONE"), []byte("done"), 0o600)
		os.Exit(0)
	}
	done := t.TempDir() + "/done"
	cmd := exec.Command(os.Args[0], "-test.run=^TestChangingCommandOutlivesClosedPipe$")
	cmd.Env = append(os.Environ(), "NF_SIGPIPE_CHILD=1", "NF_SIGPIPE_DONE="+done)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = out.Close() // nobody reads: like apply | head after head ended
	_ = cmd.Wait()
	if _, err := os.Stat(done); err != nil {
		t.Error("the command stopped when its output closed")
	}
}
