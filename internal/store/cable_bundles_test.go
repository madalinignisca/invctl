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
	"errors"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// mustBundle declares a bundle with no members yet and returns its id.
func mustBundle(t *testing.T, s *SQLStore, ctx context.Context, code, name string) string {
	t.Helper()
	b, err := domain.NewCableBundle(NewID(), domain.CableBundleSpec{Code: code, Name: name}, s.Now())
	if err != nil {
		t.Fatalf("building bundle %s: %v", code, err)
	}
	if err := s.CreateBundle(ctx, testPermit, b); err != nil {
		t.Fatalf("creating bundle %s: %v", code, err)
	}
	return b.ID
}

// TestCreateGetListBundle covers the plain CRUD wiring: a created bundle is
// gettable and appears in the live list.
func TestCreateGetListBundle(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			id := mustBundle(t, s, ctx, "duct-a", "Duct A")

			got, err := s.GetBundle(ctx, id)
			if err != nil {
				t.Fatalf("getting bundle: %v", err)
			}
			if got.Code != "duct-a" || got.Name != "Duct A" {
				t.Errorf("got %+v, want code=duct-a name=%q", got, "Duct A")
			}
			if got.Lifecycle != domain.LifecycleActive {
				t.Errorf("lifecycle = %s, want active", got.Lifecycle)
			}

			rows, err := s.ListBundles(ctx)
			if err != nil {
				t.Fatalf("listing bundles: %v", err)
			}
			if len(rows) != 1 || rows[0].ID != id {
				t.Fatalf("ListBundles = %+v, want exactly the one bundle just created", rows)
			}
			if rows[0].Members != 0 {
				t.Errorf("members = %d, want 0 for a freshly declared bundle", rows[0].Members)
			}
		})
	}
}

// changesForEntity is changesFor without the entityTagFixture receiver, for
// suites that don't build one.
func changesForEntity(t *testing.T, s *SQLStore, ctx context.Context, entityType, entityID string) []domain.ChangeLog {
	t.Helper()
	rows, err := s.ListChangesForEntity(ctx, entityType, entityID, 50)
	if err != nil {
		t.Fatalf("listing changes for %s %s: %v", entityType, entityID, err)
	}
	return rows
}

// TestSetBundleMembersProducesAnAuditDiff is the test CLAUDE.md asks for by
// name: membership is a set, replaced wholesale, and a change consisting of
// NOTHING but a membership edit must still write a change_log row with a
// diff naming it. This has gone wrong three times in this repo because a set
// replacement produces no diff on the parent struct by itself -- the fold
// into bundleAudit.Members is what's actually being tested here, not the
// SQL.
func TestSetBundleMembersProducesAnAuditDiff(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			a1 := mustAsset(t, s, ctx, domain.KindServer, "srv-a", nil)
			a2 := mustAsset(t, s, ctx, domain.KindServer, "srv-b", nil)
			pa := mustInterface(t, s, ctx, a1, "eth0")
			pb := mustInterface(t, s, ctx, a2, "eth0")
			cable := mustCable(t, s, ctx, pa, pb)

			bundleID := mustBundle(t, s, ctx, "duct-a", "Duct A")

			before := changesForEntity(t, s, ctx, "cable_bundle", bundleID)

			if err := s.SetBundleMembers(ctx, testPermit, bundleID, []string{cable}); err != nil {
				t.Fatalf("setting bundle members: %v", err)
			}

			after := changesForEntity(t, s, ctx, "cable_bundle", bundleID)
			if len(after) != len(before)+1 {
				t.Fatalf("got %d change_log rows after adding a member, want %d (one more than before %d) -- "+
					"a membership edit that writes no audit row at all",
					len(after), len(before)+1, len(before))
			}
			latest := after[0]
			if latest.Action != domain.ActionUpdate {
				t.Errorf("action = %s, want update", latest.Action)
			}
			if !strings.Contains(latest.Diff, "members") {
				t.Fatalf("diff = %s, does not mention members -- the set replacement produced "+
					"no diff on the parent struct, the exact failure CLAUDE.md warns about", latest.Diff)
			}

			rows, err := s.ListBundleMembers(ctx, bundleID)
			if err != nil {
				t.Fatalf("listing bundle members: %v", err)
			}
			if len(rows) != 1 || rows[0].LinkID != cable {
				t.Fatalf("ListBundleMembers = %+v, want exactly the one cable just added", rows)
			}
		})
	}
}

