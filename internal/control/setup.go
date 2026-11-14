// SPDX-License-Identifier: AGPL-3.0-or-later

package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Serraniel/nepfleet/internal/remote"
)

// Setup kinds: preparing a machine (Docker, Compose, ingress network), and
// installing NepFleet on it, as `nepfleet host bootstrap` and `host install`.
const (
	SetupBootstrap = "bootstrap"
	SetupInstall   = "install"
	// SetupMounts makes the machine's network mounts match its hosts/<name>.yaml
	// in the fleet repository folder (Settings), as `nepfleet host apply`.
	SetupMounts = "mounts"
)

// SetupPlan is what a setup would do on a remote, for a person to review.
type SetupPlan struct {
	Kind  string        `json:"kind"`
	Steps []remote.Step `json:"steps"`
	// ID names exactly these steps: starting the setup needs it, so what runs is
	// what was reviewed.
	ID string `json:"id,omitempty"`
	// Error says why it can't run (Docker missing, no root, …); the steps then
	// show what it would need.
	Error string `json:"error,omitempty"`
	// SudoPassword: root steps run as a user whose sudo asks for a password, so
	// starting the setup needs it. It is used for that run only, never kept.
	SudoPassword bool `json:"sudoPassword,omitempty"`
}

// SetupJob is a running or finished setup.
type SetupJob struct {
	Kind     string    `json:"kind"`
	State    string    `json:"state"` // running, succeeded, failed
	Log      string    `json:"log"`
	Error    string    `json:"error,omitempty"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished,omitzero"`
}

// Setup holds what setups need besides the server's probe.
type Setup struct {
	// Run runs a script on a remote, as root steps expect (remote.SSH.Run).
	Run func(ctx context.Context, t remote.Target, script string) ([]byte, error)
	// Compose is the compose.yaml an install writes, pinned to a version.
	Compose []byte
	// Ingress is the ingress network bootstrap creates.
	Ingress string
	// Mounts plans a machine's mounts (nil: the daemon can't); run reaches
	// the machine.
	Mounts func(ctx context.Context, name string, run func(context.Context, string) ([]byte, error), r *remote.Report) ([]remote.Step, error)

	mu   sync.Mutex
	jobs map[string]*setupJob
	// starting: remotes whose setup is being checked before it starts.
	starting map[string]bool
}

type setupJob struct {
	mu  sync.Mutex
	job SetupJob
	log strings.Builder
}

func (j *setupJob) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.log.Write(p)
}

func (j *setupJob) snapshot() SetupJob {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := j.job
	out.Log = j.log.String()
	return out
}

func planID(kind string, steps []remote.Step) string {
	h := sha256.New()
	h.Write([]byte(kind))
	for _, s := range steps {
		h.Write([]byte{0})
		h.Write([]byte(s.Text))
		h.Write([]byte{0})
		h.Write([]byte(s.Script))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// setupPlan probes the remote again and plans the setup; the fresh report is kept.
func (s *Server) setupPlan(ctx context.Context, name, kind string) (*SetupPlan, *remote.Report, remote.Target, error) {
	s.mu.Lock()
	i := s.index(name)
	var addr string
	if i >= 0 {
		addr = s.remotes[i].Address
	}
	s.mu.Unlock()
	if i < 0 {
		return nil, nil, remote.Target{}, errNoRemote
	}
	t, err := remote.ParseTarget(addr)
	if err != nil {
		return nil, nil, t, err
	}
	rep, err := s.Probe(ctx, t)
	if err != nil {
		return nil, nil, t, err
	}
	s.mu.Lock()
	if i := s.index(name); i >= 0 {
		s.remotes[i].Report, s.remotes[i].Next, s.remotes[i].Checked = rep, rep.Next(), time.Now().UTC()
		_ = s.save()
	}
	s.mu.Unlock()
	var steps []remote.Step
	switch kind {
	case SetupBootstrap:
		steps, err = remote.BootstrapPlan(rep, s.Setup.Ingress)
	case SetupInstall:
		steps, err = remote.InstallPlan(rep, name, s.Setup.Compose, false)
	case SetupMounts:
		if s.Setup.Mounts == nil || s.Setup.Run == nil {
			return nil, nil, t, errors.New("this daemon can't set up mounts")
		}
		run := func(ctx context.Context, script string) ([]byte, error) { return s.Setup.Run(ctx, t, script) }
		steps, err = s.Setup.Mounts(ctx, name, run, rep)
		if err == nil && len(steps) > 0 && !rep.Root && !rep.Sudo && !rep.SudoPassword {
			err = errors.New("this needs root: log in as root, or as a user who may use sudo")
		}
		if err == nil && len(steps) == 0 {
			err = errors.New("nothing to do: the mounts match the host file already")
		}
	default:
		return nil, nil, t, errors.New("kind is bootstrap, install or mounts")
	}
	p := &SetupPlan{Kind: kind, Steps: steps}
	if p.Steps == nil {
		p.Steps = []remote.Step{}
	}
	if err != nil {
		p.Error = err.Error()
	} else {
		p.ID = planID(kind, steps)
		p.SudoPassword = remote.NeedsSudoPassword(rep, steps)
	}
	return p, rep, t, nil
}

var errNoRemote = errors.New("no such remote")

// setupRoutes: GET …/setup?kind= plans, POST …/setup starts the reviewed plan,
// GET …/setup/job follows it.
func (s *Server) setupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/remotes/{name}/setup", func(w http.ResponseWriter, r *http.Request) {
		p, _, _, err := s.setupPlan(r.Context(), r.PathValue("name"), r.URL.Query().Get("kind"))
		switch {
		case errors.Is(err, errNoRemote):
			fail(w, http.StatusNotFound, err.Error())
		case err != nil:
			failErr(w, http.StatusBadGateway, err)
		default:
			writeJSON(w, http.StatusOK, p)
		}
	})
	mux.HandleFunc("POST /v1/remotes/{name}/setup", s.startSetup)
	mux.HandleFunc("GET /v1/remotes/{name}/setup/job", func(w http.ResponseWriter, r *http.Request) {
		s.Setup.mu.Lock()
		j := s.Setup.jobs[r.PathValue("name")]
		s.Setup.mu.Unlock()
		if j == nil {
			fail(w, http.StatusNotFound, "no setup has run for this remote since the daemon started")
			return
		}
		writeJSON(w, http.StatusOK, j.snapshot())
	})
}

