// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ArchNewsURL is Arch Linux's news feed (RSS).
const ArchNewsURL = "https://archlinux.org/feeds/news/"

// NewsFeed reads a news feed, keeping the answer for an hour so that
// checking several machines asks the site once.
type NewsFeed struct {
	URL    string
	Client *http.Client

	mu      sync.Mutex
	items   []NewsItem
	fetched time.Time
}

// Since lists the feed's items published after t, newest first.
func (f *NewsFeed) Since(ctx context.Context, t time.Time) ([]NewsItem, error) {
	items, err := f.all(ctx)
	if err != nil {
		return nil, err
	}
	var out []NewsItem
	for _, it := range items {
		if it.Published.After(t) {
			out = append(out, it)
		}
	}
	return out, nil
}

func (f *NewsFeed) all(ctx context.Context) ([]NewsItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.fetched.IsZero() && time.Since(f.fetched) < time.Hour {
		return f.items, nil
	}
	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reading the news feed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("reading the news feed: %s", resp.Status)
	}
	items, err := parseRSS(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("reading the news feed: %w", err)
	}
	f.items, f.fetched = items, time.Now()
	return items, nil
}

func parseRSS(r io.Reader) ([]NewsItem, error) {
	var doc struct {
		Items []struct {
			Title   string `xml:"title"`
			Link    string `xml:"link"`
			PubDate string `xml:"pubDate"`
		} `xml:"channel>item"`
	}
	if err := xml.NewDecoder(r).Decode(&doc); err != nil {
		return nil, err
	}
	var out []NewsItem
	for _, it := range doc.Items {
		ts, err := time.Parse(time.RFC1123Z, strings.TrimSpace(it.PubDate))
		if err != nil {
			continue
		}
		link := strings.TrimSpace(it.Link)
		if !strings.HasPrefix(link, "https://") {
			link = ""
		}
		out = append(out, NewsItem{Title: Clean(strings.TrimSpace(it.Title)), Link: link, Published: ts.UTC()})
	}
	return out, nil
}
