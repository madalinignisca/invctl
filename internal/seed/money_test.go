// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package seed_test

import (
	"testing"

	"github.com/madalinignisca/invctl/internal/seed"
	"github.com/madalinignisca/invctl/internal/store"
)

// TestTheFixtureShowsARefreshAndAPriceSeries.
//
// THE FOURTH TIME THIS TEST HAS HAD TO BE WRITTEN, and the reason it keeps
// being needed is that a feature with no fixture behind it renders an empty
// panel, which is indistinguishable from a feature that does not work. Cabling,
// physical fit and the notes panel each shipped that way first.
//
// It asserts the SHAPE the features need, not the exact figures: the amounts
// move whenever somebody reprices the fixture, but "a lineage exists" and "a
// price moved more than once" must survive every one of those edits.
func TestTheFixtureShowsARefreshAndAPriceSeries(t *testing.T) {
	seed.CompanyEstate = true
	t.Cleanup(func() { seed.CompanyEstate = false })

	eachEngine(t, func(t *testing.T, f *fixture) {
		// A refresh: something replaced something, and both ends are priced, or
		// the comparison J1 exists for has nothing to compare.
		successor, ok := f.refs.Assets["hv-esx-01"]
		if !ok {
			t.Fatal("the company layer has no hv-esx-01")
		}
		cmp, err := f.store.ReplacementFor(f.ctx, successor, f.store.Now())
		if err != nil {
			t.Fatalf("reading the replacement: %v", err)
		}
		if cmp == nil {
			t.Fatal("no asset in the fixture replaces another; WP-J1 has nothing to show")
		}
		if !cmp.Comparable() {
			t.Error("the refresh pair is not both priced, so no price comparison renders")
		}
		if cmp.PercentChange() == 0 {
			t.Error("the refresh cost exactly what it replaced; a fixture where nothing " +
				"moved cannot demonstrate a comparison")
		}
		if !cmp.PredecessorRetired {
			t.Error("the predecessor is still active, which is the warning case rather " +
				"than the ordinary one this is meant to show")
		}

		// A price series: at least one thing has been repriced more than once,
		// because two figures are a change and three are a trend.
		rack, ok := f.refs.Assets["colo-rack-07"]
		if !ok {
			t.Fatal("the company layer has no colo-rack-07")
		}
		series, err := f.store.PriceMovementForAsset(f.ctx, rack)
		if err != nil {
			t.Fatalf("reading price movement: %v", err)
		}
		var moved *store.PriceSeries
		for i := range series {
			if series[i].Moved() {
				moved = &series[i]
				break
			}
		}
		if moved == nil {
			t.Fatal("no cost line in the fixture has ever moved; WP-J2 renders nothing")
		}
		if len(moved.Steps) < 3 {
			t.Errorf("the series has %d steps; three make a trend, two only a change",
				len(moved.Steps))
		}
		if moved.TotalPercentChange() <= 0 {
			t.Error("the series does not rise, so it cannot demonstrate a price " +
				"outrunning inflation")
		}
		// Windows must not overlap, or a total computed for the overlapping day
		// counts one cost twice.
		for i := 1; i < len(moved.Steps); i++ {
			prev, cur := moved.Steps[i-1], moved.Steps[i]
			if prev.Until == "" {
				t.Errorf("step %d is open-ended but is not the last", i-1)
				continue
			}
			if prev.Until >= cur.From {
				t.Errorf("step %d closes %s and step %d opens %s; the windows overlap",
					i-1, prev.Until, i, cur.From)
			}
		}
	})
}
