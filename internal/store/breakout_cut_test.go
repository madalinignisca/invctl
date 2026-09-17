// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"sort"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Task 4 of the breakout-cables plan: "cutting one strand of a breakout cuts
// all of them -- one connector, one moulded assembly." This is NOT free.
//
// Checked against the walker before writing a line here: linkEdges (graph.go)
// keys strictly on l.id, so each strand of a breakout contributes its OWN,
// independent edge to the graph -- a QSFP-to-4xSFP+ DAC landing on four
// different forwarder groups produces four separate entries in g.Net.Uplinks,
// one per strand, each carrying only ITS OWN link id. LinkCutEffect's
// predicate (before this) was a plain equality, `u.LinkID == linkID`, so
// asking "what does cutting strand 1 do" only ever withdrew strand 1's edge
// -- the other three stayed in the graph exactly as if the physical cable
// were untouched. That is the wrong answer for a QSFP-to-4xSFP+ DAC: a
// backhoe (or a yanked connector) takes the whole assembly, not one leg of
// it. So this needed real code (store.BreakoutStrandIDs, and LinkCutEffect
// widened to use it), not a test of something that already worked.

// mustBreakout declares an n-strand breakout from a-end (on aAsset) to n
// freshly-built server interfaces, one per group in groupIDs (len(groupIDs)
// == n), and returns the created strands in position order.
func mustBreakout(t *testing.T, s *SQLStore, ctx context.Context, aAsset, aIface string, bAssetPrefix string, groupIDs []string) []*domain.Link {
	t.Helper()
	var bIfaces []string
	for i, gID := range groupIDs {
		srv := mustAsset(t, s, ctx, domain.KindServer, bAssetPrefix+string(rune('a'+i)), nil)
		mustNetGroupMember(t, s, ctx, gID, srv, "member")
		bIfaces = append(bIfaces, mustInterface(t, s, ctx, srv, "eth0"))
	}
	links, err := s.CreateBreakout(ctx, testPermit, domain.BreakoutSpec{
		AInterfaceID: aIface, BInterfaceIDs: bIfaces,
		Medium: mediumPtr("dac"), LengthM: lengthPtr(1),
	})
	if err != nil {
		t.Fatalf("creating breakout: %v", err)
	}
	return links
}

// TestCuttingOneBreakoutStrandCutsThemAll is the motivating case: a
// QSFP-to-2xSFP+ DAC (kept to two strands -- BreakoutSpec's own minimum) with
// its two legs landing in TWO DIFFERENT forwarder groups. Asking what
// happens if strand 1 alone is severed must report BOTH boundaries the
// physical cable touches, because severing one connector takes both legs.
func TestCuttingOneBreakoutStrandCutsThemAll(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			swAsset := mustAsset(t, s, ctx, domain.KindSwitch, "sw-breakout-cut", nil)
			swIface := mustInterface(t, s, ctx, swAsset, "eth1")
			gSwitch := mustNetGroup(t, s, ctx, "grp-breakout-sw", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			mustNetGroupMember(t, s, ctx, gSwitch, swAsset, "member")

			gB := mustNetGroup(t, s, ctx, "grp-breakout-b", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			gC := mustNetGroup(t, s, ctx, "grp-breakout-c", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)

			strands := mustBreakout(t, s, ctx, swAsset, swIface, "srv-breakout-", []string{gB, gC})
			if len(strands) != 2 {
				t.Fatalf("got %d strands, want 2", len(strands))
			}

			// Sanity: the graph really does carry two independent edges before
			// asking the question this test is about.
			g, err := s.LoadGraph(ctx)
			if err != nil {
				t.Fatalf("loading graph: %v", err)
			}
			var edgeIDs []string
			for _, u := range g.Net.Uplinks {
				if u.LinkID == strands[0].ID || u.LinkID == strands[1].ID {
					edgeIDs = append(edgeIDs, u.LinkID)
				}
			}
			if len(edgeIDs) != 2 {
				t.Fatalf("graph carries %d edges for this breakout's two strands, want 2 -- "+
					"this test proves nothing if the strands do not derive independent edges", len(edgeIDs))
			}

			cut, err := s.LinkCutEffect(ctx, strands[0].ID)
			if err != nil {
				t.Fatalf("LinkCutEffect on strand 1: %v", err)
			}
			if !cut.Joins {
				t.Fatal("cut.Joins is false; this test proves nothing")
			}
			if len(cut.JoinedPairs) != 2 {
				t.Fatalf("JoinedPairs = %+v, want both boundaries this ONE PHYSICAL CABLE "+
					"touches -- cutting strand 1 alone must report strand 2's boundary too, "+
					"because severing the connector takes both legs", cut.JoinedPairs)
			}
			if len(cut.SeparatedPairs) != 2 {
				t.Fatalf("SeparatedPairs = %+v, want both boundaries severed -- each strand "+
					"is the only path to its own group, and cutting the shared connector "+
					"takes both at once", cut.SeparatedPairs)
			}

			// And asking about strand 2 must answer identically -- it is the
			// same physical event asked from the other leg.
			cut2, err := s.LinkCutEffect(ctx, strands[1].ID)
			if err != nil {
				t.Fatalf("LinkCutEffect on strand 2: %v", err)
			}
			if len(cut2.SeparatedPairs) != 2 {
				t.Fatalf("asking from strand 2's side, SeparatedPairs = %+v, want both "+
					"boundaries -- the physical cable is the same regardless of which leg "+
					"an operator clicked through to", cut2.SeparatedPairs)
			}
		})
	}
}

