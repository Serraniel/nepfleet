// SPDX-License-Identifier: AGPL-3.0-or-later

package render

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/template"
)

func setup(t *testing.T) (*fleet.Fleet, *template.Template) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "app")
	must(t, os.MkdirAll(filepath.Join(dir, "files"), 0o750))
	write(t, filepath.Join(dir, "template.yaml"), `apiVersion: nepfleet/v1alpha1
kind: Template
metadata: {name: app, title: App, description: d, tier: app}
spec:
  version: 1.0.0
  compose: compose.yaml
  images: [{name: nginx, track: minor}]
  inputs:
    - {name: DOMAIN, type: hostname, required: true}
    - {name: MODE, type: enum, options: [a, b], default: a}
    - {name: ADMIN_TOKEN, type: secret}
  generate:
    - {name: KEY, kind: secret, length: 32, encoding: hex}
  files:
    - {src: files/app.conf, dest: conf/app.conf}
`)
	write(t, filepath.Join(dir, "compose.yaml"), "services:\n  web:\n    image: nginx:1.29\n    environment: {D: '${DOMAIN}', K: '${KEY}', M: '${MODE}', T: '${ADMIN_TOKEN}'}\n")
	write(t, filepath.Join(dir, "files/app.conf"), "server ${DOMAIN};\nliteral $${DOMAIN};\nkeep {$CADDY_STYLE}\n")
	tpl, err := template.Load(dir)
	must(t, err)
	must(t, tpl.Validate())
	f := &fleet.Fleet{}
	f.Spec.IngressNetwork, f.Spec.TZ = "ing", "Europe/Berlin"
	return f, tpl
}

func stack(values map[string]string) *fleet.Stack {
	s := &fleet.Stack{}
	s.Metadata.Name = "myapp"
	s.Spec.Values = fleet.Literals(values)
	return s
}

func TestRender(t *testing.T) {
	f, tpl := setup(t)
	out, err := Render(Input{Fleet: f, Stack: stack(map[string]string{"DOMAIN": "app.example.org"}), Template: tpl,
		Secrets: map[string]string{"ADMIN_TOKEN": "$argon2id$x"}})
	must(t, err)
	env := string(out.Files[EnvFile])
	for _, want := range []string{"DOMAIN='app.example.org'", "MODE='a'", "ADMIN_TOKEN='$argon2id$x'", "NEPFLEET_STACK='myapp'", "TZ='Europe/Berlin'", "NEPFLEET_INGRESS_NETWORK='ing'"} {
		if !strings.Contains(env, want) {
			t.Errorf(".env lacks %s:\n%s", want, env)
		}
	}
	if len(out.Generated["KEY"]) != 32 || !strings.Contains(env, "KEY='"+out.Generated["KEY"]+"'") {
		t.Errorf("generated KEY missing: %v", out.Generated)
	}
	if conf := string(out.Files["conf/app.conf"]); conf != "server app.example.org;\nliteral ${DOMAIN};\nkeep {$CADDY_STYLE}\n" {
		t.Errorf("app.conf:\n%s", conf)
	}
	if m := string(out.Masked(EnvFile)); strings.Contains(m, "argon2id") || strings.Contains(m, out.Generated["KEY"]) {
		t.Errorf("secrets not masked:\n%s", m)
	}
}

func TestGeneratedValuesAreStable(t *testing.T) {
	f, tpl := setup(t)
	out, err := Render(Input{Fleet: f, Stack: stack(map[string]string{"DOMAIN": "a.b"}), Template: tpl, Secrets: map[string]string{"KEY": "existing"}})
	must(t, err)
	if len(out.Generated) != 0 || !strings.Contains(string(out.Files[EnvFile]), "KEY='existing'") {
		t.Fatalf("existing secret was regenerated: %v", out.Generated)
	}
}

func TestRenderRejects(t *testing.T) {
	f, tpl := setup(t)
	for name, tc := range map[string]struct {
		values  map[string]string
		secrets map[string]string
		want    string
	}{
		"missing required": {map[string]string{}, nil, "DOMAIN is required"},
		"bad hostname":     {map[string]string{"DOMAIN": "not a host"}, nil, "not a host name"},
		"bad enum":         {map[string]string{"DOMAIN": "a.b", "MODE": "c"}, nil, "must be one of"},
		"typo":             {map[string]string{"DOMAIN": "a.b", "DOMIAN": "x"}, nil, "DOMIAN is not an input"},
		"secret in values": {map[string]string{"DOMAIN": "a.b", "ADMIN_TOKEN": "x"}, nil, "is a secret"},
		"unrepresentable":  {map[string]string{"DOMAIN": "a.b"}, map[string]string{"ADMIN_TOKEN": "it's"}, "single quote"},
	} {
		_, err := Render(Input{Fleet: f, Stack: stack(tc.values), Template: tpl, Secrets: tc.secrets})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want error containing %q", name, err, tc.want)
		}
	}
}

