// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// metricsEvents is how far back metrics look; the store keeps about this many.
const metricsEvents = 2000

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// writeMetrics writes Prometheus' text format. Events are kept only for a while, so
// these are times of the last event, not counters that would seem to fall.
func writeMetrics(w io.Writer, info Info, stacks int, last map[[2]string]time.Time) {
	fmt.Fprintln(w, "# HELP nepfleet_info This server. Always 1.")
	fmt.Fprintln(w, "# TYPE nepfleet_info gauge")
	fmt.Fprintf(w, "nepfleet_info{name=\"%s\",version=\"%s\"} 1\n", labelEscaper.Replace(info.Name), labelEscaper.Replace(info.Version))
	fmt.Fprintln(w, "# HELP nepfleet_stacks Stacks in the fleet this token covers.")
	fmt.Fprintln(w, "# TYPE nepfleet_stacks gauge")
	fmt.Fprintf(w, "nepfleet_stacks %d\n", stacks)
	fmt.Fprintln(w, "# HELP nepfleet_stack_last_event_timestamp_seconds When a stack last had this event, such as apply.succeeded or apply.rolled_back.")
	fmt.Fprintln(w, "# TYPE nepfleet_stack_last_event_timestamp_seconds gauge")
	keys := make([][2]string, 0, len(last))
	for k := range last {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool {
		if keys[a][0] != keys[b][0] {
			return keys[a][0] < keys[b][0]
		}
		return keys[a][1] < keys[b][1]
	})
	for _, k := range keys {
		fmt.Fprintf(w, "nepfleet_stack_last_event_timestamp_seconds{stack=\"%s\",event=\"%s\"} %d\n",
			labelEscaper.Replace(k[0]), labelEscaper.Replace(k[1]), last[k].Unix())
	}
}

// serveMetrics answers /metrics. It needs a token like the rest: stack names and what
// happened to them aren't for anyone who can reach the port.
func serveMetrics(o Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t, _ := TokenFrom(r.Context())
		allow := func(stack string) bool { return stack != "" && t.Allows(auth.ScopeRead, stack) }
		count := 0
		if o.Stacks != nil {
			all, err := o.Stacks()
			if err != nil {
				log.Printf("api: metrics: %v", err)
				http.Error(w, "the fleet cannot be read", http.StatusInternalServerError)
				return
			}
			for _, s := range all {
				if allow(s.Name) {
					count++
				}
			}
		}
		last := map[[2]string]time.Time{}
		if o.Events != nil {
			evs, err := o.Events(0, metricsEvents, allow)
			if err != nil {
				log.Printf("api: metrics: %v", err)
				http.Error(w, "events cannot be read", http.StatusInternalServerError)
				return
			}
			raw, err := json.Marshal(evs)
			if err != nil {
				http.Error(w, "events cannot be read", http.StatusInternalServerError)
				return
			}
			var list []struct {
				Name  string    `json:"name"`
				Stack string    `json:"stack"`
				Time  time.Time `json:"time"`
			}
			if err := json.Unmarshal(raw, &list); err != nil {
				http.Error(w, "events cannot be read", http.StatusInternalServerError)
				return
			}
			for _, e := range list {
				k := [2]string{e.Stack, e.Name}
				if e.Time.After(last[k]) {
					last[k] = e.Time
				}
			}
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		writeMetrics(w, o.Info, count, last)
	}
}