// TestBreakoutStrandIDsOnAnOrdinaryCableIsJustItself pins the no-regression
// case: TestSingleCableCutReportsOnePairUnchanged already proves LinkCutEffect
// itself didn't change shape for a plain cable; this proves the mechanism
// that widening rides on (BreakoutStrandIDs) answers exactly one id for one
// -- an ordinary link shares its fate with nobody.
func TestBreakoutStrandIDsOnAnOrdinaryCableIsJustItself(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			linkID, _, _ := cabled(t, s, ctx, "sw-plain-a", "sw-plain-b")

			ids, err := s.BreakoutStrandIDs(ctx, linkID)
			if err != nil {
				t.Fatalf("BreakoutStrandIDs: %v", err)
			}
			if len(ids) != 1 || ids[0] != linkID {
				t.Fatalf("BreakoutStrandIDs(%s) = %v, want exactly [%s] for an ordinary cable",
					linkID, ids, linkID)
			}
		})
	}
}

// TestBreakoutStrandIDsExcludesARetiredSibling: a strand somebody already
// unplugged is not "cut" by a later event -- it is already gone, and
// including it would double-count it as part of the blast radius of a
// second, unrelated action.
func TestBreakoutStrandIDsExcludesARetiredSibling(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			swAsset := mustAsset(t, s, ctx, domain.KindSwitch, "sw-retired-sibling", nil)
			swIface := mustInterface(t, s, ctx, swAsset, "eth1")
			var bIfaces []string
			for i := 0; i < 3; i++ {
				srv := mustAsset(t, s, ctx, domain.KindServer, "srv-retired-sibling-"+string(rune('a'+i)), nil)
				bIfaces = append(bIfaces, mustInterface(t, s, ctx, srv, "eth0"))
			}
			links, err := s.CreateBreakout(ctx, testPermit, domain.BreakoutSpec{
				AInterfaceID: swIface, BInterfaceIDs: bIfaces,
				Medium: mediumPtr("dac"), LengthM: lengthPtr(1),
			})
			if err != nil {
				t.Fatalf("creating breakout: %v", err)
			}

			if err := s.RetireLink(ctx, testPermit, links[2].ID); err != nil {
				t.Fatalf("retiring strand 3: %v", err)
			}

			ids, err := s.BreakoutStrandIDs(ctx, links[0].ID)
			if err != nil {
				t.Fatalf("BreakoutStrandIDs: %v", err)
			}
			sort.Strings(ids)
			want := []string{links[0].ID, links[1].ID}
			sort.Strings(want)
			if len(ids) != 2 || ids[0] != want[0] || ids[1] != want[1] {
				t.Fatalf("BreakoutStrandIDs = %v, want exactly the two LIVE strands %v -- "+
					"a strand already unplugged is not taken down by cutting a live one",
					ids, want)
			}
		})
	}
}