func (s *Server) startSetup(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req struct {
		Kind string `json:"kind"`
		Plan string `json:"plan"`
		// SudoPassword is for this run only: never stored, logged or returned.
		SudoPassword string `json:"sudoPassword,omitempty"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Plan == "" {
		fail(w, http.StatusBadRequest, `send {"kind": "bootstrap|install|mounts", "plan": "<id of the reviewed plan>"}`)
		return
	}
	if s.Setup.Run == nil {
		fail(w, http.StatusServiceUnavailable, "this daemon can't run setups")
		return
	}
	// Reserve the remote before probing, so two requests can't both start.
	s.Setup.mu.Lock()
	if j := s.Setup.jobs[name]; (j != nil && j.snapshot().State == "running") || s.Setup.starting[name] {
		s.Setup.mu.Unlock()
		fail(w, http.StatusConflict, "a setup runs on this remote already")
		return
	}
	if s.Setup.starting == nil {
		s.Setup.starting = map[string]bool{}
	}
	s.Setup.starting[name] = true
	s.Setup.mu.Unlock()
	defer func() {
		s.Setup.mu.Lock()
		delete(s.Setup.starting, name)
		s.Setup.mu.Unlock()
	}()
	p, rep, t, err := s.setupPlan(r.Context(), name, req.Kind)
	switch {
	case errors.Is(err, errNoRemote):
		fail(w, http.StatusNotFound, err.Error())
		return
	case err != nil:
		failErr(w, http.StatusBadGateway, err)
		return
	case p.Error != "":
		fail(w, http.StatusConflict, p.Error)
		return
	case p.ID != req.Plan:
		// The machine changed since the review: show the new plan instead.
		fail(w, http.StatusConflict, "the plan changed since it was shown; review it again")
		return
	case p.SudoPassword && req.SudoPassword == "":
		fail(w, http.StatusBadRequest, remote.ErrPasswordNeeded.Error())
		return
	}
	password := req.SudoPassword
	if !p.SudoPassword {
		password = "" // nothing asks for it
	}
	j := &setupJob{job: SetupJob{Kind: req.Kind, State: "running", Started: time.Now().UTC()}}
	s.Setup.mu.Lock()
	if s.Setup.jobs == nil {
		s.Setup.jobs = map[string]*setupJob{}
	}
	s.Setup.jobs[name] = j
	s.Setup.mu.Unlock()
	go func() {
		// Not the request's context: closing the window doesn't stop a setup halfway.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Minute)
		defer cancel()
		run := func(ctx context.Context, script string) ([]byte, error) { return s.Setup.Run(ctx, t, script) }
		err := remote.RunBootstrap(ctx, run, rep, p.Steps, j, password)
		// What the machine is now, saved before the job reads as done: a
		// client that sees it finished then finds the new state.
		if rep, perr := s.Probe(ctx, t); perr == nil {
			s.mu.Lock()
			if i := s.index(name); i >= 0 {
				s.remotes[i].Report, s.remotes[i].Next, s.remotes[i].Checked = rep, rep.Next(), time.Now().UTC()
				_ = s.save()
			}
			s.mu.Unlock()
		}
		j.mu.Lock()
		j.job.Finished = time.Now().UTC()
		if err != nil {
			j.job.State, j.job.Error = "failed", err.Error()
		} else {
			j.job.State = "succeeded"
		}
		j.mu.Unlock()
	}()
	writeJSON(w, http.StatusAccepted, j.snapshot())
}
