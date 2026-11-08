// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/Serraniel/neptide/pkg/registry"

	"github.com/Serraniel/nepfleet/internal/lock"
	"github.com/Serraniel/nepfleet/internal/notify"
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

// errNoAttestation: the image's build attached no SBOM for this platform.
var errNoAttestation = errors.New("no SBOM attestation")

// attestedContents reads the SBOM an image's build attached, for this host's
// platform. It downloads only the attestation, not the image.
func attestedContents(ctx context.Context, ref string) ([]sbom.Component, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "buildx", "imagetools", "inspect", ref, "--format", "{{json .SBOM}}").Output() //nolint:gosec // an image reference from the fleet, as one argument
	if err != nil {
		return nil, fmt.Errorf("reading attestations: %w", err)
	}
	found, ok, err := sbom.ParseAttestation(out, "linux/"+runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errNoAttestation
	}
	return found, nil
}

// imageContents uses what an image's build attested, and looks inside with
// Syft only where it attested nothing (if Syft is there).
func imageContents(attested, scan sbom.Contents, haveScanner bool) sbom.Contents {
	return func(ctx context.Context, ref string) ([]sbom.Component, error) {
		found, err := attested(ctx, ref)
		if err == nil {
			return found, nil
		}
		if !haveScanner {
			return nil, fmt.Errorf("%w, and Syft isn't installed to look inside", err)
		}
		return scan(ctx, ref)
	}
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

// reportAdvisories prints what's known about a document's packages. Not knowing is
// an error, so it can't read as clean.
func (e *env) reportAdvisories(ctx context.Context, doc *sbom.Document, notifyNew bool, stdout, stderr io.Writer) error {
	found, unchecked, err := (&sbom.OSV{URL: sbom.OSVURL}).Check(ctx, doc)
	if err != nil {
		return fmt.Errorf("checking advisories: %w", err)
	}
	for _, image := range unchecked {
		fmt.Fprintf(stderr, "⚠ %s wasn't checked: nothing is known about what's inside it\n", image)
	}
	if len(found) == 0 {
		fmt.Fprintln(stdout, "no known vulnerabilities in the images that were checked")
	}
	for _, a := range found {
		fixed := "no fix known"
		if a.Fixed != "" {
			fixed = "fixed in " + a.Fixed
		}
		fmt.Fprintf(stdout, "%s: %s %s %s (%s, %s)\n", a.Image, a.ID, a.Severity, a.Package, a.Summary, fixed)
	}
	if !notifyNew {
		return nil
	}
	return e.notifyAdvisories(found, stdout)
}

// notifyAdvisories reports new advisories per image, once each.
func (e *env) notifyAdvisories(found []sbom.Advisory, stdout io.Writer) error {
	path := filepath.Join(e.stateDir, "sbom", "notified.json")
	seen := map[string]bool{}
	if raw, err := os.ReadFile(path); err == nil { //nolint:gosec // the installation's own state directory
		if json.Unmarshal(raw, &seen) != nil {
			seen = map[string]bool{} // damaged: report again rather than stay quiet
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fresh := map[string][]string{}
	now := map[string]bool{}
	for _, a := range found {
		key := a.Image + " " + a.ID
		now[key] = true
		if !seen[key] {
			line := a.ID + " in " + a.Package
			if a.Fixed != "" {
				line += ", fixed in " + a.Fixed
			}
			fresh[a.Image] = append(fresh[a.Image], line)
		}
	}
	n, err := e.notifier()
	if err != nil {
		return err
	}
	const maxLines = 20
	images := make([]string, 0, len(fresh))
	for image := range fresh {
		images = append(images, image)
	}
	slices.Sort(images)
	for _, image := range images {
		lines := fresh[image]
		count := len(lines)
		if count > maxLines {
			lines = append(lines[:maxLines:maxLines], fmt.Sprintf("and %d more", count-maxLines))
		}
		e.emit(n, stdout, notify.Event{Name: "security.vulnerable_image", Severity: notify.Warning,
			Title: fmt.Sprintf("%s has %d new known vulnerabilities", image, count), Message: strings.Join(lines, "\n")})
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(now)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

// cmdSBOM: nepfleet sbom [stack...] [--out DIR] [--contents]. It only reads.
func cmdSBOM(args []string, stdout, stderr io.Writer) error {
	var c common
	fs := flag.NewFlagSet("sbom", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c.register(fs)
	out := fs.String("out", "", "write one <stack>.cdx.json per stack and fleet.cdx.json here, instead of printing the aggregate")
	contents := fs.Bool("contents", false, "also list what's inside each image: from its build's SBOM attestation, else with Syft if installed (cached by digest)")
	advisories := fs.Bool("advisories", false, "look up known vulnerabilities at osv.dev instead of printing the document; sends package names and versions, and implies --contents")
	notifyNew := fs.Bool("notify", false, "with --advisories: send new ones to the fleet's notification channels")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *advisories {
		*contents = true
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
		_, err := exec.LookPath("syft")
		look = sbom.CachedContents(filepath.Join(e.stateDir, "sbom"), imageContents(attestedContents, syftContents, err == nil))
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
	if *advisories {
		if err := e.reportAdvisories(ctx, all, *notifyNew, stdout, stderr); err != nil {
			return err
		}
	}
	if *out == "" {
		if !*advisories {
			if err := write(stdout, all); err != nil {
				return err
			}
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
