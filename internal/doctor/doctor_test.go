// SPDX-License-Identifier: AGPL-3.0-or-later

package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeDocker map[string]string

func (f fakeDocker) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	call := strings.Join(args, " ")
	for prefix, out := range f {
		if strings.HasPrefix(call, prefix) {
			if out == "ERR" {
				return nil, errors.New("failed")
			}
			return []byte(out), nil
		}
	}
	return nil, errors.New("unexpected: " + call)
}

func env(docker fakeDocker, files map[string]string) Env {
	return Env{
		Docker:   docker,
		LookPath: func(string) (string, error) { return "/usr/bin/x", nil },
		ReadFile: func(p string) ([]byte, error) {
			if s, ok := files[p]; ok {
				return []byte(s), nil
			}
			return nil, errors.New("no")
		},
		FreeBytes: func(string) (uint64, error) { return 50 << 30, nil },
		Command: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "age-keygen" {
				return []byte("age1fleet\n"), nil
			}
			return []byte("yes\n"), nil
		},
	}
}

const multiArch = `{"manifests":[{"platform":{"architecture":"amd64","os":"linux"}},{"platform":{"architecture":"arm64","os":"linux"}}]}`
const amdOnly = `{"manifests":[{"platform":{"architecture":"amd64","os":"linux"}}]}`

func byName(cs []Check) map[string]Check {
	m := map[string]Check{}
	for _, c := range cs {
		if _, seen := m[c.Name]; !seen || c.Status > m[c.Name].Status {
			m[c.Name] = c
		}
	}
	return m
}

func TestHealthyHost(t *testing.T) {
	d := fakeDocker{
		"info":                 `{"ServerVersion":"29.9.2","Architecture":"x86_64","DockerRootDir":"/var/lib/docker","CgroupVersion":"2"}`,
		"compose version":      "5.7.0\n",
		"manifest inspect":     multiArch,
		"ps --filter publish=": "",
	}
	cs := Run(context.Background(), env(d, map[string]string{"/key": "AGE-SECRET-KEY-1", "/proc/net/tcp": ""}),
		Input{Recipients: []string{"age1fleet"}, AgeKeyFile: "/key", Images: []string{"nginx:1"}, Ingress: true})
	if Failed(cs) {
		t.Fatalf("healthy host failed: %+v", cs)
	}
}

func TestProblemsAreReported(t *testing.T) {
	// :0050 = port 80, state 0A = LISTEN
	tcp := "  sl  local_address rem_address   st\n   0: 00000000:0050 00000000:0000 0A 00000000\n"
	d := fakeDocker{
		"info":                 `{"ServerVersion":"24.0.1","Architecture":"aarch64","DockerRootDir":"/var/lib/docker","CgroupVersion":"2"}`,
		"compose version":      "2.20.0\n",
		"version --format":     "arm64\n",
		"manifest inspect":     amdOnly,
		"ps --filter publish=": "nginx-legacy (nginx:1)\n",
	}
	cs := byName(Run(context.Background(), env(d, map[string]string{"/proc/net/tcp": tcp}),
		Input{Recipients: []string{"age1fleet"}, AgeKeyFile: "/missing", Images: []string{"amd-only:1"}, Ingress: true}))
	for name, want := range map[string]string{
		"Docker":              "24.0.1",
		"Docker Compose":      "2.20.0",
		"age key":             "no private key",
		"Port 80":             "nginx-legacy",
		"Image architectures": "not built for arm64",
	} {
		c := cs[name]
		if c.Status != Fail || !strings.Contains(c.Detail, want) {
			t.Errorf("%s: got %v %q, want failure mentioning %q", name, c.Status, c.Detail, want)
		}
	}
	if cs["Port 443"].Status != OK {
		t.Errorf("443 should be free: %+v", cs["Port 443"])
	}
}

func TestWrongAgeKey(t *testing.T) {
	d := fakeDocker{"info": `{"ServerVersion":"29.0.0","Architecture":"x86_64","DockerRootDir":"/","CgroupVersion":"2"}`, "compose version": "5.0.0"}
	cs := byName(Run(context.Background(), env(d, map[string]string{"/key": "k"}), Input{Recipients: []string{"age1other"}, AgeKeyFile: "/key"}))
	if c := cs["age key"]; c.Status != Fail || !strings.Contains(c.Detail, "not a recipient") {
		t.Errorf("age key: %+v", c)
	}
}

func TestPlatforms(t *testing.T) {
	pi32 := platform{"arm", variant("aarch64", "arm")} // 32-bit userland, 64-bit kernel
	zero := platform{"arm", variant("armv6l", "arm")}
	for _, c := range []struct {
		host    platform
		arch, v string
		want    bool
	}{
		{pi32, "arm", "v7", true}, {pi32, "arm", "v6", true}, {pi32, "arm64", "", false},
		{zero, "arm", "v7", false}, {zero, "arm", "v6", true},
		{platform{"amd64", ""}, "amd64", "", true}, {platform{"arm64", ""}, "arm64", "v8", true},
	} {
		if got := c.host.runs(c.arch, c.v); got != c.want {
			t.Errorf("%s runs %s/%s = %v, want %v", c.host, c.arch, c.v, got, c.want)
		}
	}
}
