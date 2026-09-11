// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// This file closes the three claims writeSurfaceGaps's "FHRPGroup" entry made
// impossible: a group's own fields (protocol, group number, name,
// description), a member's priority, and the group's virtual address were all
// declare-once-and-redraw. One claim, three corrections -- CLAUDE.md's
// framing for this work package, and the reason all three live in one file.

// TestUpdateFHRPGroupCorrectsItsFields is the first correction: protocol,
// group number, name and description, each checked in isolation so a mutation
// breaking one column's assignment does not hide behind the others passing.
func TestUpdateFHRPGroupCorrectsItsFields(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			tests := []struct {
				name  string
				apply func(g *domain.FHRPGroup)
				check func(t *testing.T, g *domain.FHRPGroup)
			}{
				{
					name:  "protocol",
					apply: func(g *domain.FHRPGroup) { g.Protocol = domain.FHRPHSRP },
					check: func(t *testing.T, g *domain.FHRPGroup) {
						if g.Protocol != domain.FHRPHSRP {
							t.Errorf("protocol = %q, want %q", g.Protocol, domain.FHRPHSRP)
						}
					},
				},
				{
					name:  "group_number",
					apply: func(g *domain.FHRPGroup) { g.GroupNumber = 99 },
					check: func(t *testing.T, g *domain.FHRPGroup) {
						if g.GroupNumber != 99 {
							t.Errorf("group_number = %d, want 99", g.GroupNumber)
						}
					},
				},
				{
					name:  "name",
					apply: func(g *domain.FHRPGroup) { g.Name = "gw-corrected" },
					check: func(t *testing.T, g *domain.FHRPGroup) {
						if g.Name != "gw-corrected" {
							t.Errorf("name = %q, want %q", g.Name, "gw-corrected")
						}
					},
				},
				{
					name: "description",
					apply: func(g *domain.FHRPGroup) {
						d := "the segment this actually answers for"
						g.Description = &d
					},
					check: func(t *testing.T, g *domain.FHRPGroup) {
						if g.Description == nil || *g.Description != "the segment this actually answers for" {
							t.Errorf("description = %v, want the corrected text", g.Description)
						}
					},
				},
			}

			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					id := mustFHRP(t, s, ctx, 10, "gw-"+tc.name)
					g, err := s.GetFHRPGroup(ctx, id)
					if err != nil {
						t.Fatalf("loading: %v", err)
					}
					tc.apply(g)
					if err := s.UpdateFHRPGroup(ctx, testPermit, g); err != nil {
						t.Fatalf("UpdateFHRPGroup: %v", err)
					}
					got, err := s.GetFHRPGroup(ctx, id)
					if err != nil {
						t.Fatalf("reloading: %v", err)
					}
					tc.check(t, got)
					if got.RowVersion != 2 {
						t.Errorf("row_version = %d, want 2 after one correction", got.RowVersion)
					}
				})
			}
		})
	}
}

// TestUpdateFHRPGroupRefusesADuplicateName mirrors CreateFHRPGroup's own
// collision handling (fhrp_group_name_key, live rows only) rather than
// letting a raw driver error reach the caller as a 500.
//
// NOT group_number: migration 00032's own comment says a VRID or HSRP number
// "is unique only on the segment they run on, never globally", and there is
// no unique index on it at all -- checked directly against both migration
// files. Two groups legitimately sharing a number on different segments is
// not a collision this store may refuse, so the only real uniqueness a
// correction can hit is the one CreateFHRPGroup already enforces: the name.
func TestUpdateFHRPGroupRefusesADuplicateName(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			mustFHRP(t, s, ctx, 10, "gw-taken")
			otherID := mustFHRP(t, s, ctx, 11, "gw-renaming")

			other, err := s.GetFHRPGroup(ctx, otherID)
			if err != nil {
				t.Fatalf("loading: %v", err)
			}
			other.Name = "gw-taken"
			err = s.UpdateFHRPGroup(ctx, testPermit, other)
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("renaming onto a live name = %v, want ErrConflict", err)
			}

			// Refused, not half-applied: the row still carries its old name.
			reloaded, err := s.GetFHRPGroup(ctx, otherID)
			if err != nil {
				t.Fatalf("reloading: %v", err)
			}
			if reloaded.Name != "gw-renaming" {
				t.Errorf("name = %q after a refused rename, want the original", reloaded.Name)
			}
		})
	}
}

