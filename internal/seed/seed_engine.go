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

// The edge types the engine learned about in WP-I1 and WP-E2.
//
// WHY THIS IS IN THE BASE FIXTURE and not the company layer. Clusters, VLAN
// membership, first-hop redundancy and overlays all changed what the impact
// engine concludes -- cluster HA changed a propagation -- and none of them
// appeared in the fixture the test suite reasons about. So the features were
// proven by unit tests and demonstrated by nothing: a fresh seed produced an
// estate where losing a hypervisor still reported every guest lost, because
// there was no cluster to consult.
//
// EVERY SHAPE IS HERE ON PURPOSE, the way seed_hardware.go arranges the power
// chain. A fixture that merely contains rows proves the tables exist; this one
// is arranged so each finding has something to find:
//
//	prod-virt      three hosts, restart, no floor  -> losing one RELOCATES
//	gw-transit     one router                      -> single member, not redundancy
//	gw-prod        two routers                     -> healthy, and reduced to one by an outage
//	mgmt VLAN 10   ports on both core switches     -> survives losing one
//	transit VLAN 99 ports on sw-core-1 only        -> EMPTIED by losing it
//	site-stretch   one termination                 -> an overlay connecting nothing
//	TN-DEMO-1      one end recorded                -> a circuit half known
//
// If somebody removes a rule, the fixture stops demonstrating it and the tests
// in seed_engine_test.go say so.

func (b *builder) engineEdges() {
	if !b.ok() {
		return
	}
	b.virtualisationCluster()
	b.vlanMembership()
	b.firstHopRedundancy()
	b.overlayAndCircuit()
}

// virtualisationCluster makes the three hypervisors carry each other.
//
// Without it the engine treats every hypervisor as standalone and reports every
// guest lost -- which is what it did before WP-E2 and is wrong for this estate,
// where hv-01, hv-02 and hv-03 are one Proxmox cluster.
func (b *builder) virtualisationCluster() {
	c, err := domain.NewCluster(store.NewID(), "prod-virt", domain.ClusterProxmox)
	if err != nil {
		b.fail(fmt.Errorf("building cluster: %w", err))
		return
	}
	c.HAPolicy = domain.HARestart
	// THREE HOSTS AND THREE NEEDED, which is the finding worth demonstrating:
	// HA is configured and cannot help, and it looks identical to a healthy
	// cluster on every page that does not do the arithmetic.
	//
	// It is also the only choice that leaves the fixture honest. Clustering
	// these three with capacity to spare would have relocated their guests, and
	// TestContainmentResolvesThroughClosure -- which asserts that losing a
	// hypervisor takes its guests, correctly -- would have started failing. That
	// test encodes the containment model; a fixture change must not quietly
	// rewrite what the engine is supposed to do. Successful relocation is
	// demonstrated by the unit tests in internal/impact and internal/store,
	// which build their own clusters and assert both outcomes.
	c.MinHosts = num(3)
	c.Description = str("three hosts, three needed: HA is configured and cannot survive " +
		"losing one")
	if err := b.store.CreateCluster(b.ctx, Actor, c); err != nil {
		b.fail(fmt.Errorf("seeding cluster: %w", err))
		return
	}
	var members []domain.ClusterMember
	for _, name := range []string{"hv-01", "hv-02", "hv-03"} {
		id, ok := b.refs.Assets[name]
		if !ok {
			b.fail(fmt.Errorf("seeding cluster: unknown host %s", name))
			return
		}
		members = append(members, domain.ClusterMember{ClusterID: c.ID, AssetID: id})
	}
	if err := b.store.SetClusterMembers(b.ctx, Actor, c.ID, members); err != nil {
		b.fail(fmt.Errorf("seeding cluster members: %w", err))
	}
}

// vlanMembership puts ports in the VLANs the prefixes already name.
//
// The VLANs existed and held no ports, so every one of them was a declared
// record rather than a broadcast domain and the engine had nothing to empty.
// VLAN 99 lives on ONE switch on purpose: losing sw-core-1 must empty something,
// or the emptied-structure finding has nothing to demonstrate.
func (b *builder) vlanMembership() {
	type member struct {
		asset, port, vlan, mode string
	}
	for _, m := range []member{
		// Management spans both core switches, so it survives losing one.
		{"sw-core-1", "Ethernet1", "management", domain.VLANModeTagged},
		{"sw-core-2", "Ethernet1", "management", domain.VLANModeTagged},
		// Production workloads likewise.
		{"sw-core-1", "Ethernet2", "production-workloads", domain.VLANModeTagged},
		{"sw-core-2", "Ethernet2", "production-workloads", domain.VLANModeTagged},
		// Transit is on sw-core-1 alone: this is the one an outage empties.
		{"sw-core-1", "Ethernet46", "transit", domain.VLANModeUntagged},
	} {
		if !b.ok() {
			return
		}
		vlanID, ok := b.refs.VLANs[m.vlan]
		if !ok {
			b.fail(fmt.Errorf("seeding VLAN membership: unknown VLAN %s", m.vlan))
			return
		}
		ifaceID, ok := b.interfaceIDs[m.asset+"/"+m.port]
		if !ok {
			b.fail(fmt.Errorf("seeding VLAN membership: unknown port %s/%s", m.asset, m.port))
			return
		}
		current, err := b.store.ListInterfaceVLANMembers(b.ctx, ifaceID)
		if err != nil {
			b.fail(fmt.Errorf("reading VLAN membership: %w", err))
			return
		}
		current = append(current, domain.InterfaceVLAN{
			InterfaceID: ifaceID, VLANID: vlanID, Mode: m.mode,
		})
		if err := b.store.SetInterfaceVLANs(b.ctx, domain.AdministratorPermit(Actor), ifaceID, current); err != nil {
			b.fail(fmt.Errorf("seeding VLAN membership for %s/%s: %w", m.asset, m.port, err))
			return
		}
	}
}

