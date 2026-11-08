// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os/signal"
	"syscall"
	"time"

	"github.com/Serraniel/neptide/pkg/registry"
	track "github.com/Serraniel/neptide/pkg/version"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/lock"
)

// imageUpdate is what the registry has for one image of a stack.
type imageUpdate = api.ImageUpdate

// cmdUpdates reports image updates for the stacks on this host: within each
// image's track (templates: spec.images; plain stacks follow digests),
// beyond it, and tags rebuilt since lock.yaml pinned them. Read-only.
func cmdUpdates(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "apply" {
		return cmdUpdatesApply(args[1:], stdout, stderr)
	}
	var c common
	fs := flag.NewFlagSet("updates", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c.register(fs)
	asJSON := fs.Bool("json", false, "print the result as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := c.open()
	if err != nil {
		return err
	}
	stacks, err := e.stacks(fs.Args())
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	e.resolverOnce()
	var all []imageUpdate
	for _, s := range stacks {
		found, err := e.stackUpdates(ctx, s)
		if err != nil {
			fmt.Fprintf(stderr, "%v\n", err)
			continue
		}
		all = append(all, found...)
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(all)
	}
	printUpdates(stdout, all)
	return nil
}

// stackUpdates is what the registry has for each image of a stack.
func (e *env) stackUpdates(ctx context.Context, s *fleet.Stack) ([]imageUpdate, error) {
	p, err := e.prepare(s)
	if err != nil {
		return nil, err
	}
	tracks := map[string]track.Track{}
	if p.tpl != nil {
		for _, img := range p.tpl.Spec.Images {
			if r, err := registry.ParseRef(img.Name); err == nil {
				tracks[r.Name()] = track.Track(img.Track)
			}
		}
	}
	pins, _ := lock.Load(s.Dir)
	images, _ := composeImages(p.out)
	var out []imageUpdate
	for _, img := range images {
		out = append(out, e.checkImage(ctx, s.Metadata.Name, img, tracks, pins))
	}
	return out, nil
}

func (e *env) checkImage(ctx context.Context, stack, img string, tracks map[string]track.Track, pins *lock.Lock) imageUpdate {
	u := imageUpdate{Stack: stack, Image: img, Track: string(track.Digest)}
	ref, err := registry.ParseRef(img)
	if err != nil || ref.Digest != "" {
		u.Error = "pinned by digest in the compose file"
		return u
	}
	u.Image, u.Current = ref.Name(), ref.Tag
	tr, ok := tracks[ref.Name()]
	if ok {
		u.Track = string(tr)
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if tags, err := e.reg.Tags(cctx, ref); err == nil {
		next, newest := track.Next(ref.Tag, tags, track.Track(u.Track))
		if next != "" && next != ref.Tag {
			u.Next = next
		}
		if newest != "" && newest != next && newest != ref.Tag {
			u.Beyond = newest
		}
	}
	if pinned := pins.Images[img]; pinned != "" {
		d, err := e.reg.Digest(cctx, ref)
		switch {
		case err != nil:
			u.Error = err.Error()
		case d != pinned:
			u.Rebuilt = true
		}
	}
	return u
}

func printUpdates(w io.Writer, all []imageUpdate) {
	found := false
	for _, u := range all {
		var notes []string
		if u.Next != "" {
			notes = append(notes, fmt.Sprintf("%s → %s within track %s (comes with a template update)", u.Current, u.Next, u.Track))
		}
		if u.Rebuilt {
			notes = append(notes, fmt.Sprintf(":%s was rebuilt upstream (nepfleet apply --update-lock %s)", u.Current, u.Stack))
		}
		if u.Beyond != "" {
			notes = append(notes, fmt.Sprintf("%s is out, beyond track %s (information only)", u.Beyond, u.Track))
		}
		if u.Error != "" && u.Error != "pinned by digest in the compose file" {
			notes = append(notes, "⚠ "+u.Error)
		}
		if len(notes) == 0 {
			continue
		}
		found = true
		fmt.Fprintf(w, "%s: %s\n", u.Stack, u.Image)
		for _, n := range notes {
			fmt.Fprintf(w, "  %s\n", n)
		}
	}
	if !found {
		fmt.Fprintln(w, "all images are current")
	}
}
