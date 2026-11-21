// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/render"
	"github.com/Serraniel/nepfleet/internal/template"
)

// lockWait is how long a change through the API waits for an apply that
// holds the lock (an automatic one after an update can take minutes) before
// it answers busy.
const lockWait = 2 * time.Minute

// lockAPI takes the API lock, waiting up to lockWait or until ctx ends.
func lockAPI(ctx context.Context) bool {
	deadline := time.Now().Add(lockWait)
	for {
		if apiMu.TryLock() {
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// stackTemplateInputs loads a stack and the template it renders from.
func stackTemplateInputs(e *env, stack string) (*fleet.Stack, *template.Template, error) {
	s, err := e.fleet.LoadStack(stack)
	if err != nil {
		return nil, nil, err
	}
	tpl, err := e.pinnedTemplate(s)
	return s, tpl, err
}

// stackInputsAPI is GET …/inputs: the template's inputs, whether each is set,
// and the values of the ones that aren't secret.
func stackInputsAPI(c common, stack string) ([]api.StackInput, error) {
	e, err := c.open()
	if err != nil {
		return nil, err
	}
	s, tpl, err := stackTemplateInputs(e, stack)
	if err != nil || tpl == nil {
		return nil, err
	}
	secrets, err := e.store.Load(filepath.Join(s.Dir, fleet.SecretsFile))
	if err != nil {
		return nil, err
	}
	out := make([]api.StackInput, 0, len(tpl.Spec.Inputs))
	for _, in := range tpl.Spec.Inputs {
		v := s.Spec.Values[in.Name]
		_, inSecrets := secrets[in.Name]
		set := v.Literal != "" || v.From != "" || inSecrets
		si := api.StackInput{
			Name: in.Name, Type: in.Type, Title: in.Title, Description: in.Description,
			Required: in.Required, Default: in.Default, Options: in.Options, Pattern: in.Pattern,
			Secret: in.Type == "secret", Set: set,
			Missing: in.Required && in.Default == "" && !set,
		}
		if !si.Secret {
			si.Value = v.Literal
		}
		out = append(out, si)
	}
	return out, nil
}

// setInputsAPI is PUT …/inputs: checks each value against its input, writes
// secret ones encrypted into values.secret.yaml and the others into
// stack.yaml, and commits both as by. An empty value leaves an input as it is.
// Values never reach a log or an error.
func setInputsAPI(ctx context.Context, c common, stack string, values map[string]string, by string) (string, error) {
	if !lockAPI(ctx) {
		return "", api.ErrBusy
	}
	defer apiMu.Unlock()
	e, err := c.open()
	if err != nil {
		return "", err
	}
	s, tpl, err := stackTemplateInputs(e, stack)
	if err != nil {
		return "", err
	}
	if tpl == nil {
		return "", api.InvalidError{Reason: stack + " isn't made from a template: edit its files instead"}
	}
	plain, secret := map[string]string{}, map[string]string{}
	var names, bad []string
	for k, v := range values {
		if v == "" {
			continue
		}
		i := slices.IndexFunc(tpl.Spec.Inputs, func(in template.Input) bool { return in.Name == k })
		if i < 0 {
			return "", api.InvalidError{Reason: k + " is not an input of template " + tpl.Metadata.Name}
		}
		in := tpl.Spec.Inputs[i]
		if err := render.CheckInput(in, v); err != nil {
			bad = append(bad, fmt.Sprintf("%s: %v", k, err))
			continue
		}
		if in.Type == "secret" {
			secret[k] = v
		} else {
			plain[k] = v
		}
		names = append(names, k)
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		return "", api.InvalidError{Reason: strings.Join(bad, "; ")}
	}
	if len(names) == 0 {
		return "", api.InvalidError{Reason: "no value to set"}
	}
	sort.Strings(names)

	stackFile := filepath.Join(s.Dir, "stack.yaml")
	secretFile := filepath.Join(s.Dir, fleet.SecretsFile)
	paths := []string{relTo(e.fleet.Dir, stackFile), relTo(e.fleet.Dir, secretFile)}
	st, err := gitOut(e.fleet.Dir, append([]string{"status", "--porcelain", "--"}, paths...)...)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(st) != "" {
		return "", api.ErrUncommitted
	}
	// Put both files back as they were if anything fails.
	type saved struct {
		path string
		raw  []byte
		err  error
	}
	var before []saved
	for _, p := range []string{stackFile, secretFile} {
		raw, err := os.ReadFile(p) //nolint:gosec // paths inside the fleet repository
		before = append(before, saved{p, raw, err})
	}
	restore := func() {
		for _, b := range before {
			if b.err != nil {
				_ = os.Remove(b.path)
				continue
			}
			_ = os.WriteFile(b.path, b.raw, 0o600) //nolint:gosec // the same path, as it was
		}
	}
	var changed []string
	if len(plain) > 0 {
		out, err := setValues(before[0].raw, plain)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(stackFile, out, 0o600); err != nil {
			restore()
			return "", err
		}
		changed = append(changed, paths[0])
	}
	if len(secret) > 0 {
		vals, err := e.store.Load(secretFile)
		if err != nil {
			restore()
			return "", err
		}
		for k, v := range secret {
			vals[k] = v
		}
		if err := e.store.Save(secretFile, vals); err != nil {
			restore()
			return "", err
		}
		changed = append(changed, paths[1])
	}
	if _, err := gitOut(e.fleet.Dir, append([]string{"add", "--"}, changed...)...); err != nil {
		restore()
		return "", err
	}
	msg := fmt.Sprintf("chore(%s): set %s", stack, strings.Join(names, ", "))
	if err := commitAs(e.fleet.Dir, apiAuthor(by), msg, changed...); err != nil {
		_, _ = gitOut(e.fleet.Dir, append([]string{"reset", "-q", "--"}, changed...)...)
		restore()
		return "", err
	}
	head, err := gitOut(e.fleet.Dir, "rev-parse", "HEAD")
	return strings.TrimSpace(head), err
}
