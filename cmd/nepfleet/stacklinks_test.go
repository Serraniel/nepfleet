// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"testing"

	"github.com/Serraniel/nepfleet/internal/fleet"
)

func TestStackLinks(t *testing.T) {
	f := &fleet.Fleet{Dir: t.TempDir()}
	f.Spec.UptimeKuma = []fleet.KumaInstance{{Name: "home", URL: "https://kuma.example.test/"}}
	stack := func(name, target, domain, template string) *fleet.Stack {
		s := &fleet.Stack{}
		s.Metadata.Name = name
		s.Spec.Target = target
		s.Spec.Values = fleet.Literals(map[string]string{"DOMAIN": domain})
		if template != "" {
			s.Spec.Template = &fleet.TemplateRef{Name: template}
		}
		return s
	}
	web := stack("my web", "nas", "web.example.test", "")
	dozzle := stack("logs", "nas", "logs.example.test", "dozzle")
	elsewhere := stack("db", "other", "", "")
	all := []*fleet.Stack{web, dozzle, elsewhere}

	if got := stackAddress(f, web); got != "https://web.example.test" {
		t.Error(got)
	}
	if got := stackAddress(f, elsewhere); got != "" {
		t.Error(got)
	}
	web.Spec.Access = &fleet.Access{Mode: "port", Port: 8096}
	if got := stackAddress(f, web); got != "http://nas:8096" {
		t.Error(got)
	}
	web.Spec.Access = nil

	links := stackLinks(f, all, web)
	if len(links) != 2 || links[0].URL != "https://logs.example.test/stack/my%20web" || links[1].URL != "https://kuma.example.test/dashboard" {
		t.Errorf("%+v", links)
	}
	// A Dozzle on another host can't see this stack.
	if links := stackLinks(f, all, elsewhere); len(links) != 1 || links[0].Name != "Monitors in home" {
		t.Errorf("%+v", links)
	}
}
