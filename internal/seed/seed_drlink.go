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

// The fibre between the office and the DR site (WP-E1b).
//
// WRITTEN BECAUSE THE ESTATE COULD NOT DEMONSTRATE THE FEATURE. Every circuit
// in the fixture terminated on a site or on one interface, so not one of them
// was a connectivity edge and "simulate cutting this" had nothing to answer
// anywhere in the demo. A check with no case is a check nobody can trust.
//
// IT ALSO CLOSES A REAL HOLE, which is why it is worth doing rather than
// staging. dr-bergen had a switch and a firewall and NO forwarder group at all,
// so the reach model did not cover the disaster-recovery site — the one part of
// an estate whose whole purpose is to still be reachable. Anything at Bergen
// was invisible to every reachability answer the software gave.
//
// The topology is the honest small-company one: a single dark fibre between the
// two buildings. It is the sole path, so cutting it partitions Bergen — which
// is the finding, and it is also a true statement about a company that has one
// fibre and no second carrier. An estate with a backup path would report
// nothing when either was cut, and that is the answer redundancy is bought for;
// this one does not have that yet, and the demo should not pretend it does.
func (b *builder) companyDRLink() {
	b.drGroup()
	b.drAttachments()
	b.drInterfaces()
	b.drCircuit()
}

// drAttachments puts the DR hosts on the DR switch.
//
// WITHOUT THIS THE EDGE EXISTS AND MEANS NOTHING. A group with no hosts
// attached to it partitions perfectly and produces no finding, because nothing
// is depending on being on the far side of the cut -- which is exactly what the
// first version of this fixture did, and the test caught it as "cutting the
// only fibre changed the answer from 0 to 0".
//
// The anchors all live in Oslo (fw-edge and sw-core), so a Bergen host reaching
// one of them has to cross the fibre. That is what makes the cut consequential
// and it is also true of the real thing: a DR site whose hosts answer to
// nothing at the primary site is not a DR site, it is a second office.
func (b *builder) drAttachments() {
	groupID, ok := b.refs.NetGroups["sw-dr"]
	if !ok {
		return
	}
	// Read once: the listing is per GROUP, so asking it per host would be the
	// same query three times.
	existing, err := b.store.ListNetAttachments(b.ctx, groupID)
	if err != nil {
		b.fail(fmt.Errorf("reading the DR group's attachments: %w", err))
		return
	}
	already := map[string]bool{}
	for _, a := range existing {
		if a.Plane == domain.PlaneData {
			already[a.AssetID] = true
		}
	}

	for _, host := range []string{"hv-dr-01", "hv-dr-02", "hv-dr-03"} {
		if !b.ok() {
			return
		}
		hostID, ok := b.refs.Assets[host]
		if !ok {
			continue
		}
		if already[hostID] { // idempotent: a second run leaves it alone
			continue
		}
		att, err := domain.NewNetAttachment(store.NewID(), hostID, groupID, domain.PlaneData, b.now)
		if err != nil {
			b.fail(fmt.Errorf("building the attachment for %s: %w", host, err))
			return
		}
		// No pins: sw-dr is a group of one, so naming the chassis says exactly
		// as much as the group does and adds a row that can rot.
		if err := b.store.CreateNetAttachment(b.ctx, domain.AdministratorPermit(Actor), att, nil); err != nil {
			b.fail(fmt.Errorf("attaching %s to the DR switch: %w", host, err))
			return
		}
	}
}

// drGroup gives the DR site a forwarder group, so it exists to the reach model.
func (b *builder) drGroup() {
	if !b.ok() {
		return
	}
	if _, exists := b.refs.NetGroups["sw-dr"]; exists {
		return
	}
	g, err := domain.NewNetGroup(store.NewID(), domain.NetGroupSpec{
		Code: "sw-dr", Name: "DR site switch", Kind: domain.NetGroupStandalone,
		Role: domain.NetRoleCore, Availability: domain.AvailStandalone,
	}, b.now)
	if err != nil {
		b.fail(fmt.Errorf("building the DR net group: %w", err))
		return
	}
	if err := b.store.CreateNetGroup(b.ctx, domain.AdministratorPermit(Actor), g); err != nil {
		b.fail(fmt.Errorf("seeding the DR net group: %w", err))
		return
	}
	b.refs.NetGroups["sw-dr"] = g.ID

	assetID, ok := b.refs.Assets["sw-dr-1"]
	if !ok {
		return // the compute layer did not run; nothing to put in it
	}
	m, err := domain.NewNetGroupMember(g.ID, assetID, "member", b.now)
	if err != nil {
		b.fail(fmt.Errorf("building the DR group member: %w", err))
		return
	}
	if err := b.store.AddNetGroupMember(b.ctx, domain.AdministratorPermit(Actor), m); err != nil {
		b.fail(fmt.Errorf("seeding the DR group member: %w", err))
	}
}

