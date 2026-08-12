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

// A circuit as a connectivity edge, at the level the engine actually works at.
//
// WRITTEN BECAUSE MUTATION FOUND THE HOLE. The store-level tests asserted that
// cutting the DR fibre separates Bergen -- and they went on passing with the
// engine's withdrawal disabled entirely, because the page's answer comes from
// its own breadth-first walk and never touched components(). Two implementations
// of "is this still connected" with nothing holding them to each other is the
// exact shape of a bug that shows up as a page disagreeing with a simulation.
//
// These exercise components() directly, which is the union-find every
// reachability answer is built on.

// twoGroups builds the smallest graph that can be partitioned: two forwarder
// groups and whatever edges a test wants between them.
func twoGroups(uplinks ...NetUplinkInfo) (*NetGraph, map[string]domain.Status) {
	net := NewNetGraph()
	net.Groups["g-oslo"] = NetGroupInfo{ID: "g-oslo", Code: "sw-core"}
	net.Groups["g-bergen"] = NetGroupInfo{ID: "g-bergen", Code: "sw-dr"}
	net.Uplinks = uplinks
	status := map[string]domain.Status{
		"g-oslo":   domain.StatusOK,
		"g-bergen": domain.StatusOK,
	}
	return net, status
}

func joined(comp map[string]string) bool { return comp["g-oslo"] == comp["g-bergen"] }

// TestACircuitEdgeJoinsTwoGroups. Without this the derivation could produce
// rows nothing reads.
func TestACircuitEdgeJoinsTwoGroups(t *testing.T) {
	net, status := twoGroups(NetUplinkInfo{
		GroupID: "g-oslo", UpstreamGroupID: "g-bergen",
		Plane: domain.PlaneData, CircuitID: "c1", Label: "DF-1",
	})
	comp := components(net, status, domain.PlaneData, alive, nil)
	if !joined(comp) {
		t.Error("two groups joined only by a circuit are in different components; " +
			"the circuit edge is not being unioned")
	}

	// NEGATIVE CONTROL: no edge at all, so they must NOT be joined. Without
	// this, a components() that joined everything would satisfy the assertion
	// above.
	bare, bareStatus := twoGroups()
	if joined(components(bare, bareStatus, domain.PlaneData, alive, nil)) {
		t.Error("two groups with no edge between them are in one component")
	}
}

// TestCuttingTheCircuitWithdrawsItsEdge. The half the store-level test could
// not see.
func TestCuttingTheCircuitWithdrawsItsEdge(t *testing.T) {
	net, status := twoGroups(NetUplinkInfo{
		GroupID: "g-oslo", UpstreamGroupID: "g-bergen",
		Plane: domain.PlaneData, CircuitID: "c1",
	})
	if !joined(components(net, status, domain.PlaneData, alive, nil)) {
		t.Fatal("the groups are not joined before the cut, so cutting proves nothing")
	}
	cut := components(net, status, domain.PlaneData, alive, map[string]bool{"c1": true})
	if joined(cut) {
		t.Error("cutting the only circuit joining two groups left them in one " +
			"component; the edge is not being withdrawn")
	}
}

// TestCuttingOneCircuitLeavesTheOthers.
//
// Two things at once, both of which a naive implementation gets wrong: cutting
// by id must not drop every circuit edge, and a second path must survive the
// cut -- which is the answer redundancy is bought for and the one an operator
// most wants to be true.
func TestCuttingOneCircuitLeavesTheOthers(t *testing.T) {
	net, status := twoGroups(
		NetUplinkInfo{GroupID: "g-oslo", UpstreamGroupID: "g-bergen",
			Plane: domain.PlaneData, CircuitID: "c1"},
		NetUplinkInfo{GroupID: "g-oslo", UpstreamGroupID: "g-bergen",
			Plane: domain.PlaneData, CircuitID: "c2"},
	)
	if !joined(components(net, status, domain.PlaneData, alive, map[string]bool{"c1": true})) {
		t.Error("cutting one of two circuits separated the groups; the other one " +
			"still joins them, and reporting an outage here would be crying wolf")
	}
	// Both gone is a real partition, so the check above is not simply never
	// separating anything.
	if joined(components(net, status, domain.PlaneData, alive,
		map[string]bool{"c1": true, "c2": true})) {
		t.Error("cutting both circuits left the groups joined")
	}
}

// TestADeclaredUplinkIsNeverWithdrawnByACircuitCut.
//
// CircuitID is empty on a declared net_uplink, and a cut set must not touch it.
// A `cutCircuits[u.CircuitID]` lookup without the empty-string guard would drop
// every declared uplink the moment any circuit was cut -- turning a fibre cut
// into a total estate partition, which is both alarming and wrong.
func TestADeclaredUplinkIsNeverWithdrawnByACircuitCut(t *testing.T) {
	net, status := twoGroups(NetUplinkInfo{
		GroupID: "g-oslo", UpstreamGroupID: "g-bergen", Plane: domain.PlaneData,
	})
	// A cut set containing the empty string is the shape a missing guard would
	// match against.
	cut := components(net, status, domain.PlaneData, alive, map[string]bool{"": true, "c1": true})
	if !joined(cut) {
		t.Error("a declared uplink was withdrawn by a circuit cut; only edges " +
			"carrying that circuit's id may be dropped")
	}
}

// TestACircuitOnAnotherPlaneDoesNotJoinTheDataPlane. Planes are separate
// graphs, and an edge leaking between them would make a management link look
// like a production path.
func TestACircuitOnAnotherPlaneDoesNotJoinTheDataPlane(t *testing.T) {
	net, status := twoGroups(NetUplinkInfo{
		GroupID: "g-oslo", UpstreamGroupID: "g-bergen",
		Plane: domain.PlaneMgmt, CircuitID: "c1",
	})
	if joined(components(net, status, domain.PlaneData, alive, nil)) {
		t.Error("a management-plane edge joined two groups on the data plane")
	}
	if !joined(components(net, status, domain.PlaneMgmt, alive, nil)) {
		t.Error("the edge does not join them on its own plane either, so the test " +
			"above proves nothing about planes")
	}
}

func alive(s domain.Status) bool { return s != domain.StatusDown }
