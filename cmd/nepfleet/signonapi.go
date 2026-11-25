// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/template"
)

// signOnAPI is GET /api/v1/signon: the fleet's provider, the stacks that
// could be one, and how each stack that supports sign-on signs in.
func signOnAPI(c common) (api.SignOnSettings, error) {
	// Templates may come from a source that has to be fetched.
	if !apiMu.TryLock() {
		return api.SignOnSettings{}, api.ErrBusy
	}
	defer apiMu.Unlock()
	e, err := c.open()
	if err != nil {
		return api.SignOnSettings{}, err
	}
	all, err := e.fleet.Stacks()
	if err != nil {
		return api.SignOnSettings{}, err
	}
	out := api.SignOnSettings{Kinds: slices.Sorted(maps.Keys(providers)), Candidates: []string{}, Stacks: []api.StackSignOnMode{}}
	if p := e.fleet.Spec.SignOn; p != nil {
		out.Provider = p.Provider
	}
	for _, s := range all {
		name := s.Metadata.Name
		if s.Spec.Template != nil {
			if _, ok := providers[s.Spec.Template.Name]; ok {
				out.Candidates = append(out.Candidates, name)
			}
		}
		explicit := s.Spec.SignOn != nil && s.Spec.SignOn.Mode != ""
		// A template that can't be loaded now counts as not supporting it.
		tpl, _ := e.pinnedTemplate(s)
		oidc := tpl != nil && tpl.Spec.OIDC != nil
		if oidc || explicit {
			out.Stacks = append(out.Stacks, api.StackSignOnMode{Name: name, Mode: fleet.SignOnMode(e.fleet, s, oidc), Explicit: explicit})
		}
	}
	return out, nil
}

// setSignOnAPI is PUT /api/v1/signon: fleet.yaml's spec.signOn.provider,
// a stack running a template NepFleet can register clients in.
func setSignOnAPI(c common, provider, by string) (string, error) {
	return changeChannels(c, by, "chore(fleet): sign in through "+provider, func(e *env, _ map[string]string) (bool, error) {
		ps, err := e.fleet.LoadStack(provider)
		if err != nil {
			return false, api.InvalidError{Reason: "there is no stack " + provider}
		}
		kind := ""
		if ps.Spec.Template != nil {
			kind = ps.Spec.Template.Name
		}
		if _, ok := providers[kind]; !ok {
			return false, api.InvalidError{Reason: fmt.Sprintf("%s runs %q, not a provider NepFleet can register clients in (%s)",
				provider, kind, strings.Join(slices.Sorted(maps.Keys(providers)), ", "))}
		}
		if p := e.fleet.Spec.SignOn; p != nil && p.Provider == provider {
			return false, api.InvalidError{Reason: provider + " is the provider already"}
		}
		if err := fleet.SetSignOnProvider(e.fleet.Dir, provider); err != nil {
			return false, api.InvalidError{Reason: cleanReason(err, e.fleet.Dir)}
		}
		return false, nil
	})
}

// removeSignOnAPI is DELETE /api/v1/signon. Stacks in mode integrated by
// default fall back to none; ones that set integrated fail to plan until
// they change it. Clients stay in the provider (docs/sign-on.md).
func removeSignOnAPI(c common, by string) (string, error) {
	return changeChannels(c, by, "chore(fleet): remove the sign-on provider", func(e *env, _ map[string]string) (bool, error) {
		found, err := fleet.RemoveSignOn(e.fleet.Dir)
		if err == nil && !found {
			err = api.ErrNoSignOn
		}
		return false, err
	})
}

// stackSignOnAPI is GET /api/v1/stacks/{name}/signon.
func stackSignOnAPI(c common, stack string) (api.StackSignOn, error) {
	e, err := c.open()
	if err != nil {
		return api.StackSignOn{}, err
	}
	s, err := e.fleet.LoadStack(stack)
	if err != nil {
		return api.StackSignOn{}, api.ErrNotFound
	}
	tpl, err := e.pinnedTemplate(s)
	if err != nil {
		return api.StackSignOn{}, err
	}
	out := api.StackSignOn{Supported: tpl != nil && tpl.Spec.OIDC != nil}
	out.Mode = fleet.SignOnMode(e.fleet, s, out.Supported)
	if so := s.Spec.SignOn; so != nil {
		out.Set, out.Issuer, out.ClientID = so.Mode, so.Issuer, so.ClientID
	}
	if p := e.fleet.Spec.SignOn; p != nil {
		out.Provider = p.Provider
	}
	keys, err := secretKeys(c, stack)
	if err != nil {
		return api.StackSignOn{}, err
	}
	set := slices.Contains(keys, template.OIDCClientSecret)
	out.SecretSet = &set
	return out, nil
}

