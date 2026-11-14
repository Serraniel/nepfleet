// SPDX-License-Identifier: AGPL-3.0-or-later

package control

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// Settings are the app's choices the daemon keeps.
type Settings struct {
	// FleetDir is a local clone of the fleet repository: machine mounts are
	// planned from its hosts/ files, and their passwords decrypted with the age
	// key of this computer, as nepfleet host apply does here.
	FleetDir string `json:"fleetDir,omitempty"`
}

var settingsMu sync.Mutex

func (s *Server) settingsPath() string { return filepath.Join(s.Dir, "settings.json") }

// LoadSettings reads the settings; none saved is the zero value.
func (s *Server) LoadSettings() Settings {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	var st Settings
	if raw, err := os.ReadFile(s.settingsPath()); err == nil { //nolint:gosec // the daemon's own directory
		_ = json.Unmarshal(raw, &st)
	}
	return st
}

func (s *Server) saveSettings(st Settings) error {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.settingsPath(), raw, 0o600)
}

// checkFleetDir: an absolute folder with a fleet.yaml, or "" for none.
func checkFleetDir(dir string) (string, error) {
	if dir == "" {
		return "", nil
	}
	if !filepath.IsAbs(dir) {
		return "", errors.New("give the folder's full path")
	}
	dir = filepath.Clean(dir)
	if fi, err := os.Stat(filepath.Join(dir, "fleet.yaml")); err != nil || !fi.Mode().IsRegular() {
		return "", errors.New("that folder has no fleet.yaml: choose the folder of your fleet repository")
	}
	return dir, nil
}

func (s *Server) settingsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/settings", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.LoadSettings())
	})
	mux.HandleFunc("PUT /v1/settings", func(w http.ResponseWriter, r *http.Request) {
		var st Settings
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&st); err != nil {
			fail(w, http.StatusBadRequest, `send {"fleetDir": "/path/to/fleet"}`)
			return
		}
		dir, err := checkFleetDir(st.FleetDir)
		if err != nil {
			fail(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		st.FleetDir = dir
		if err := s.saveSettings(st); err != nil {
			failErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, st)
	})
}