// drInterfaces adds the two ports the fibre lands on.
//
// Named for what they are rather than for their slot, because the name is what
// somebody reads at 03:00 while deciding whether the problem is the port or the
// span between two ports.
func (b *builder) drInterfaces() {
	for _, i := range []struct{ asset, name, form string }{
		{"fw-edge-1", "dr-fibre", "sfp+"},
		{"sw-dr-1", "uplink-oslo", "sfp+"},
	} {
		if !b.ok() {
			return
		}
		key := i.asset + "/" + i.name
		if _, exists := b.interfaceIDs[key]; exists {
			continue
		}
		assetID, ok := b.refs.Assets[i.asset]
		if !ok {
			continue
		}
		// ASKED OF THE STORE, NOT ONLY OF THE REF MAP. b.interfaceIDs is
		// populated by a fresh Load and is empty on a top-up, so the check
		// above alone reports "not there" for a port that exists and the
		// insert conflicts. Two phases learned this the same way; the map is a
		// cache of what THIS run created, never a picture of the estate.
		ports, err := b.store.ListInterfaces(b.ctx, assetID)
		if err != nil {
			b.fail(fmt.Errorf("reading interfaces on %s: %w", i.asset, err))
			return
		}
		found := false
		for _, p := range ports {
			if p.Name == i.name {
				b.interfaceIDs[key] = p.ID
				found = true
				break
			}
		}
		if found {
			continue
		}
		iface, err := domain.NewInterface(store.NewID(), assetID, i.name, i.form)
		if err != nil {
			b.fail(fmt.Errorf("building interface %s: %w", key, err))
			return
		}
		if err := b.store.CreateInterface(b.ctx, domain.AdministratorPermit(Actor), iface); err != nil {
			b.fail(fmt.Errorf("seeding interface %s: %w", key, err))
			return
		}
		b.interfaceIDs[key] = iface.ID
	}
}

// drCircuit is the fibre itself, landed at both ends.
//
// BOTH ENDS ON AN INTERFACE is the whole point. A termination on a site says
// "it arrives in this building", which names no forwarder and joins nothing —
// that is what every other circuit in this fixture does, and it is why none of
// them is an edge.
func (b *builder) drCircuit() {
	if !b.ok() {
		return
	}
	existing, err := b.store.ListCircuits(b.ctx)
	if err != nil {
		b.fail(fmt.Errorf("reading circuits: %w", err))
		return
	}
	for _, c := range existing {
		if c.CID == "DF-OSLO-BGO-01" {
			return // already there; a top-up must not create a second one
		}
	}

	p, err := domain.NewProvider(store.NewID(), "Nordvind Fiber")
	if err != nil {
		b.fail(fmt.Errorf("building the fibre provider: %w", err))
		return
	}
	p.AccountRef = str("NF-2291")
	if err := b.store.CreateProvider(b.ctx, Permit, p); err != nil {
		b.fail(fmt.Errorf("seeding the fibre provider: %w", err))
		return
	}

	circuit, err := domain.NewCircuit(store.NewID(), "DF-OSLO-BGO-01", p.ID)
	if err != nil {
		b.fail(fmt.Errorf("building the DR fibre: %w", err))
		return
	}
	circuit.ServiceType = str("dark fibre")
	circuit.CommitMbps = num(10000)
	circuit.InstallDate = str(domain.FormatDate(b.now.AddDate(-2, -3, 0)))
	circuit.ContractEnd = str(domain.FormatDate(b.now.AddDate(1, 5, 0)))
	circuit.Description = str("Oslo to Bergen dark fibre — the only path to the DR site")
	if err := b.store.CreateCircuit(b.ctx, Permit, circuit); err != nil {
		b.fail(fmt.Errorf("seeding the DR fibre: %w", err))
		return
	}

	for _, end := range []struct {
		side string
		key  string
	}{
		{domain.SideA, "fw-edge-1/dr-fibre"},
		{domain.SideZ, "sw-dr-1/uplink-oslo"},
	} {
		ifaceID, ok := b.interfaceIDs[end.key]
		if !ok {
			// A circuit with one end is a gap the findings page already
			// reports, so leaving it half-landed is survivable -- but it means
			// the edge does not exist, and saying so beats a silent skip.
			b.fail(fmt.Errorf("landing the DR fibre: no interface %s", end.key))
			return
		}
		t, err := domain.NewCircuitTermination(store.NewID(), circuit.ID, end.side, nil, &ifaceID)
		if err != nil {
			b.fail(fmt.Errorf("building the %s end of the DR fibre: %w", end.side, err))
			return
		}
		if err := b.store.CreateCircuitTermination(b.ctx, Permit, t); err != nil {
			b.fail(fmt.Errorf("landing the %s end of the DR fibre: %w", end.side, err))
			return
		}
	}

	// Dark fibre between two cities is a monthly line and a large one; it is
	// usually the second most expensive thing after the people. Approximate,
	// and said so, like every other researched figure in this fixture.
	cost, err := domain.NewCost(store.NewID(), domain.CostSpec{
		Kind: "operating", Period: domain.CostMonthly, AmountMinor: major(1450),
		Note: str("Oslo–Bergen dark fibre, 10G — approximate, not confirmed against a contract"),
	}, b.now)
	if err != nil {
		b.fail(fmt.Errorf("building the DR fibre cost: %w", err))
		return
	}
	if err := b.store.AddCircuitCost(b.ctx, Actor, circuit.ID, cost); err != nil {
		b.fail(fmt.Errorf("pricing the DR fibre: %w", err))
	}
}
