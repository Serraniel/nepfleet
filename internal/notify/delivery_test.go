// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicholas-fedor/shoutrrr/pkg/types"
)

type failing struct{}

func (failing) Send(string, *types.Params) []error {
	return []error{errors.New(`Post "https://ntfy.example.test/alerts?auth=s3cr3t": 401 Unauthorized`)}
}

func TestDeliveriesRecorded(t *testing.T) {
	dir := t.TempDir()
	n, err := New([]Channel{
		{Name: "ok", URL: "generic+https://example.test/a"},
		{Name: "bad", URL: "generic+https://example.test/b"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	n.SetSender("ok", &fake{})
	n.SetSender("bad", failing{})
	n.RecordTo(&Deliveries{Dir: dir})
	if err := n.Send(Event{Name: "apply.failed", Severity: Error, Title: "t"}); err == nil {
		t.Fatal("the failing channel must report its error")
	}

	// Another process reads what this one wrote.
	got, err := (&Deliveries{Dir: dir}).All()
	if err != nil {
		t.Fatal(err)
	}
	ok, bad := got["ok"], got["bad"]
	if !ok.OK || ok.Error != "" || ok.Delivered == nil || ok.At.IsZero() {
		t.Errorf("ok: %+v", ok)
	}
	if bad.OK || bad.Delivered != nil || !strings.Contains(bad.Error, "401 Unauthorized") || strings.Contains(bad.Error, "s3cr3t") {
		t.Errorf("bad: %+v", bad)
	}
	if fi, err := os.Stat(filepath.Join(dir, "deliveries.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("state file: %v %v", fi, err)
	}
}

func TestDeliveryKeepsLastSuccess(t *testing.T) {
	d := &Deliveries{Dir: t.TempDir()}
	first := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	if err := d.Record("phone", nil, first); err != nil {
		t.Fatal(err)
	}
	if err := d.Record("phone", errors.New(strings.Repeat("x", 1000)), first.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, _ := d.All()
	p := got["phone"]
	if p.OK || p.Delivered == nil || !p.Delivered.Equal(first) || !p.At.Equal(first.Add(time.Hour)) {
		t.Errorf("a failure keeps when it last went out: %+v", p)
	}
	if len([]rune(p.Error)) > maxDeliveryError+1 {
		t.Errorf("reason not shortened: %d", len(p.Error))
	}
}

func TestDeliveriesDamagedOrMissing(t *testing.T) {
	dir := t.TempDir()
	d := &Deliveries{Dir: dir}
	if got, err := d.All(); err != nil || len(got) != 0 {
		t.Errorf("no file yet: %v %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "deliveries.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := d.All(); err != nil || len(got) != 0 {
		t.Errorf("damaged file starts over: %v %v", got, err)
	}
	if err := d.Record("a", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.All(); !got["a"].OK {
		t.Errorf("recorded after damage: %v", got)
	}
	var nilD *Deliveries
	if err := nilD.Record("a", nil, time.Now()); err != nil {
		t.Errorf("no record kept: %v", err)
	}
}
