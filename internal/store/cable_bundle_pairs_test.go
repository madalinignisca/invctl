// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// THE DEFECT THIS FIXES: cutEffect collected every matching edge into `ends`
// but only ever read Groups off ends[0] -- so a bundle that is the only path
// between TWO DIFFERENT pairs of groups reported just the first pair. The
// verdict (Separates) was always computed across every edge and was correct;
// only the sentence under-reported. This file pins the report, not the
// verdict -- TestBundleCutEffectIsTheSameWalkerWithASetPredicate (Task 2)
// already pins Separates itself.

// TestBundleCutEffectReportsEveryPairItSeparates is the motivating case: a
// bundle whose two cables are each the sole path for a DIFFERENT pair of
// groups must report both boundaries, not the first cable's alone.
func TestBundleCutEffectReportsEveryPairItSeparates(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			// A-B: joined solely by cable1.
			cable1, a1, b1 := cabled(t, s, ctx, "sw-pairs-a1", "sw-pairs-b1")
			gA := mustNetGroup(t, s, ctx, "grp-pairs-a", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			gB := mustNetGroup(t, s, ctx, "grp-pairs-b", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			mustNetGroupMember(t, s, ctx, gA, a1, "member")
			mustNetGroupMember(t, s, ctx, gB, b1, "member")

			// C-D: an entirely different pair of groups, joined solely by
			// cable2. Nothing connects {A,B} to {C,D}, so the two boundaries
			// are independent of each other.
			cable2, c1, d1 := cabled(t, s, ctx, "sw-pairs-c1", "sw-pairs-d1")
			gC := mustNetGroup(t, s, ctx, "grp-pairs-c", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			gD := mustNetGroup(t, s, ctx, "grp-pairs-d", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			mustNetGroupMember(t, s, ctx, gC, c1, "member")
			mustNetGroupMember(t, s, ctx, gD, d1, "member")

			bundleID := mustBundle(t, s, ctx, "duct-pairs", "Two Boundary Duct")
			if err := s.SetBundleMembers(ctx, testPermit, bundleID, []string{cable1, cable2}); err != nil {
				t.Fatalf("bundling both cables: %v", err)
			}

			got, err := s.BundleCutEffect(ctx, bundleID)
			if err != nil {
				t.Fatalf("BundleCutEffect: %v", err)
			}
			if !got.Joins {
				t.Fatal("the bundle joins nothing; this test proves nothing")
			}
			if !got.Separates {
				t.Fatal("the bundle does not separate anything; it holds the only cable " +
					"for two independent boundaries")
			}
			if len(got.SeparatedPairs) != 2 {
				t.Fatalf("SeparatedPairs = %+v, want both boundaries this bundle opens "+
					"(the defect this fixes reported only the first)", got.SeparatedPairs)
			}
			if len(got.JoinedPairs) != 2 {
				t.Fatalf("JoinedPairs = %+v, want both pairs this bundle touches", got.JoinedPairs)
			}
		})
	}
}

// TestSingleCableCutReportsOnePairUnchanged: a single cable's cut has always
// produced exactly one edge, so it must keep reporting exactly one pair,
// with no behaviour change from before this fix.
func TestSingleCableCutReportsOnePairUnchanged(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			cable, a, b := cabled(t, s, ctx, "sw-single-a", "sw-single-b")
			gA := mustNetGroup(t, s, ctx, "grp-single-a", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			gB := mustNetGroup(t, s, ctx, "grp-single-b", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			mustNetGroupMember(t, s, ctx, gA, a, "member")
			mustNetGroupMember(t, s, ctx, gB, b, "member")

			cut, err := s.LinkCutEffect(ctx, cable)
			if err != nil {
				t.Fatalf("LinkCutEffect: %v", err)
			}
			if !cut.Joins || !cut.Separates {
				t.Fatalf("cut = %+v, want a single cable that is the only path", cut)
			}
			if len(cut.JoinedPairs) != 1 {
				t.Fatalf("JoinedPairs = %+v, want exactly one pair for a single cable", cut.JoinedPairs)
			}
			if len(cut.SeparatedPairs) != 1 {
				t.Fatalf("SeparatedPairs = %+v, want exactly one pair for a single cable", cut.SeparatedPairs)
			}
			if cut.SeparatedPairs[0] != cut.JoinedPairs[0] {
				t.Fatalf("SeparatedPairs %+v and JoinedPairs %+v disagree; a single "+
					"cable's only edge is both what it touches and what it opens",
					cut.SeparatedPairs[0], cut.JoinedPairs[0])
			}
		})
	}
}

