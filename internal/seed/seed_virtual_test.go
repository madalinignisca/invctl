// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package seed_test

import (
	"sort"
	"strings"
	"testing"
)

// The virtual layer's whole claim is that a guest's wire can be walked all the
// way to a switch port without inferring anything. These tests walk it, hop by
// hop, and fail on the first hop that is missing -- which is the failure mode
// that matters, because a chain broken in the middle still LOOKS modelled from
// either end.

// hop is one step of the walk, named so a failure says which link is missing
// rather than "expected 4 rows, got 3".
type hop struct {
	what string
	from string // "asset/interface"
	to   string // "asset/interface"
}

// TestTheVirtualChainResolvesDownToACablePort walks veth -> bridge -> bond ->
// physical NIC -> switch port for every hypervisor docs/DEMO.md visits.
//
// It asserts the chain by NAME rather than by counting rows, because the
// diagram this fixture exists to feed draws a specific line between two
// specific ports, and a count would stay green while every guest hung off the
// wrong host's bridge.
func TestTheVirtualChainResolvesDownToACablePort(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		cases := []struct {
			guest string
			hops  []hop
		}{
			{"vm-app-1", []hop{
				{"veth pair", "vm-app-1/eth0", "hv-01-br0/vnet2"},
				{"bridge uplink", "hv-01-br0/uplink", "hv-01/bond0"},
				{"cable", "hv-01/eno2", "sw-core-1/Ethernet1"},
			}},
			{"vm-queue-1", []hop{
				{"veth pair", "vm-queue-1/eth0", "hv-02-br0/vnet3"},
				{"bridge uplink", "hv-02-br0/uplink", "hv-02/bond0"},
				{"cable", "hv-02/eno3", "sw-core-2/Ethernet2"},
			}},
			// hv-03 is the single-homed host every reachability scenario turns
			// on, so its chain reaches exactly one chassis.
			{"vm-sso-1", []hop{
				{"veth pair", "vm-sso-1/eth0", "hv-03-br0/vnet1"},
				{"bridge uplink", "hv-03-br0/uplink", "hv-03/bond0"},
				{"cable", "hv-03/eno2", "sw-core-2/Ethernet3"},
			}},
		}
		for _, tc := range cases {
			t.Run(tc.guest, func(t *testing.T) {
				for _, h := range tc.hops {
					if !linked(t, f, h.from, h.to) {
						t.Errorf("the %s %s <-> %s is missing, so %s's wire cannot be walked to a "+
							"switch port and the virtual layer has nothing to draw",
							h.what, h.from, h.to, tc.guest)
					}
				}
			})
		}
	})
}

// TestEveryGuestIsCabledToItsOwnHostsBridge is the census the per-guest walk
// above deliberately does not do: no guest may be left off a bridge, and none
// may land on another hypervisor's.
//
// A guest cabled to the wrong host's bridge is the one error a diagram would
// render confidently and wrongly -- it draws a wire that does not exist, on a
// page an operator is reading during an incident.
func TestEveryGuestIsCabledToItsOwnHostsBridge(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		var rows []struct {
			Guest      string `db:"guest"`
			GuestHost  string `db:"guest_host"`
			Bridge     string `db:"bridge"`
			BridgeHost string `db:"bridge_host"`
		}
		// Both link orientations, because a link is undirected in meaning and
		// the seed happens to write the guest on the a side today.
		if err := f.store.DB().Reader.SelectContext(f.ctx, &rows, `
			SELECT g.name AS guest, gh.name AS guest_host,
			       br.name AS bridge, brh.name AS bridge_host
			FROM link l
			JOIN interface gi ON gi.id = l.a_interface_id
			JOIN interface bi ON bi.id = l.b_interface_id
			JOIN asset g   ON g.id   = gi.asset_id
			JOIN asset br  ON br.id  = bi.asset_id AND br.kind = 'bridge'
			JOIN asset gh  ON gh.id  = g.parent_id
			JOIN asset brh ON brh.id = br.parent_id
			WHERE l.lifecycle = 'active' AND gi.name = 'eth0'
			ORDER BY g.name`); err != nil {
			t.Fatalf("walking guest veths: %v", err)
		}

		onABridge := map[string]bool{}
		for _, r := range rows {
			onABridge[r.Guest] = true
			if r.GuestHost != r.BridgeHost {
				t.Errorf("%s runs on %s but its veth lands on %s, which is inside %s -- a diagram "+
					"built on this draws a wire between two hosts that share no cable",
					r.Guest, r.GuestHost, r.Bridge, r.BridgeHost)
			}
		}

		var guests []string
		if err := f.store.DB().Reader.SelectContext(f.ctx, &guests, `
			SELECT a.name FROM asset a
			WHERE a.kind IN ('vm', 'k8s_node') AND a.lifecycle <> 'retired'
			ORDER BY a.name`); err != nil {
			t.Fatalf("listing guests: %v", err)
		}
		var stranded []string
		for _, g := range guests {
			if !onABridge[g] {
				stranded = append(stranded, g)
			}
		}
		if len(stranded) != 0 {
			t.Errorf("%d guest(s) have no veth to any bridge: %v. Every guest in this fixture has an "+
				"eth0 with an address, so one with no link is a guest whose connectivity is still "+
				"pure inference -- exactly the gap the virtual layer exists to close", len(stranded), stranded)
		}
	})
}

