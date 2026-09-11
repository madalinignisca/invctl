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

// A prefix and an address can be withdrawn (migration 00064), refusing while
// anything still lives inside them (RetirePrefix) or depends on them
// (RetireIPAddress) -- the write-surface census's two remaining gaps.

// TestRetirePrefixAgreesWithTheTree is the pin the task called for: build an
// estate where the prefixes page's own tree shows a child, and assert
// RetirePrefix refuses on that same parent. RetirePrefix answers "does this
// prefix have a child" by asking ListPrefixTree -- the exact function the
// operator's page calls -- rather than a second, hand-written containment
// query. This test is what would catch the two of them disagreeing.
func TestRetirePrefixAgreesWithTheTree(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			parent := mustPrefix(t, s, ctx, "10.70.0.0/16")
			_ = mustPrefix(t, s, ctx, "10.70.1.0/24")

			tree, err := s.ListPrefixTree(ctx)
			if err != nil {
				t.Fatalf("listing tree: %v", err)
			}
			var found bool
			for _, n := range tree {
				if n.ID == parent && n.Children > 0 {
					found = true
				}
			}
			if !found {
				t.Fatal("the tree does not show the child under the parent; this test " +
					"is asserting nothing about the property it exists to pin")
			}

			err = s.RetirePrefix(ctx, testPermit, parent)
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("retiring a parent with a child prefix = %v, want ErrConflict",
					err)
			}
			if got := prefixLifecycleOf(t, s, ctx, parent); got != domain.LifecycleActive {
				t.Errorf("the refused retire still changed lifecycle to %q", got)
			}
		})
	}
}

// TestRetirePrefixRefusesWhileAnAddressIsAssigned covers the second hold:
// a live ip_address inside the span, independent of any child prefix.
func TestRetirePrefixRefusesWhileAnAddressIsAssigned(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			prefixID := mustPrefix(t, s, ctx, "10.71.0.0/24")
			assetID := mustAsset(t, s, ctx, domain.KindServer, "srv-pfx-addr", nil)
			ifaceID := mustInterface(t, s, ctx, assetID, "eth0")
			addr, err := domain.NewIPAddress(NewID(), "10.71.0.5", &ifaceID, domain.IPRolePrimary)
			if err != nil {
				t.Fatalf("building address: %v", err)
			}
			if err := s.CreateIPAddress(ctx, testPermit, addr); err != nil {
				t.Fatalf("creating address: %v", err)
			}

			err = s.RetirePrefix(ctx, testPermit, prefixID)
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("retiring a network with a live address inside it = %v, want ErrConflict",
					err)
			}
			if !containsMsg(err, "address") {
				t.Errorf("refusal %q does not name what is still inside it", err)
			}
		})
	}
}

// TestRetirePrefixRefusesWhileAReservationSpansIt covers the third hold: a
// live ip_range reservation, which carries no PrefixNode field of its own and
// is checked by a dedicated query rather than through the tree.
func TestRetirePrefixRefusesWhileAReservationSpansIt(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			prefixID := mustPrefix(t, s, ctx, "10.72.0.0/24")
			mustRange(t, s, ctx, "10.72.0.10", "10.72.0.20")

			err := s.RetirePrefix(ctx, testPermit, prefixID)
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("retiring a network with a reservation inside it = %v, want ErrConflict",
					err)
			}
			if !containsMsg(err, "reservation") {
				t.Errorf("refusal %q does not name what is still inside it", err)
			}
		})
	}
}

// TestRetirePrefixWithdrawsWhenEmpty is the success path: nothing inside,
// the withdrawal goes through, and the row leaves ListPrefixes and
// ListPrefixTree -- the census's own reason for calling this a gap ("keeps
// taking part in every containment answer computed over the tree").
func TestRetirePrefixWithdrawsWhenEmpty(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			id := mustPrefix(t, s, ctx, "10.73.0.0/24")

			if err := s.RetirePrefix(ctx, testPermit, id); err != nil {
				t.Fatalf("retiring an empty network: %v", err)
			}
			if got := prefixLifecycleOf(t, s, ctx, id); got != domain.LifecycleRetired {
				t.Errorf("lifecycle = %q after retiring, want retired", got)
			}

			rows, err := s.ListPrefixes(ctx)
			if err != nil {
				t.Fatalf("listing prefixes: %v", err)
			}
			for _, r := range rows {
				if r.ID == id {
					t.Error("ListPrefixes still returns a withdrawn network")
				}
			}
			tree, err := s.ListPrefixTree(ctx)
			if err != nil {
				t.Fatalf("listing tree: %v", err)
			}
			for _, n := range tree {
				if n.ID == id {
					t.Error("ListPrefixTree still returns a withdrawn network")
				}
			}

			// Retiring twice must not claim a second withdrawal.
			before := countChangeLog(t, s, ctx, id)
			if err := s.RetirePrefix(ctx, testPermit, id); err != nil {
				t.Fatalf("second retire: %v", err)
			}
			if after := countChangeLog(t, s, ctx, id); after != before {
				t.Errorf("a second retire wrote another change_log row (%d -> %d)", before, after)
			}
		})
	}
}

