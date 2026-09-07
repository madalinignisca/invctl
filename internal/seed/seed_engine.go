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
//	corp            two APs                        -> reduced to one by losing one
//	guest           two APs                        -> EMPTIED by losing both
//	warehouse-scan  one AP                         -> a standing single point of failure
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
	b.wirelessLANs()
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
	if err := b.store.CreateCluster(b.ctx, Permit, c); err != nil {
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
	if err := b.store.SetClusterMembers(b.ctx, Permit, c.ID, members); err != nil {
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
		if err := b.store.SetInterfaceVLANs(b.ctx, Permit, ifaceID, current); err != nil {
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
		if err := b.store.CreateFHRPGroup(b.ctx, Permit, grp); err != nil {
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
		if err := b.store.SetFHRPMembers(b.ctx, Permit, grp.ID, members); err != nil {
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
	if err := b.store.CreateL2VPN(b.ctx, Permit, vpn); err != nil {
		b.fail(fmt.Errorf("seeding overlay: %w", err))
		return
	}
	if vlanID, ok := b.refs.VLANs["production-workloads"]; ok {
		t, err := domain.NewL2VPNTermination(store.NewID(), vpn.ID, &vlanID, nil)
		if err != nil {
			b.fail(fmt.Errorf("building termination: %w", err))
			return
		}
		if err := b.store.CreateL2VPNTermination(b.ctx, Permit, t); err != nil {
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

// wirelessLANs gives WP-F1's structure kind two access points, three radios
// each -- which is what makes D2's "three, not one" visible in the demo --
// and three SSIDs arranged so BOTH structure findings have something to
// find, exactly as vlanMembership arranges VLAN 99 and firstHopRedundancy
// arranges gw-transit/gw-prod above.
//
//	corp on ap-1/radio_5g and ap-2/radio_5g   -> losing one AP reduces it to one
//	guest on radio_2g4+radio_5g of both APs   -> losing both APs EMPTIES it
//	warehouse-scan on ap-2/radio_2g4 alone    -> a standing single point of
//	                                              failure, not a consequence of
//	                                              this outage (§2.2)
//
// ap-2/radio_2g4 broadcasts BOTH guest and warehouse-scan on purpose: one
// radio serving several SSIDs is the ordinary case (§2.4) and is the reason
// this is not a `link` row. Both APs' radio_6g stay unused, which is its own
// small finding -- a declared radio broadcasting nothing -- visible on the
// asset's own Radios panel (Task 7b).
func (b *builder) wirelessLANs() {
	if !b.ok() {
		return
	}

	// Two access points under the Oslo site, alongside the switches that
	// already live in rack-a1/rack-b1 -- an AP is estate hardware like any
	// other, not a special case that needs its own site.
	b.asset(domain.KindAccessPoint, "ap-1", "dc-oslo", []string{"prod"}, nil)
	b.asset(domain.KindAccessPoint, "ap-2", "dc-oslo", []string{"prod"}, nil)
	if !b.ok() {
		return
	}

	type radioSpec struct{ asset, name, formFactor string }
	radios := []radioSpec{
		{"ap-1", "radio-2g4", domain.FFRadio2G4},
		{"ap-1", "radio-5g", domain.FFRadio5G},
		{"ap-1", "radio-6g", domain.FFRadio6G},
		{"ap-2", "radio-2g4", domain.FFRadio2G4},
		{"ap-2", "radio-5g", domain.FFRadio5G},
		{"ap-2", "radio-6g", domain.FFRadio6G},
	}
	for _, r := range radios {
		if !b.ok() {
			return
		}
		assetID, ok := b.refs.Assets[r.asset]
		if !ok {
			b.fail(fmt.Errorf("seeding radio %s: unknown asset %s", r.name, r.asset))
			return
		}
		// TOP-UP: a radio already on this asset is left alone and its id is
		// recorded, so the phases below find it. CreateInterface would
		// otherwise collide on UNIQUE (asset_id, name) and take the whole
		// top-up down with it.
		if existingID, found, err := b.existingInterface(assetID, r.name); err != nil {
			b.fail(fmt.Errorf("looking for radio %s/%s: %w", r.asset, r.name, err))
			return
		} else if found {
			b.interfaceIDs[r.asset+"/"+r.name] = existingID
			continue
		}
		iface, err := domain.NewInterface(store.NewID(), assetID, r.name, r.formFactor)
		if err != nil {
			b.fail(fmt.Errorf("building radio %s/%s: %w", r.asset, r.name, err))
			return
		}
		if err := b.store.CreateInterface(b.ctx, Permit, iface); err != nil {
			b.fail(fmt.Errorf("seeding radio %s/%s: %w", r.asset, r.name, err))
			return
		}
		b.interfaceIDs[r.asset+"/"+r.name] = iface.ID
	}
	if !b.ok() {
		return
	}

	// Three SSIDs. corp gets BOTH a psk_ref and wpa2_enterprise -- an odd
	// real-world pairing, deliberate here so the demo shows every declared
	// wireless field on screen at once (§4 item 8): NewWirelessLAN ties
	// neither to the other (D4/§2.5's "no cross-field rule"), so recording
	// both on one row is a legitimate state, not a validation gap.
	type wlanSpec struct {
		name, ssid, security string
		vlan, authService    string // vlan/authService are ref names; "" means unset
		pskRef               string
	}
	specs := []wlanSpec{
		{"Corp", "corp", "wpa2_enterprise", "", "sso", "kv/demo/wifi/corp/psk"},
		{"Guest", "guest", "open", "production-workloads", "", ""},
		{"Warehouse scanners", "warehouse-scan", "wpa2_personal", "", "", ""},
	}
	// Read once rather than per spec: three lookups against a table this phase
	// is about to write to would each see a different estate.
	existingWLANs, err := b.store.ListWirelessLANs(b.ctx)
	if err != nil {
		b.fail(fmt.Errorf("reading the wireless LANs already present: %w", err))
		return
	}
	bySSID := make(map[string]string, len(existingWLANs))
	for _, row := range existingWLANs {
		bySSID[row.SSID] = row.ID
	}

	for _, s := range specs {
		if !b.ok() {
			return
		}
		// TOP-UP: an SSID already recorded keeps whatever somebody set on it.
		// Creating it again would collide on the partial unique index over
		// (ssid, scope_asset_id) -- these are all estate-wide, so they land in
		// the NULL-scope half of it.
		if id, exists := bySSID[s.ssid]; exists {
			b.refs.WirelessLANs[s.ssid] = id
			continue
		}
		w, err := domain.NewWirelessLAN(store.NewID(), s.name, s.ssid, s.security, nil)
		if err != nil {
			b.fail(fmt.Errorf("building wireless lan %s: %w", s.ssid, err))
			return
		}
		if s.vlan != "" {
			if vlanID, ok := b.refs.VLANs[s.vlan]; ok {
				w.VLANID = &vlanID
			}
		}
		if s.authService != "" {
			if svcID, ok := b.refs.Services[s.authService]; ok {
				w.AuthServiceID = &svcID
			}
		}
		if s.pskRef != "" {
			w.PSKRef = str(s.pskRef)
		}
		if err := b.store.CreateWirelessLAN(b.ctx, Permit, w); err != nil {
			b.fail(fmt.Errorf("seeding wireless lan %s: %w", s.ssid, err))
			return
		}
		b.refs.WirelessLANs[s.ssid] = w.ID
	}
	if !b.ok() {
		return
	}

	// Membership, radio by radio, THROUGH ListInterfaceWLANMembers ->
	// append -> SetInterfaceWLANs -- never a direct INSERT, which would skip
	// the audit fold (D7). Same shape as vlanMembership above.
	type member struct{ asset, radio, ssid string }
	for _, m := range []member{
		{"ap-1", "radio-5g", "corp"},
		{"ap-2", "radio-5g", "corp"},
		{"ap-1", "radio-2g4", "guest"},
		{"ap-1", "radio-5g", "guest"},
		{"ap-2", "radio-2g4", "guest"},
		{"ap-2", "radio-5g", "guest"},
		{"ap-2", "radio-2g4", "warehouse-scan"},
	} {
		if !b.ok() {
			return
		}
		wlanID, ok := b.refs.WirelessLANs[m.ssid]
		if !ok {
			b.fail(fmt.Errorf("seeding wireless membership: unknown wireless lan %s", m.ssid))
			return
		}
		ifaceID, ok := b.interfaceIDs[m.asset+"/"+m.radio]
		if !ok {
			b.fail(fmt.Errorf("seeding wireless membership: unknown radio %s/%s", m.asset, m.radio))
			return
		}
		current, err := b.store.ListInterfaceWLANMembers(b.ctx, ifaceID)
		if err != nil {
			b.fail(fmt.Errorf("reading wireless membership: %w", err))
			return
		}
		// TOP-UP: already a member is nothing to do. Appending regardless would
		// send SetInterfaceWLANs a list with the pair twice, colliding on the
		// (interface_id, wireless_lan_id) primary key -- and, worse, a write
		// that changed nothing would still be a change_log entry saying the
		// port moved when it did not.
		alreadyMember := false
		for _, existing := range current {
			if existing.WirelessLANID == wlanID {
				alreadyMember = true
				break
			}
		}
		if alreadyMember {
			continue
		}
		current = append(current, domain.InterfaceWLAN{InterfaceID: ifaceID, WirelessLANID: wlanID})
		if err := b.store.SetInterfaceWLANs(b.ctx, Permit, ifaceID, current); err != nil {
			b.fail(fmt.Errorf("seeding wireless membership for %s/%s on %s: %w",
				m.asset, m.radio, m.ssid, err))
			return
		}
	}
}
