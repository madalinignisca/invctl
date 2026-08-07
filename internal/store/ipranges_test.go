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

// The allocator's arithmetic is proven in internal/domain against hand-worked
// numbers. What these cover is the join: three different tables have to reach
// it, and a query that misses one produces a confident answer that hands out an
// address somebody is already using.

func mustPrefix(t *testing.T, s *SQLStore, ctx context.Context, cidr string) string {
	t.Helper()
	p, err := domain.NewPrefix(NewID(), cidr)
	if err != nil {
		t.Fatalf("building %s: %v", cidr, err)
	}
	if err := s.CreatePrefix(ctx, testActor, p); err != nil {
		t.Fatalf("creating %s: %v", cidr, err)
	}
	return p.ID
}

func mustRange(t *testing.T, s *SQLStore, ctx context.Context, start, end string) string {
	t.Helper()
	r, err := domain.NewIPRange(NewID(), start, end)
	if err != nil {
		t.Fatalf("building %s-%s: %v", start, end, err)
	}
	if err := s.CreateIPRange(ctx, testActor, r); err != nil {
		t.Fatalf("creating %s-%s: %v", start, end, err)
	}
	return r.ID
}

func TestNextFreeAddressExcludesAllThreeSources(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			id := mustPrefix(t, s, ctx, "10.60.0.0/24")

			// Nothing declared yet: the network address is never offered.
			got, err := s.NextFreeAddress(ctx, id)
			if err != nil {
				t.Fatalf("next free: %v", err)
			}
			if !got.Found || got.Address != "10.60.0.1" {
				t.Fatalf("empty /24 offered %q (found=%v), want 10.60.0.1", got.Address, got.Found)
			}

			// 1. An assignment.
			assetID := mustAsset(t, s, ctx, domain.KindServer, "srv-free", nil)
			ifaceID := mustInterface(t, s, ctx, assetID, "eth0")
			addr, err := domain.NewIPAddress(NewID(), "10.60.0.1", &ifaceID, domain.IPRolePrimary)
			if err != nil {
				t.Fatalf("building address: %v", err)
			}
			if err := s.CreateIPAddress(ctx, testActor, addr); err != nil {
				t.Fatalf("creating address: %v", err)
			}
			if got, _ = s.NextFreeAddress(ctx, id); got.Address != "10.60.0.2" {
				t.Errorf("with .1 assigned, offered %s, want 10.60.0.2", got.Address)
			}

			// 2. A reservation. Skipped whole, because whatever owns it will
			//    issue from it without asking this system.
			rangeID := mustRange(t, s, ctx, "10.60.0.2", "10.60.0.99")
			if got, _ = s.NextFreeAddress(ctx, id); got.Address != "10.60.0.100" {
				t.Errorf("with .2-.99 reserved, offered %s, want 10.60.0.100. A "+
					"reservation the allocator cannot see is a pool it will hand "+
					"addresses out of twice", got.Address)
			}

			// 3. A child prefix. Delegated, so not ours to hand out one at a time.
			mustPrefix(t, s, ctx, "10.60.0.96/27") // .96-.127
			if got, _ = s.NextFreeAddress(ctx, id); got.Address != "10.60.0.128" {
				t.Errorf("with .96/27 carved out, offered %s, want 10.60.0.128", got.Address)
			}

			// Retiring the reservation returns its space -- but .96/27 still
			// covers .100, so the answer moves back only as far as that allows.
			if err := s.RetireIPRange(ctx, testActor, rangeID); err != nil {
				t.Fatalf("retiring the range: %v", err)
			}
			if got, _ = s.NextFreeAddress(ctx, id); got.Address != "10.60.0.2" {
				t.Errorf("after the reservation was withdrawn, offered %s, want "+
					"10.60.0.2 -- a retired reservation must stop excluding", got.Address)
			}
		})
	}
}

// TestAPrefixWithNothingLeftSaysSoRatherThanGuessing.
func TestAPrefixWithNothingLeftSaysSoRatherThanGuessing(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			// A /30 has exactly two usable addresses; reserve both.
			id := mustPrefix(t, s, ctx, "10.61.0.0/30")
			mustRange(t, s, ctx, "10.61.0.1", "10.61.0.2")

			got, err := s.NextFreeAddress(ctx, id)
			if err != nil {
				t.Fatalf("next free: %v", err)
			}
			if got.Found {
				t.Errorf("offered %s from a /30 whose two usable addresses are both "+
					"reserved", got.Address)
			}
		})
	}
}

// TestRetiringAReservationIsAudited. Soft delete, and the audit obligation is
// the ordinary one -- a range that stops reserving space is a change somebody
// has to be able to find later.
func TestRetiringAReservationIsAudited(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			rangeID := mustRange(t, s, ctx, "10.62.0.10", "10.62.0.20")
			if err := s.RetireIPRange(ctx, testActor, rangeID); err != nil {
				t.Fatalf("retiring: %v", err)
			}

			entries, err := s.ListChangesForEntity(ctx, "ip_range", rangeID, 50)
			if err != nil {
				t.Fatalf("reading the change log: %v", err)
			}
			if len(entries) < 2 {
				t.Errorf("the range has %d change_log entries, want at least 2 "+
					"(the declaration and the withdrawal)", len(entries))
			}

			// It must also leave the live list.
			rows, err := s.ListIPRanges(ctx)
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			for _, r := range rows {
				if r.ID == rangeID {
					t.Error("a retired reservation is still in the live list")
				}
			}
		})
	}
}