// TestSetBundleMembersNoOpWritesNoSecondRow proves the fold does not fire on
// every call -- re-submitting the SAME set must not grow change_log, the
// same no-op contract every other logUpdate caller in this repo gets.
func TestSetBundleMembersNoOpWritesNoSecondRow(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			a1 := mustAsset(t, s, ctx, domain.KindServer, "srv-a", nil)
			a2 := mustAsset(t, s, ctx, domain.KindServer, "srv-b", nil)
			pa := mustInterface(t, s, ctx, a1, "eth0")
			pb := mustInterface(t, s, ctx, a2, "eth0")
			cable := mustCable(t, s, ctx, pa, pb)
			bundleID := mustBundle(t, s, ctx, "duct-a", "Duct A")

			if err := s.SetBundleMembers(ctx, testPermit, bundleID, []string{cable}); err != nil {
				t.Fatalf("first set: %v", err)
			}
			afterFirst := changesForEntity(t, s, ctx, "cable_bundle", bundleID)

			if err := s.SetBundleMembers(ctx, testPermit, bundleID, []string{cable}); err != nil {
				t.Fatalf("second (no-op) set: %v", err)
			}
			afterSecond := changesForEntity(t, s, ctx, "cable_bundle", bundleID)

			if len(afterSecond) != len(afterFirst) {
				t.Errorf("re-submitting an unchanged membership set wrote %d more change_log row(s)",
					len(afterSecond)-len(afterFirst))
			}
		})
	}
}

// TestSetBundleMembersRefusesACableAlreadyInAnotherBundle is the
// one-bundle-per-cable rule (cable_bundle_member_link_key), and CLAUDE.md's
// requirement that it surface as a field message naming the conflict, not a
// 500 and not a raw driver error.
func TestSetBundleMembersRefusesACableAlreadyInAnotherBundle(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			a1 := mustAsset(t, s, ctx, domain.KindServer, "srv-a", nil)
			a2 := mustAsset(t, s, ctx, domain.KindServer, "srv-b", nil)
			pa := mustInterface(t, s, ctx, a1, "eth0")
			pb := mustInterface(t, s, ctx, a2, "eth0")
			cable := mustCable(t, s, ctx, pa, pb)

			bundleA := mustBundle(t, s, ctx, "duct-a", "Duct A")
			bundleB := mustBundle(t, s, ctx, "duct-b", "Duct B")

			if err := s.SetBundleMembers(ctx, testPermit, bundleA, []string{cable}); err != nil {
				t.Fatalf("adding to bundle A: %v", err)
			}

			err := s.SetBundleMembers(ctx, testPermit, bundleB, []string{cable})
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("adding an already-bundled cable to a second bundle = %v, want ErrConflict", err)
			}
			if !strings.Contains(err.Error(), cable) || !strings.Contains(err.Error(), "duct-a") {
				t.Errorf("error %q does not name the conflicting cable and its bundle", err.Error())
			}

			// And the refusal must not have partially applied: bundle B stays
			// empty, bundle A keeps the cable.
			bMembers, err := s.ListBundleMembers(ctx, bundleB)
			if err != nil {
				t.Fatalf("listing bundle B members: %v", err)
			}
			if len(bMembers) != 0 {
				t.Errorf("bundle B has %d members after a refused add, want 0", len(bMembers))
			}
			aMembers, err := s.ListBundleMembers(ctx, bundleA)
			if err != nil {
				t.Fatalf("listing bundle A members: %v", err)
			}
			if len(aMembers) != 1 || aMembers[0].LinkID != cable {
				t.Errorf("bundle A's own membership changed as a side effect of the refused add: %+v", aMembers)
			}
		})
	}
}