// TestTheBridgeUplinkLandsOnABondedPair pins WHY a bond sits between the bridge
// and the cable plant.
//
// It is not a modelling flourish. store.CreateLink allows one active link per
// port, so a bridge cannot be cabled to hv-01/eno2 while that port carries the
// cable to sw-core-1 -- and the model would be wrong even without the
// invariant, because a host dual-homed into an MC-LAG pair bonds its NICs and
// gives the bridge the bond. If somebody ever "simplifies" this by pointing the
// uplink at a physical NIC, either the seed stops loading or the fixture starts
// claiming a bridge owns a cable it shares.
func TestTheBridgeUplinkLandsOnABondedPair(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		var rows []struct {
			Bridge    string `db:"bridge"`
			PeerAsset string `db:"peer_asset"`
			PeerPort  string `db:"peer_port"`
			PeerForm  string `db:"peer_form"`
			Members   int    `db:"member_list"`
		}
		if err := f.store.DB().Reader.SelectContext(f.ctx, &rows, `
			SELECT br.name AS bridge, pa.name AS peer_asset, pi.name AS peer_port,
			       pi.form_factor AS peer_form,
			       (SELECT COUNT(*) FROM interface m WHERE m.lag_parent_id = pi.id) AS member_list
			FROM link l
			JOIN interface ui ON ui.id = l.a_interface_id AND ui.name = 'uplink'
			JOIN interface pi ON pi.id = l.b_interface_id
			JOIN asset br ON br.id = ui.asset_id AND br.kind = 'bridge'
			JOIN asset pa ON pa.id = pi.asset_id
			WHERE l.lifecycle = 'active'
			ORDER BY br.name`); err != nil {
			t.Fatalf("walking bridge uplinks: %v", err)
		}
		if len(rows) != 3 {
			t.Fatalf("%d bridge uplinks, want 3 -- one per hypervisor", len(rows))
		}
		for _, r := range rows {
			if r.PeerForm != "lag" {
				t.Errorf("%s uplinks to %s/%s, whose form factor is %q. A bridge uplink must land on "+
					"the host's bond: a physical port already carries a cable, and CreateLink allows "+
					"one active link per port", r.Bridge, r.PeerAsset, r.PeerPort, r.PeerForm)
			}
			if r.Members == 0 {
				t.Errorf("%s uplinks to %s/%s, which has no enslaved members. A bond with nothing in it "+
					"is a dead end: the chain stops before it reaches a cable",
					r.Bridge, r.PeerAsset, r.PeerPort)
			}
			if r.PeerAsset != strings.TrimSuffix(r.Bridge, "-br0") {
				t.Errorf("%s uplinks into %s, which is not its own host", r.Bridge, r.PeerAsset)
			}
		}
	})
}

