// SPDX-License-Identifier: AGPL-3.0-or-later

package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Portainer reads stacks through the Portainer API with an access token
// (My account → Access tokens). Its compose files live in Portainer's data
// volume, at paths only the Portainer container sees, so `import compose`
// cannot read them from the host.
type Portainer struct {
	URL   string // e.g. https://portainer.example.org:9443
	Token string
	HTTP  *http.Client
}

// PortainerStack is one stack as the API lists it.
type PortainerStack struct {
	ID         int    `json:"Id"`
	Name       string `json:"Name"`
	Type       int    `json:"Type"` // 1 swarm, 2 compose, 3 kubernetes
	EndpointID int    `json:"EndpointId"`
	Status     int    `json:"Status"` // 1 active, 2 inactive
	Env        []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"Env"`
}

// Kind describes the stack type for listings.
func (s PortainerStack) Kind() string {
	switch s.Type {
	case 1:
		return "swarm"
	case 2:
		return "compose"
	case 3:
		return "kubernetes"
	}
	return fmt.Sprintf("type %d", s.Type)
}

func (p Portainer) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(p.URL, "/")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", p.Token)
	client := p.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return errors.New("Portainer refused the access token (create one under My account → Access tokens)")
	}
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("GET %s: HTTP %d: %s", path, res.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// Stacks lists every stack the token may see, sorted by name.
func (p Portainer) Stacks(ctx context.Context) ([]PortainerStack, error) {
	var out []PortainerStack
	if err := p.get(ctx, "/api/stacks", &out); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// File returns a stack's compose file.
func (p Portainer) File(ctx context.Context, id int) (string, error) {
	var f struct {
		StackFileContent string `json:"StackFileContent"`
	}
	err := p.get(ctx, fmt.Sprintf("/api/stacks/%d/file", id), &f)
	return f.StackFileContent, err
}

// relativeBind matches relative paths in a compose file: bind mount sources
// (- ./x:/y, ["./x:/y"], source: ./x), build contexts and env files.
var relativeBind = regexp.MustCompile(`(^|[\s\[,'":])\.{1,2}/`)

// StageProject writes a stack's compose file and env to dir and returns it
// as a Project for ImportCompose. dataDir is where Portainer's data volume
// is on this host; relative paths in the stack pointed into it.
func StageProject(dir string, s PortainerStack, content, dataDir string) (Project, Options, []string, error) {
	var warnings []string
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(content), 0o600); err != nil {
		return Project{}, Options{}, nil, err
	}
	var env strings.Builder
	for _, e := range s.Env {
		if strings.ContainsAny(e.Value, "\n\r") {
			warnings = append(warnings, fmt.Sprintf("%s contains a newline; check its imported value", e.Name))
		}
		fmt.Fprintf(&env, "%s=%s\n", e.Name, envQuote(e.Value))
	}
	if err := os.WriteFile(filepath.Join(dir, "stack.env"), []byte(env.String()), 0o600); err != nil {
		return Project{}, Options{}, nil, err
	}
	project := filepath.Join(dataDir, "compose", fmt.Sprint(s.ID))
	if relativeBind.MatchString(content) {
		warnings = append(warnings, fmt.Sprintf("relative bind mounts pointed into Portainer's data volume; they now point at %s; check that this is where the data is on this host", project))
	}
	o := Options{Source: fmt.Sprintf("Portainer stack %s (id %d)", s.Name, s.ID), RewriteDir: [2]string{dir, project}}
	if s.Type == 1 {
		o.Mode = "swarm"
	}
	return Project{Name: s.Name, ConfigFiles: []string{filepath.Join(dir, "docker-compose.yml")}, WorkingDir: dir,
		EnvFiles: []string{filepath.Join(dir, "stack.env")}}, o, warnings, nil
}

// ValidPortainerURL accepts https, and http only on loopback.
func ValidPortainerURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("--url %q: want https://host[:port]", raw)
	}
	if u.Scheme == "https" {
		return nil
	}
	h := u.Hostname()
	if u.Scheme == "http" && (h == "localhost" || h == "127.0.0.1" || h == "::1") {
		return nil
	}
	return errors.New("--url must use https (plain http only on localhost): the token would travel in clear text")
}

// envQuote writes a value so the env file parser reads it back literally,
// as Portainer passed it: single quotes keep $ and # as they are; a value
// with a single quote goes in double quotes with \, ", $ and newlines escaped.
func envQuote(v string) string {
	if !strings.Contains(v, "'") {
		return "'" + v + "'"
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", "$$", "\n", `\n`)
	return `"` + r.Replace(v) + `"`
}
