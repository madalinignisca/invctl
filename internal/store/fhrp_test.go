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

func mustFHRP(t *testing.T, s *SQLStore, ctx context.Context, num int, name string) string {
	t.Helper()
	g, err := domain.NewFHRPGroup(NewID(), domain.FHRPVRRP3, num, name)
	if err != nil {
		t.Fatalf("building group: %v", err)
	}
	if err := s.CreateFHRPGroup(ctx, testPermit, g); err != nil {
		t.Fatalf("creating group: %v", err)
	}
	return g.ID
}

// TestOneMemberIsNotRedundancy is the finding this work package exists for.
//
// A VIP with two routers survives losing one. A VIP with ONE is a single point
// of failure wearing the costume of a redundant one -- it looks identical on
// every other screen, and the difference is what somebody needs at 03:00.
func TestOneMemberIsNotRedundancy(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			gid := mustFHRP(t, s, ctx, 10, "gw-prod")
			a1 := mustAsset(t, s, ctx, domain.KindFirewall, "fw-a", nil)
			a2 := mustAsset(t, s, ctx, domain.KindFirewall, "fw-b", nil)
			i1 := mustInterface(t, s, ctx, a1, "eth0")
			i2 := mustInterface(t, s, ctx, a2, "eth0")

			find := func() FHRPGroupRow {
				t.Helper()
				rows, err := s.ListFHRPGroups(ctx)
				if err != nil {
					t.Fatalf("listing: %v", err)
				}
				for _, r := range rows {
					if r.ID == gid {
						return r
					}
				}
				t.Fatal("the group vanished from the list")
				return FHRPGroupRow{}
			}

			if got := find().Redundancy(); got != domain.FHRPNoMembers {
				t.Errorf("an empty group reports %q, want %q", got, domain.FHRPNoMembers)
			}

			one := 200
			if err := s.SetFHRPMembers(ctx, testPermit, gid, []domain.FHRPMember{
				{GroupID: gid, InterfaceID: i1, Priority: &one},
			}); err != nil {
				t.Fatalf("setting one member: %v", err)
			}
			if got := find().Redundancy(); got != domain.FHRPSingleMember {
				t.Errorf("a one-router group reports %q, want %q -- the protocol is "+
					"configured and buys nothing", got, domain.FHRPSingleMember)
			}

			if err := s.SetFHRPMembers(ctx, testPermit, gid, []domain.FHRPMember{
				{GroupID: gid, InterfaceID: i1, Priority: &one},
				{GroupID: gid, InterfaceID: i2},
			}); err != nil {
				t.Fatalf("setting two members: %v", err)
			}
			if got := find().Redundancy(); got != domain.FHRPRedundant {
				t.Errorf("a two-router group reports %q, want %q", got, domain.FHRPRedundant)
			}
		})
	}
}

// TestChangingMembershipIsAuditedOnTheGroup. The fifth set table, same rule.
func TestChangingMembershipIsAuditedOnTheGroup(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			gid := mustFHRP(t, s, ctx, 20, "gw-dev")
			a1 := mustAsset(t, s, ctx, domain.KindFirewall, "fw-c", nil)
			a2 := mustAsset(t, s, ctx, domain.KindFirewall, "fw-d", nil)
			i1 := mustInterface(t, s, ctx, a1, "eth0")
			i2 := mustInterface(t, s, ctx, a2, "eth0")

			if err := s.SetFHRPMembers(ctx, testPermit, gid, []domain.FHRPMember{
				{GroupID: gid, InterfaceID: i1},
			}); err != nil {
				t.Fatalf("setting members: %v", err)
			}
			before, _ := s.ListChangesForEntity(ctx, "fhrp_group", gid, 50)

			// The router that leaves is the change somebody has to find later.
			if err := s.SetFHRPMembers(ctx, testPermit, gid, []domain.FHRPMember{
				{GroupID: gid, InterfaceID: i2},
			}); err != nil {
				t.Fatalf("replacing members: %v", err)
			}
			after, err := s.ListChangesForEntity(ctx, "fhrp_group", gid, 50)
			if err != nil {
				t.Fatalf("reading the change log: %v", err)
			}
			if len(after) <= len(before) {
				t.Fatalf("swapping the group's only router wrote no change_log entry "+
					"(%d before, %d after)", len(before), len(after))
			}
			if !strings.Contains(after[0].Diff, "fw-d") {
				t.Errorf("the audit entry does not name the router that joined: %s", after[0].Diff)
			}
		})
	}
}

