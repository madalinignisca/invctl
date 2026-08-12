// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package seed_test

import (
	"testing"

	"github.com/madalinignisca/invctl/internal/impact"
	"github.com/madalinignisca/invctl/internal/seed"
)

// Circuits as connectivity edges (WP-E1b), against the fixture.
//
// The engine's rules are unit-tested; these prove the estate holds the
// arrangement the rules are for. Before the DR fibre was seeded, every circuit
// in this fixture terminated on a site or on a single interface, so not one was
// an edge and a test naming the feature would have been describing nothing.

// TestTheEstateHasACircuitThatJoinsTwoGroups.
//
// A NEGATIVE CONTROL IS BUILT IN: the fixture also holds circuits that join
// nothing, so a query returning "true" for everything would fail here rather
// than pass.
func TestTheEstateHasACircuitThatJoinsTwoGroups(t *testing.T) {
	seed.CompanyEstate = true
	t.Cleanup(func() { seed.CompanyEstate = false })

	eachEngine(t, func(t *testing.T, f *fixture) {
		s, ctx := f.store, f.ctx

		circuits, err := s.ListCircuits(ctx)
		if err != nil {
			t.Fatalf("listing circuits: %v", err)
		}
		joins, doesNot := 0, 0
		for _, c := range circuits {
			ok, err := s.CircuitJoinsGroups(ctx, c.ID)
			if err != nil {
				t.Fatalf("checking %s: %v", c.CID, err)
			}
			if ok {
				joins++
			} else {
				doesNot++
			}
		}
		if joins == 0 {
			t.Error("no circuit in the estate joins two groups, so nothing exercises " +
				"the edge derivation and 'simulate cutting this' has nothing to answer")
		}
		if doesNot == 0 {
			t.Error("every circuit joins two groups, so this cannot tell a working " +
				"derivation from one that returns true for anything — the fixture is " +
				"supposed to hold provider-terminated circuits too")
		}
	})
}

// TestCuttingTheDRFibreSeparatesBergen.
//
// The whole point of the work package: a circuit is not an asset, so this
// outage cannot be expressed as a down set and before E1b there was no way to
// ask the question at all.
//
// IT ASSERTS SEPARATION, NOT SERVICE IMPACT, and the difference is the honest
// one. The DR site currently runs nothing, so cutting the only fibre to it
// partitions the estate and breaks no service -- and an assertion on findings
// would have been satisfied by the circuit contributing no edge whatsoever.
// The first version of this test made exactly that mistake and reported
// "0 -> 0" against a mechanism that was working.
func TestCuttingTheDRFibreSeparatesBergen(t *testing.T) {
	seed.CompanyEstate = true
	t.Cleanup(func() { seed.CompanyEstate = false })

	eachEngine(t, func(t *testing.T, f *fixture) {
		s, ctx := f.store, f.ctx

		var fibreID string
		circuits, err := s.ListCircuits(ctx)
		if err != nil {
			t.Fatalf("listing circuits: %v", err)
		}
		for _, c := range circuits {
			if c.CID == "DF-OSLO-BGO-01" {
				fibreID = c.ID
			}
		}
		if fibreID == "" {
			t.Fatal("the DR fibre is not in the estate, so this proves nothing")
		}

		cut, err := s.CircuitCutEffect(ctx, fibreID)
		if err != nil {
			t.Fatalf("resolving the cut: %v", err)
		}
		if !cut.Joins {
			t.Fatal("the DR fibre contributes no edge; both ends land on interfaces " +
				"of assets in different groups, so it should")
		}
		if !cut.Separates {
			t.Errorf("cutting the only fibre to the DR site separates nothing. It is "+
				"the sole path to Bergen, so either another path has been added or the "+
				"edge is not being withdrawn. Groups: %v", cut.Groups)
		}
		if len(cut.Groups) != 2 {
			t.Errorf("the cut names %v, want the two groups it joins", cut.Groups)
		}
	})
}

// TestCuttingACircuitThatJoinsNothingChangesNothing.
//
// The other half, and the one that catches a derivation that fires on every
// circuit: a provider-terminated circuit is not an edge, so cutting it must
// leave the answer exactly as it was.
func TestCuttingACircuitThatJoinsNothingChangesNothing(t *testing.T) {
	seed.CompanyEstate = true
	t.Cleanup(func() { seed.CompanyEstate = false })

	eachEngine(t, func(t *testing.T, f *fixture) {
		s, ctx := f.store, f.ctx

		var lonely string
		circuits, err := s.ListCircuits(ctx)
		if err != nil {
			t.Fatalf("listing circuits: %v", err)
		}
		for _, c := range circuits {
			ok, err := s.CircuitJoinsGroups(ctx, c.ID)
			if err != nil {
				t.Fatalf("checking %s: %v", c.CID, err)
			}
			if !ok {
				lonely = c.ID
				break
			}
		}
		if lonely == "" {
			t.Fatal("every circuit joins two groups, so there is no non-edge to check")
		}

		before, err := s.Simulate(ctx, impact.Request{WindowSeconds: 180})
		if err != nil {
			t.Fatalf("baseline: %v", err)
		}
		after, err := s.Simulate(ctx, impact.Request{
			CutCircuitIDs: []string{lonely}, WindowSeconds: 180,
		})
		if err != nil {
			t.Fatalf("cutting: %v", err)
		}
		if len(after.Isolated) != len(before.Isolated) ||
			len(after.Unreachable) != len(before.Unreachable) {
			t.Errorf("cutting a circuit that joins nothing changed the answer "+
				"(isolated %d->%d, unreachable %d->%d). It terminates at a provider "+
				"whose side is not modelled; it cannot partition anything",
				len(before.Isolated), len(after.Isolated),
				len(before.Unreachable), len(after.Unreachable))
		}
	})
}