// checkStackSignOn refuses a mode the stack can't use, before it's saved:
// planning would fail on it.
func checkStackSignOn(e *env, stack string, tpl *template.Template, mode string) error {
	if mode == "" || mode == fleet.SignOnNone {
		return nil
	}
	if tpl == nil || tpl.Spec.OIDC == nil {
		return api.InvalidError{Reason: "its template doesn't support sign-on"}
	}
	if mode != fleet.SignOnIntegrated {
		return nil
	}
	p := e.fleet.Spec.SignOn
	if p == nil {
		return api.InvalidError{Reason: "integrated sign-on needs a provider in fleet.yaml first"}
	}
	if p.Provider == stack {
		return api.InvalidError{Reason: stack + " is the fleet's provider and can't sign in through itself"}
	}
	return nil
}

// setStackSignOnAPI is PUT /api/v1/stacks/{name}/signon: stack.yaml's
// spec.signOn, committed as by; mode "" removes it. It doesn't apply.
func setStackSignOnAPI(c common, stack string, u api.StackSignOnUpdate, by string) (string, error) {
	if !apiMu.TryLock() {
		return "", api.ErrBusy
	}
	defer apiMu.Unlock()
	e, err := c.open()
	if err != nil {
		return "", err
	}
	s, err := e.fleet.LoadStack(stack)
	if err != nil {
		return "", api.ErrNotFound
	}
	tpl, err := e.pinnedTemplate(s)
	if err != nil {
		return "", err
	}
	if err := checkStackSignOn(e, stack, tpl, u.Mode); err != nil {
		return "", err
	}
	path := filepath.Join(s.Dir, "stack.yaml")
	rel := relTo(e.fleet.Dir, path)
	st, err := gitOut(e.fleet.Dir, "status", "--porcelain", "--", rel)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(st) != "" {
		return "", api.ErrUncommitted
	}
	raw, err := os.ReadFile(path) //nolint:gosec // a stack's own file
	if err != nil {
		return "", err
	}
	out, err := putStackSignOn(raw, u)
	if err != nil {
		return "", api.InvalidError{Reason: "stack.yaml: " + err.Error()}
	}
	if bytes.Equal(out, raw) {
		head, err := gitOut(e.fleet.Dir, "rev-parse", "HEAD")
		return strings.TrimSpace(head), err
	}
	if err := os.WriteFile(path, out, 0o600); err != nil { //nolint:gosec // a stack's own file
		return "", err
	}
	restore := func() { _ = os.WriteFile(path, raw, 0o600) } //nolint:gosec // as it was
	if _, err := e.fleet.LoadStack(stack); err != nil {
		restore()
		return "", api.InvalidError{Reason: cleanReason(err, e.fleet.Dir)}
	}
	msg := "chore(" + stack + "): sign-on " + u.Mode
	if u.Mode == "" {
		msg = "chore(" + stack + "): default sign-on"
	}
	if err := commitAs(e.fleet.Dir, apiAuthor(by), msg, rel); err != nil {
		restore()
		return "", err
	}
	head, err := gitOut(e.fleet.Dir, "rev-parse", "HEAD")
	return strings.TrimSpace(head), err
}

// signOnNode is spec.signOn as stack.yaml holds it, empty fields left out.
func signOnNode(mode, issuer, clientID string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode}
	for _, kv := range [][2]string{{"mode", mode}, {"issuer", issuer}, {"clientID", clientID}} {
		if kv[1] != "" {
			n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: kv[0]}, &yaml.Node{Kind: yaml.ScalarNode, Value: kv[1]})
		}
	}
	return n
}

// putStackSignOn sets (or, with mode "", removes) spec.signOn of a
// stack.yaml, keeping the rest and its comments.
func putStackSignOn(raw []byte, u api.StackSignOnUpdate) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, errors.New("stack.yaml is empty")
	}
	spec, err := mappingChild(doc.Content[0], "spec", yaml.MappingNode)
	if err != nil {
		return nil, err
	}
	i := -1
	for k := 0; k+1 < len(spec.Content); k += 2 {
		if spec.Content[k].Value == "signOn" {
			i = k
		}
	}
	switch {
	case u.Mode == "" && i < 0:
		return raw, nil
	case u.Mode == "":
		spec.Content = slices.Delete(spec.Content, i, i+2)
	case i < 0:
		spec.Content = append(spec.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "signOn"}, signOnNode(u.Mode, u.Issuer, u.ClientID))
	default:
		spec.Content[i+1] = signOnNode(u.Mode, u.Issuer, u.ClientID)
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