// TestEveryDataNICIsEnslavedToItsHostsBond covers the hop the link table cannot
// express, and the first use of interface.lag_parent_id in the repository.
//
// hv-03/eno3 is in the list on purpose: it is a configured member of the bond
// and carries no cable, which is what "the bond has two slots and one is
// patched" looks like. That keeps hv-03's single-homing a cabling decision
// rather than a missing NIC, which is the claim the port's own comment has
// always made and nothing has ever checked.
func TestEveryDataNICIsEnslavedToItsHostsBond(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		want := map[string]string{
			"hv-01/eno2": "hv-01/bond0",
			"hv-01/eno3": "hv-01/bond0",
			"hv-02/eno2": "hv-02/bond0",
			"hv-02/eno3": "hv-02/bond0",
			"hv-03/eno2": "hv-03/bond0",
			"hv-03/eno3": "hv-03/bond0",
		}

		var rows []struct {
			Slave  string `db:"slave"`
			Master string `db:"master"`
		}
		if err := f.store.DB().Reader.SelectContext(f.ctx, &rows, `
			SELECT sa.name || '/' || si.name AS slave, ma.name || '/' || mi.name AS master
			FROM interface si
			JOIN interface mi ON mi.id = si.lag_parent_id
			JOIN asset sa ON sa.id = si.asset_id
			JOIN asset ma ON ma.id = mi.asset_id
			ORDER BY slave`); err != nil {
			t.Fatalf("reading enslavements: %v", err)
		}

		got := map[string]string{}
		for _, r := range rows {
			got[r.Slave] = r.Master
		}
		for slave, master := range want {
			if got[slave] != master {
				t.Errorf("%s has master %q, want %q", slave, got[slave], master)
			}
		}
		for slave, master := range got {
			if _, expected := want[slave]; !expected {
				t.Errorf("%s is enslaved to %s and the fixture does not declare that", slave, master)
			}
		}

		// The management NIC must NOT be in the bond. eno1 is the out-of-band
		// path and folding it into the data bond would put the management plane
		// behind the data one -- the sw-oob-1 trap, one layer down.
		for _, mgmt := range []string{"hv-01/eno1", "hv-02/eno1", "hv-03/eno1"} {
			if master, enslaved := got[mgmt]; enslaved {
				t.Errorf("%s is enslaved to %s; the management NIC is a separate path and must not "+
					"be bonded into the data one", mgmt, master)
			}
		}
	})
}

// TestBridgesDeclareTheEnvironmentsTheyActuallyCarry.
//
// hv-03's bridge carries the production guests AND the development guest, so it
// really does span two non-transit environments and belongs in the
// segmentation-span report next to the two core switches. Declaring it
// production-only would make the fixture lie to keep a report short -- the same
// argument seed.go already makes about sw-core-2.
func TestBridgesDeclareTheEnvironmentsTheyActuallyCarry(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		want := map[string][]string{
			"hv-01-br0": {"prod"},
			"hv-02-br0": {"prod"},
			"hv-03-br0": {"dev", "prod"},
		}
		for bridge, codes := range want {
			id, ok := f.refs.Assets[bridge]
			if !ok {
				t.Fatalf("the fixture has no bridge named %s", bridge)
			}
			var got []string
			if err := f.store.DB().Reader.SelectContext(f.ctx, &got, `
				SELECT e.code FROM asset_environment ae
				JOIN environment e ON e.id = ae.environment_id
				WHERE ae.asset_id = ?
				ORDER BY e.code`, id); err != nil {
				t.Fatalf("reading environments of %s: %v", bridge, err)
			}
			sort.Strings(got)
			if strings.Join(got, ",") != strings.Join(codes, ",") {
				t.Errorf("%s is declared in %v, want %v -- a bridge carries whatever is plugged into "+
					"it, and hv-03's guests are not all production", bridge, got, codes)
			}
		}
	})
}

// linked reports whether two "asset/interface" names share an active link, in
// either orientation.
func linked(t *testing.T, f *fixture, a, b string) bool {
	t.Helper()
	aAsset, aPort := split(t, a)
	bAsset, bPort := split(t, b)
	var n int
	if err := f.store.DB().Reader.GetContext(f.ctx, &n, `
		SELECT COUNT(*)
		FROM link l
		JOIN interface xi ON xi.id = l.a_interface_id
		JOIN interface yi ON yi.id = l.b_interface_id
		JOIN asset xa ON xa.id = xi.asset_id
		JOIN asset ya ON ya.id = yi.asset_id
		WHERE l.lifecycle = 'active'
		  AND ((xa.name = ? AND xi.name = ? AND ya.name = ? AND yi.name = ?)
		    OR (xa.name = ? AND xi.name = ? AND ya.name = ? AND yi.name = ?))`,
		aAsset, aPort, bAsset, bPort, bAsset, bPort, aAsset, aPort); err != nil {
		t.Fatalf("looking for a link %s <-> %s: %v", a, b, err)
	}
	return n > 0
}

func split(t *testing.T, ref string) (asset, port string) {
	t.Helper()
	i := strings.Index(ref, "/")
	if i < 0 {
		t.Fatalf("malformed interface reference %q, want asset/interface", ref)
	}
	return ref[:i], ref[i+1:]
}
