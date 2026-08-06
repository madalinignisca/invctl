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
	"sort"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// ---------- the virtual layer ----------
//
// Before this existed, a VM's connectivity was pure inference: the guest sat
// inside a hypervisor, the hypervisor was cabled, and nothing in the database
// said which of the host's ports carried the guest -- or that a bridge was
// involved at all. Every VM in the fixture had an eth0 with an address and zero
// link rows, so "what is this VM actually plugged into" had no answer that did
// not go through containment and a guess.
//
// The chain this declares, end to end, for all three hypervisors:
//
//   vm-app-1/eth0  --link(veth)-->  hv-01-br0/vnet2      the guest's half
//   hv-01-br0/uplink --link-->      hv-01/bond0          the bridge's uplink
//   hv-01/eno2, hv-01/eno3          master hv-01/bond0   enslaved, in networking()
//   hv-01/eno2     --link(DAC)-->   sw-core-1/Ethernet1  the cable plant
//
// Four hops, every one of them a declared row, and the last two already
// existed. That is the layered picture: a line in the virtual layer resolves
// down to a line in the physical one.
//
// WHY A BOND SITS IN THE MIDDLE, and it is not decoration. A bridge cannot be
// cabled to hv-01/eno2: that port already carries the cable to sw-core-1, and
// store.CreateLink enforces one active link per port under writeSerializable.
// The invariant is right and the model would be wrong even without it -- a host
// dual-homed into an MC-LAG pair does not hand a bridge one of its two cables,
// it bonds them and gives the bridge the bond. Adding bond0 makes the fixture
// more accurate rather than less, and it is what lets the bridge's uplink be a
// plain link row like every other adjacency, with no invariant bent to fit.
//
// WHAT IS DELIBERATELY NOT MODELLED. hv-03-br0 carries prod guests and the
// development guest on one bridge, which is what the fixture actually is and
// why it is declared in both environments -- it is a real segmentation
// exception on a shared host, and making that visible is the tool's purpose.
// The honest fix in a real estate is VLAN separation (bond0.30 and bond0.40),
// and interface carries no VLAN column: docs/DEMO.md already names trunking as
// the weakest part of the model, and inventing a column here to tidy up one
// fixture row is not the way to fix it.

// virtual declares a bridge per hypervisor, its ports, and the veth pair
// joining every guest to it.
//
// It runs after networking(): the bridges' uplinks land on bond0, and every
// guest's eth0 has to exist before it can be cabled.
func (b *builder) virtual() {
	if !b.ok() {
		return
	}
	b.bridges()
	b.bridgePorts()
	b.virtualLinks()
}

// bridgeName is the asset name of a hypervisor's bridge.
//
// Qualified by host rather than left as the bare "br0" every Linux box calls
// it: asset names are the fixture's own key space and three assets called br0
// would be three collisions. It also sorts next to its host in every list.
func bridgeName(host string) string { return host + "-br0" }

// guestsOf returns the guests running on one host, in placement order. The
// order decides which vnet index each guest gets, so it has to come from the
// one placement table rather than from a map.
func guestsOf(host string) []guest {
	var out []guest
	for _, g := range guests() {
		if g.host == host {
			out = append(out, g)
		}
	}
	return out
}

// bridgeEnvironments is the set of environments a host's bridge carries,
// derived from its guests rather than declared.
//
// Derived on purpose: a bridge carries whatever is plugged into it, and hard
// coding "prod" would have quietly hidden that hv-03's bridge also carries the
// development guest. That asset then belongs in the segmentation-span report,
// which is exactly where an operator should find it.
func bridgeEnvironments(host string) []string {
	seen := map[string]bool{}
	var out []string
	for _, g := range guestsOf(host) {
		if seen[g.env] {
			continue
		}
		seen[g.env] = true
		out = append(out, g.env)
	}
	sort.Strings(out)
	return out
}

func (b *builder) bridges() {
	for _, h := range hypervisors() {
		if !b.ok() {
			return
		}
		host := h.name
		b.asset(domain.KindBridge, bridgeName(host), host, bridgeEnvironments(host), func(a *domain.Asset) {
			// No vendor, model or serial. A Linux bridge is a kernel object,
			// not a part somebody bought, and filling those fields in to make
			// the row look complete would be the fixture inventing provenance.
			a.TeamID = b.team("platform")
		})
	}
}

