// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"cmp"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/fleet"
)

// MirrorAction is the doctor's action on a Repository mirror check that
// isn't OK: the app's mirror setup (POST /api/v1/mirror/remotes) and push.
const MirrorAction = "mirror"

// noMirror is the Repository mirror warning of a fleet without mirrors.
const noMirror = "the fleet repository has no second remote"

// mirrorSSH is the ssh program a mirror's deploy key is used with (a fake in
// tests).
var mirrorSSH = "ssh"

// A mirror's secrets in fleet.secret.yaml, named by its ID: the deploy key
// the app made (OpenSSH PEM), or "user:token" for an https mirror.
func mirrorKeySecret(u string) string {
	return "MIRROR_" + strings.ToUpper(api.MirrorID(u)) + "_SSH_KEY"
}

func mirrorAuthSecret(u string) string {
	return "MIRROR_" + strings.ToUpper(api.MirrorID(u)) + "_HTTP_AUTH"
}

// newDeployKey makes an ed25519 key: the private half as OpenSSH PEM, the
// public half as an authorized_keys line to paste into the forge.
func newDeployKey() (private, public string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, "nepfleet mirror")
	if err != nil {
		return "", "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(block)), deployKeyLine(sshPub), nil
}

func deployKeyLine(k ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))) + " nepfleet-mirror"
}

// publicOf is the authorized_keys line of a stored deploy key, or "".
func publicOf(private string) string {
	s, err := ssh.ParsePrivateKey([]byte(private))
	if err != nil {
		return ""
	}
	return deployKeyLine(s.PublicKey())
}

// addMirrorAPI is POST /api/v1/mirror/remotes: the URL into spec.mirrors and,
// for ssh, a new deploy key (for https, the token) into the fleet's
// secrets, committed together. It doesn't push: the forge needs the key
// first.
func addMirrorAPI(c common, req api.MirrorAdd, kind api.MirrorKind, by string) (api.MirrorAdded, error) {
	st := api.MirrorStatus{ID: api.MirrorID(req.URL), URL: req.URL, Auth: "host"}
	commit, err := changeChannels(c, by, "chore(fleet): add mirror "+req.URL, func(e *env, secrets map[string]string) (bool, error) {
		added, err := fleet.AddMirror(e.fleet.Dir, req.URL)
		if err != nil {
			return false, err
		}
		if !added {
			return false, api.InvalidError{Reason: "that URL is a mirror already"}
		}
		switch {
		case kind == api.MirrorSSH:
			private, public, err := newDeployKey()
			if err != nil {
				return false, err
			}
			secrets[mirrorKeySecret(req.URL)] = private
			st.Auth, st.PublicKey = "deploy-key", public
			return true, nil
		case kind == api.MirrorHTTPS && req.Token != "":
			secrets[mirrorAuthSecret(req.URL)] = cmp.Or(req.User, "git") + ":" + req.Token
			st.Auth = "token"
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return api.MirrorAdded{}, err
	}
	return api.MirrorAdded{Mirror: st, Commit: commit}, nil
}

// removeMirrorAPI is DELETE /api/v1/mirror/remotes/{id}: the mirror out of
// spec.mirrors, and the key or token the app stored for it out of the
// secrets. The mirror itself, and the key on the forge, stay.
func removeMirrorAPI(c common, id, by string) (string, error) {
	return changeChannels(c, by, "chore(fleet): remove mirror "+id, func(e *env, secrets map[string]string) (bool, error) {
		removed, err := fleet.RemoveMirror(e.fleet.Dir, func(u string) bool { return api.MirrorID(u) == id })
		if err != nil {
			return false, err
		}
		if len(removed) == 0 {
			return false, api.ErrNoMirror
		}
		changed := false
		for _, u := range removed {
			for _, k := range []string{mirrorKeySecret(u), mirrorAuthSecret(u)} {
				if _, ok := secrets[k]; ok {
					delete(secrets, k)
					changed = true
				}
			}
		}
		return changed, nil
	})
}

// mirrorSecrets reads the keys and tokens the app stored for mirrors. It
// decrypts fleet.secret.yaml only when that holds any: a fleet whose
// mirrors use the host's own credentials mirrors without the age key.
type mirrorSecrets struct {
	e      *env
	names  map[string]bool
	values map[string]string
	err    error
}

func newMirrorSecrets(e *env) *mirrorSecrets {
	m := &mirrorSecrets{e: e, names: map[string]bool{}}
	keys, err := secretFileKeys(filepath.Join(e.fleet.Dir, fleet.FleetSecretsFile))
	if err != nil {
		m.err = err
	}
	for _, k := range keys {
		m.names[k] = true
	}
	return m
}

// get is the secret k, and whether there is one.
func (m *mirrorSecrets) get(k string) (string, bool, error) {
	if m.err != nil {
		return "", false, m.err
	}
	if !m.names[k] {
		return "", false, nil
	}
	if m.values == nil {
		if m.values, m.err = m.e.store.Load(filepath.Join(m.e.fleet.Dir, fleet.FleetSecretsFile)); m.err != nil {
			return "", false, fmt.Errorf("reading the mirror's key from %s: %w", fleet.FleetSecretsFile, m.err)
		}
	}
	v, ok := m.values[k]
	return v, ok, nil
}

// auth is how pushes to u sign in: "deploy-key", "token" or "host".
func (m *mirrorSecrets) auth(u string) string {
	switch {
	case m.names[mirrorKeySecret(u)]:
		return "deploy-key"
	case m.names[mirrorAuthSecret(u)]:
		return "token"
	}
	return "host"
}

// pushEnv is the environment git pushes to u with: the deploy key (in a file
// only the push reads, removed by done) or the token the app stored, or
// nothing more for a mirror that uses the host's own credentials. Never a
// prompt: nobody is there to answer it.
func (m *mirrorSecrets) pushEnv(stateDir, u string) (env []string, done func(), err error) {
	env, done = []string{"GIT_TERMINAL_PROMPT=0"}, func() {}
	if key, ok, err := m.get(mirrorKeySecret(u)); err != nil {
		return nil, done, err
	} else if ok {
		if err := os.MkdirAll(stateDir, 0o700); err != nil {
			return nil, done, err
		}
		f, err := os.CreateTemp(stateDir, "mirror-key-*")
		if err != nil {
			return nil, done, err
		}
		done = func() { _ = os.Remove(f.Name()) }
		_, werr := f.WriteString(key)
		if cerr := f.Close(); werr != nil || cerr != nil {
			done()
			return nil, func() {}, errors.Join(werr, cerr)
		}
		// The forge's host key is trusted on first use and kept with this
		// host's state; a changed one fails the push.
		known := filepath.Join(stateDir, "mirror_known_hosts")
		cmd := strings.Join([]string{shellQuote(mirrorSSH), "-i", shellQuote(f.Name()),
			"-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none", "-o", "BatchMode=yes",
			"-o", "StrictHostKeyChecking=accept-new", "-o", shellQuote("UserKnownHostsFile=" + known)}, " ")
		return append(env, "GIT_SSH_COMMAND="+cmd, "GIT_SSH_VARIANT=ssh"), done, nil
	}
	if auth, ok, err := m.get(mirrorAuthSecret(u)); err != nil {
		return nil, done, err
	} else if ok {
		// In the environment, which only this user can read, rather than in
		// the URL or on the command line.
		return append(env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Basic "+base64.StdEncoding.EncodeToString([]byte(auth))), done, nil
	}
	return env, done, nil
}