// TestUpdateFHRPGroupDoesNotDisturbMembers. Correcting the group's own fields
// and correcting its membership are two different verbs (UpdateFHRPGroup and
// SetFHRPMembers) writing the same row's audited value from two different
// places -- auditedFHRPGroup folds membership into every entry either one
// writes, and a mutation that let UpdateFHRPGroup drop the fold, or SQL that
// touched fhrp_member as a side effect, would both pass every other test in
// this file while silently losing a router from the group's audited state.
func TestUpdateFHRPGroupDoesNotDisturbMembers(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			gid := mustFHRP(t, s, ctx, 10, "gw-members")
			a1 := mustAsset(t, s, ctx, domain.KindFirewall, "fw-e", nil)
			i1 := mustInterface(t, s, ctx, a1, "eth0")
			prio := 150
			if err := s.SetFHRPMembers(ctx, testPermit, gid, []domain.FHRPMember{
				{GroupID: gid, InterfaceID: i1, Priority: &prio},
			}); err != nil {
				t.Fatalf("setting a member: %v", err)
			}

			g, err := s.GetFHRPGroup(ctx, gid)
			if err != nil {
				t.Fatalf("loading: %v", err)
			}
			g.Name = "gw-members-renamed"
			if err := s.UpdateFHRPGroup(ctx, testPermit, g); err != nil {
				t.Fatalf("UpdateFHRPGroup: %v", err)
			}

			members, err := s.ListFHRPMembers(ctx, gid)
			if err != nil {
				t.Fatalf("listing members: %v", err)
			}
			if len(members) != 1 || members[0].InterfaceID != i1 {
				t.Fatalf("members after correcting the group's own fields = %+v, want the "+
					"one router untouched", members)
			}
			if members[0].Priority == nil || *members[0].Priority != 150 {
				t.Errorf("priority = %v after an unrelated correction, want 150 kept", members[0].Priority)
			}
		})
	}
}

// TestUpdateFHRPGroupCannotLogAWithdrawalThatDidNotHappen is the third of this
// family (UpdateProvider, UpdateInterface, UpdateNetGroup) and the pin lives
// in internal/store for the reason all three record: the plausible property
// -- "the submitted lifecycle does not reach the row" -- is guaranteed by the
// UPDATE statement itself, which never names the lifecycle column, so
// deleting `g.Lifecycle = before.Lifecycle` would leave that assertion green.
// The real property is the change_log diff, and routed through
// FHRPUpdate the pin is unreachable: that handler builds its argument as
// `updated := *existing`, so Lifecycle already holds the stored value by the
// time the store sees it. This test forges the struct directly, the way a
// future caller building one from a payload rather than a loaded row would.
//
// Worse than the other three for the reader at 03:00: a real withdrawal of an
// FHRP group is exactly the row RetireFHRPGroup itself refuses to write while
// a VIP still names it, so a phantom one in the audit claims an impossible
// event happened.
func TestUpdateFHRPGroupCannotLogAWithdrawalThatDidNotHappen(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			id := mustFHRP(t, s, ctx, 10, "gw-pin")

			g, err := s.GetFHRPGroup(ctx, id)
			if err != nil {
				t.Fatalf("loading: %v", err)
			}
			// A REAL change alongside the forged one, so there is a genuine
			// update and a diff to inspect -- submitting only the lifecycle
			// produces no diff at all once the pin is in place, which would
			// leave this test asserting over nothing.
			forged := *g
			forged.Name = "gw-pin-renamed"
			forged.Lifecycle = domain.LifecycleRetired
			if err := s.UpdateFHRPGroup(ctx, testPermit, &forged); err != nil {
				t.Fatalf("UpdateFHRPGroup with a submitted lifecycle: %v", err)
			}

			var life string
			if err := s.DB().Reader.Get(&life, s.DB().Reader.Rebind(
				`SELECT lifecycle FROM fhrp_group WHERE id = ?`), id); err != nil {
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
						"still active: %s\nRetireFHRPGroup refuses to withdraw a group "+
						"while a VIP still names it -- this audit entry claims an event "+
						"that method itself would have refused.", d)
				}
			}
		})
	}
}

