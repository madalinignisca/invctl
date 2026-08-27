// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package seed

import (
	"fmt"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// The patch panel somebody put in the shallow cabinet (WP-C3).
//
// WRITTEN BECAUSE THE ESTATE COULD NOT DEMONSTRATE ANY OF IT. The fixture held
// 28 cables and produced no cabling finding at all: nothing was densely
// patched, no two cabled boxes had opposing port faces, and no declared length
// was short. Three checks with nothing to check are three checks nobody can
// trust.
//
// ONE ADDITION COVERS ALL THREE, and it is the ordinary way a rack ends up
// miserable rather than a contrivance:
//
//	Somebody mounted a 24-port patch panel at the top of rack-a2 -- the 600mm
//	comms cabinet that already has servers in it that do not fit -- and ran
//	everything to it. The panel's ports face the FRONT. The servers' ports face
//	the REAR. So every one of those leads leaves the back of a server, travels
//	round the cabinet and arrives at the front of the panel, in a cabinet whose
//	side channel is about 58mm wide.
//
// And one of the patch leads is declared at 1m across a 28-unit gap, which is
// the ordinary data-entry mistake: somebody recorded the length of the patch
// lead in their hand rather than the one that was fitted.

// companyCabling adds the panel and its leads.
func (b *builder) companyCabling() {
	b.patchPanel()
	b.patchLeads()
}

// patchPanel is the front-ported panel at the top of the shallow cabinet.
func (b *builder) patchPanel() {
	b.manufacturer("panduit", "Panduit", "https://www.panduit.com/support")
	b.model("panduit", "CP24BLY", "CP24BLY", 1, "")
	if !b.ok() {
		return
	}
	// A panel is passive metal: it moves no air, weighs little, and its ports
	// face the front. Declaring all three is what makes it a useful comparison
	// rather than another gap.
	if id, ok := b.refs.DeviceTypes["CP24BLY"]; ok {
		dt, err := b.store.GetDeviceType(b.ctx, id)
		if err != nil {
			b.fail(fmt.Errorf("reading the patch panel model: %w", err))
			return
		}
		updated := dt.DeviceType
		updated.DepthMM = num(120)
		updated.WeightGrams = num(1800)
		updated.Airflow = str(domain.AirflowPassive)
		updated.PortFace = str(domain.PortFaceFront)
		updated.FullDepth = false
		if err := b.store.UpdateDeviceType(b.ctx, Permit, &updated); err != nil {
			b.fail(fmt.Errorf("measuring the patch panel model: %w", err))
			return
		}
	}

	b.asset(domain.KindPatchPanel, "pp-a2-1", "rack-a2", []string{"dev"}, func(a *domain.Asset) {
		a.Vendor, a.Model = str("Panduit"), str("CP24BLY")
		a.DeviceTypeID = b.deviceType("CP24BLY")
		a.TeamID = b.team("network")
		a.RackPosition, a.RackFace = num(40), str(domain.FaceFront)
		a.Attrs = `{"note":"24-port copper panel; everything in this cabinet lands here"}`
	})
	b.assetCosts([]costLine{
		{"pp-a2-1", "acquisition", domain.CostOnce, 180, -600,
			"24-port Cat6A patch panel — approximate"},
	})
}

// patchLeads wires the cabinet to the panel.
//
// TWENTY-FOUR LEADS, which is DenseLeadCount exactly. Not a coincidence and not
// cheating: a 24-port panel patched out is what the threshold was chosen to
// describe, and an estate where the number sits just under it would make the
// check look like it never fires.
func (b *builder) patchLeads() {
	if !b.ok() {
		return
	}
	if _, ok := b.refs.Assets["pp-a2-1"]; !ok {
		return
	}
	panelID, ok := b.refs.Assets["pp-a2-1"]
	if !ok {
		return
	}
	// What the panel already has. IDEMPOTENCY HANGS ON THIS: b.interfaceIDs is
	// populated by a fresh Load and is empty on a top-up, so a check against it
	// alone reports "not there" for a port that exists -- and the insert
	// collides. Two phases learned that the same way before this one.
	existing := map[string]bool{}
	ports, err := b.store.ListInterfaces(b.ctx, panelID)
	if err != nil {
		b.fail(fmt.Errorf("reading the patch panel's ports: %w", err))
		return
	}
	for _, p := range ports {
		existing[p.Name] = true
		b.interfaceIDs["pp-a2-1/"+p.Name] = p.ID
	}
	// Already patched on a previous run: leave the whole phase alone rather
	// than patch a subset and leave the cabinet half-wired.
	if len(existing) > 0 {
		return
	}

	panelPorts := make([]string, 0, 24)
	for i := 1; i <= 24; i++ {
		name := fmt.Sprintf("port-%02d", i)
		key := "pp-a2-1/" + name
		id := panelID
		iface, err := domain.NewInterface(store.NewID(), id, name, "rj45")
		if err != nil {
			b.fail(fmt.Errorf("building %s: %w", key, err))
			return
		}
		if err := b.store.CreateInterface(b.ctx, Permit, iface); err != nil {
			b.fail(fmt.Errorf("seeding %s: %w", key, err))
			return
		}
		b.interfaceIDs[key] = iface.ID
		panelPorts = append(panelPorts, key)
	}

	// The far ends: ports on the rear-facing hosts already in this cabinet.
	// Created here rather than reused, because the hosts' existing ports are
	// already patched elsewhere and a second active link on one port is
	// refused -- correctly.
	type lead struct {
		host, port string
		lengthM    int
	}
	var leads []lead
	hosts := []string{"hv-esx-01", "hv-esx-02", "hv-esx-03"}
	for i := 0; i < 24; i++ {
		host := hosts[i%len(hosts)]
		l := lead{host: host, port: fmt.Sprintf("patch-%02d", i+1), lengthM: 2}
		// ONE OF THEM IS WRONG, and it is the ordinary mistake: somebody
		// recorded the length of the lead in their hand rather than the one
		// that was fitted. The panel is at U40 and the hosts are at U10-U12,
		// so a 1m lead cannot reach.
		if i == 0 {
			l.lengthM = 1
		}
		leads = append(leads, l)
	}

	for i, l := range leads {
		if !b.ok() {
			return
		}
		hostKey := l.host + "/" + l.port
		if _, exists := b.interfaceIDs[hostKey]; !exists {
			hostID, ok := b.refs.Assets[l.host]
			if !ok {
				continue
			}
			iface, err := domain.NewInterface(store.NewID(), hostID, l.port, "rj45")
			if err != nil {
				b.fail(fmt.Errorf("building %s: %w", hostKey, err))
				return
			}
			if err := b.store.CreateInterface(b.ctx, Permit, iface); err != nil {
				b.fail(fmt.Errorf("seeding %s: %w", hostKey, err))
				return
			}
			b.interfaceIDs[hostKey] = iface.ID
		}

		aID, bID := b.interfaceIDs[panelPorts[i]], b.interfaceIDs[hostKey]
		if aID == "" || bID == "" {
			continue
		}
		link, err := domain.NewLink(store.NewID(), aID, bID)
		if err != nil {
			b.fail(fmt.Errorf("building patch lead %d: %w", i+1, err))
			return
		}
		link.Medium = str("Cat6A")
		link.LengthM = num(l.lengthM)
		if err := b.store.CreateLink(b.ctx, Permit, link); err != nil {
			b.fail(fmt.Errorf("seeding patch lead %d: %w", i+1, err))
			return
		}
	}
}
