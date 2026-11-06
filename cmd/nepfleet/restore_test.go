// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"maps"
	"slices"
	"testing"
)

func TestArchiveVolumes(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range []string{
		"/backup", "/backup/volumes", "/backup/volumes/app_db", "/backup/volumes/app_db/_data", "/backup/volumes/app_db/_data/x",
		"/backup/volumes/app_data/_data/a/b", "/backup/volumes/other_x/_data", "/backup/volumes/app_bad/notdata", "/etc/passwd",
	} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := archiveVolumes(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"app_data", "app_db", "other_x"}; !slices.Equal(slices.Sorted(maps.Keys(got)), want) {
		t.Errorf("volumes = %v, want %v", got, want)
	}
	if got["app_db"] != "/backup/volumes/app_db/_data" {
		t.Errorf("member name = %q", got["app_db"])
	}
	if _, err := archiveVolumes(bytes.NewReader([]byte("not gzip"))); err == nil {
		t.Error("a non-gzip archive must fail")
	}
}
