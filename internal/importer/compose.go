// SPDX-License-Identifier: AGPL-3.0-or-later

// Package importer adopts existing deployments into the fleet repository
// without touching them: the first apply after an import must not recreate
// a single container.
package importer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/nepfleet/internal/deploy"
	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/secrets"
)

// Project is a Compose project found on this host.
type Project struct {
	Name        string
	Status      string
	ConfigFiles []string
	WorkingDir  string
	EnvFiles    []string
}

// ListCompose lists the Compose projects the local daemon knows, running or not.
func ListCompose(ctx context.Context, r deploy.Runner) ([]Project, error) {
	out, err := r.Run(ctx, "", "compose", "ls", "--all", "--format", "json")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Name, Status, ConfigFiles string
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("docker compose ls: %w", err)
	}
	var projects []Project
	for _, p := range raw {
		proj := Project{Name: p.Name, Status: p.Status, ConfigFiles: splitList(p.ConfigFiles)}
		labels, err := r.Run(ctx, "", "ps", "--all", "--filter", "label=com.docker.compose.project="+p.Name, "--format",
			`{{.Label "com.docker.compose.project.working_dir"}}|{{.Label "com.docker.compose.project.environment_file"}}`)
		if err == nil {
			first, _, _ := strings.Cut(strings.TrimSpace(string(labels)), "\n")
			wd, envs, _ := strings.Cut(first, "|")
			proj.WorkingDir, proj.EnvFiles = wd, splitList(envs)
		}
		if proj.WorkingDir == "" && len(proj.ConfigFiles) > 0 {
			proj.WorkingDir = filepath.Dir(proj.ConfigFiles[0])
		}
		if len(proj.EnvFiles) == 0 && proj.WorkingDir != "" {
			if _, err := os.Stat(filepath.Join(proj.WorkingDir, ".env")); err == nil {
				proj.EnvFiles = []string{filepath.Join(proj.WorkingDir, ".env")}
			}
		}
		projects = append(projects, proj)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Name < projects[j].Name })
	return projects, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Result describes one import.
type Result struct {
	Stack    string
	Dir      string
	Values   []string // variable names stored in stack.yaml
	Secrets  []string // variable names stored encrypted
	Warnings []string
}

// secretName guesses which variables hold secrets. It errs on the side of
// encrypting: a value encrypted needlessly costs nothing.
var secretName = regexp.MustCompile(`(?i)(PASS|SECRET|TOKEN|KEY|PRIVATE|CREDENTIAL|AUTH|SALT|HASH|CERT|DSN|_URI$|_URL$)`)

