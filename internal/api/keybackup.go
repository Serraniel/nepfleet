// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// KeyBackupRequest names who can open the backup: age public keys (age1…) or
// SSH public keys (ssh-ed25519 …, ssh-rsa …), as age --recipient takes them.
// No passphrase travels: the person keeps the matching private key.
type KeyBackupRequest struct {
	Recipients []string `json:"recipients"`
}

// ErrNoKeyFile means the server's age key isn't a file it can back up (it
// comes from SOPS_AGE_KEY or SOPS_AGE_KEY_CMD, or is missing).
var ErrNoKeyFile = errors.New("this server's age key isn't a file it can back up")

// recipient is an age or SSH public key, nothing an option could hide in.
var recipient = regexp.MustCompile(`^(age1[02-9ac-hj-np-z]{58}|ssh-ed25519 [A-Za-z0-9+/]{68}={0,2}( [\x20-\x7e]{0,100})?|ssh-rsa [A-Za-z0-9+/]{200,}={0,2}( [\x20-\x7e]{0,100})?)$`)

// RecipientPrints names recipients by a short fingerprint, so a key nobody
// knows stands out in the log and the notification.
func RecipientPrints(rs []string) string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		key, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(r, "ssh-ed25519 "), "ssh-rsa "), " ")
		sum := sha256.Sum256([]byte(key))
		kind := "age"
		if strings.HasPrefix(r, "ssh-") {
			kind = "ssh"
		}
		out = append(out, kind+":"+hex.EncodeToString(sum[:6]))
	}
	return strings.Join(out, ", ")
}

func registerKeyBackup(mux *http.ServeMux, o Options, l *limiter) {
	// The key opens every secret of the fleet. Only a signed-in person, who
	// confirmed with a passkey just now, may take it, and the fleet hears of it.
	mux.HandleFunc("POST /api/v1/key/backup", requireAuth(o, l, auth.ScopeAdmin, func(w http.ResponseWriter, r *http.Request) {
		if o.KeyBackup == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		t, _ := TokenFrom(r.Context())
		if t.ID != "session" {
			http.Error(w, "backing up the age key needs a person signed in, not an API token", http.StatusForbidden)
			return
		}
		if !mayChangeAccess(w, r, o) {
			return
		}
		var req KeyBackupRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || len(req.Recipients) == 0 || len(req.Recipients) > 10 {
			http.Error(w, `send {"recipients": ["age1…" or "ssh-ed25519 AAAA…"]}`, http.StatusBadRequest)
			return
		}
		for _, rc := range req.Recipients {
			if len(rc) > 1400 || !recipient.MatchString(rc) {
				http.Error(w, "a recipient is an age public key (age1…) or an SSH public key (ssh-ed25519 or ssh-rsa)", http.StatusUnprocessableEntity)
				return
			}
		}
		// One passkey confirmation, one backup.
		if c, err := r.Cookie(SessionCookie); err == nil && o.Accounts != nil {
			o.Accounts.Sessions.SpendVerification(c.Value)
		}
		armored, err := o.KeyBackup(r.Context(), req.Recipients, t.Name)
		switch {
		case errors.Is(err, ErrNoKeyFile):
			http.Error(w, err.Error(), http.StatusConflict)
			return
		case writeErr(w, err):
			return
		}
		log.Printf("api: %q took a backup of the age key, for %s", t.Name, RecipientPrints(req.Recipients)) //nolint:gosec // %q escapes the name
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="nepfleet-age-key.age"`)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(armored)
	}))
}
