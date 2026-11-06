// SPDX-License-Identifier: AGPL-3.0-or-later

package deploy

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Serraniel/neptide/pkg/engine"
	"github.com/Serraniel/neptide/pkg/snapshot"
)

// fakeEngine is enough Docker for DataSafety and the snapshotter.
type fakeEngine struct {
	cts               []engine.ContainerSummary
	stopped, started  []string
	failCreateVolume  bool
	removed, createdV []string
}

func (f *fakeEngine) AllContainers(context.Context) ([]engine.ContainerSummary, error) {
	return f.cts, nil
}
func (f *fakeEngine) Containers(context.Context, ...string) ([]engine.ContainerSummary, error) {
	return nil, nil // nothing else runs
}
func (f *fakeEngine) Inspect(_ context.Context, id string) (*engine.Container, error) {
	ct := &engine.Container{ID: id, Name: "/" + id}
	ct.Mounts = []engine.Mount{{Type: "volume", Name: "data", Destination: "/data", RW: true}}
	return ct, nil
}
func (f *fakeEngine) Stop(_ context.Context, id string, _ time.Duration) error {
	f.stopped = append(f.stopped, id)
	return nil
}
func (f *fakeEngine) Start(_ context.Context, id string) error {
	f.started = append(f.started, id)
	return nil
}
func (f *fakeEngine) InspectVolume(_ context.Context, name string) (*engine.Volume, error) {
	return &engine.Volume{Name: name, Driver: "local"}, nil
}
func (f *fakeEngine) RemoveVolume(_ context.Context, name string) error {
	f.removed = append(f.removed, name)
	return nil
}
func (f *fakeEngine) Pull(context.Context, string, *engine.RegistryAuth) error { return nil }
func (f *fakeEngine) Create(context.Context, string, map[string]any, map[string]any, map[string]map[string]any) (string, error) {
	return "helper", nil
}
func (f *fakeEngine) Wait(context.Context, string) (int, error) { return 0, nil }
func (f *fakeEngine) Remove(context.Context, string) error      { return nil }
func (f *fakeEngine) Volumes(context.Context, ...string) ([]engine.Volume, error) {
	return nil, nil
}
func (f *fakeEngine) CreateVolume(_ context.Context, name string, _ map[string]string) error {
	if f.failCreateVolume {
		return errors.New("disk full")
	}
	f.createdV = append(f.createdV, name)
	return nil
}

func TestTakeFailureStartsTheStackAgain(t *testing.T) {
	f := &fakeEngine{
		cts: []engine.ContainerSummary{
			{ID: "app", State: "running", Labels: map[string]string{"com.docker.compose.project": "web"}},
			{ID: "job", State: "exited", Labels: map[string]string{"com.docker.compose.project": "web"}},
			{ID: "other", State: "running", Labels: map[string]string{"com.docker.compose.project": "else"}},
		},
		failCreateVolume: true,
	}
	d := &DataSafety{Snapshots: &snapshot.Snapshotter{Engine: f, Now: time.Now}, Engine: f}
	if _, err := d.take(context.Background(), "web"); err == nil {
		t.Fatal("a failing snapshot must fail the apply")
	}
	if !slices.Equal(f.stopped, []string{"app"}) || !slices.Equal(f.started, []string{"app"}) {
		t.Errorf("stopped %v, started %v: only the stack's running container, and it must run again", f.stopped, f.started)
	}
}

func TestStopAllStopsServicesOfTheFailedVersion(t *testing.T) {
	f := &fakeEngine{cts: []engine.ContainerSummary{
		{ID: "app", State: "running", Labels: map[string]string{"com.docker.compose.project": "web"}},
		{ID: "new-worker", State: "running", Labels: map[string]string{"com.docker.compose.project": "web"}},
	}}
	g := &guard{d: &DataSafety{Engine: f}, stack: "web", snaps: []*snapshot.Snapshot{{Container: "app"}}}
	if err := g.stopAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.stopped, []string{"app", "new-worker"}) {
		t.Errorf("stopped %v", f.stopped)
	}
	g.snaps = []*snapshot.Snapshot{{Items: []snapshot.Item{{Volume: "neptide-snap-app-1"}}}}
	g.discard(context.Background())
	if !slices.Equal(f.removed, []string{"neptide-snap-app-1"}) || g.snaps != nil {
		t.Errorf("discard removed %v", f.removed)
	}
}
