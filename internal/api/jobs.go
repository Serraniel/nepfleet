// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"time"
)

// Errors StartApply returns before a job starts (409 Conflict).
var (
	ErrBusy         = errors.New("another apply is running on this backend; try again when it is done")
	ErrPlanChanged  = errors.New("the plan changed since you reviewed it; review the new plan and apply that")
	ErrNeedsConfirm = errors.New("the plan has destructive changes; confirm them to apply")
	ErrNotInHistory = errors.New("that commit is not in this stack's history")
)

// Job is an apply running or done in the background.
type Job struct {
	ID       string    `json:"id"`
	Stack    string    `json:"stack"`
	Kind     string    `json:"kind"`  // apply, rollback, updates (no stack: the fleet)
	State    string    `json:"state"` // running, succeeded, failed
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished,omitzero"`
	Error    string    `json:"error,omitempty"`
	Log      string    `json:"log"`
	By       string    `json:"by"` // the token's name
}

// jobLog is the output of a running job, readable while it is written.
type jobLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// maxLog bounds a job's log; an apply prints a plan and progress lines.
const maxLog = 1 << 20

func (l *jobLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if room := maxLog - l.buf.Len(); room > 0 {
		l.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (l *jobLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

type jobEntry struct {
	mu  sync.Mutex
	job Job
	log *jobLog
}

// jobs keeps the last jobs in memory; a restart forgets them (the outcome
// of an apply is in the stack's state and the notifications anyway).
type jobs struct {
	mu    sync.Mutex
	order []string
	byID  map[string]*jobEntry
}

const keepJobs = 50

func newJobs() *jobs { return &jobs{byID: map[string]*jobEntry{}} }

// start runs fn in the background as a new job.
func (j *jobs) start(kind, stack, by string, fn func(log *jobLog) error) Job {
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	e := &jobEntry{log: &jobLog{}, job: Job{ID: hex.EncodeToString(id), Stack: stack, Kind: kind, State: "running", Started: time.Now().UTC(), By: by}}
	j.mu.Lock()
	j.byID[e.job.ID] = e
	j.order = append(j.order, e.job.ID)
	if len(j.order) > keepJobs {
		delete(j.byID, j.order[0])
		j.order = j.order[1:]
	}
	j.mu.Unlock()
	go func() {
		err := func() (err error) {
			// A bug in a job must not take the server down mid-deploy.
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("the job crashed: %v", r)
					log.Printf("api: job %s crashed: %v\n%s", e.job.ID, r, debug.Stack())
				}
			}()
			return fn(e.log)
		}()
		e.mu.Lock()
		defer e.mu.Unlock()
		e.job.Finished = time.Now().UTC()
		if err != nil {
			e.job.State, e.job.Error = "failed", err.Error()
		} else {
			e.job.State = "succeeded"
		}
	}()
	return e.snapshot()
}

func (e *jobEntry) snapshot() Job {
	e.mu.Lock()
	defer e.mu.Unlock()
	j := e.job
	j.Log = e.log.String()
	return j
}

func (j *jobs) get(id string) (Job, bool) {
	j.mu.Lock()
	e, ok := j.byID[id]
	j.mu.Unlock()
	if !ok {
		return Job{}, false
	}
	return e.snapshot(), true
}