// TestAVIPIsARealAddressAndTheAllocatorKnowsIt.
//
// The VIP is an ip_address row rather than text on the group, and this is what
// that buys: the allocator will not offer a live gateway address to somebody
// else. Recording it as text would have looked identical and handed it out.
func TestAVIPIsARealAddressAndTheAllocatorKnowsIt(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			pid := mustPrefix(t, s, ctx, "10.90.0.0/24")
			gid := mustFHRP(t, s, ctx, 30, "gw-vip")

			// .1 is the obvious gateway, and the allocator would offer it first.
			free, err := s.NextFreeAddress(ctx, pid)
			if err != nil {
				t.Fatalf("next free: %v", err)
			}
			if free.Address != "10.90.0.1" {
				t.Fatalf("first free = %s, want 10.90.0.1", free.Address)
			}

			// CREATED ON A PORT, deliberately. The first version of this test made
			// the address with no interface, so "assigning clears the interface"
			// was asserted against a field that was already nil -- removing the
			// clear from the UPDATE left the test green. It has to start held by
			// a port for the clearing to mean anything.
			holder := mustAsset(t, s, ctx, domain.KindFirewall, "fw-holder", nil)
			holderPort := mustInterface(t, s, ctx, holder, "eth0")
			addr, err := domain.NewIPAddress(NewID(), "10.90.0.1", &holderPort, domain.IPRolePrimary)
			if err != nil {
				t.Fatalf("building the VIP: %v", err)
			}
			if err := s.CreateIPAddress(ctx, testPermit, addr); err != nil {
				t.Fatalf("creating the VIP: %v", err)
			}
			if err := s.AssignVIP(ctx, testPermit, addr.ID, gid); err != nil {
				t.Fatalf("assigning the VIP: %v", err)
			}

			free, err = s.NextFreeAddress(ctx, pid)
			if err != nil {
				t.Fatalf("next free: %v", err)
			}
			if free.Address == "10.90.0.1" {
				t.Error("the allocator offered the gateway address. A VIP recorded as " +
					"text on the group would have looked the same and handed it out")
			}

			// And it must no longer claim a port: a gateway answered for by a
			// group does not live on one box.
			got, err := s.GetIPAddress(ctx, addr.ID)
			if err != nil {
				t.Fatalf("getting the VIP: %v", err)
			}
			if got.InterfaceID != nil {
				t.Error("the VIP still names an interface. An address answered for by a " +
					"group does not live on one box, and leaving the port set says the " +
					"gateway is that router's -- the opposite of what the protocol is for")
			}
			if got.FHRPGroupID == nil || *got.FHRPGroupID != gid {
				t.Error("the VIP does not point at its group")
			}
		})
	}
}

// TestAGroupWithALiveVIPCannotBeRetired.
func TestAGroupWithALiveVIPCannotBeRetired(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			gid := mustFHRP(t, s, ctx, 40, "gw-retire")
			addr, _ := domain.NewIPAddress(NewID(), "10.91.0.1", nil, domain.IPRolePrimary)
			if err := s.CreateIPAddress(ctx, testPermit, addr); err != nil {
				t.Fatalf("creating the VIP: %v", err)
			}
			if err := s.AssignVIP(ctx, testPermit, addr.ID, gid); err != nil {
				t.Fatalf("assigning: %v", err)
			}

			if err := s.RetireFHRPGroup(ctx, testPermit, gid); err == nil {
				t.Fatal("a group still answering for an address was retired")
			} else if !errors.Is(err, domain.ErrConflict) {
				t.Errorf("error = %v, want ErrConflict so the handler returns 409", err)
			}
		})
	}
}