// TestRetirePrefixIsResolvedForContainment. A withdrawn network takes no
// further part in a containment answer -- ResolveAddress must not still
// resolve through it.
func TestRetirePrefixIsResolvedForContainment(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			id := mustPrefix(t, s, ctx, "10.74.0.0/24")
			if _, err := s.ResolveAddress(ctx, "10.74.0.5"); err != nil {
				t.Fatalf("resolving before retiring: %v", err)
			}
			if err := s.RetirePrefix(ctx, testPermit, id); err != nil {
				t.Fatalf("retiring: %v", err)
			}
			if _, err := s.ResolveAddress(ctx, "10.74.0.5"); err == nil {
				t.Error("ResolveAddress still answers through a withdrawn network")
			}
		})
	}
}

// TestRetireIPAddressRefusesWhileAnEndpointBinds is the first IPAddress hold.
func TestRetireIPAddressRefusesWhileAnEndpointBinds(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			assetID := mustAsset(t, s, ctx, domain.KindServer, "srv-addr-ep", nil)
			ifaceID := mustInterface(t, s, ctx, assetID, "eth0")
			addr, err := domain.NewIPAddress(NewID(), "10.80.0.5", &ifaceID, domain.IPRolePrimary)
			if err != nil {
				t.Fatalf("building address: %v", err)
			}
			if err := s.CreateIPAddress(ctx, testPermit, addr); err != nil {
				t.Fatalf("creating address: %v", err)
			}
			svcID := mustService(t, s, ctx, "svc-addr-ep")
			port := 443
			ep, err := domain.NewEndpoint(NewID(), svcID, "https", domain.ProtoTCP, &port, domain.BindHost)
			if err != nil {
				t.Fatalf("building endpoint: %v", err)
			}
			ep.IPAddressID = &addr.ID
			if err := s.CreateEndpoint(ctx, testPermit, ep); err != nil {
				t.Fatalf("creating endpoint: %v", err)
			}

			err = s.RetireIPAddress(ctx, testPermit, addr.ID)
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("retiring an address bound to a live endpoint = %v, want ErrConflict", err)
			}
			if !containsMsg(err, "endpoint") {
				t.Errorf("refusal %q does not name what is still bound to it", err)
			}

			// Retiring the endpoint first clears the hold.
			if err := s.RetireEndpoint(ctx, testPermit, ep.ID); err != nil {
				t.Fatalf("retiring endpoint: %v", err)
			}
			if err := s.RetireIPAddress(ctx, testPermit, addr.ID); err != nil {
				t.Fatalf("retiring the address once its endpoint is gone: %v", err)
			}
		})
	}
}

// TestRetireIPAddressRefusesForAnFHRPVirtualAddress is the second hold, and
// the finding the task asked for explicitly: RetireFHRPGroup already refuses
// to retire a group while a VIP still names it, and nothing in this codebase
// clears fhrp_group_id once AssignVIP sets it -- so a group does NOT
// tolerate losing its virtual address, and this refusal is symmetric with
// that one rather than a new restriction invented for it.
func TestRetireIPAddressRefusesForAnFHRPVirtualAddress(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			groupID := mustFHRP(t, s, ctx, 10, "vip-group")
			addr, err := domain.NewIPAddress(NewID(), "10.81.0.1", nil, domain.IPRoleVIP)
			if err != nil {
				t.Fatalf("building address: %v", err)
			}
			if err := s.CreateIPAddress(ctx, testPermit, addr); err != nil {
				t.Fatalf("creating address: %v", err)
			}
			if err := s.AssignVIP(ctx, testPermit, addr.ID, groupID); err != nil {
				t.Fatalf("assigning vip: %v", err)
			}

			err = s.RetireIPAddress(ctx, testPermit, addr.ID)
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("retiring a group's virtual address = %v, want ErrConflict", err)
			}
			if !containsMsg(err, "redundancy group") {
				t.Errorf("refusal %q does not name what it still answers for", err)
			}
		})
	}
}