// firstHopRedundancy declares two groups, one of which is not redundancy.
//
// gw-prod has both edge firewalls and is healthy -- and an outage taking one
// reduces it to a single router, which is the finding worth having. gw-transit
// has one member and is a single point of failure wearing the costume of a
// redundant one, which is the finding that needs no outage at all.
func (b *builder) firstHopRedundancy() {
	groups := []struct {
		name    string
		vid     int
		members []string
	}{
		{"gw-prod", 30, []string{"fw-edge-1", "fw-edge-2"}},
		{"gw-transit", 99, []string{"fw-edge-1"}},
	}
	for _, g := range groups {
		if !b.ok() {
			return
		}
		grp, err := domain.NewFHRPGroup(store.NewID(), domain.FHRPVRRP3, g.vid, g.name)
		if err != nil {
			b.fail(fmt.Errorf("building %s: %w", g.name, err))
			return
		}
		if err := b.store.CreateFHRPGroup(b.ctx, domain.AdministratorPermit(Actor), grp); err != nil {
			b.fail(fmt.Errorf("seeding %s: %w", g.name, err))
			return
		}
		var members []domain.FHRPMember
		for i, asset := range g.members {
			ifaceID, ok := b.interfaceIDs[asset+"/ethernet1/1"]
			if !ok {
				b.fail(fmt.Errorf("seeding %s: unknown port on %s", g.name, asset))
				return
			}
			priority := 200 - i*100
			members = append(members, domain.FHRPMember{
				GroupID: grp.ID, InterfaceID: ifaceID, Priority: &priority,
			})
		}
		if err := b.store.SetFHRPMembers(b.ctx, domain.AdministratorPermit(Actor), grp.ID, members); err != nil {
			b.fail(fmt.Errorf("seeding %s members: %w", g.name, err))
			return
		}
	}
}

// overlayAndCircuit gives the last two findings something to find.
//
// Both are deliberately INCOMPLETE, because a complete one demonstrates
// nothing: an overlay terminating twice and a circuit with both ends recorded
// are the healthy state, and the fixture already has plenty of healthy.
func (b *builder) overlayAndCircuit() {
	if !b.ok() {
		return
	}
	vpn, err := domain.NewL2VPN(store.NewID(), "site-stretch", domain.L2VPNVXLAN)
	if err != nil {
		b.fail(fmt.Errorf("building overlay: %w", err))
		return
	}
	vni := int64(10030)
	vpn.Identifier = &vni
	vpn.Description = str("declared with one end; the far side was never built")
	if err := b.store.CreateL2VPN(b.ctx, domain.AdministratorPermit(Actor), vpn); err != nil {
		b.fail(fmt.Errorf("seeding overlay: %w", err))
		return
	}
	if vlanID, ok := b.refs.VLANs["production-workloads"]; ok {
		t, err := domain.NewL2VPNTermination(store.NewID(), vpn.ID, &vlanID, nil)
		if err != nil {
			b.fail(fmt.Errorf("building termination: %w", err))
			return
		}
		if err := b.store.CreateL2VPNTermination(b.ctx, domain.AdministratorPermit(Actor), t); err != nil {
			b.fail(fmt.Errorf("seeding termination: %w", err))
			return
		}
	}

	p, err := domain.NewProvider(store.NewID(), "Demo Telecom")
	if err != nil {
		b.fail(fmt.Errorf("building provider: %w", err))
		return
	}
	p.AccountRef = str("ACC-0001")
	if err := b.store.CreateProvider(b.ctx, Permit, p); err != nil {
		b.fail(fmt.Errorf("seeding provider: %w", err))
		return
	}
	circuit, err := domain.NewCircuit(store.NewID(), "TN-DEMO-1", p.ID)
	if err != nil {
		b.fail(fmt.Errorf("building circuit: %w", err))
		return
	}
	circuit.ServiceType = str("DIA")
	circuit.CommitMbps = num(1000)
	// Renewing inside the expiry horizon, so the circuit half of that report
	// has a row rather than only assets and certificates.
	circuit.ContractEnd = str(domain.FormatDate(b.now.AddDate(0, 2, 0)))
	circuit.Description = str("one end recorded; where it comes from was never entered")
	if err := b.store.CreateCircuit(b.ctx, Permit, circuit); err != nil {
		b.fail(fmt.Errorf("seeding circuit: %w", err))
		return
	}
	if ifaceID, ok := b.interfaceIDs["fw-edge-1/ethernet1/1"]; ok {
		t, err := domain.NewCircuitTermination(store.NewID(), circuit.ID, domain.SideA, nil, &ifaceID)
		if err != nil {
			b.fail(fmt.Errorf("building circuit end: %w", err))
			return
		}
		if err := b.store.CreateCircuitTermination(b.ctx, Permit, t); err != nil {
			b.fail(fmt.Errorf("seeding circuit end: %w", err))
		}
	}
}