// bridgePorts gives each bridge one uplink and one guest-facing port per VM.
//
// A real bridge's ports ARE the interfaces enslaved to it, and Linux names them
// after those devices. Modelling the bridge as its own asset means giving it
// port rows of its own, so they are named for the role they play: "uplink" for
// the one facing the host's bond, vnetN for the guest halves -- which is what
// libvirt calls the host side of a veth pair anyway.
//
// No MAC and no address on any of them, matching every other virtual port in
// this fixture: a bridge port's address is the guest's, and a second copy of it
// here would resolve one IP to two assets in search.
func (b *builder) bridgePorts() {
	for _, h := range hypervisors() {
		if !b.ok() {
			return
		}
		host := h.name
		br := bridgeName(host)

		b.virtualInterface(br, "uplink", bondSpeed(host))
		for i := range guestsOf(host) {
			// Matches the guest's own eth0: a veth pair has one speed, not two.
			b.virtualInterface(br, fmt.Sprintf("vnet%d", i), guestPortSpeed)
		}
	}
}

// guestPortSpeed is the speed every guest eth0 in the fixture declares, and
// therefore the speed of the bridge port facing it.
const guestPortSpeed = 10000

// bondSpeed is the aggregate of the host's CABLED data NICs, and the speed the
// bridge's uplink therefore runs at. One function so the bond row and the
// uplink row can never disagree.
func bondSpeed(host string) int {
	if host == "hv-03" {
		return 25000 // one cabled member; the second bond slot is unpatched
	}
	return 50000
}

// virtualInterface creates one virtual port and records its id.
func (b *builder) virtualInterface(asset, name string, speed int) {
	if !b.ok() {
		return
	}
	assetID, ok := b.refs.Assets[asset]
	if !ok {
		b.fail(fmt.Errorf("seeding interface %s/%s: unknown asset %s", asset, name, asset))
		return
	}
	iface, err := domain.NewInterface(store.NewID(), assetID, name, domain.FFVirtual)
	if err != nil {
		b.fail(fmt.Errorf("building interface %s/%s: %w", asset, name, err))
		return
	}
	iface.SpeedMbps = num(speed)
	if err := b.store.CreateInterface(b.ctx, Actor, iface); err != nil {
		b.fail(fmt.Errorf("seeding interface %s/%s: %w", asset, name, err))
		return
	}
	b.interfaceIDs[asset+"/"+name] = iface.ID
}

// virtualLinks cables each bridge to its host's bond and each guest to its
// bridge.
//
// medium and length_m are left NULL on every row here, and that is the honest
// answer rather than a gap: medium names a physical medium and there is none,
// length is metres of it. Do not read NULL as the marker for a virtual
// adjacency, though -- that holds in this fixture only because all eleven
// cables happen to declare one. The durable discriminator is
// interface.form_factor, which is 'virtual' or 'lag' on both ends of every link
// below and a physical connector on both ends of every cable.
func (b *builder) virtualLinks() {
	type vlink struct{ a, b string }
	var links []vlink

	for _, h := range hypervisors() {
		host := h.name
		br := bridgeName(host)
		// The uplink first, so a partially-seeded database is never a bridge
		// with guests on it and no way out.
		links = append(links, vlink{br + "/uplink", host + "/bond0"})
		for i, g := range guestsOf(host) {
			links = append(links, vlink{g.name + "/eth0", fmt.Sprintf("%s/vnet%d", br, i)})
		}
	}

	for _, l := range links {
		if !b.ok() {
			return
		}
		aID, ok := b.interfaceIDs[l.a]
		if !ok {
			b.fail(fmt.Errorf("seeding virtual link %s-%s: unknown interface %s", l.a, l.b, l.a))
			return
		}
		bID, ok := b.interfaceIDs[l.b]
		if !ok {
			b.fail(fmt.Errorf("seeding virtual link %s-%s: unknown interface %s", l.a, l.b, l.b))
			return
		}
		link, err := domain.NewLink(store.NewID(), aID, bID)
		if err != nil {
			b.fail(fmt.Errorf("building virtual link %s-%s: %w", l.a, l.b, err))
			return
		}
		if err := b.store.CreateLink(b.ctx, Actor, link); err != nil {
			b.fail(fmt.Errorf("seeding virtual link %s-%s: %w", l.a, l.b, err))
			return
		}
	}
}