// TestFHRPMemberPriorityIsCorrectedNotChurned is the second correction: a
// member's priority moves from "declare once, then remove-and-re-add to
// change your mind" to a real in-place correction.
//
// SetFHRPMembers already accepted an arbitrary priority per member in the
// slice it is given -- what was missing was a caller that preserved every
// OTHER member untouched while replacing just one router's number, which is
// exactly what a "correct this priority" form does (the shape FHRPMemberUpdate
// uses). This checks the property that shape depends on: the audited value
// reflects a priority change with the same router still named against it, not
// two membership events (a departure at the old priority, an arrival at the
// new one) that never happened.
func TestFHRPMemberPriorityIsCorrectedNotChurned(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			gid := mustFHRP(t, s, ctx, 10, "gw-priority")
			a1 := mustAsset(t, s, ctx, domain.KindFirewall, "fw-f", nil)
			a2 := mustAsset(t, s, ctx, domain.KindFirewall, "fw-g", nil)
			i1 := mustInterface(t, s, ctx, a1, "eth0")
			i2 := mustInterface(t, s, ctx, a2, "eth0")

			was := 100
			untouched := 50
			if err := s.SetFHRPMembers(ctx, testPermit, gid, []domain.FHRPMember{
				{GroupID: gid, InterfaceID: i1, Priority: &was},
				{GroupID: gid, InterfaceID: i2, Priority: &untouched},
			}); err != nil {
				t.Fatalf("setting members: %v", err)
			}
			before, err := s.ListChangesForEntity(ctx, "fhrp_group", gid, 50)
			if err != nil {
				t.Fatalf("reading the change log: %v", err)
			}

			// Correct fw-f's priority, carrying fw-g's untouched -- exactly what
			// a caller preserving every other member does.
			now := 200
			if err := s.SetFHRPMembers(ctx, testPermit, gid, []domain.FHRPMember{
				{GroupID: gid, InterfaceID: i1, Priority: &now},
				{GroupID: gid, InterfaceID: i2, Priority: &untouched},
			}); err != nil {
				t.Fatalf("correcting the priority: %v", err)
			}

			members, err := s.ListFHRPMembers(ctx, gid)
			if err != nil {
				t.Fatalf("listing members: %v", err)
			}
			if len(members) != 2 {
				t.Fatalf("member count = %d after a priority correction, want 2 -- "+
					"nobody left and nobody joined", len(members))
			}
			for _, m := range members {
				switch m.InterfaceID {
				case i1:
					if m.Priority == nil || *m.Priority != 200 {
						t.Errorf("fw-f's priority = %v, want 200", m.Priority)
					}
				case i2:
					if m.Priority == nil || *m.Priority != 50 {
						t.Errorf("fw-g's priority = %v, want the untouched 50", m.Priority)
					}
				}
			}

			after, err := s.ListChangesForEntity(ctx, "fhrp_group", gid, 50)
			if err != nil {
				t.Fatalf("reading the change log: %v", err)
			}
			if len(after) != len(before)+1 {
				t.Fatalf("the priority correction wrote %d change_log entries, want exactly "+
					"1 -- a departure-and-arrival pair would write for a router that never left",
					len(after)-len(before))
			}
			d := after[0].Diff
			if !strings.Contains(d, "fw-f") {
				t.Errorf("the audit entry does not name the router whose priority changed: %s", d)
			}
			if !strings.Contains(d, "100") || !strings.Contains(d, "200") {
				t.Errorf("the audit entry does not show the priority moving from 100 to 200: %s", d)
			}
		})
	}
}

