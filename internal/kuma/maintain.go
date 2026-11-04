// SPDX-License-Identifier: AGPL-3.0-or-later

package kuma

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// OptOutLabel on a service keeps its monitors out of maintenance windows.
const OptOutLabel = "nepfleet.maintenance"

// MonitorNames returns the AutoKuma monitor names a rendered compose file
// declares (labels kuma.<id>.<type>.name), with ${VAR} taken from env.
func MonitorNames(compose []byte, env map[string]string) []string {
	var doc struct {
		Services map[string]struct {
			Labels any `yaml:"labels"`
		} `yaml:"services"`
	}
	if yaml.Unmarshal(compose, &doc) != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, svc := range doc.Services {
		labels := labelMap(svc.Labels)
		if labels[OptOutLabel] == "false" {
			continue
		}
		for k, v := range labels {
			parts := strings.Split(k, ".")
			if len(parts) == 4 && parts[0] == "kuma" && parts[3] == "name" {
				name := os.Expand(v, func(n string) string { return env[n] })
				if name != "" && !seen[name] {
					seen[name] = true
					out = append(out, name)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func labelMap(labels any) map[string]string {
	out := map[string]string{}
	switch l := labels.(type) {
	case map[string]any:
		for k, v := range l {
			out[k] = fmt.Sprint(v)
		}
	case []any:
		for _, item := range l {
			if s, ok := item.(string); ok {
				k, v, _ := strings.Cut(s, "=")
				out[k] = v
			}
		}
	}
	return out
}

// Target is one Kuma instance with credentials.
type Target struct {
	Name, URL, Username, Password string
}

// Window is an open maintenance window across instances.
type Window struct {
	parts []open
}

type open struct {
	target Target
	m      Maintenance
}

// Open puts the named monitors into a manual maintenance window in every
// target that has them. Problems are returned as warnings, never as errors:
// a monitoring outage must not block a deployment.
func Open(ctx context.Context, targets []Target, monitors []string, title, description string, hc *http.Client) (*Window, []string) {
	w := &Window{}
	var warnings []string
	for _, t := range targets {
		tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		ids, err := func() ([]int, error) {
			k, err := Login(tctx, t.URL, t.Username, t.Password, hc)
			if err != nil {
				return nil, err
			}
			defer k.Close()
			all, err := k.Monitors(tctx)
			if err != nil {
				return nil, err
			}
			want := map[string]bool{}
			for _, n := range monitors {
				want[n] = true
			}
			var ids []int
			for _, m := range all {
				if want[m.Name] {
					ids = append(ids, m.ID)
				}
			}
			if len(ids) == 0 {
				return nil, nil
			}
			m, err := k.StartMaintenance(tctx, MaintenanceWindow{Title: title, Description: description, Monitors: ids})
			if err != nil {
				return nil, err
			}
			w.parts = append(w.parts, open{target: t, m: m})
			return ids, nil
		}()
		cancel()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Uptime Kuma %s: no maintenance window (%v)", t.Name, err))
		} else if ids != nil {
			warnings = append(warnings, fmt.Sprintf("Uptime Kuma %s: %d monitor(s) in maintenance", t.Name, len(ids)))
		}
	}
	return w, warnings
}

// Close ends every window that was opened.
func (w *Window) Close(ctx context.Context, hc *http.Client) []string {
	var warnings []string
	for _, p := range w.parts {
		tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := func() error {
			k, err := Login(tctx, p.target.URL, p.target.Username, p.target.Password, hc)
			if err != nil {
				return err
			}
			defer k.Close()
			return k.EndMaintenance(tctx, p.m)
		}()
		cancel()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Uptime Kuma %s: could not end %s, end it in the UI (%v)", p.target.Name, p.m, err))
		}
	}
	return warnings
}
