// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// APIPort is where a NepFleet server listens on its own machine.
const APIPort = 7480

// Token is an API token made on a remote over SSH. Secret stays in memory.
type Token struct {
	ID     string
	Secret string
}

var (
	tokenLabelRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	tokenIDRe    = regexp.MustCompile(`^[0-9a-f]{8}$`)
	mintedIDRe   = regexp.MustCompile(`(?m)^token ([0-9a-f]{8}) \(`)
	mintedRe     = regexp.MustCompile(`(?m)^(nft_[A-Za-z0-9_-]{20,128})$`)
)

// nepfleetCmd is shell that runs nepfleet on the remote: in its running
// container if there is one (it has the right state directory), else the CLI.
const nepfleetCmd = `
nf() {
  c=""
  if command -v docker >/dev/null 2>&1; then
    c="$(docker ps --format '{{.ID}} {{.Image}}' 2>/dev/null | grep -m1 'nepfleet/nepfleet\|/nepfleet:\|^[0-9a-f]* nepfleet:' | cut -d' ' -f1)"
  fi
  if [ -n "$c" ]; then docker exec "$c" nepfleet "$@"; else nepfleet "$@"; fi
}
`

// mintScript creates a token. It takes only checked values: see MintToken.
func mintScript(label, lifetime string) string {
	return nepfleetCmd + "nf token create --name " + label + " --scope apply --expires " + lifetime + " 2>&1\n"
}

// parseMinted reads what token create printed.
func parseMinted(out []byte) (Token, error) {
	id, secret := mintedIDRe.FindSubmatch(out), mintedRe.FindSubmatch(out)
	if id == nil || secret == nil {
		// Half an answer may still hold a secret: errors go to logs and clients.
		return Token{}, fmt.Errorf("creating a token on the remote failed: %s", Clean(mintedRe.ReplaceAllString(string(out), "(token hidden)")))
	}
	return Token{ID: string(id[1]), Secret: string(secret[1])}, nil
}

// ErrNoNepFleet: a remote has no NepFleet to connect to yet.
var ErrNoNepFleet = errors.New("NepFleet isn't installed on this machine yet: prepare it and install NepFleet first")

// MintToken creates an apply-scope token on the remote. SSH access to the
// machine is what entitles this client to it.
func (s SSH) MintToken(ctx context.Context, t Target, label, lifetime string) (Token, error) {
	if !tokenLabelRe.MatchString(label) {
		return Token{}, fmt.Errorf("%q cannot name a token", label)
	}
	if lifetime != "24h" {
		return Token{}, errors.New("only tokens of 24h are minted")
	}
	out, err := s.Run(ctx, t, mintScript(label, lifetime))
	if err != nil {
		// 127: the shell found no nepfleet there.
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 127 {
			return Token{}, fmt.Errorf("%s: %w", t, ErrNoNepFleet)
		}
		return Token{}, err
	}
	return parseMinted(out)
}

// RevokeToken removes a token from the remote.
func (s SSH) RevokeToken(ctx context.Context, t Target, id string) error {
	if !tokenIDRe.MatchString(id) {
		return fmt.Errorf("%q is not a token ID", id)
	}
	_, err := s.Run(ctx, t, nepfleetCmd+"nf token revoke "+id+" 2>&1\n")
	return err
}

// ForwardArgs is ssh's command line for a tunnel: a private local socket to
// the API on the remote's loopback, opening no port on either side. If the
// forward can't be set up, ssh exits rather than sit there doing nothing, and
// the keepalives notice a dead connection.
func (s SSH) ForwardArgs(t Target, socket string) []string {
	args := []string{"-N", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=10",
		"-o", "ExitOnForwardFailure=yes", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3",
		"-o", "StreamLocalBindMask=0177", "-o", "StreamLocalBindUnlink=yes",
		"-L", socket + ":127.0.0.1:" + strconv.Itoa(APIPort)}
	if s.ConfigFile != "" {
		args = append(args, "-F", s.ConfigFile)
	}
	if t.Port != 0 {
		args = append(args, "-p", strconv.Itoa(t.Port))
	}
	if t.User != "" {
		args = append(args, "-l", t.User)
	}
	return append(args, "--", t.Host)
}

// Forward runs the tunnel until ctx ends or ssh exits.
func (s SSH) Forward(ctx context.Context, t Target, socket string) error {
	if !strings.HasPrefix(socket, "127.0.0.1:") {
		if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	cmd := exec.CommandContext(ctx, "ssh", s.ForwardArgs(t, socket)...) //nolint:gosec // names checked in ParseTarget, after "--"
	var stderr bytes.Buffer
	cmd.Stderr = &limitWriter{w: &stderr, n: 4 << 10}
	err := cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return sshError(t, Clean(stderr.String()), err)
	}
	return fmt.Errorf("%s: the tunnel ended", t)
}
