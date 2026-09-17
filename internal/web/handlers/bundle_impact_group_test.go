// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"testing"

	"github.com/madalinignisca/invctl/internal/store"
)

// Task 5's OWN GUARD (docs/breakout-cables-design.md D5): "a page that
// silently stopped grouping would overstate every breakout in it, and
// nothing else would notice." This file is that guard -- it pins
// groupBundleMembers directly, independent of the store or a rendered page,
// so a regression here fails fast and names exactly what broke.

func breakoutID(v string) *string { return &v }
func strandPos(v int) *int        { return &v }

// TestGroupBundleMembersFoldsABreakoutIntoOneEntry is the motivating case
// from the design doc: four link rows sharing one breakout_id must collapse
// to ONE group, not four -- "1 breakout cable (4 strands)", not "4 cables".
func TestGroupBundleMembersFoldsABreakoutIntoOneEntry(t *testing.T) {
	bID := "brk-1"
	members := []store.BundleMemberRow{
		{LinkID: "l1", Lifecycle: "active", BreakoutID: breakoutID(bID), BreakoutPosition: strandPos(1)},
		{LinkID: "l2", Lifecycle: "active", BreakoutID: breakoutID(bID), BreakoutPosition: strandPos(2)},
		{LinkID: "l3", Lifecycle: "active", BreakoutID: breakoutID(bID), BreakoutPosition: strandPos(3)},
		{LinkID: "l4", Lifecycle: "active", BreakoutID: breakoutID(bID), BreakoutPosition: strandPos(4)},
	}

	groups := groupBundleMembers(members)
	if len(groups) != 1 {
		t.Fatalf("groupBundleMembers returned %d groups, want 1 -- a duct holding one "+
			"physical breakout cable must report one entry, not %d", len(groups), len(members))
	}
	if !groups[0].IsBreakout() {
		t.Error("the folded group does not report IsBreakout() -- the page would render it " +
			"as an ordinary cable instead of naming it a breakout")
	}
	if len(groups[0].Strands) != 4 {
		t.Fatalf("the folded group carries %d strands, want all 4 -- the set of things "+
			"going dark must stay complete even though the COUNT changes", len(groups[0].Strands))
	}
}

// TestGroupBundleMembersLeavesOrdinaryCablesAlone: a bundle with no breakouts
// in it must report exactly as many groups as cables -- the fold must not
// change behaviour for the common case.
func TestGroupBundleMembersLeavesOrdinaryCablesAlone(t *testing.T) {
	members := []store.BundleMemberRow{
		{LinkID: "a1", Lifecycle: "active"},
		{LinkID: "a2", Lifecycle: "active"},
		{LinkID: "a3", Lifecycle: "retired"},
	}
	groups := groupBundleMembers(members)
	if len(groups) != 3 {
		t.Fatalf("groupBundleMembers returned %d groups for 3 ordinary cables, want 3",
			len(groups))
	}
	for _, g := range groups {
		if g.IsBreakout() {
			t.Errorf("group for link %s reports IsBreakout() true, want false -- it has no "+
				"breakout_id", g.Strands[0].LinkID)
		}
	}
}

// TestGroupBundleMembersDoesNotClaimBreakoutForALoneStrand: a breakout with
// only ONE of its strands actually pulled through this particular duct must
// not be reported as "1 breakout cable (1 strand)" -- that claims a fact
// (how many strands are in THIS bundle) that isn't true; it displays exactly
// like an ordinary cable instead.
func TestGroupBundleMembersDoesNotClaimBreakoutForALoneStrand(t *testing.T) {
	members := []store.BundleMemberRow{
		{LinkID: "lone", Lifecycle: "active", BreakoutID: breakoutID("brk-lone"), BreakoutPosition: strandPos(1)},
	}
	groups := groupBundleMembers(members)
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	if groups[0].IsBreakout() {
		t.Error("a group with exactly one strand reports IsBreakout() true -- it would " +
			"render as a breakout cable this bundle only partially contains")
	}
}

// TestGroupBundleMembersHandlesTwoSeparateBreakouts: two distinct breakouts
// in the same duct must fold into two groups, not one and not four -- proves
// the fold keys on breakout_id, not merely "has one".
func TestGroupBundleMembersHandlesTwoSeparateBreakouts(t *testing.T) {
	members := []store.BundleMemberRow{
		{LinkID: "x1", Lifecycle: "active", BreakoutID: breakoutID("brk-x"), BreakoutPosition: strandPos(1)},
		{LinkID: "x2", Lifecycle: "active", BreakoutID: breakoutID("brk-x"), BreakoutPosition: strandPos(2)},
		{LinkID: "y1", Lifecycle: "active", BreakoutID: breakoutID("brk-y"), BreakoutPosition: strandPos(1)},
		{LinkID: "y2", Lifecycle: "active", BreakoutID: breakoutID("brk-y"), BreakoutPosition: strandPos(2)},
	}
	groups := groupBundleMembers(members)
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2 -- two distinct breakouts must not fold into one "+
			"or stay unfolded into four", len(groups))
	}
	for _, g := range groups {
		if !g.IsBreakout() || len(g.Strands) != 2 {
			t.Errorf("group %+v is not a 2-strand breakout as built", g)
		}
	}
}