// ImportCompose writes stacks/<project>/ from a running Compose project.
func ImportCompose(ctx context.Context, r deploy.Runner, f *fleet.Fleet, store secrets.Store, p Project, target string) (*Result, error) {
	if len(p.ConfigFiles) == 0 {
		return nil, fmt.Errorf("%s: compose did not report its config files", p.Name)
	}
	dir := filepath.Join(f.Dir, "stacks", p.Name)
	if _, err := os.Stat(dir); err == nil {
		return nil, fmt.Errorf("%s already exists", dir)
	}
	res := &Result{Stack: p.Name, Dir: dir}

	// Normalised, but not interpolated: relative paths become absolute (so
	// bind mounts keep pointing at the existing data) and ${VAR} stays.
	args := []string{"compose", "--project-name", p.Name}
	if p.WorkingDir != "" {
		args = append(args, "--project-directory", p.WorkingDir)
	}
	for _, cf := range p.ConfigFiles {
		args = append(args, "--file", cf)
	}
	for _, ef := range p.EnvFiles {
		args = append(args, "--env-file", ef)
	}
	cfg, err := r.Run(ctx, p.WorkingDir, append(args, "config", "--no-interpolate")...)
	if err != nil {
		return nil, err
	}

	env := map[string]string{}
	for _, ef := range p.EnvFiles {
		vars, err := parseEnvFile(ef)
		if err != nil {
			return nil, err
		}
		for k, v := range vars {
			env[k] = v
		}
	}
	res.Warnings = append(res.Warnings, inspect(cfg, env)...)

	values, sec := map[string]string{}, map[string]string{}
	for k, v := range env {
		if strings.ContainsAny(v, "'\n\r") {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s contains a single quote or newline and cannot be written to an env file literally; change it before applying", k))
		}
		if secretName.MatchString(k) {
			sec[k] = v
			res.Secrets = append(res.Secrets, k)
		} else {
			values[k] = v
			res.Values = append(res.Values, k)
		}
	}
	sort.Strings(res.Values)
	sort.Strings(res.Secrets)

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	header := fmt.Sprintf("# Imported by NepFleet on %s from %s\n", time.Now().UTC().Format("2006-01-02"), strings.Join(p.ConfigFiles, ", "))
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), append([]byte(header), cfg...), 0o600); err != nil {
		return nil, err
	}
	stack := map[string]any{
		"apiVersion": "nepfleet/v1alpha1",
		"kind":       "Stack",
		"metadata":   map[string]string{"name": p.Name},
		"spec": map[string]any{
			"target": target,
			"mode":   "compose",
			"apply":  "manual",
		},
	}
	if len(values) > 0 {
		stack["spec"].(map[string]any)["values"] = values
	}
	raw, err := yaml.Marshal(stack)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "stack.yaml"), raw, 0o600); err != nil {
		return nil, err
	}
	if len(sec) > 0 {
		if err := store.Save(filepath.Join(dir, fleet.SecretsFile), sec); err != nil {
			_ = os.RemoveAll(dir)
			return nil, fmt.Errorf("encrypting secrets (nothing was imported): %w", err)
		}
	}
	return res, nil
}

var varRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)([:?+-][^}]*)?\}`)

// inspect warns about things an import cannot carry into the fleet repository.
func inspect(cfg []byte, env map[string]string) []string {
	var warn []string
	var doc struct {
		Services map[string]struct {
			Build   any `yaml:"build"`
			EnvFile []struct {
				Path string `yaml:"path"`
			} `yaml:"env_file"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(cfg, &doc); err == nil {
		names := make([]string, 0, len(doc.Services))
		for n := range doc.Services {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			s := doc.Services[n]
			if s.Build != nil {
				warn = append(warn, fmt.Sprintf("service %s is built locally; the build context stays outside the fleet repository", n))
			}
			for _, ef := range s.EnvFile {
				warn = append(warn, fmt.Sprintf("service %s reads %s, which is not imported (secrets in it stay unencrypted on disk)", n, ef.Path))
			}
		}
	}
	seen := map[string]bool{}
	for _, m := range varRef.FindAllSubmatch(cfg, -1) {
		name, hasDefault := string(m[1]), len(m[2]) > 0
		if _, ok := env[name]; !ok && !hasDefault && !seen[name] {
			seen[name] = true
			warn = append(warn, fmt.Sprintf("${%s} has no value in the project's env files (it may have come from the shell); add it to values or secrets", name))
		}
	}
	return warn
}

// parseEnvFile reads KEY=VALUE lines the way Compose does for the common
// cases: comments, export prefix, single quotes (literal), double quotes
// (with \n, \", \\ escapes).
func parseEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("%s:%d: not KEY=VALUE", path, n)
		}
		v = strings.TrimSpace(v)
		switch {
		case len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'':
			v = v[1 : len(v)-1]
		case len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"':
			v = strings.NewReplacer(`\n`, "\n", `\"`, `"`, `\\`, `\`).Replace(v[1 : len(v)-1])
		default:
			if i := strings.Index(v, " #"); i >= 0 {
				v = strings.TrimSpace(v[:i])
			}
		}
		out[k] = v
	}
	return out, errors.Join(sc.Err())
}