// TestRetireIPAddressWithdrawsAndFreesTheAllocator is the payoff the census
// named: "an address freed cannot be released, so the allocator keeps
// treating it as taken." Proven end to end through NextFreeAddress, the same
// join TestNextFreeAddressExcludesAllThreeSources already exercises.
func TestRetireIPAddressWithdrawsAndFreesTheAllocator(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			prefixID := mustPrefix(t, s, ctx, "10.82.0.0/29")
			assetID := mustAsset(t, s, ctx, domain.KindServer, "srv-addr-free", nil)
			ifaceID := mustInterface(t, s, ctx, assetID, "eth0")
			addr, err := domain.NewIPAddress(NewID(), "10.82.0.1", &ifaceID, domain.IPRolePrimary)
			if err != nil {
				t.Fatalf("building address: %v", err)
			}
			if err := s.CreateIPAddress(ctx, testPermit, addr); err != nil {
				t.Fatalf("creating address: %v", err)
			}

			got, err := s.NextFreeAddress(ctx, prefixID)
			if err != nil {
				t.Fatalf("next free before withdrawing: %v", err)
			}
			if !got.Found || got.Address != "10.82.0.2" {
				t.Fatalf("next free before withdrawing = %q (found=%v), want 10.82.0.2",
					got.Address, got.Found)
			}

			if err := s.RetireIPAddress(ctx, testPermit, addr.ID); err != nil {
				t.Fatalf("retiring: %v", err)
			}

			got, err = s.NextFreeAddress(ctx, prefixID)
			if err != nil {
				t.Fatalf("next free after withdrawing: %v", err)
			}
			if !got.Found || got.Address != "10.82.0.1" {
				t.Errorf("next free after withdrawing = %q (found=%v), want 10.82.0.1 -- "+
					"a withdrawn address must be released back to the allocator", got.Address, got.Found)
			}

			// Second half of the payoff: the row leaves the port it was assigned
			// to, the same way a withdrawn cable stops showing as a far end.
			ifaces, err := s.ListInterfaces(ctx, assetID)
			if err != nil {
				t.Fatalf("listing interfaces: %v", err)
			}
			for _, row := range ifaces {
				for _, a := range row.Addresses {
					if a.ID == addr.ID {
						t.Error("a withdrawn address is still listed on its port")
					}
				}
			}
		})
	}
}

// TestRetiringAnAddressTwiceLogsOneWithdrawal. RetireIPRange, RetireInterface
// and RetirePrefix all guard this and it is easy to leave out.
func TestRetiringAnAddressTwiceLogsOneWithdrawal(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			assetID := mustAsset(t, s, ctx, domain.KindServer, "srv-addr-twice", nil)
			ifaceID := mustInterface(t, s, ctx, assetID, "eth0")
			addr, err := domain.NewIPAddress(NewID(), "10.83.0.1", &ifaceID, domain.IPRolePrimary)
			if err != nil {
				t.Fatalf("building address: %v", err)
			}
			if err := s.CreateIPAddress(ctx, testPermit, addr); err != nil {
				t.Fatalf("creating address: %v", err)
			}

			if err := s.RetireIPAddress(ctx, testPermit, addr.ID); err != nil {
				t.Fatalf("first retire: %v", err)
			}
			before := countChangeLog(t, s, ctx, addr.ID)
			if err := s.RetireIPAddress(ctx, testPermit, addr.ID); err != nil {
				t.Fatalf("second retire: %v", err)
			}
			if after := countChangeLog(t, s, ctx, addr.ID); after != before {
				t.Errorf("a second retire wrote another change_log row (%d -> %d)", before, after)
			}
		})
	}
}

func prefixLifecycleOf(t *testing.T, s *SQLStore, ctx context.Context, id string) string {
	t.Helper()
	p, err := s.GetPrefix(ctx, id)
	if err != nil {
		t.Fatalf("reading prefix %s: %v", id, err)
	}
	return p.Lifecycle
}

func containsMsg(err error, substr string) bool {
	return err != nil && strings.Contains(err.Error(), substr)
}
