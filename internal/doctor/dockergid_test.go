// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build unix

package doctor

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestDockerGID(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "var/run"), 0o750); err != nil {
		t.Fatal(err)
	}
	// A plain file stands in for the socket: only its group matters.
	if err := os.WriteFile(filepath.Join(root, "var/run/docker.sock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	want := strconv.Itoa(os.Getgid())

	t.Setenv("DOCKER_HOST", "")
	if got := DockerGID(root); got != want {
		t.Errorf("default socket: got %q, want %q", got, want)
	}
	if got := DockerGID(t.TempDir()); got != "" {
		t.Errorf("no socket: got %q, want empty", got)
	}

	// DOCKER_HOST names another socket: that one counts.
	other := filepath.Join(t.TempDir(), "docker.sock")
	if err := os.WriteFile(other, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_HOST", "unix://"+other)
	if got := DockerGID("/"); got != want {
		t.Errorf("DOCKER_HOST socket: got %q, want %q", got, want)
	}
	// A TCP DOCKER_HOST (a socket proxy) has no file: fall back to the default.
	t.Setenv("DOCKER_HOST", "tcp://socket-proxy:2375")
	if got := DockerGID(root); got != want {
		t.Errorf("tcp DOCKER_HOST: got %q, want %q", got, want)
	}
}
