// SPDX-License-Identifier: AGPL-3.0-or-later

package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// scripted answers docker commands by their longest matching prefix ("ERR"
// fails) and records them.
type scripted struct {
	answers map[string]string
	calls   []string
}

func (s *scripted) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	call := strings.Join(args, " ")
	s.calls = append(s.calls, call)
	best := ""
	for prefix := range s.answers {
		if strings.HasPrefix(call, prefix) && len(prefix) > len(best) {
			best = prefix
		}
	}
	if best == "" {
		return nil, nil
	}
	if s.answers[best] == "ERR" {
		return nil, errors.New("docker failed")
	}
	return []byte(s.answers[best]), nil
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
	isDataNode := func(err error) bool {
		var d *DataNodeError
		return errors.As(err, &d)
	}
	cases := []struct {
		name    string
		answers map[string]string
		update  string // the node update it makes, "" for none
		refuses string // a DataNodeError containing this
		fails   bool   // another error
	}{
		{name: "labelled already", answers: map[string]string{"node ls": "n1\n", "node inspect": "true\n"}},
		{name: "labelled by hand with another value", answers: map[string]string{"node ls": "n1\n", "node inspect": "yes\n"}, refuses: "needs true"},
		{name: "two nodes labelled", answers: map[string]string{"node ls": "n1\nn2\n"}, refuses: "2 nodes carry"},
		{name: "new stack: this node", answers: map[string]string{"service ls": "", "info": "self1\n"},
			update: "node update --label-add nepfleet.data.app=true self1"},
		{name: "running on one node", answers: map[string]string{"service ls": "s1\n", "service ps app_db": "t1\n",
			"service ps app_web": "t2\n", "inspect --format {{.NodeID}} t1": "nodeB\n", "inspect --format {{.NodeID}} t2": "nodeB\n"},
			update: "node update --label-add nepfleet.data.app=true nodeB"},
		{name: "running, a renamed service has no tasks yet", answers: map[string]string{"service ls": "s1\n", "service ps app_db": "t1\n",
			"service ps app_web": "ERR", "inspect": "nodeB\n"},
			update: "node update --label-add nepfleet.data.app=true nodeB"},
		{name: "exists, but no running task tells where", answers: map[string]string{"service ls": "s1\n", "service ps": "ERR"},
			refuses: "none of its services"},
		{name: "spread over nodes", answers: map[string]string{"service ls": "s1\n", "service ps app_db": "t1\n",
			"service ps app_web": "t2\n", "inspect --format {{.NodeID}} t1": "nodeA\n", "inspect --format {{.NodeID}} t2": "nodeB\n"},
			refuses: "nodeA, nodeB"},
		{name: "listing the stack fails", answers: map[string]string{"service ls": "ERR"}, fails: true},
	}
	for _, c := range cases {
		s := &scripted{answers: c.answers}
		err := ensureDataNode(ctx, s, "app", []string{"db", "web"})
		switch {
		case c.refuses != "":
			if !isDataNode(err) || !strings.Contains(err.Error(), c.refuses) {
				t.Errorf("%s: %v", c.name, err)
			}
		case c.fails:
			if err == nil || isDataNode(err) {
				t.Errorf("%s: %v", c.name, err)
			}
		case err != nil:
			t.Errorf("%s: %v", c.name, err)
		}
		if s.updated() != c.update {
			t.Errorf("%s: update %q, want %q", c.name, s.updated(), c.update)
		}
	}
}
