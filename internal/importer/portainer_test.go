// SPDX-License-Identifier: AGPL-3.0-or-later

package importer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPortainer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "ptr_ok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/stacks":
			_, _ = w.Write([]byte(`[{"Id":7,"Name":"web","Type":2,"Status":1,"Env":[{"name":"DB_PASSWORD","value":"it's $ecret"}]},{"Id":2,"Name":"app","Type":1,"Status":2}]`))
		case "/api/stacks/7/file":
			_, _ = w.Write([]byte(`{"StackFileContent":"services:\n  web:\n    image: nginx\n    volumes: [\"./html:/usr/share/nginx/html\"]\n"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	if _, err := (Portainer{URL: srv.URL, Token: "wrong"}).Stacks(ctx); err == nil || !strings.Contains(err.Error(), "access token") {
		t.Errorf("bad token: %v", err)
	}
	p := Portainer{URL: srv.URL + "/", Token: "ptr_ok"}
	stacks, err := p.Stacks(ctx)
	if err != nil || len(stacks) != 2 || stacks[0].Name != "app" || stacks[0].Kind() != "swarm" {
		t.Fatalf("stacks = %+v, %v", stacks, err)
	}
	content, err := p.File(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	proj, o, warnings, err := StageProject(dir, stacks[1], content, "/var/lib/docker/volumes/portainer_data/_data")
	if err != nil {
		t.Fatal(err)
	}
	if proj.Name != "web" || o.Mode != "" || o.RewriteDir != [2]string{dir, "/var/lib/docker/volumes/portainer_data/_data/compose/7"} {
		t.Errorf("project %+v, options %+v", proj, o)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "relative bind mounts") {
		t.Errorf("warnings = %v", warnings)
	}
	env, _ := os.ReadFile(filepath.Join(dir, "stack.env"))
	vars, _, err := parseEnvFile(filepath.Join(dir, "stack.env"), map[string]string{})
	if err != nil || vars["DB_PASSWORD"] != "it's $ecret" {
		t.Errorf("env round trip: %q → %v %v", env, vars, err)
	}
	for _, v := range []string{"plain", "$HOME and #hash", `it's "quoted" \ $x`, "a\nb's"} {
		path := filepath.Join(dir, "rt.env")
		_ = os.WriteFile(path, []byte("K="+envQuote(v)+"\n"), 0o600)
		if got, _, err := parseEnvFile(path, map[string]string{}); err != nil || got["K"] != v {
			t.Errorf("round trip %q: got %q %v", v, got["K"], err)
		}
	}
}

func TestPortainerURL(t *testing.T) {
	for _, ok := range []string{"https://portainer.example.org:9443", "http://localhost:9000", "http://127.0.0.1:9000"} {
		if err := ValidPortainerURL(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://nas.lan:9000", "portainer:9000", ""} {
		if ValidPortainerURL(bad) == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}
