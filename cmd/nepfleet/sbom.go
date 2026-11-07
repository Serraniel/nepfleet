// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Serraniel/neptide/pkg/registry"

	"github.com/Serraniel/nepfleet/internal/lock"
	"github.com/Serraniel/nepfleet/internal/sbom"
)

// syftContents looks into an image with Syft, which has to be installed.
func syftContents(ctx context.Context, ref string) ([]sbom.Component, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "syft", "--quiet", "registry:"+ref, "-o", "cyclonedx-json") //nolint:gosec // an image reference from the fleet, as one argument
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("syft: %w", err)
	}
	return sbom.ParseComponents(out)
}

// imagesOf turns a stack's image references and pins into what a document lists.
func imagesOf(refs []string, pins *lock.Lock) []sbom.Image {
	var out []sbom.Image
	for _, ref := range refs {
		parsed, err := registry.ParseRef(ref)
		if err != nil {
			out = append(out, sbom.Image{Repository: ref})
			continue
		}
		digest := parsed.Digest
		if digest == "" && pins != nil {
			digest = pins.Images[ref]
		}
		out = append(out, sbom.Image{Repository: parsed.Name(), Tag: parsed.Tag, Digest: digest})
	}
	return out
}

// cmdSBOM: nepfleet sbom [stack...] [--out DIR] [--contents]. It only reads.
func cmdSBOM(args []string, stdout, stderr io.Writer) error {
	var c common
	fs := flag.NewFlagSet("sbom", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c.register(fs)
	out := fs.String("out", "", "write one <stack>.cdx.json per stack and fleet.cdx.json here, instead of printing the aggregate")
	contents := fs.Bool("contents", false, "also list what's inside each image, with Syft (cached by digest)")
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
	var look sbom.Contents
	if *contents {
		if _, err := exec.LookPath("syft"); err != nil {
			return fmt.Errorf("--contents needs Syft installed: %w", err)
		}
		look = sbom.CachedContents(filepath.Join(e.stateDir, "sbom"), syftContents)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	now := time.Now()
	docs := map[string]*sbom.Document{}
	failed := 0
	for _, s := range stacks {
		p, err := e.prepare(s)
		if err != nil {
			fmt.Fprintf(stderr, "%v\n", err)
			failed++
			continue
		}
		refs, _ := composeImages(p.out)
		pins, _ := lock.Load(s.Dir)
		images := imagesOf(refs, pins)
		for _, i := range images {
			if i.Digest == "" {
				fmt.Fprintf(stderr, "%s: %s has no pinned digest, so this can't say exactly what runs\n", s.Metadata.Name, i.Reference())
			}
		}
		doc, err := sbom.ForStack(ctx, s.Metadata.Name, images, look, now)
		if err != nil {
			fmt.Fprintf(stderr, "%s: ⚠ listed without its contents: %v\n", s.Metadata.Name, err)
			failed++
		}
		docs[s.Metadata.Name] = doc
	}
	all := sbom.Aggregate(e.host, docs, now)
	write := func(w io.Writer, doc *sbom.Document) error {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false) // package URLs hold &
		return enc.Encode(doc)
	}
	if *out == "" {
		if err := write(stdout, all); err != nil {
			return err
		}
	} else {
		if err := os.MkdirAll(*out, 0o750); err != nil {
			return err
		}
		docs["fleet"] = all
		for name, doc := range docs {
			f, err := os.Create(filepath.Join(*out, name+".cdx.json")) //nolint:gosec // a stack's checked name
			if err != nil {
				return err
			}
			err = write(f, doc)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		}
		fmt.Fprintf(stdout, "wrote %d documents to %s\n", len(docs), *out)
	}
	if failed > 0 {
		return fmt.Errorf("%d stack(s) are missing or incomplete", failed)
	}
	return nil
}