// TestSetBundleMembersDedupesInput proves a duplicated id in the caller's
// slice does not trip the (bundle_id, link_id) primary key on the second
// INSERT -- a set collapses duplicates rather than refusing them.
func TestSetBundleMembersDedupesInput(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			a1 := mustAsset(t, s, ctx, domain.KindServer, "srv-a", nil)
			a2 := mustAsset(t, s, ctx, domain.KindServer, "srv-b", nil)
			pa := mustInterface(t, s, ctx, a1, "eth0")
			pb := mustInterface(t, s, ctx, a2, "eth0")
			cable := mustCable(t, s, ctx, pa, pb)
			bundleID := mustBundle(t, s, ctx, "duct-a", "Duct A")

			if err := s.SetBundleMembers(ctx, testPermit, bundleID, []string{cable, cable}); err != nil {
				t.Fatalf("setting members with a duplicate id: %v", err)
			}
			rows, err := s.ListBundleMembers(ctx, bundleID)
			if err != nil {
				t.Fatalf("listing bundle members: %v", err)
			}
			if len(rows) != 1 {
				t.Fatalf("got %d membership rows from a duplicated input id, want 1", len(rows))
			}
		})
	}
}

// TestARetiredLinkStaysInItsBundle is the design doc's rule verbatim:
// membership records what was pulled together, and a withdrawn cable is
// still part of that history. RetireLink must not touch cable_bundle_member,
// and this bundle's own store methods must not filter it out.
func TestARetiredLinkStaysInItsBundle(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			a1 := mustAsset(t, s, ctx, domain.KindServer, "srv-a", nil)
			a2 := mustAsset(t, s, ctx, domain.KindServer, "srv-b", nil)
			pa := mustInterface(t, s, ctx, a1, "eth0")
			pb := mustInterface(t, s, ctx, a2, "eth0")
			cable := mustCable(t, s, ctx, pa, pb)
			bundleID := mustBundle(t, s, ctx, "duct-a", "Duct A")

			if err := s.SetBundleMembers(ctx, testPermit, bundleID, []string{cable}); err != nil {
				t.Fatalf("adding member: %v", err)
			}
			if err := s.RetireLink(ctx, testPermit, cable); err != nil {
				t.Fatalf("retiring the cable: %v", err)
			}

			rows, err := s.ListBundleMembers(ctx, bundleID)
			if err != nil {
				t.Fatalf("listing bundle members: %v", err)
			}
			if len(rows) != 1 || rows[0].LinkID != cable {
				t.Fatalf("bundle membership after the cable was retired = %+v, want the cable still present", rows)
			}
			if rows[0].Lifecycle != domain.LifecycleRetired {
				t.Errorf("member lifecycle = %s, want retired", rows[0].Lifecycle)
			}

			got, err := s.BundleForLink(ctx, cable)
			if err != nil {
				t.Fatalf("BundleForLink after retirement: %v", err)
			}
			if got.ID != bundleID {
				t.Errorf("BundleForLink = %s, want %s", got.ID, bundleID)
			}
		})
	}
}

// TestBundleForLinkNotFound: a cable in no bundle is ErrNotFound, not a
// zero-value bundle -- Task 3's impact page treats that as "not bundled".
func TestBundleForLinkNotFound(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			a1 := mustAsset(t, s, ctx, domain.KindServer, "srv-a", nil)
			a2 := mustAsset(t, s, ctx, domain.KindServer, "srv-b", nil)
			pa := mustInterface(t, s, ctx, a1, "eth0")
			pb := mustInterface(t, s, ctx, a2, "eth0")
			cable := mustCable(t, s, ctx, pa, pb)

			_, err := s.BundleForLink(ctx, cable)
			if !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("BundleForLink for an unbundled cable = %v, want ErrNotFound", err)
			}
		})
	}
}

