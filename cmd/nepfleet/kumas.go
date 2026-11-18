// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"cmp"
	"path/filepath"
	"slices"

	"github.com/Serraniel/nepfleet/internal/fleet"
)

// kumaInstance is an Uptime Kuma NepFleet opens maintenance windows in and
// lists them from, with its password.
type kumaInstance struct {
	fleet.KumaInstance
	password string
}

// kumas lists the Uptime Kumas of this host: those fleet.yaml names
// (spec.uptimeKuma), and the uptime-kuma stacks planned here that it
// doesn't name. A stack's Kuma is reached on the NepFleet API network, which
// the template joins, by its container's name, with the admin user the stack
// set it up with.
func (e *env) kumas() ([]kumaInstance, error) {
	var out []kumaInstance
	if len(e.fleet.Spec.UptimeKuma) > 0 {
		secrets, err := e.store.Load(filepath.Join(e.fleet.Dir, fleet.FleetSecretsFile))
		if err != nil {
			return nil, err
		}
		for _, k := range e.fleet.Spec.UptimeKuma {
			out = append(out, kumaInstance{KumaInstance: k, password: secrets[k.PasswordSecret]})
		}
	}
	all, err := e.fleet.Stacks()
	if err != nil {
		return nil, err
	}
	for _, s := range all {
		t := s.Spec.Template
		if t == nil || t.Name != "uptime-kuma" || (s.Spec.Target != e.host && s.Spec.Target != "local") {
			continue
		}
		name := s.Metadata.Name
		if slices.ContainsFunc(out, func(k kumaInstance) bool { return k.Name == name }) {
			continue
		}
		secrets, err := e.store.Load(filepath.Join(s.Dir, fleet.SecretsFile))
		if err != nil {
			return nil, err
		}
		out = append(out, kumaInstance{
			KumaInstance: fleet.KumaInstance{
				Name:           name,
				URL:            "http://" + name + "-uptime-kuma-1:3001",
				Username:       cmp.Or(s.Spec.Values["KUMA_USERNAME"].Literal, "admin"),
				RollingUpdates: "incident",
			},
			password: secrets["KUMA_PASSWORD"],
		})
	}
	return out, nil
}
