// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package seed_test

import (
	"fmt"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/impact"
	"github.com/madalinignisca/invctl/internal/store"
)

// The fixture has to DEMONSTRATE what the engine learned, not merely contain
// the rows.
//
// This is the same argument seed_hardware_test.go makes for the power chain,
// and it is the half of WP-I1 that was missing: cluster HA changed a
// PROPAGATION, and the fixture the suite reasons about had no cluster in it. So
// the feature was proven by unit tests and demonstrated by nothing -- break it
// tomorrow and every test here stays green while a fresh seed quietly reports
// every guest lost.

func simulateLoss(t *testing.T, f *fixture, assetName string) impact.Result {
	t.Helper()
	id, ok := f.refs.Assets[assetName]
	if !ok {
		t.Fatalf("the fixture has no %s", assetName)
	}
	res, err := f.store.Simulate(f.ctx, impact.Request{
		DownAssetIDs: []string{id}, WindowSeconds: 300,
	})
	if err != nil {
		t.Fatalf("simulating the loss of %s: %v", assetName, err)
	}
	return res
}

// TestTheClusterReachesTheEngineAndCannotSaveItsGuests.
//
// The fixture's cluster has three hosts and needs three, so losing one is NOT
// survivable -- an estate that believes it has HA and has outgrown it, which is
// the finding worth demonstrating and looks identical to a healthy cluster
// everywhere else.
//
// It is also the only arrangement that leaves the fixture honest.
// TestContainmentResolvesThroughClosure asserts that losing a hypervisor takes
// its guests, which is correct; giving this cluster spare capacity would have
// relocated them and quietly rewritten what that test says the engine does.
// Successful relocation is proven by unit tests that build their own clusters.
func TestTheClusterReachesTheEngineAndCannotSaveItsGuests(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		res := simulateLoss(t, f, "hv-01")

		if len(res.Relocations) == 0 {
			t.Fatal("losing hv-01 produced no relocation finding at all; the fixture's " +
				"cluster is not reaching the engine and nothing would notice if it " +
				"stopped being loaded")
		}
		var found *impact.RelocationFinding
		for i := range res.Relocations {
			if res.Relocations[i].ClusterName == "prod-virt" {
				found = &res.Relocations[i]
			}
		}
		if found == nil {
			t.Fatalf("prod-virt is not among the relocations: %+v", res.Relocations)
		}
		if found.Relocated() {
			t.Errorf("prod-virt relocated its guests with %d of %d hosts needed; the "+
				"fixture is meant to demonstrate HA that cannot help",
				found.Surviving, found.Needed)
		}
		if found.Guests == 0 {
			t.Error("the finding names no guests, so it demonstrates nothing")
		}
		if found.Surviving != 2 || found.Needed != 3 {
			t.Errorf("the finding says %d surviving of %d needed, want 2 of 3",
				found.Surviving, found.Needed)
		}
	})
}

// TestLosingASwitchEmptiesTheVLANThatOnlyLivedOnIt. The WP-I1 structure
// finding, and the reason VLAN 99 is deliberately on one switch.
func TestLosingASwitchEmptiesTheVLANThatOnlyLivedOnIt(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		res := simulateLoss(t, f, "sw-core-1")

		// BY NAME, not by counting. Asserting "at least one emptied and at least
		// one reduced" is satisfied by any arrangement that happens to produce
		// both, so deleting management's second port left it passing -- the
		// fixture had quietly stopped demonstrating what its comment claims.
		state := map[string]int{}
		for _, s := range res.Structures {
			if s.Kind == impact.StructureVLAN {
				state[s.Name] = s.Remaining
			}
		}
		if r, ok := state["transit"]; !ok || r != 0 {
			t.Errorf("transit is %v after losing sw-core-1, want emptied. It lives on "+
				"that switch alone precisely so this finding has something to find",
				stateOf(state, "transit"))
		}
		for _, name := range []string{"management", "production-workloads"} {
			if r, ok := state[name]; !ok || r != 1 {
				t.Errorf("%s is %v after losing sw-core-1, want one asset left. It spans "+
					"both core switches precisely so this finding has something to find",
					name, stateOf(state, name))
			}
		}
	})
}