// TestRetireBundleDoesNotCascade: withdrawing a bundle must not touch its
// members. "We stopped managing these together" is not "somebody pulled the
// cables" -- cascading would misattribute a cable's removal to whoever
// retired the grouping.
func TestRetireBundleDoesNotCascade(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			a1 := mustAsset(t, s, ctx, domain.KindServer, "srv-a", nil)
			a2 := mustAsset(t, s, ctx, domain.KindServer, "srv-b", nil)
			pa := mustInterface(t, s, ctx, a1, "eth0")
			pb := mustInterface(t, s, ctx, a2, "eth0")
			cable := mustCable(t, s, ctx, pa, pb)
			bundleID := mustBundle(t, s, ctx, "duct-a", "Duct A")
			if err := s.SetBundleMembers(ctx, testPermit, bundleID, []string{cable}); err != nil {
				t.Fatalf("adding member: %v", err)
			}
			linkChangesBefore := changesForEntity(t, s, ctx, "link", cable)

			if err := s.RetireBundle(ctx, testPermit, bundleID); err != nil {
				t.Fatalf("retiring bundle: %v", err)
			}

			rows, err := s.ListBundleMembers(ctx, bundleID)
			if err != nil {
				t.Fatalf("listing bundle members: %v", err)
			}
			if len(rows) != 1 || rows[0].LinkID != cable {
				t.Fatalf("membership after retiring the bundle = %+v, want the cable still there", rows)
			}
			if rows[0].Lifecycle != domain.LifecycleActive {
				t.Errorf("the cable itself was retired as a side effect of retiring its bundle, lifecycle = %s",
					rows[0].Lifecycle)
			}
			linkChangesAfter := changesForEntity(t, s, ctx, "link", cable)
			if len(linkChangesAfter) != len(linkChangesBefore) {
				t.Errorf("retiring the bundle wrote %d change_log row(s) against the cable itself, want 0",
					len(linkChangesAfter)-len(linkChangesBefore))
			}

			got, err := s.GetBundle(ctx, bundleID)
			if err != nil {
				t.Fatalf("getting bundle: %v", err)
			}
			if got.Lifecycle != domain.LifecycleRetired {
				t.Errorf("bundle lifecycle = %s, want retired", got.Lifecycle)
			}

			// A retired bundle is not offered again on a plain retire.
			if err := s.RetireBundle(ctx, testPermit, bundleID); err != nil {
				t.Errorf("retiring an already-retired bundle should be a no-op, got %v", err)
			}
		})
	}
}

// TestUpdateBundleCannotRetireOrRevive is both halves the task brief calls
// out by name: a plain correction submitted while active must not be able to
// smuggle a lifecycle change through, and once retired, UpdateBundle must not
// be a second way to bring the bundle back -- only RetireBundle (and nothing
// this task builds) changes lifecycle.
func TestUpdateBundleCannotRetireOrRevive(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			t.Run("a correction submitted with lifecycle=retired does not retire the row", func(t *testing.T) {
				id := mustBundle(t, s, ctx, "duct-a", "Duct A")
				b, err := s.GetBundle(ctx, id)
				if err != nil {
					t.Fatalf("getting bundle: %v", err)
				}
				b.Name = "Duct A (relabelled)"
				b.Lifecycle = domain.LifecycleRetired // what a forged/stale form might carry
				if err := s.UpdateBundle(ctx, testPermit, b); err != nil {
					t.Fatalf("updating bundle: %v", err)
				}
				got, err := s.GetBundle(ctx, id)
				if err != nil {
					t.Fatalf("getting bundle: %v", err)
				}
				if got.Lifecycle != domain.LifecycleActive {
					t.Fatalf("lifecycle after an ordinary correction = %s, want active "+
						"(UpdateBundle must not accept lifecycle from the caller)", got.Lifecycle)
				}
				if got.Name != "Duct A (relabelled)" {
					t.Errorf("name = %q, want the correction to have applied", got.Name)
				}
				changes := changesForEntity(t, s, ctx, "cable_bundle", id)
				if len(changes) == 0 {
					t.Fatal("no change_log row for the correction at all")
				}
				if strings.Contains(changes[0].Diff, "lifecycle") {
					t.Errorf("diff %q claims a lifecycle change that never happened", changes[0].Diff)
				}
			})

			t.Run("a correction submitted with lifecycle=active does not revive a retired row", func(t *testing.T) {
				id := mustBundle(t, s, ctx, "duct-b", "Duct B")
				if err := s.RetireBundle(ctx, testPermit, id); err != nil {
					t.Fatalf("retiring bundle: %v", err)
				}
				b, err := s.GetBundle(ctx, id)
				if err != nil {
					t.Fatalf("getting retired bundle: %v", err)
				}
				b.Name = "Duct B (relabelled)"
				b.Lifecycle = domain.LifecycleActive // what a stale form still open before the retire would carry
				if err := s.UpdateBundle(ctx, testPermit, b); err != nil {
					t.Fatalf("updating retired bundle: %v", err)
				}
				got, err := s.GetBundle(ctx, id)
				if err != nil {
					t.Fatalf("getting bundle: %v", err)
				}
				if got.Lifecycle != domain.LifecycleRetired {
					t.Fatalf("lifecycle after a correction submitted against a retired row = %s, want retired "+
						"(UpdateBundle must not be a second revival path)", got.Lifecycle)
				}
			})
		})
	}
}
