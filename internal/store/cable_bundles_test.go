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

// TestSetBundleMembersAllowsAMovedCableFromARetiredBundle is the trap Task 2b
// fixes: refusing to edit a retired bundle's membership, plus an
// unconditional one-bundle-per-cable index, together strand a cable the
// moment the duct it was pulled in gets retired. A cable in a RETIRED bundle
// must be free to join a new live one.
func TestSetBundleMembersAllowsAMovedCableFromARetiredBundle(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			a1 := mustAsset(t, s, ctx, domain.KindServer, "srv-a", nil)
			a2 := mustAsset(t, s, ctx, domain.KindServer, "srv-b", nil)
			pa := mustInterface(t, s, ctx, a1, "eth0")
			pb := mustInterface(t, s, ctx, a2, "eth0")
			cable := mustCable(t, s, ctx, pa, pb)

			oldBundle := mustBundle(t, s, ctx, "duct-old", "Old Duct")
			if err := s.SetBundleMembers(ctx, testPermit, oldBundle, []string{cable}); err != nil {
				t.Fatalf("adding to old bundle: %v", err)
			}
			if err := s.RetireBundle(ctx, testPermit, oldBundle); err != nil {
				t.Fatalf("retiring old bundle: %v", err)
			}

			newBundle := mustBundle(t, s, ctx, "duct-new", "New Duct")
			if err := s.SetBundleMembers(ctx, testPermit, newBundle, []string{cable}); err != nil {
				t.Fatalf("adding a cable from a retired bundle to a new one should succeed, got: %v", err)
			}

			newMembers, err := s.ListBundleMembers(ctx, newBundle)
			if err != nil {
				t.Fatalf("listing new bundle members: %v", err)
			}
			if len(newMembers) != 1 || newMembers[0].LinkID != cable {
				t.Fatalf("new bundle members = %+v, want exactly the moved cable", newMembers)
			}

			// The retired bundle's own history is untouched by the cable
			// moving on -- SetBundleMembers only ever writes newBundle's rows.
			oldMembers, err := s.ListBundleMembers(ctx, oldBundle)
			if err != nil {
				t.Fatalf("listing old bundle members: %v", err)
			}
			if len(oldMembers) != 1 || oldMembers[0].LinkID != cable {
				t.Fatalf("old (retired) bundle members = %+v, want the cable still recorded as history", oldMembers)
			}
		})
	}
}

// TestSetBundleMembersRefusesACableStillInALiveBundle is the other half: the
// live-scoped rule still refuses a cable that is claimed by a bundle that has
// NOT been retired, with the same named-conflict message as before.
func TestSetBundleMembersRefusesACableStillInALiveBundle(t *testing.T) {
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
				t.Fatalf("adding a cable still in a LIVE bundle to a second bundle = %v, want ErrConflict", err)
			}
			if !strings.Contains(err.Error(), cable) || !strings.Contains(err.Error(), "duct-a") {
				t.Errorf("error %q does not name the conflicting cable and its (live) bundle", err.Error())
			}
		})
	}
}

// TestSetBundleMembersRefusesEditingARetiredBundle is the other decision this
// task adds: editing a withdrawn grouping's membership is meaningless, and
// this is also what makes the live-scoped uniqueness rule load-bearing --
// it's the only way a cable ever gets OUT of a retired bundle's claim.
func TestSetBundleMembersRefusesEditingARetiredBundle(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			a1 := mustAsset(t, s, ctx, domain.KindServer, "srv-a", nil)
			a2 := mustAsset(t, s, ctx, domain.KindServer, "srv-b", nil)
			pa := mustInterface(t, s, ctx, a1, "eth0")
			pb := mustInterface(t, s, ctx, a2, "eth0")
			cable := mustCable(t, s, ctx, pa, pb)

			bundleID := mustBundle(t, s, ctx, "duct-a", "Duct A")
			if err := s.RetireBundle(ctx, testPermit, bundleID); err != nil {
				t.Fatalf("retiring bundle: %v", err)
			}

			err := s.SetBundleMembers(ctx, testPermit, bundleID, []string{cable})
			var ve *domain.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("editing a retired bundle's membership = %v, want a *domain.ValidationError", err)
			}

			// The retired bundle's membership must be exactly what it was
			// before the refused edit -- empty, in this case -- not
			// partially applied.
			members, err := s.ListBundleMembers(ctx, bundleID)
			if err != nil {
				t.Fatalf("listing bundle members: %v", err)
			}
			if len(members) != 0 {
				t.Errorf("retired bundle members after a refused edit = %+v, want none (the refusal must not apply)", members)
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

// TestCandidateLinksForBundleExcludesWhatAnotherLiveBundleClaims is the
// picker's own test: a cable claimed by a live bundle must not be offered to
// a different one (SetBundleMembers would refuse it anyway), a cable in no
// bundle must be offered, and this bundle's OWN live member must still be
// offered -- an unrelated save must not make an already-ticked cable vanish
// from its own picker.
func TestCandidateLinksForBundleExcludesWhatAnotherLiveBundleClaims(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			a1 := mustAsset(t, s, ctx, domain.KindServer, "cand-a", nil)
			a2 := mustAsset(t, s, ctx, domain.KindServer, "cand-b", nil)
			a3 := mustAsset(t, s, ctx, domain.KindServer, "cand-c", nil)

			claimedCable := mustCable(t, s, ctx,
				mustInterface(t, s, ctx, a1, "eth0"), mustInterface(t, s, ctx, a2, "eth0"))
			freeCable := mustCable(t, s, ctx,
				mustInterface(t, s, ctx, a1, "eth1"), mustInterface(t, s, ctx, a2, "eth1"))
			ownCable := mustCable(t, s, ctx,
				mustInterface(t, s, ctx, a1, "eth2"), mustInterface(t, s, ctx, a3, "eth0"))

			other := mustBundle(t, s, ctx, "duct-other", "Other duct")
			if err := s.SetBundleMembers(ctx, testPermit, other, []string{claimedCable}); err != nil {
				t.Fatalf("claiming cable in the other bundle: %v", err)
			}

			mine := mustBundle(t, s, ctx, "duct-mine", "My duct")
			if err := s.SetBundleMembers(ctx, testPermit, mine, []string{ownCable}); err != nil {
				t.Fatalf("adding this bundle's own member: %v", err)
			}

			candidates, err := s.CandidateLinksForBundle(ctx, mine)
			if err != nil {
				t.Fatalf("listing candidates: %v", err)
			}
			ids := map[string]bool{}
			for _, c := range candidates {
				ids[c.LinkID] = true
			}
			if ids[claimedCable] {
				t.Errorf("a cable claimed by a live bundle was offered as a candidate to another bundle")
			}
			if !ids[freeCable] {
				t.Errorf("an unclaimed cable was not offered as a candidate")
			}
			if !ids[ownCable] {
				t.Errorf("this bundle's own live member was not offered in its own picker")
			}
		})
	}
}