func TestForSwarm(t *testing.T) {
	in := []byte(`services:
  web:
    image: nginx
    labels:
      caddy: app.example.org # route
      caddy.reverse_proxy: "{{upstreams 80}}"
      com.example.keep: "yes"
      docker-volume-backup.stop-during-backup: "true"
      nepfleet.metrics.port: "9100"
    deploy:
      replicas: 2
  worker:
    image: busybox
    labels: ["kuma.w.http.url=https://x", "other=1"]
`)
	out, err := ForSwarm(in)
	must(t, err)
	s := string(out)
	for _, want := range []string{"replicas: 2", "com.example.keep", "# route"} {
		if !strings.Contains(s, want) {
			t.Errorf("lost %q:\n%s", want, s)
		}
	}
	webDeploy := s[strings.Index(s, "deploy:"):strings.Index(s, "worker:")]
	if !strings.Contains(webDeploy, "nepfleet.metrics.port") {
		t.Errorf("metrics label not moved to deploy.labels:\n%s", s)
	}
	if !strings.Contains(webDeploy, "docker-volume-backup.stop-during-backup") {
		t.Errorf("backup label not moved to deploy.labels:\n%s", s)
	}
	if !strings.Contains(webDeploy, "caddy: app.example.org") || !strings.Contains(webDeploy, "caddy.reverse_proxy") {
		t.Errorf("caddy labels not moved to deploy.labels:\n%s", s)
	}
	if strings.Count(s, "caddy: app.example.org") != 1 {
		t.Errorf("label duplicated:\n%s", s)
	}
	if !strings.Contains(s[strings.Index(s, "worker:"):], "kuma.w.http.url") {
		t.Errorf("list labels not handled:\n%s", s)
	}
}

