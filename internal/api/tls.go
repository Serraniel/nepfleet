// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"
)

// CheckListen refuses a plain-HTTP address beyond loopback: tokens would
// cross the network in the clear. behindProxy says a reverse proxy
// terminates TLS in front of this server.
func CheckListen(addr string, tlsOn, behindProxy bool) error {
	if tlsOn || behindProxy {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("--listen %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
		return nil
	}
	return fmt.Errorf("refusing to listen on %s without TLS: use --tls-cert and --tls-key or --tls-domain, or --behind-proxy when a reverse proxy terminates TLS (see docs/security.md)", addr)
}

// CertReloader serves a certificate from files and picks up renewals without
// a restart. If a renewed pair cannot be loaded, the last good one stays.
type CertReloader struct {
	CertFile, KeyFile string

	mu      sync.Mutex
	cert    *tls.Certificate
	modTime [2]time.Time
	checked time.Time
}

// Load reads the pair; call it at start, so a wrong path fails at once.
func (c *CertReloader) Load() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.load()
}

func (c *CertReloader) load() error {
	ci, err := os.Stat(c.CertFile)
	if err != nil {
		return err
	}
	ki, err := os.Stat(c.KeyFile)
	if err != nil {
		return err
	}
	mods := [2]time.Time{ci.ModTime(), ki.ModTime()}
	if c.cert != nil && mods == c.modTime {
		return nil
	}
	cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		return err
	}
	c.cert, c.modTime = &cert, mods
	return nil
}

// GetCertificate is for tls.Config. It looks at the files at most once a
// minute.
func (c *CertReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.checked) >= time.Minute {
		c.checked = time.Now()
		_ = c.load() // keep serving the last good pair during a renewal
	}
	if c.cert == nil {
		return nil, errors.New("no certificate loaded")
	}
	return c.cert, nil
}