// TestBundleCutEffectDedupesTwoCablesBetweenTheSamePair: two cables in a
// bundle landing on the same two groups is one boundary, not two -- the
// bundle is the only path for that pair exactly once, however many cables
// carry it.
func TestBundleCutEffectDedupesTwoCablesBetweenTheSamePair(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			cable1, a1, b1 := cabled(t, s, ctx, "sw-dedup-a1", "sw-dedup-b1")
			cable2, a2, b2 := cabled(t, s, ctx, "sw-dedup-a2", "sw-dedup-b2")
			gA := mustNetGroup(t, s, ctx, "grp-dedup-a", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			gB := mustNetGroup(t, s, ctx, "grp-dedup-b", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			mustNetGroupMember(t, s, ctx, gA, a1, "member")
			mustNetGroupMember(t, s, ctx, gB, b1, "member")
			mustNetGroupMember(t, s, ctx, gA, a2, "member")
			mustNetGroupMember(t, s, ctx, gB, b2, "member")

			bundleID := mustBundle(t, s, ctx, "duct-dedup", "Same Pair Duct")
			if err := s.SetBundleMembers(ctx, testPermit, bundleID, []string{cable1, cable2}); err != nil {
				t.Fatalf("bundling both cables: %v", err)
			}

			got, err := s.BundleCutEffect(ctx, bundleID)
			if err != nil {
				t.Fatalf("BundleCutEffect: %v", err)
			}
			if !got.Joins || !got.Separates {
				t.Fatalf("cut = %+v, want the bundle to hold the only two cables joining "+
					"A and B", got)
			}
			if len(got.JoinedPairs) != 1 {
				t.Fatalf("JoinedPairs = %+v, want the A-B pair reported ONCE even though "+
					"two cables carry it", got.JoinedPairs)
			}
			if len(got.SeparatedPairs) != 1 {
				t.Fatalf("SeparatedPairs = %+v, want the A-B boundary reported ONCE",
					got.SeparatedPairs)
			}
		})
	}
}

// TestCutThatSeparatesNothingReportsNoPairsSeparated: a cable that joins two
// groups but is not the only path between them must still report Joins ==
// true (what it touches) while SeparatedPairs stays empty -- Separates ==
// false must not be paired with an invented boundary.
func TestCutThatSeparatesNothingReportsNoPairsSeparated(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			// Two REDUNDANT cables between the same two groups, cabled and
			// grouped exactly as TestBundleCutEffectIsTheSameWalkerWithASetPredicate
			// does -- alone, neither separates anything because the other
			// still joins the groups.
			cable1, a1, b1 := cabled(t, s, ctx, "sw-redundant-a1", "sw-redundant-b1")
			cable2, a2, b2 := cabled(t, s, ctx, "sw-redundant-a2", "sw-redundant-b2")
			gA := mustNetGroup(t, s, ctx, "grp-redundant-a", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			gB := mustNetGroup(t, s, ctx, "grp-redundant-b", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			mustNetGroupMember(t, s, ctx, gA, a1, "member")
			mustNetGroupMember(t, s, ctx, gB, b1, "member")
			mustNetGroupMember(t, s, ctx, gA, a2, "member")
			mustNetGroupMember(t, s, ctx, gB, b2, "member")
			_ = cable2

			cut, err := s.LinkCutEffect(ctx, cable1)
			if err != nil {
				t.Fatalf("LinkCutEffect: %v", err)
			}
			if !cut.Joins {
				t.Fatal("cable1 joins nothing; this test proves nothing")
			}
			if cut.Separates {
				t.Fatal("cable1 alone separates the groups; the redundancy this test " +
					"relies on is not present")
			}
			if len(cut.SeparatedPairs) != 0 {
				t.Fatalf("SeparatedPairs = %+v, want none invented when Separates is false",
					cut.SeparatedPairs)
			}
			if len(cut.JoinedPairs) != 1 {
				t.Fatalf("JoinedPairs = %+v, want the one pair this cable still touches",
					cut.JoinedPairs)
			}
		})
	}
}
