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

// TestUpdateLinkCorrectsMediumAndLength is the ordinary case UpdateLink
// exists for: a cable's medium or length was typed wrong, and fixing it must
// not touch anything else about the row.
func TestUpdateLinkCorrectsMediumAndLength(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			linkID, _, _ := cabled(t, s, ctx, "sw-medium-a", "sw-medium-b")

			before, err := s.GetLink(ctx, linkID)
			if err != nil {
				t.Fatalf("loading: %v", err)
			}

			corrected := *before
			corrected.Medium = strPtr("mmf")
			corrected.LengthM = intPtr(50)
			if err := s.UpdateLink(ctx, testPermit, &corrected); err != nil {
				t.Fatalf("UpdateLink: %v", err)
			}

			after, err := s.GetLink(ctx, linkID)
			if err != nil {
				t.Fatalf("reloading: %v", err)
			}
			if after.Medium == nil || *after.Medium != "mmf" {
				t.Errorf("medium = %v, want mmf", after.Medium)
			}
			if after.LengthM == nil || *after.LengthM != 50 {
				t.Errorf("length_m = %v, want 50", after.LengthM)
			}
			if after.RowVersion != before.RowVersion+1 {
				t.Errorf("row_version = %d, want %d", after.RowVersion, before.RowVersion+1)
			}

			var diffs []string
			if err := s.DB().Reader.Select(&diffs, s.DB().Reader.Rebind(
				`SELECT COALESCE(diff, '') FROM change_log
				  WHERE entity_id = ? AND action = 'update'`), linkID); err != nil {
				t.Fatalf("reading the change log: %v", err)
			}
			if len(diffs) == 0 {
				t.Fatal("no update was logged, so this test is checking nothing")
			}
			joined := strings.Join(diffs, " ")
			if !strings.Contains(joined, "medium") || !strings.Contains(joined, "length_m") {
				t.Errorf("change_log does not record the medium/length_m change: %s", joined)
			}
		})
	}
}

// TestUpdateLinkCannotMoveOrWithdrawACableThatWasNotTouched is the guard the
// column pin in UpdateLink protects, and the second time this family of
// test has been needed here (TestUpdateNetGroupCannotLogAWithdrawalThatDidNotHappen
// is the first). The UPDATE statement UpdateLink issues never names
// a_interface_id, b_interface_id or lifecycle, so the DATABASE is safe
// regardless of what this test submits -- deleting the pin in UpdateLink
// leaves the database assertions below green. What only the pin protects is
// logUpdate's diff, built from the two STRUCTS: without it, a forged
// endpoint or a forged lifecycle would still reach change_log as a claim
// that the cable moved to a different port or was unpatched, when neither
// happened -- exactly the physical-event-that-never-happened harm the
// write-surface census recorded against Link.
//
// A REAL change (medium) rides along, so an update is genuinely logged and
// there is a diff to inspect; submitting only the forged fields produces no
// diff at all once the pin is in place, which would leave this test
// asserting over nothing.
func TestUpdateLinkCannotMoveOrWithdrawACableThatWasNotTouched(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			linkID, aID, bID := cabled(t, s, ctx, "sw-forge-a", "sw-forge-b")
			// A third, unpatched port -- the far end the forged submission
			// will claim this cable now terminates on.
			cID := mustAsset(t, s, ctx, domain.KindSwitch, "sw-forge-c", nil)
			ifC := mustInterface(t, s, ctx, cID, "eth0")

			before, err := s.GetLink(ctx, linkID)
			if err != nil {
				t.Fatalf("loading: %v", err)
			}

			forged := *before
			forged.Medium = strPtr("mmf")              // the real change
			forged.BInterfaceID = ifC                  // the forged move
			forged.Lifecycle = domain.LifecycleRetired // the forged withdrawal
			if err := s.UpdateLink(ctx, testPermit, &forged); err != nil {
				t.Fatalf("UpdateLink with a forged endpoint and lifecycle: %v", err)
			}

			after, err := s.GetLink(ctx, linkID)
			if err != nil {
				t.Fatalf("reloading: %v", err)
			}
			if after.AInterfaceID != before.AInterfaceID || after.BInterfaceID != before.BInterfaceID {
				t.Errorf("the cable's endpoints moved: a=%s b=%s, want a=%s b=%s",
					after.AInterfaceID, after.BInterfaceID, before.AInterfaceID, before.BInterfaceID)
			}
			if after.Lifecycle != domain.LifecycleActive {
				t.Errorf("lifecycle = %q, want active -- nobody unpatched this cable", after.Lifecycle)
			}
			if after.Medium == nil || *after.Medium != "mmf" {
				t.Fatalf("the real change (medium) did not land, so this test cannot tell "+
					"whether anything was logged at all: medium = %v", after.Medium)
			}

			var diffs []string
			if err := s.DB().Reader.Select(&diffs, s.DB().Reader.Rebind(
				`SELECT COALESCE(diff, '') FROM change_log
				  WHERE entity_id = ? AND action = 'update'`), linkID); err != nil {
				t.Fatalf("reading the change log: %v", err)
			}
			if len(diffs) == 0 {
				t.Fatal("no update was logged at all, so this test is checking nothing")
			}
			for _, d := range diffs {
				if strings.Contains(d, "a_interface_id") || strings.Contains(d, "b_interface_id") {
					t.Errorf("change_log records an endpoint move that did not happen: %s\n"+
						"The audit says this cable was re-patched to a different port -- "+
						"nobody touched it.", d)
				}
				if strings.Contains(d, "lifecycle") {
					t.Errorf("change_log records a withdrawal that did not happen: %s\n"+
						"The audit says this cable was unpatched -- it is still active.", d)
				}
			}

			// Neither port, nor what is fastened to them, moved either. ifC
			// stays bare -- the forged B-end never actually got the cable.
			ifARow, err := s.GetInterface(ctx, before.AInterfaceID)
			if err != nil {
				t.Fatalf("reloading A: %v", err)
			}
			if ifARow.AssetID != aID {
				t.Errorf("A's owning asset changed: %s, want %s", ifARow.AssetID, aID)
			}
			ifBRow, err := s.GetInterface(ctx, before.BInterfaceID)
			if err != nil {
				t.Fatalf("reloading B: %v", err)
			}
			if ifBRow.AssetID != bID {
				t.Errorf("B's owning asset changed: %s, want %s", ifBRow.AssetID, bID)
			}
			rows, err := s.ListInterfaces(ctx, cID)
			if err != nil {
				t.Fatalf("listing C's interfaces: %v", err)
			}
			for _, r := range rows {
				if r.IsPatched() {
					t.Errorf("port %s on the forged far end is patched, and it never should "+
						"have been: nothing here re-cabled it", r.Name)
				}
			}
		})
	}
}