// TestAssignVIPMovesTheVIPAndReleasesTheOldAddress is the third correction,
// and the exact bug the task description named: "nothing in the codebase
// ever clears [fhrp_group_id]... so a VIP declared against the wrong address
// is permanent, and RetireIPAddress then refuses to withdraw that address
// because fhrp_group_id is set. The row is stuck." This proves the row is no
// longer stuck: reassigning frees the address AssignVIP releases, and that
// freed address can then be withdrawn -- the thing RetireIPAddress's own
// comment said could never happen.
func TestAssignVIPMovesTheVIPAndReleasesTheOldAddress(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			gid := mustFHRP(t, s, ctx, 10, "gw-move")

			wrong, err := domain.NewIPAddress(NewID(), "10.92.0.1", nil, domain.IPRoleVIP)
			if err != nil {
				t.Fatalf("building the wrong address: %v", err)
			}
			if err := s.CreateIPAddress(ctx, testPermit, wrong); err != nil {
				t.Fatalf("creating the wrong address: %v", err)
			}
			right, err := domain.NewIPAddress(NewID(), "10.92.0.2", nil, domain.IPRoleVIP)
			if err != nil {
				t.Fatalf("building the right address: %v", err)
			}
			if err := s.CreateIPAddress(ctx, testPermit, right); err != nil {
				t.Fatalf("creating the right address: %v", err)
			}

			if err := s.AssignVIP(ctx, testPermit, wrong.ID, gid); err != nil {
				t.Fatalf("declaring the wrong address as the VIP: %v", err)
			}

			// STUCK, before the move: the wrong address cannot be withdrawn
			// while it is a live VIP.
			if err := s.RetireIPAddress(ctx, testPermit, wrong.ID); !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("withdrawing a live VIP = %v, want ErrConflict (setup assumption)", err)
			}

			if err := s.AssignVIP(ctx, testPermit, right.ID, gid); err != nil {
				t.Fatalf("moving the VIP to the right address: %v", err)
			}

			gotWrong, err := s.GetIPAddress(ctx, wrong.ID)
			if err != nil {
				t.Fatalf("reloading the wrong address: %v", err)
			}
			if gotWrong.FHRPGroupID != nil {
				t.Error("the wrong address still names the group after the VIP moved -- " +
					"the stuck-row bug is not fixed")
			}
			gotRight, err := s.GetIPAddress(ctx, right.ID)
			if err != nil {
				t.Fatalf("reloading the right address: %v", err)
			}
			if gotRight.FHRPGroupID == nil || *gotRight.FHRPGroupID != gid {
				t.Error("the right address does not name the group after the move")
			}

			// UNSTUCK: now that fhrp_group_id is clear, the address that was
			// wrongly declared can finally be withdrawn.
			if err := s.RetireIPAddress(ctx, testPermit, wrong.ID); err != nil {
				t.Errorf("withdrawing the released address: %v, want it to succeed now "+
					"that nothing answers through it", err)
			}

			// BOTH sides of the move are audited, in the transaction the move ran in.
			releaseLog, err := s.ListChangesForEntity(ctx, "ip_address", wrong.ID, 50)
			if err != nil {
				t.Fatalf("reading the wrong address's change log: %v", err)
			}
			foundRelease := false
			for _, c := range releaseLog {
				if strings.Contains(c.Diff, "fhrp_group_id") {
					foundRelease = true
				}
			}
			if !foundRelease {
				t.Error("no change_log entry records fhrp_group_id being released on the old address")
			}
			assignLog, err := s.ListChangesForEntity(ctx, "ip_address", right.ID, 50)
			if err != nil {
				t.Fatalf("reading the right address's change log: %v", err)
			}
			foundAssign := false
			for _, c := range assignLog {
				if strings.Contains(c.Diff, "fhrp_group_id") {
					foundAssign = true
				}
			}
			if !foundAssign {
				t.Error("no change_log entry records fhrp_group_id being set on the new address")
			}
		})
	}
}

// TestAssignVIPToItsOwnCurrentAddressIsANoOp guards against the move logic
// releasing and re-setting the SAME address -- which would still be correct
// data but a spurious pair of change_log entries (a release immediately
// followed by an assign) for nothing having changed at all.
func TestAssignVIPToItsOwnCurrentAddressIsANoOp(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			gid := mustFHRP(t, s, ctx, 10, "gw-noop")
			addr, err := domain.NewIPAddress(NewID(), "10.93.0.1", nil, domain.IPRoleVIP)
			if err != nil {
				t.Fatalf("building the address: %v", err)
			}
			if err := s.CreateIPAddress(ctx, testPermit, addr); err != nil {
				t.Fatalf("creating the address: %v", err)
			}
			if err := s.AssignVIP(ctx, testPermit, addr.ID, gid); err != nil {
				t.Fatalf("assigning: %v", err)
			}
			before, err := s.GetIPAddress(ctx, addr.ID)
			if err != nil {
				t.Fatalf("reloading: %v", err)
			}

			if err := s.AssignVIP(ctx, testPermit, addr.ID, gid); err != nil {
				t.Fatalf("re-assigning the group's own current VIP: %v", err)
			}

			after, err := s.GetIPAddress(ctx, addr.ID)
			if err != nil {
				t.Fatalf("reloading: %v", err)
			}
			if after.RowVersion != before.RowVersion {
				t.Errorf("row_version moved from %d to %d on a no-op re-assignment",
					before.RowVersion, after.RowVersion)
			}
		})
	}
}
