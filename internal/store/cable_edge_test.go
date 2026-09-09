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
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// THE DERIVATION HALF of WP-B3's engine work. internal/impact/cable_edge_test.go
// proves components() withdraws a cable edge when it is cut; this proves the
// graph produces that edge in the first place, and -- the part the whole design
// turns on -- that it produces NOTHING for a cable inside one group.
//
// `ma.group_id <> mb.group_id` is not an optimisation. A cable inside one
// forwarder group is an intra-group adjacency, which net_attachment already
// answers; deriving an edge from it would be the "second, disagreeing answer"
// that graph_coverage_test.go's exclusion existed to prevent, and that
// exclusion was narrowed rather than deleted precisely because this case stays
// excluded.

// cabled builds two assets, a cable between them, and returns the link id.
func cabled(t *testing.T, s *SQLStore, ctx context.Context, aName, bName string) (linkID, aID, bID string) {
	t.Helper()
	aID = mustAsset(t, s, ctx, domain.KindSwitch, aName, nil)
	bID = mustAsset(t, s, ctx, domain.KindSwitch, bName, nil)
	ifA := mustInterface(t, s, ctx, aID, "eth0")
	ifB := mustInterface(t, s, ctx, bID, "eth0")
	l, err := domain.NewLink(NewID(), ifA, ifB)
	if err != nil {
		t.Fatalf("building link: %v", err)
	}
	if err := s.CreateLink(ctx, testPermit, l); err != nil {
		t.Fatalf("creating link: %v", err)
	}
	return l.ID, aID, bID
}

// cableEdges returns the uplink edges the graph derived from cables.
func cableEdges(t *testing.T, s *SQLStore, ctx context.Context) []string {
	t.Helper()
	g, err := s.LoadGraph(ctx)
	if err != nil {
		t.Fatalf("loading the graph: %v", err)
	}
	if g.Net == nil {
		return nil
	}
	var out []string
	for _, u := range g.Net.Uplinks {
		if u.LinkID != "" {
			out = append(out, u.LinkID)
		}
	}
	return out
}

// TestACableBetweenTwoGroupsDerivesAnEdge.
func TestACableBetweenTwoGroupsDerivesAnEdge(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			linkID, aID, bID := cabled(t, s, ctx, "sw-edge-a", "sw-edge-b")

			gA := mustNetGroup(t, s, ctx, "grp-a", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			gB := mustNetGroup(t, s, ctx, "grp-b", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			mustNetGroupMember(t, s, ctx, gA, aID, "member")
			mustNetGroupMember(t, s, ctx, gB, bID, "member")

			edges := cableEdges(t, s, ctx)
			if len(edges) != 1 || edges[0] != linkID {
				t.Fatalf("cable edges = %v, want exactly [%s]. A cable joining two "+
					"forwarder groups is the only thing in this model recording that "+
					"they are joined by copper", edges, linkID)
			}

			// And cutting it separates them, which is what the page will say.
			cut, err := s.LinkCutEffect(ctx, linkID)
			if err != nil {
				t.Fatalf("LinkCutEffect: %v", err)
			}
			if !cut.Joins {
				t.Error("LinkCutEffect says this cable joins nothing")
			}
			if !cut.Separates {
				t.Error("cutting the only cable between two groups does not separate " +
					"them; the page would tell an operator a cut is harmless")
			}
		})
	}
}

// TestACableInsideOneGroupDerivesNothing is the design decision, as a test.
//
// A server's uplink to its own top-of-rack switch has both ends in one group.
// Deriving an edge from it would put a cable-shaped answer next to
// net_attachment's answer about the same question, and the two would disagree
// the moment they differed.
func TestACableInsideOneGroupDerivesNothing(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			linkID, aID, bID := cabled(t, s, ctx, "sw-tor-1", "sw-tor-2")

			only := mustNetGroup(t, s, ctx, "grp-one", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			mustNetGroupMember(t, s, ctx, only, aID, "member")
			mustNetGroupMember(t, s, ctx, only, bID, "member")

			if edges := cableEdges(t, s, ctx); len(edges) != 0 {
				t.Errorf("a cable inside one group derived %v; it must derive nothing, "+
					"because intra-group reachability is net_attachment's answer and a "+
					"cable cannot tell you which way traffic flows", edges)
			}

			cut, err := s.LinkCutEffect(ctx, linkID)
			if err != nil {
				t.Fatalf("LinkCutEffect: %v", err)
			}
			if cut.Joins {
				t.Error("LinkCutEffect claims an intra-group cable joins something")
			}
		})
	}
}

// TestAnUngroupedCableDerivesNothing. Most of the estate is not in a forwarder
// group at all, and a cable between two ungrouped assets must not produce an
// edge between groups that do not exist.
func TestAnUngroupedCableDerivesNothing(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			cabled(t, s, ctx, "sw-lonely-a", "sw-lonely-b")
			if edges := cableEdges(t, s, ctx); len(edges) != 0 {
				t.Errorf("a cable between two assets in no forwarder group derived %v", edges)
			}
		})
	}
}

// TestARetiredCableDerivesNothing. Unpatching is a soft delete, so the row
// stays -- and a withdrawn cable that went on joining two groups would make
// every partition answer wrong in the safe-looking direction.
func TestARetiredCableDerivesNothing(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			linkID, aID, bID := cabled(t, s, ctx, "sw-gone-a", "sw-gone-b")
			gA := mustNetGroup(t, s, ctx, "grp-x", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			gB := mustNetGroup(t, s, ctx, "grp-y", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			mustNetGroupMember(t, s, ctx, gA, aID, "member")
			mustNetGroupMember(t, s, ctx, gB, bID, "member")
			if len(cableEdges(t, s, ctx)) != 1 {
				t.Fatal("the cable derived no edge before being unpatched, so this " +
					"test would pass by checking nothing")
			}

			if err := s.RetireLink(ctx, testPermit, linkID); err != nil {
				t.Fatalf("retiring the link: %v", err)
			}
			if edges := cableEdges(t, s, ctx); len(edges) != 0 {
				t.Errorf("an unpatched cable still derives %v; every partition answer "+
					"computed over it is wrong in the direction that looks safe", edges)
			}
		})
	}
}
