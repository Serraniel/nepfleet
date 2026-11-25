// SPDX-License-Identifier: AGPL-3.0-or-later

package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Serraniel/neptide/pkg/snapshot"
)

// recordingDocker answers like fakeDocker and remembers every call.
type recordingDocker struct {
	fakeDocker
	calls []string
}

func (r *recordingDocker) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, strings.Join(args, " "))
	return r.fakeDocker.Run(ctx, dir, args...)
}

const selfID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

const mountinfo = "1 0 0:1 / / rw - overlay overlay rw\n" +
	"2 1 8:1 /var/lib/docker/containers/" + selfID + "/hostname /etc/hostname rw - ext4 /dev/sda1 rw\n"

const dfOut = "Filesystem     1024-blocks     Used Available Capacity Mounted on\n/dev/sda1        100000000 50000000  52428800      49% /probe\n"

func TestDiskInContainerProbesWithOwnImage(t *testing.T) {
	d := &recordingDocker{fakeDocker: fakeDocker{
		"inspect --format {{.Image}} " + selfID: "sha256:self\n",
		"run --rm":                              dfOut,
	}}
	e := env(nil, map[string]string{"/proc/self/mountinfo": mountinfo})
	e.Docker, e.InContainer = d, true
	e.FreeBytes = func(string) (uint64, error) { return 0, errors.New("no such file or directory") }

	c := diskCheck(context.Background(), e, "/var/lib/docker")
	if c.Status != OK || c.Detail != "50.0 GiB free under /var/lib/docker" {
		t.Errorf("disk: %+v", c)
	}
	run := d.calls[len(d.calls)-1]
	for _, want := range []string{"--pull never", "--network none", "-v /var/lib/docker:/probe:ro", "--entrypoint df sha256:self -Pk /probe"} {
		if !strings.Contains(run, want) {
			t.Errorf("probe %q lacks %q", run, want)
		}
	}
}

func TestDiskProbeFallsBackToHelperImage(t *testing.T) {
	d := &recordingDocker{fakeDocker: fakeDocker{
		"run --rm": "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 10000000 9000000 1048576 90% /probe\n",
	}}
	e := env(nil, nil)
	e.Docker, e.InContainer = d, true // no mountinfo: own image unknown
	e.FreeBytes = func(string) (uint64, error) { return 0, errors.New("no such file or directory") }

	c := diskCheck(context.Background(), e, "/srv/docker")
	if c.Status != Fail || !strings.Contains(c.Detail, "1.0 GiB free under /srv/docker") {
		t.Errorf("disk: %+v", c)
	}
	if len(d.calls) != 1 || !strings.Contains(d.calls[0], snapshot.HelperImage) {
		t.Errorf("calls: %q", d.calls)
	}
}

func TestDiskNotCheckedIsInformational(t *testing.T) {
	d := &recordingDocker{fakeDocker: fakeDocker{
		"inspect --format": "sha256:self\n",
		"run --rm":         "ERR", // image not there, or the mount is refused
	}}
	e := env(nil, map[string]string{"/proc/self/mountinfo": mountinfo})
	e.Docker, e.InContainer = d, true
	e.FreeBytes = func(string) (uint64, error) { return 0, errors.New("no such file or directory") }

	c := diskCheck(context.Background(), e, "/var/lib/docker")
	if c.Status != OK || !strings.HasPrefix(c.Detail, "not checked") || !strings.Contains(c.Fix, "df -h /var/lib/docker") {
		t.Errorf("disk: %+v", c)
	}
	if len(d.calls) != 3 { // inspect, own image, helper image
		t.Errorf("calls: %q", d.calls)
	}
}

func TestDiskOnHostNeedsNoProbe(t *testing.T) {
	d := &recordingDocker{fakeDocker: fakeDocker{}}
	e := env(nil, nil)
	e.Docker = d
	if c := diskCheck(context.Background(), e, "/var/lib/docker"); c.Status != OK || !strings.Contains(c.Detail, "50.0 GiB") {
		t.Errorf("disk: %+v", c)
	}
	if len(d.calls) != 0 {
		t.Errorf("calls: %q", d.calls)
	}
}

func TestParseDF(t *testing.T) {
	for _, bad := range []string{"", "Filesystem\n", "h\n/dev/x 1 2\n", "h\n/dev/x 1 2 lots 3% /p\n"} {
		if _, err := parseDF([]byte(bad)); err == nil {
			t.Errorf("parseDF(%q) accepted", bad)
		}
	}
}
