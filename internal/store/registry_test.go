// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// TestAnAggregateCountsWhatHasBeenCarvedOutOfIt.
//
// "How much of this delegation have we actually used" is the question an
// aggregate exists to answer, and the arithmetic has to come from the prefixes
// falling inside it -- there is no stored link, by design, so that declaring a
// narrower network changes the answer by itself.
func TestAnAggregateCountsWhatHasBeenCarvedOutOfIt(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			agg, err := domain.NewAggregate(NewID(), "10.100.0.0/16")
			if err != nil {
				t.Fatalf("building aggregate: %v", err)
			}
			if err := s.CreateAggregate(ctx, testActor, agg); err != nil {
				t.Fatalf("creating aggregate: %v", err)
			}

			find := func() AggregateRow {
				t.Helper()
				rows, err := s.ListAggregates(ctx)
				if err != nil {
					t.Fatalf("listing: %v", err)
				}
				for _, r := range rows {
					if r.ID == agg.ID {
						return r
					}
				}
				t.Fatal("the aggregate vanished")
				return AggregateRow{}
			}

			if got := find(); !got.Unused() || got.Prefixes != 0 {
				t.Errorf("a fresh delegation shows %d prefixes, want 0", got.Prefixes)
			}

			// Two /24s inside and one /24 outside.
			mustPrefix(t, s, ctx, "10.100.1.0/24")
			mustPrefix(t, s, ctx, "10.100.2.0/24")
			mustPrefix(t, s, ctx, "10.200.1.0/24")

			// AND A v6 PREFIX THAT GENUINELY COLLIDES. The bytes of a64:100::/64
			// are 0a64 0100 …, which sit lexicographically between the v4
			// aggregate's 0a640000 and 0a64ffff -- so a comparison that ignores
			// the family counts a v6 /64 inside a v4 /16 and reports a
			// utilisation nobody would question.
			//
			// The first version of this case used 2001:db8:100::/64, whose bytes
			// are far above the v4 range and are rejected by the range comparison
			// itself. It passed with BOTH guards removed, which is to say it
			// tested nothing at all.
			mustPrefix(t, s, ctx, "a64:100::/64")

			got := find()
			if got.Prefixes != 2 {
				t.Errorf("the /16 contains %d prefixes, want 2 -- the /24 outside it and "+
					"the v6 one must not count", got.Prefixes)
			}
			if want := "512"; got.Allocated.String() != want {
				t.Errorf("allocated = %s, want %s (two /24s)", got.Allocated, want)
			}
			// 512 of 65536.
			if pct := got.UtilPercent(); pct < 0.7 || pct > 0.8 {
				t.Errorf("utilisation = %.3f%%, want ~0.78%%", pct)
			}
			if got.Unused() {
				t.Error("a delegation with two prefixes in it reports unused")
			}
		})
	}
}

func TestAnASNumberIsBoundedAndPrivateRangesAreKnown(t *testing.T) {
	cases := []struct {
		n       int64
		ok      bool
		private bool
	}{
		{0, false, false},
		{1, true, false},
		{64512, true, true},      // start of the 16-bit private range
		{65534, true, true},      // end of it
		{65535, true, false},     // reserved by convention, not by this check
		{4200000000, true, true}, // 32-bit private
		{4294967294, true, true},
		{4294967295, false, false}, // last-resort AS, reserved
	}
	for _, tc := range cases {
		_, err := domain.NewASN("a", tc.n)
		if tc.ok && err != nil {
			t.Errorf("AS%d was refused: %v", tc.n, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("AS%d was accepted, but it is reserved", tc.n)
		}
		if got := domain.IsPrivateASN(tc.n); got != tc.private {
			t.Errorf("IsPrivateASN(%d) = %v, want %v", tc.n, got, tc.private)
		}
	}
}