// TestTheFixtureHasARedundancyGroupThatIsNotRedundancy. No outage needed: a
// VRRP group with one router is a single point of failure wearing the costume
// of a redundant one, and the overview counts it.
func TestTheFixtureHasARedundancyGroupThatIsNotRedundancy(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		groups, err := f.store.ListFHRPGroups(f.ctx)
		if err != nil {
			t.Fatalf("listing groups: %v", err)
		}
		var single, redundant int
		for _, g := range groups {
			switch g.Redundancy() {
			case domain.FHRPSingleMember:
				single++
			case domain.FHRPRedundant:
				redundant++
			}
		}
		if single == 0 {
			t.Error("no single-member group: the finding this model exists for has " +
				"nothing to demonstrate")
		}
		if redundant == 0 {
			t.Error("no healthy group either, so the fixture cannot show the difference")
		}
	})
}

// TestLosingAFirewallReducesTheHealthyGroupToOne. The other half: a group that
// survives an outage and will not survive the next one.
func TestLosingAFirewallReducesTheHealthyGroupToOne(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		res := simulateLoss(t, f, "fw-edge-2")

		found := false
		for _, s := range res.Structures {
			if s.Kind == impact.StructureFHRP && s.Name == "gw-prod" && s.Remaining == 1 {
				found = true
			}
		}
		if !found {
			t.Errorf("losing fw-edge-2 did not report gw-prod as down to one router; "+
				"got %+v", res.Structures)
		}
	})
}

// TestTheFixtureHasAnIncompleteOverlayAndCircuit. Both are the states worth
// showing: a complete one demonstrates nothing the healthy estate does not.
func TestTheFixtureHasAnIncompleteOverlayAndCircuit(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		overlays, err := f.store.ListL2VPNs(f.ctx)
		if err != nil {
			t.Fatalf("listing overlays: %v", err)
		}
		oneEnded := false
		for _, o := range overlays {
			if o.Reach() == domain.L2VPNOneEnd {
				oneEnded = true
			}
		}
		if !oneEnded {
			t.Error("no one-ended overlay: an overlay connecting nothing to anything is " +
				"the finding, and the fixture has none")
		}

		circuits, err := f.store.ListCircuits(f.ctx)
		if err != nil {
			t.Fatalf("listing circuits: %v", err)
		}
		// EXACTLY ONE END, not merely "fewer than two". A circuit with no ends at
		// all also satisfies !Landed(), so the looser assertion passed with the
		// termination deleted -- and a circuit nobody has landed anywhere is a
		// different, duller finding than one where somebody recorded where it
		// arrives and never where it comes from.
		halfLanded := 0
		for _, c := range circuits {
			if c.Terminations == 1 {
				halfLanded++
			}
		}
		if halfLanded == 0 {
			t.Errorf("no circuit with exactly one end recorded. Half a fact is the "+
				"finding -- somebody knows where it arrives and not where it comes "+
				"from -- and the fixture demonstrates it with %d circuit(s)", len(circuits))
		}
	})
}

// TestTheOverviewFindsAllOfIt. The findings page is what somebody opens first,
// so the fixture must give it something in every severity.
func TestTheOverviewFindsAllOfIt(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		findings, err := f.store.EstateFindings(f.ctx)
		if err != nil {
			t.Fatalf("gathering findings: %v", err)
		}
		bySeverity := map[string]int{}
		labels := map[string]bool{}
		for _, fd := range findings {
			bySeverity[fd.Severity]++
			labels[fd.Label] = true
		}
		for _, sev := range []string{store.FindingFault, store.FindingRisk, store.FindingGap} {
			if bySeverity[sev] == 0 {
				t.Errorf("the fixture produces no %q finding, so the overview cannot "+
					"show what that severity looks like", sev)
			}
		}
		for _, want := range []string{
			"redundancy group with one member",
			"overlay with one end",
			"circuit missing an end",
		} {
			if !labels[want] {
				t.Errorf("the overview has no %q row; the fixture stopped demonstrating it", want)
			}
		}
	})
}

// stateOf renders a VLAN's post-outage state for an error message: how many
// assets are left, or that it was not reported at all -- which are different
// failures and read identically as a bare integer.
func stateOf(state map[string]int, name string) string {
	if r, ok := state[name]; ok {
		return fmt.Sprintf("down to %d asset(s)", r)
	}
	return "not reported at all"
}
