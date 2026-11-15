// SPDX-License-Identifier: AGPL-3.0-or-later

package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// scripted answers docker commands by prefix and records them.
type scripted struct {
	answers map[string]string
	calls   []string
}

func (s *scripted) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	call := strings.Join(args, " ")
	s.calls = append(s.calls, call)
	for prefix, out := range s.answers {
		if strings.HasPrefix(call, prefix) {
			if out == "ERR" {
				return nil, errors.New("no such service")
			}
			return []byte(out), nil
		}
	}
	return nil, nil
}

func (s *scripted) updated() string {
	for _, c := range s.calls {
		if strings.HasPrefix(c, "node update") {
			return c
		}
	}
	return ""
}

func TestEnsureDataNode(t *testing.T) {
	ctx := context.Background()
	// Labelled already: nothing changes.
	done := &scripted{answers: map[string]string{"node ls": "nas\n"}}
	if err := ensureDataNode(ctx, done, "app", []string{"db"}); err != nil || done.updated() != "" {
		t.Errorf("labelled: %v %q", err, done.updated())
	}
	// Running already: the node it runs on keeps its data.
	running := &scripted{answers: map[string]string{"node ls": "", "service ps app_db": "nas\n", "service ps app_web": "nas\n"}}
	if err := ensureDataNode(ctx, running, "app", []string{"db", "web"}); err != nil || running.updated() != "node update --label-add nepfleet.data.app=true nas" {
		t.Errorf("running: %v %q", err, running.updated())
	}
	// New: this node.
	fresh := &scripted{answers: map[string]string{"node ls": "", "service ps": "ERR", "info": "abc123\n"}}
	if err := ensureDataNode(ctx, fresh, "app", []string{"db"}); err != nil || fresh.updated() != "node update --label-add nepfleet.data.app=true abc123" {
		t.Errorf("fresh: %v %q", err, fresh.updated())
	}
	// Spread over nodes: a person decides, nothing is labelled.
	spread := &scripted{answers: map[string]string{"node ls": "", "service ps app_db": "nas\n", "service ps app_web": "vps\n"}}
	err := ensureDataNode(ctx, spread, "app", []string{"db", "web"})
	if err == nil || !strings.Contains(err.Error(), "nas, vps") || spread.updated() != "" {
		t.Errorf("spread: %v %q", err, spread.updated())
	}
}
