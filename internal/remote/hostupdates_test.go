// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestParseHostUpdates(t *testing.T) {
	h := parseHostUpdates([]byte("os=arch\npkg=linux\npkg=openssl\npkg=bad name; rm\nsecurity=1\nreboot=yes\nnote=arch-audit\x1b[31m output\nnoise\n"))
	if h.OS != "arch" || !h.Supported || h.Pending != 2 || h.Security != 1 || h.RebootRequired == nil || !*h.RebootRequired {
		t.Fatalf("%+v", h)
	}
	if strings.Join(h.Packages, ",") != "linux,openssl" {
		t.Errorf("packages %v", h.Packages)
	}
	if len(h.Notes) != 1 || strings.ContainsRune(h.Notes[0], '\x1b') {
		t.Errorf("notes %q", h.Notes)
	}
	// Unknown stays unknown: no arch-audit, no reboot answer.
	u := parseHostUpdates([]byte("os=fedora\nreboot=unknown\n"))
	if u.Security != -1 || u.RebootRequired != nil || u.Pending != 0 {
		t.Errorf("unknowns: %+v", u)
	}
	if n := parseHostUpdates([]byte("os=gentoo\nunsupported=yes\n")); n.Supported || n.OS != "gentoo" {
		t.Errorf("unsupported: %+v", n)
	}
	if o := parseHostUpdates([]byte("os=../etc\n")); o.OS != "unknown" {
		t.Errorf("odd os id kept: %q", o.OS)
	}
}

// The script runs with a POSIX sh, whatever the distribution (here: this
// machine), and reports at least the OS.
func TestHostUpdatesScriptRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("asks this machine's package manager, which may sync from the network")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("needs sh")
	}
	run := func(ctx context.Context, _ Target, script string) ([]byte, error) {
		return exec.CommandContext(ctx, "sh", "-c", script).Output()
	}
	h, err := CheckHostUpdates(context.Background(), run, Target{})
	if err != nil {
		t.Fatal(err)
	}
	if h.OS == "" || h.Pending < 0 {
		t.Errorf("%+v", h)
	}
	t.Logf("this machine: %+v", h)
}

func TestArchNews(t *testing.T) {
	h := parseHostUpdates([]byte("os=endeavouros\nfamily=arch\nlastupgrade=2026-10-21T15:55:00+0200\nreboot=no\n"))
	if !h.Arch() || !h.LastUpgrade.Equal(time.Date(2026, 10, 21, 13, 55, 0, 0, time.UTC)) {
		t.Fatalf("%+v", h)
	}
	if d := parseHostUpdates([]byte("os=debian\nlastupgrade=garbage\n")); d.Arch() || !d.LastUpgrade.IsZero() {
		t.Errorf("debian: %+v", d)
	}
	feed := `<?xml version="1.0"?><rss><channel>
<item><title>Newer &amp; important</title><link>https://archlinux.org/news/a/</link><pubDate>Wed, 21 Oct 2026 13:58:42 +0000</pubDate></item>
<item><title>Older</title><link>javascript:alert(1)</link><pubDate>Wed, 21 Oct 2026 13:52:37 +0000</pubDate></item>
<item><title>No date</title><link>https://x/</link><pubDate>soon</pubDate></item>
</channel></rss>`
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(feed))
	}))
	defer srv.Close()
	f := &NewsFeed{URL: srv.URL}
	items, err := f.Since(context.Background(), h.LastUpgrade)
	if err != nil || len(items) != 1 || items[0].Title != "Newer & important" || items[0].Link != "https://archlinux.org/news/a/" {
		t.Fatalf("since: %+v %v", items, err)
	}
	all, _ := f.Since(context.Background(), time.Time{})
	if len(all) != 2 || all[1].Link != "" || calls != 1 {
		t.Errorf("all: %+v (calls %d)", all, calls)
	}
}
