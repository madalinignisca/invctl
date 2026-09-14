// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"reflect"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/impact"
)

// Task 3, docs/cable-bundles-design.md's "The cut": BundleCutEffect must be
// the same walker cutEffect already provides, with a set predicate in place
// of LinkCutEffect's equality one -- not a second implementation that could
// drift from it.

// TestBundleCutEffectIsTheSameWalkerWithASetPredicate is the pin the task
// called for, in TestRetirePrefixAgreesWithTheTree's shape: drive
// BundleCutEffect through its real entry point and, separately, drive
// cutEffect directly with a hand-built set predicate over the same link ids,
// and assert the two answers are identical. Both are in package store, so
// the unexported walker is reachable from here -- the same way
// TestRetirePrefixAgreesWithTheTree calls the exact function the page calls
// rather than a hand-written second check. If a future change gave
// BundleCutEffect its own logic instead of reusing cutEffect, this is what
// would catch the two disagreeing.
func TestBundleCutEffectIsTheSameWalkerWithASetPredicate(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			// Two REDUNDANT cables between the same two groups: on its own,
			// cutting either one changes nothing, because the other still
			// joins them.
			cable1, a1, b1 := cabled(t, s, ctx, "sw-red-a1", "sw-red-b1")
			cable2, a2, b2 := cabled(t, s, ctx, "sw-red-a2", "sw-red-b2")
			gA := mustNetGroup(t, s, ctx, "grp-bundle-a", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			gB := mustNetGroup(t, s, ctx, "grp-bundle-b", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			mustNetGroupMember(t, s, ctx, gA, a1, "member")
			mustNetGroupMember(t, s, ctx, gB, b1, "member")
			mustNetGroupMember(t, s, ctx, gA, a2, "member")
			mustNetGroupMember(t, s, ctx, gB, b2, "member")

			bundleID := mustBundle(t, s, ctx, "duct-red", "Redundant Duct")
			if err := s.SetBundleMembers(ctx, testPermit, bundleID, []string{cable1, cable2}); err != nil {
				t.Fatalf("bundling both cables: %v", err)
			}

			got, err := s.BundleCutEffect(ctx, bundleID)
			if err != nil {
				t.Fatalf("BundleCutEffect: %v", err)
			}

			// The hand-built union predicate, over exactly the two cables
			// this bundle holds -- the comparison BundleCutEffect itself
			// must agree with.
			want, err := s.cutEffect(ctx, func(u impact.NetUplinkInfo) bool {
				return u.LinkID == cable1 || u.LinkID == cable2
			})
			if err != nil {
				t.Fatalf("cutEffect with the hand-built union predicate: %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("BundleCutEffect = %+v, want %+v (the same walker over the same "+
					"set of edges must agree)", got, want)
			}

			// And the redundancy story itself, so the pin above is checked
			// against a scenario where the individual and bundled answers are
			// meant to differ: alone, neither cable separates anything.
			for _, id := range []string{cable1, cable2} {
				cut, err := s.LinkCutEffect(ctx, id)
				if err != nil {
					t.Fatalf("LinkCutEffect(%s): %v", id, err)
				}
				if cut.Separates {
					t.Fatalf("cable %s alone separates the groups; the redundancy this "+
						"test relies on is not present", id)
				}
			}
			// But losing the whole bundle -- both redundant cables at once,
			// the way a backhoe would -- takes out the only two paths there
			// are.
			if !got.Separates {
				t.Fatal("BundleCutEffect says the bundle does not separate the groups, " +
					"but it holds the only two cables that join them")
			}
		})
	}
}

// TestBundleCutEffectIgnoresARetiredMember: a withdrawn cable stays in its
// bundle as history (Task 2's rule), but cutting the duct today does not
// re-sever a cable somebody already unplugged.
func TestBundleCutEffectIgnoresARetiredMember(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			cable, a, b := cabled(t, s, ctx, "sw-hist-a", "sw-hist-b")
			gA := mustNetGroup(t, s, ctx, "grp-hist-a", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			gB := mustNetGroup(t, s, ctx, "grp-hist-b", domain.NetGroupMCLAG, domain.NetRoleCore, domain.AvailActiveActive)
			mustNetGroupMember(t, s, ctx, gA, a, "member")
			mustNetGroupMember(t, s, ctx, gB, b, "member")

			bundleID := mustBundle(t, s, ctx, "duct-hist", "Historic Duct")
			if err := s.SetBundleMembers(ctx, testPermit, bundleID, []string{cable}); err != nil {
				t.Fatalf("bundling the cable: %v", err)
			}

			// Confirm the bundle DOES cut something before retiring the
			// cable, so the assertion below is not passing by checking
			// nothing.
			before, err := s.BundleCutEffect(ctx, bundleID)
			if err != nil {
				t.Fatalf("BundleCutEffect before retirement: %v", err)
			}
			if !before.Joins {
				t.Fatal("the bundle's only cable does not join anything before it is " +
					"retired; this test would pass by checking nothing")
			}

			if err := s.RetireLink(ctx, testPermit, cable); err != nil {
				t.Fatalf("retiring the cable: %v", err)
			}

			after, err := s.BundleCutEffect(ctx, bundleID)
			if err != nil {
				t.Fatalf("BundleCutEffect after retirement: %v", err)
			}
			if after.Joins {
				t.Error("BundleCutEffect still counts a retired member; a withdrawn " +
					"cable is history, not something left there to be cut")
			}
		})
	}
}

// TestBundleCutEffectEmptyBundleReturnsNoError: a bundle whose cables carry
// no uplink edges (nothing forwarding, or both ends in one group) returns an
// empty effect rather than erroring.
func TestBundleCutEffectEmptyBundleReturnsNoError(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			// A bundle with no members at all.
			emptyID := mustBundle(t, s, ctx, "duct-empty", "Empty Duct")
			cut, err := s.BundleCutEffect(ctx, emptyID)
			if err != nil {
				t.Fatalf("BundleCutEffect on an empty bundle: %v", err)
			}
			if cut.Joins || cut.Separates {
				t.Errorf("BundleCutEffect on an empty bundle = %+v, want the zero value", cut)
			}

			// A bundle with a cable that carries no edge at all: both ends
			// ungrouped, so it derives nothing (TestAnUngroupedCableDerivesNothing's
			// case, bundled).
			cable, _, _ := cabled(t, s, ctx, "sw-none-a", "sw-none-b")
			ungroupedID := mustBundle(t, s, ctx, "duct-none", "Ungrouped Duct")
			if err := s.SetBundleMembers(ctx, testPermit, ungroupedID, []string{cable}); err != nil {
				t.Fatalf("bundling the ungrouped cable: %v", err)
			}
			cut, err = s.BundleCutEffect(ctx, ungroupedID)
			if err != nil {
				t.Fatalf("BundleCutEffect on a bundle with no uplink edges: %v", err)
			}
			if cut.Joins || cut.Separates {
				t.Errorf("BundleCutEffect on a bundle with no uplink edges = %+v, want the zero value", cut)
			}
		})
	}
}