// TestUpdateLinkLeavesPortsAndAttachmentsUntouched is the "correcting a cable
// is not a create" half: an address assigned to either end of the cable, and
// the ports themselves, must come through a correction unchanged.
func TestUpdateLinkLeavesPortsAndAttachmentsUntouched(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			linkID, aID, _ := cabled(t, s, ctx, "sw-attach-a", "sw-attach-b")

			rows, err := s.ListInterfaces(ctx, aID)
			if err != nil {
				t.Fatalf("listing A's interfaces: %v", err)
			}
			if len(rows) != 1 {
				t.Fatalf("got %d interfaces on A, want 1", len(rows))
			}
			ifA := rows[0]
			addr, err := domain.NewIPAddress(NewID(), "10.90.0.1", &ifA.ID, domain.IPRolePrimary)
			if err != nil {
				t.Fatalf("building address: %v", err)
			}
			if err := s.CreateIPAddress(ctx, testPermit, addr); err != nil {
				t.Fatalf("creating address: %v", err)
			}

			link, err := s.GetLink(ctx, linkID)
			if err != nil {
				t.Fatalf("loading link: %v", err)
			}
			corrected := *link
			corrected.LengthM = intPtr(12)
			if err := s.UpdateLink(ctx, testPermit, &corrected); err != nil {
				t.Fatalf("UpdateLink: %v", err)
			}

			rowsAfter, err := s.ListInterfaces(ctx, aID)
			if err != nil {
				t.Fatalf("re-listing A's interfaces: %v", err)
			}
			if len(rowsAfter) != 1 {
				t.Fatalf("got %d interfaces on A after the correction, want 1", len(rowsAfter))
			}
			got := rowsAfter[0]
			if got.RowVersion != ifA.RowVersion {
				t.Errorf("port's row_version changed from %d to %d -- correcting the cable "+
					"touched the port", ifA.RowVersion, got.RowVersion)
			}
			if len(got.Addresses) != 1 || got.Addresses[0].ID != addr.ID {
				t.Errorf("the port's address did not survive the cable correction: %+v", got.Addresses)
			}
			if !got.IsPatched() || got.LinkID != linkID {
				t.Errorf("the port lost its cable across the correction: LinkID=%q, want %q",
					got.LinkID, linkID)
			}
		})
	}
}
