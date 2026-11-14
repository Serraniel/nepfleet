// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Serraniel/neptide/pkg/snapshot"

	"github.com/Serraniel/nepfleet/internal/api"
)

// A fleet with stack app (and, if dir isn't empty, a volume-backup stack
// keeping archives in dir), both on this host.
func restoreFleet(t *testing.T, app, dir string) common {
	t.Helper()
	c, fleetDir, _ := rollbackFleet(t)
	write := func(rel, content string) {
		p := filepath.Join(fleetDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("stacks/"+app+"/stack.yaml", "apiVersion: nepfleet/v1alpha1\nkind: Stack\nmetadata: {name: "+app+"}\nspec: {target: local}\n")
	write("stacks/"+app+"/compose.yaml", "services:\n  app:\n    image: busybox:1.37\n    volumes: [data:/data]\nvolumes:\n  data:\n")
	if dir != "" {
		write("stacks/backup/stack.yaml", "apiVersion: nepfleet/v1alpha1\nkind: Stack\nmetadata: {name: backup}\nspec:\n  target: local\n  template: {source: official, name: volume-backup}\n  values:\n    ARCHIVE_PATH: "+dir+"\n")
	}
	tgit(t, fleetDir, "add", "-A")
	tgit(t, fleetDir, "commit", "-qm", "restore", "--no-verify")
	return c
}

func TestArchiveFolder(t *testing.T) {
	c := restoreFleet(t, "app", "")
	e, _ := c.open()
	s, _ := e.fleet.LoadStack("app")
	var invalid api.InvalidError
	if _, err := archiveFolder(e, s); !errors.As(err, &invalid) || !strings.Contains(err.Error(), "enable backups") {
		t.Errorf("no backup stack: %v", err)
	}
	c = restoreFleet(t, "app", "/srv/nf-backups")
	e, _ = c.open()
	s, _ = e.fleet.LoadStack("app")
	if dir, err := archiveFolder(e, s); err != nil || dir != "/srv/nf-backups" {
		t.Errorf("folder: %q %v", dir, err)
	}
	for name, ok := range map[string]bool{
		"backup-2026-11-06T03-00-00.tar.gz.age": true, "backup-2026-11-06T03-00-00.tar.gz": true,
		"backup-x.tar.gz": false, "../backup-2026-11-06T03-00-00.tar.gz": false, "backup-2026-11-06T03-00-00.tar.gz.age.sh": false,
	} {
		if archiveName.MatchString(name) != ok {
			t.Errorf("%s: %v", name, !ok)
		}
	}
}

// The whole restore over the API's path: an encrypted archive in a host
// folder, read through a helper container, into the stack's volume.
func TestRestoreThroughAPIInDocker(t *testing.T) {
	if os.Getenv("NEPFLEET_TEST_DOCKER") == "" {
		t.Skip("set NEPFLEET_TEST_DOCKER to run with Docker")
	}
	rnd := make([]byte, 4)
	_, _ = rand.Read(rnd)
	app := "nfrestore" + hex.EncodeToString(rnd)
	vol := app + "_data"
	docker := func(args ...string) string {
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v %s", args, err, out)
		}
		return string(out)
	}
	docker("volume", "create", "--label", "com.docker.compose.project="+app, "--label", "com.docker.compose.volume=data", vol)
	t.Cleanup(func() {
		out, _ := exec.Command("docker", "volume", "ls", "-q", "--filter", "label=nepfleet.restore.of="+vol).Output()
		for _, v := range append(strings.Fields(string(out)), vol) {
			_ = exec.Command("docker", "volume", "rm", "-f", v).Run()
		}
	})
	// The archive, as docker-volume-backup writes it, encrypted to a key.
	dir := t.TempDir()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("restored\n")
	_ = tw.WriteHeader(&tar.Header{Name: "backup/volumes/" + vol + "/_data/hello.txt", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(body)
	_ = tw.Close()
	_ = gz.Close()
	key := filepath.Join(t.TempDir(), "key.txt")
	if out, err := exec.Command("age-keygen", "-o", key).CombinedOutput(); err != nil {
		t.Skipf("age-keygen: %v %s", err, out)
	}
	t.Setenv("SOPS_AGE_KEY_FILE", key)
	pub, _ := exec.Command("age-keygen", "-y", key).Output()
	name := "backup-2026-11-06T03-00-00.tar.gz.age"
	enc := exec.Command("age", "-r", strings.TrimSpace(string(pub)), "-o", filepath.Join(dir, name))
	enc.Stdin = &buf
	if out, err := enc.CombinedOutput(); err != nil {
		t.Fatalf("age: %v %s", err, out)
	}
	if err := os.Chmod(dir, 0o755); err != nil { //nolint:gosec // the helper container reads it
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, name), 0o644); err != nil { //nolint:gosec // the helper container reads it
		t.Fatal(err)
	}
	c := restoreFleet(t, app, dir)
	b, err := backupsAPI(t.Context(), c, app)
	if err != nil || len(b.Archives) != 1 || b.Archives[0] != name || len(b.Volumes) != 1 {
		t.Fatalf("backups: %+v %v", b, err)
	}
	run, err := startRestore(t.Context(), c, app, name, nil, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	if err := run(&log); err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	got, err := exec.Command("docker", "run", "--rm", "-v", vol+":/v:ro", snapshot.HelperImage, "cat", "/v/hello.txt").Output()
	if err != nil || string(got) != "restored\n" {
		t.Errorf("restored %q\n%s", got, log.String())
	}
}

// A running compose project of this host becomes a committed, adopted stack.
func TestImportComposeAPIInDocker(t *testing.T) {
	if os.Getenv("NEPFLEET_TEST_DOCKER") == "" {
		t.Skip("set NEPFLEET_TEST_DOCKER to run with Docker")
	}
	rnd := make([]byte, 4)
	_, _ = rand.Read(rnd)
	project := "nfimport" + hex.EncodeToString(rnd)
	dir := t.TempDir()
	compose := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(compose, []byte("services:\n  app:\n    image: "+snapshot.HelperImage+"\n    command: [sleep, infinity]\n    environment:\n      GREETING: hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	up := exec.Command("docker", "compose", "-p", project, "-f", compose, "up", "-d")
	if out, err := up.CombinedOutput(); err != nil {
		t.Fatalf("up: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "compose", "-p", project, "-f", compose, "down", "-t", "0").Run() })
	c, fleetDir, _ := rollbackFleet(t)
	ps, err := composeProjectsAPI(t.Context(), c)
	if err != nil || !slices.ContainsFunc(ps, func(p api.ComposeProject) bool { return p.Name == project && !p.Stack }) {
		t.Fatalf("projects: %+v %v", ps, err)
	}
	res, err := importComposeAPI(t.Context(), c, api.ImportCompose{Project: project}, "Ada")
	if err != nil || res.Stack != project || res.Commit == "" || !strings.Contains(res.Log, "adopted") {
		t.Fatalf("%+v %v", res, err)
	}
	if log := tgit(t, fleetDir, "log", "-1", "--format=%an %s"); !strings.Contains(log, "Ada feat("+project+"): import compose project") {
		t.Errorf("commit: %s", log)
	}
	if _, err := importComposeAPI(t.Context(), c, api.ImportCompose{Project: project}, "Ada"); !errors.Is(err, api.ErrStackExists) {
		t.Errorf("again: %v", err)
	}
}