func write(t *testing.T, path, s string) {
	t.Helper()
	must(t, os.WriteFile(path, []byte(s), 0o600))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestRenderRefs(t *testing.T) {
	f, tpl := setup(t)
	s := stack(map[string]string{})
	s.Spec.Values = map[string]fleet.Value{
		"DOMAIN":      {From: "web", Value: "DOMAIN"},
		"ADMIN_TOKEN": {From: "auth", Secret: "TOKEN"},
	}
	refs := map[string]string{"DOMAIN": "app.example.org", "ADMIN_TOKEN": "tok3n"}
	out, err := Render(Input{Fleet: f, Stack: s, Template: tpl, Refs: refs})
	must(t, err)
	env := EnvValues(out.Files[EnvFile])
	if env["DOMAIN"] != "app.example.org" || env["ADMIN_TOKEN"] != "tok3n" {
		t.Errorf("env = %v", env)
	}
	if !out.Secret["ADMIN_TOKEN"] || out.Secret["DOMAIN"] {
		t.Errorf("masking: %v", out.Secret)
	}
	if masked := string(out.Masked(EnvFile)); strings.Contains(masked, "tok3n") {
		t.Errorf("a referenced secret shows in the plan:\n%s", masked)
	}
	// A value that reached a secret through a chain is masked too.
	s.Spec.Values["DOMAIN"] = fleet.Value{From: "web", Value: "DOMAIN"}
	out, err = Render(Input{Fleet: f, Stack: s, Template: tpl, Refs: refs, RefSecret: map[string]bool{"DOMAIN": true}})
	must(t, err)
	if !out.Secret["DOMAIN"] {
		t.Error("a value from a secret chain must be masked")
	}
	// Referenced values are validated like typed ones.
	if _, err := Render(Input{Fleet: f, Stack: s, Template: tpl, Refs: map[string]string{"DOMAIN": "not a host", "ADMIN_TOKEN": "x"}}); err == nil {
		t.Error("an invalid referenced hostname must fail")
	}
	if _, err := Render(Input{Fleet: f, Stack: s, Template: tpl}); err == nil || !strings.Contains(err.Error(), "not resolved") {
		t.Errorf("unresolved: %v", err)
	}
}

func TestEmptyTypedValueTakesDefault(t *testing.T) {
	f, tpl := setup(t)
	out, err := Render(Input{Fleet: f, Stack: stack(map[string]string{"DOMAIN": "app.example.org", "MODE": ""}), Template: tpl})
	must(t, err)
	if got := EnvValues(out.Files[EnvFile])["MODE"]; got != "a" {
		t.Errorf("MODE = %q, want the default a", got)
	}
	if _, err := Render(Input{Fleet: f, Stack: stack(map[string]string{"DOMAIN": ""}), Template: tpl}); err == nil || !strings.Contains(err.Error(), "DOMAIN is required") {
		t.Errorf("an empty required host name: %v", err)
	}
}

func TestBackupBuiltins(t *testing.T) {
	f, tpl := setup(t)
	f.Spec.Secrets.Age = []string{"age1aaa", "age1bbb"}
	out, err := Render(Input{Fleet: f, Stack: stack(map[string]string{"DOMAIN": "app.example.org"}), Template: tpl, NoBackupVolumes: []string{"immich_model-cache", "ollama_models.v1"}})
	must(t, err)
	env := EnvValues(out.Files[EnvFile])
	if env["NEPFLEET_AGE_RECIPIENTS"] != "age1aaa,age1bbb" {
		t.Errorf("recipients = %q", env["NEPFLEET_AGE_RECIPIENTS"])
	}
	re := regexp.MustCompile("/backup/volumes/(" + env["NEPFLEET_NO_BACKUP_VOLUMES"] + ")/")
	for path, want := range map[string]bool{
		"/backup/volumes/immich_model-cache/_data/x": true,
		"/backup/volumes/ollama_models.v1/_data":     true,
		"/backup/volumes/ollama_modelsXv1/_data":     false, // the dot is literal
		"/backup/volumes/immich_db/_data":            false,
	} {
		if re.MatchString(path) != want {
			t.Errorf("%s: excluded = %v", path, !want)
		}
	}
	// None: the pattern matches nothing, not everything.
	none := regexp.MustCompile("/backup/volumes/(" + noBackupPattern(nil) + ")/")
	if none.MatchString("/backup/volumes/anything/_data") || none.MatchString("/backup/volumes//") {
		t.Error("an empty list must exclude nothing")
	}
}

func TestSwarmUpdateDefaults(t *testing.T) {
	in := []byte(`services:
  web:
    image: nginx
    ports: [{target: 80, published: 8080, mode: ingress}]
  db:
    image: postgres
    volumes: [{type: volume, source: db, target: /var/lib/postgresql}]
  agent:
    image: a
    deploy: {mode: global}
  hostport:
    image: h
    ports: [{target: 53, published: 53, mode: host}]
  cache:
    image: c
    volumes: [{type: tmpfs, target: /tmp}]
  own:
    image: o
    deploy: {update_config: {order: stop-first, failure_action: pause}}
  onhost:
    image: h
    networks: {host: {}}
  job:
    image: j
    deploy: {mode: replicated-job}
`)
	out, err := ForSwarm(in)
	must(t, err)
	var doc struct {
		Services map[string]struct {
			Deploy struct {
				UpdateConfig   map[string]string `yaml:"update_config"`
				RollbackConfig map[string]string `yaml:"rollback_config"`
			} `yaml:"deploy"`
		} `yaml:"services"`
	}
	must(t, yaml.Unmarshal(out, &doc))
	for name, want := range map[string]string{"web": "start-first", "cache": "start-first", "db": "stop-first", "agent": "stop-first", "hostport": "stop-first", "onhost": "stop-first"} {
		d := doc.Services[name].Deploy
		if d.UpdateConfig["order"] != want || d.RollbackConfig["order"] != want || d.UpdateConfig["failure_action"] != "rollback" || d.UpdateConfig["monitor"] != "30s" {
			t.Errorf("%s: update %v rollback %v, want order %s", name, d.UpdateConfig, d.RollbackConfig, want)
		}
	}
	if job := doc.Services["job"].Deploy; job.UpdateConfig != nil || job.RollbackConfig != nil {
		t.Errorf("a job got update settings: %+v", job)
	}
	if own := doc.Services["own"].Deploy.UpdateConfig; own["order"] != "stop-first" || own["failure_action"] != "pause" || own["monitor"] != "30s" {
		t.Errorf("a service's own settings must win: %v", own)
	}
}
