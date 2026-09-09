// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package impact

import (
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// A CABLE as a connectivity edge -- WP-B3's engine half, at the level the
// engine actually works at.
//
// The sibling file (circuit_edge_test.go) says why these exercise components()
// directly rather than going through a page: the page has its own
// breadth-first walk, and two implementations of "is this still connected"
// with nothing holding them to each other is the exact shape of a bug that
// surfaces as a page disagreeing with a simulation.

// TestACableEdgeJoinsTwoGroups.
func TestACableEdgeJoinsTwoGroups(t *testing.T) {
	net, status := twoGroups(NetUplinkInfo{
		GroupID: "g-oslo", UpstreamGroupID: "g-bergen",
		Plane: domain.PlaneData, LinkID: "l1", Label: "sw-core eth1 – sw-dr eth1",
	})
	if !joined(components(net, status, domain.PlaneData, alive, cutMedia{})) {
		t.Error("two groups joined only by a cable are in different components; " +
			"the link edge is not being unioned")
	}

	// NEGATIVE CONTROL: no edge at all, so they must NOT be joined. Without
	// this a components() that joined everything would satisfy the assertion
	// above.
	bare, bareStatus := twoGroups()
	if joined(components(bare, bareStatus, domain.PlaneData, alive, cutMedia{})) {
		t.Error("two groups with no edge between them are in one component")
	}
}

// TestCuttingTheCableWithdrawsItsEdge is the whole of WP-B3's engine half.
//
// Before this, `impact.Request` accepted DownAssetIDs and CutCircuitIDs and
// nothing else, so cutting a cable concluded nothing: an operator's only
// recourse was to down an asset, which answers a different and blunter
// question. A switch losing power is not the same event as one of its uplinks
// being unplugged.
func TestCuttingTheCableWithdrawsItsEdge(t *testing.T) {
	net, status := twoGroups(NetUplinkInfo{
		GroupID: "g-oslo", UpstreamGroupID: "g-bergen",
		Plane: domain.PlaneData, LinkID: "l1",
	})
	if !joined(components(net, status, domain.PlaneData, alive, cutMedia{})) {
		t.Fatal("the groups are not joined before the cut, so cutting proves nothing")
	}
	cut := components(net, status, domain.PlaneData, alive,
		cutMedia{links: map[string]bool{"l1": true}})
	if joined(cut) {
		t.Error("cutting the only cable joining two groups left them in one " +
			"component; the edge is not being withdrawn")
	}
}

// TestASurvivingPathMakesACableCutHarmless -- the answer redundancy is bought
// for, and the one that must not be reported as a partition.
func TestASurvivingPathMakesACableCutHarmless(t *testing.T) {
	net, status := twoGroups(
		NetUplinkInfo{GroupID: "g-oslo", UpstreamGroupID: "g-bergen",
			Plane: domain.PlaneData, LinkID: "l1"},
		NetUplinkInfo{GroupID: "g-oslo", UpstreamGroupID: "g-bergen",
			Plane: domain.PlaneData, LinkID: "l2"},
	)
	if !joined(components(net, status, domain.PlaneData, alive,
		cutMedia{links: map[string]bool{"l1": true}})) {
		t.Error("cutting one of two cables separated the groups; a surviving " +
			"path must leave the partition unchanged")
	}
	// Both gone is a real partition, so the check above is not simply never
	// separating anything.
	if joined(components(net, status, domain.PlaneData, alive,
		cutMedia{links: map[string]bool{"l1": true, "l2": true}})) {
		t.Error("cutting both cables left the groups joined")
	}
}

// TestCuttingACableDoesNotWithdrawACircuitEdge, and the converse.
//
// THE TWO ID SPACES MUST NOT BE CONFUSED. cutMedia.severs checks each id
// against its own map; a single shared map keyed on "whatever id this edge
// carries" would work by accident today -- both are UUIDv7 and never collide --
// and would silently start dropping the wrong edges the first time anything
// reused an identifier.
func TestCuttingACableDoesNotWithdrawACircuitEdge(t *testing.T) {
	net, status := twoGroups(NetUplinkInfo{
		GroupID: "g-oslo", UpstreamGroupID: "g-bergen",
		Plane: domain.PlaneData, CircuitID: "c1",
	})
	if !joined(components(net, status, domain.PlaneData, alive,
		cutMedia{links: map[string]bool{"c1": true}})) {
		t.Error("a circuit edge was withdrawn by a CABLE cut naming the same id; " +
			"the two id spaces are being conflated")
	}

	net2, status2 := twoGroups(NetUplinkInfo{
		GroupID: "g-oslo", UpstreamGroupID: "g-bergen",
		Plane: domain.PlaneData, LinkID: "l1",
	})
	if !joined(components(net2, status2, domain.PlaneData, alive,
		cutMedia{circuits: map[string]bool{"l1": true}})) {
		t.Error("a cable edge was withdrawn by a CIRCUIT cut naming the same id")
	}
}

// TestADeclaredUplinkIsNeverWithdrawnByACableCut.
//
// LinkID is empty on a declared net_uplink and on a circuit-derived one, and a
// cable cut must touch neither. A `cutLinks[u.LinkID]` lookup without the
// empty-string guard would drop every declared uplink the moment any cable was
// cut -- turning one unplugged patch lead into a total estate partition, which
// is both alarming and wrong. The circuit half of this file makes the identical
// assertion; the guard lives in cutMedia.severs and covers both.
func TestADeclaredUplinkIsNeverWithdrawnByACableCut(t *testing.T) {
	net, status := twoGroups(NetUplinkInfo{
		GroupID: "g-oslo", UpstreamGroupID: "g-bergen", Plane: domain.PlaneData,
	})
	// A cut set containing the empty string is the shape a missing guard would
	// match against.
	if !joined(components(net, status, domain.PlaneData, alive,
		cutMedia{links: map[string]bool{"": true, "l1": true}})) {
		t.Error("a declared uplink was withdrawn by a cable cut; only edges " +
			"carrying that cable's id may be dropped")
	}
}

// TestACableOnAnotherPlaneDoesNotJoinTheDataPlane. Planes are separate graphs.
func TestACableOnAnotherPlaneDoesNotJoinTheDataPlane(t *testing.T) {
	net, status := twoGroups(NetUplinkInfo{
		GroupID: "g-oslo", UpstreamGroupID: "g-bergen",
		Plane: domain.PlaneMgmt, LinkID: "l1",
	})
	if joined(components(net, status, domain.PlaneData, alive, cutMedia{})) {
		t.Error("a management-plane cable joined two groups on the data plane")
	}
	if !joined(components(net, status, domain.PlaneMgmt, alive, cutMedia{})) {
		t.Error("the edge does not join them on its own plane either, so the " +
			"test above proves nothing about planes")
	}
}
