// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// TestUpdateNetGroupCannotLogAWithdrawalThatDidNotHappen guards the audit,
// which is what `g.Lifecycle = before.Lifecycle` in UpdateNetGroup protects.
//
// THE THIRD TIME IN THIS FAMILY, and the first two lessons were not enough.
// UpdateProvider and UpdateInterface each taught that the plausible property
// (a submitted lifecycle reaches the row) is guaranteed by the SQL, not the
// pin -- none of these UPDATE statements names the column -- and that the real
// property is the diff. This test was written knowing that, asserted the diff,
// AND STILL COULD NOT FAIL, because it was written in internal/web.
//
// The reason is worth keeping: NetworkGroupUpdate builds its argument as
// `updated := *existing`, so by the time the store sees it, Lifecycle already
// holds the stored value and the pin overwrites a correct value with itself.
// Through that handler the pin is unreachable, and no single mutation exposes
// it. The property belongs to the store, so the test does too -- a future
// caller (an import job, the planned JSON API) that builds a NetGroup from a
// payload rather than copying a loaded row is exactly what the pin is for, and
// this test is that caller.
//
// Without the pin, logUpdate records lifecycle retired while the row stays
// active. For a group that is worse than for a provider or a port: a real
// withdrawal cascades, so the audit would also imply the members, uplinks and
// anchors went with it when every one of them is still there.
func TestUpdateNetGroupCannotLogAWithdrawalThatDidNotHappen(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			id := mustNetGroup(t, s, ctx, "grp-pin", domain.NetGroupMCLAG,
				domain.NetRoleCore, domain.AvailActiveActive)

			g, err := s.GetNetGroup(ctx, id)
			if err != nil {
				t.Fatalf("loading: %v", err)
			}
			// A REAL change alongside the forged one, so an update is genuinely
			// logged and there is a diff to inspect. Submitting only the
			// lifecycle produces no diff at all once the pin is in place --
			// correct behaviour, and it would leave this asserting over nothing.
			forged := *g
			forged.Name = "Renamed core"
			forged.Lifecycle = domain.LifecycleRetired
			if err := s.UpdateNetGroup(ctx, testPermit, &forged); err != nil {
				t.Fatalf("UpdateNetGroup with a submitted lifecycle: %v", err)
			}

			var life string
			if err := s.DB().Reader.Get(&life, s.DB().Reader.Rebind(
				`SELECT lifecycle FROM net_group WHERE id = ?`), id); err != nil {
				t.Fatalf("reading the group's lifecycle: %v", err)
			}
			if life != domain.LifecycleActive {
				t.Errorf("the row is %q, want active", life)
			}

			var diffs []string
			if err := s.DB().Reader.Select(&diffs, s.DB().Reader.Rebind(
				`SELECT COALESCE(diff, '') FROM change_log
				  WHERE entity_id = ? AND action = 'update'`), id); err != nil {
				t.Fatalf("reading the change log: %v", err)
			}
			if len(diffs) == 0 {
				t.Fatal("no update was logged at all, so this test is checking nothing")
			}
			for _, d := range diffs {
				if strings.Contains(d, "lifecycle") {
					t.Errorf("change_log records a lifecycle change for a group that is "+
						"still active: %s\nThe audit says somebody withdrew this group -- "+
						"and a withdrawal cascades, so it also claims the members, uplinks "+
						"and anchors went with it. All of them are still there.", d)
				}
			}
		})
	}
}

// TestUpdateNetAnchorCannotLogAWithdrawalThatDidNotHappen is the same claim for
// the anchor half of the pair, and it is not redundant: the two carry-overs are
// separate lines in separate methods, so a mutation of either survives the
// other's test. The anchor's phantom is quieter than the group's -- no cascade
// to misreport -- but it is the entry point a reachability scope enters the
// estate through, so an audit trail claiming it was withdrawn points anyone
// reading it at the wrong cause during exactly the incident it exists for.
func TestUpdateNetAnchorCannotLogAWithdrawalThatDidNotHappen(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			grp := mustNetGroup(t, s, ctx, "grp-anchor-pin", domain.NetGroupMCLAG,
				domain.NetRoleCore, domain.AvailActiveActive)
			anchor, err := domain.NewNetAnchor(NewID(), "pin-net", "Pin net",
				"environment", grp, s.Now())
			if err != nil {
				t.Fatalf("building anchor: %v", err)
			}
			if err := s.CreateNetAnchor(ctx, testPermit, anchor); err != nil {
				t.Fatalf("creating anchor: %v", err)
			}

			loaded, err := s.GetNetAnchor(ctx, anchor.ID)
			if err != nil {
				t.Fatalf("loading: %v", err)
			}
			forged := *loaded
			forged.Name = "Renamed net"
			forged.Lifecycle = domain.LifecycleRetired
			if err := s.UpdateNetAnchor(ctx, testPermit, &forged); err != nil {
				t.Fatalf("UpdateNetAnchor with a submitted lifecycle: %v", err)
			}

			var life string
			if err := s.DB().Reader.Get(&life, s.DB().Reader.Rebind(
				`SELECT lifecycle FROM net_anchor WHERE id = ?`), anchor.ID); err != nil {
				t.Fatalf("reading the anchor's lifecycle: %v", err)
			}
			if life != domain.LifecycleActive {
				t.Errorf("the row is %q, want active", life)
			}

			var diffs []string
			if err := s.DB().Reader.Select(&diffs, s.DB().Reader.Rebind(
				`SELECT COALESCE(diff, '') FROM change_log
				  WHERE entity_id = ? AND action = 'update'`), anchor.ID); err != nil {
				t.Fatalf("reading the change log: %v", err)
			}
			if len(diffs) == 0 {
				t.Fatal("no update was logged at all, so this test is checking nothing")
			}
			for _, d := range diffs {
				if strings.Contains(d, "lifecycle") {
					t.Errorf("change_log records a lifecycle change for an anchor that is "+
						"still active: %s\nThe audit points an incident investigation at a "+
						"withdrawn entry point that was never withdrawn.", d)
				}
			}
		})
	}
}
