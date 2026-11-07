// SPDX-License-Identifier: AGPL-3.0-or-later

package events

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestStore(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if got, err := s.After(0, 10, nil); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	for i := range 3 {
		e, err := s.Add(Event{Name: "apply.succeeded", Stack: fmt.Sprint("s", i), Title: "t"})
		if err != nil || e.ID != int64(i+1) || e.Time.IsZero() {
			t.Fatalf("add: %+v %v", e, err)
		}
	}
	got, err := s.After(1, 10, nil)
	if err != nil || len(got) != 2 || got[0].ID != 2 {
		t.Errorf("after 1: %+v %v", got, err)
	}
	got, _ = s.After(0, 10, func(e Event) bool { return e.Stack == "s1" })
	if len(got) != 1 || got[0].ID != 2 {
		t.Errorf("filter: %+v", got)
	}
	got, _ = s.After(0, 2, nil)
	if len(got) != 2 {
		t.Errorf("limit: %+v", got)
	}
	if fi, err := os.Stat(s.path()); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Errorf("file: %v %v", fi, err)
	}
	// A line torn by a crash is skipped, and IDs go on.
	f, err := os.OpenFile(s.path(), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"id":4,"na`)
	_ = f.Close()
	got, _ = s.After(0, 10, nil)
	if len(got) != 3 {
		t.Errorf("after a torn line: %+v", got)
	}
	if e, err := s.Add(Event{Name: "y", Title: "t"}); err != nil || e.ID != 4 {
		t.Errorf("next: %+v %v", e, err)
	}
	if got, _ = s.After(3, 10, nil); len(got) != 1 || got[0].Name != "y" {
		t.Errorf("an event after a torn line must survive: %+v", got)
	}
}

func TestBounded(t *testing.T) {
	const keep, most = 50, 60
	s := &Store{Dir: t.TempDir(), Keep: keep}
	for range most + 5 {
		if _, err := s.Add(Event{Name: "x", Title: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.read()
	if err != nil || len(all) > most {
		t.Fatalf("kept %d: %v", len(all), err)
	}
	if all[len(all)-1].ID != most+5 {
		t.Errorf("IDs must go on after cutting: %d", all[len(all)-1].ID)
	}
	if matches, _ := filepath.Glob(filepath.Join(s.Dir, ".events-*")); len(matches) != 0 {
		t.Errorf("temp files left: %v", matches)
	}
}

func TestConcurrentWriters(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, err := s.Add(Event{Name: "x", Title: "t"}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	all, _ := s.read()
	seen := map[int64]bool{}
	for _, e := range all {
		if seen[e.ID] {
			t.Errorf("duplicate ID %d", e.ID)
		}
		seen[e.ID] = true
	}
	if len(all) != 20 {
		t.Errorf("%d events", len(all))
	}
}
