// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"time"

	"github.com/Serraniel/neptide/pkg/kuma"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/fleet"
)

// fleetMaintenance lists the maintenance windows of every Uptime Kuma of
// this host: those in fleet.yaml, and uptime-kuma stacks. One that can't be
// reached is listed with its error.
func fleetMaintenance(ctx context.Context, c common) ([]api.KumaMaintenance, error) {
	e, err := c.open()
	if err != nil {
		return nil, err
	}
	kumas, err := e.kumas()
	if err != nil {
		return nil, err
	}
	var out []api.KumaMaintenance
	for _, k := range kumas {
		out = append(out, kumaMaintenance(ctx, k.KumaInstance, k.password))
	}
	return out, nil
}

func kumaMaintenance(ctx context.Context, k fleet.KumaInstance, password string) api.KumaMaintenance {
	m := api.KumaMaintenance{Kuma: k.Name, Windows: []api.MaintenanceWindow{}}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	client, err := kuma.Login(ctx, k.URL, k.Username, password, nil)
	if err != nil {
		m.Error = err.Error()
		return m
	}
	defer client.Close()
	all, err := client.Maintenances(ctx)
	if err != nil {
		m.Error = err.Error()
		return m
	}
	for _, w := range all {
		mw := api.MaintenanceWindow{ID: w.ID, Title: w.Title, Description: w.Description, Strategy: w.Strategy, Active: w.Active, Status: w.Status}
		if len(w.Timeslots) > 0 {
			mw.Start, mw.End = w.Timeslots[0].Start, w.Timeslots[0].End
		}
		m.Windows = append(m.Windows, mw)
	}
	return m
}
