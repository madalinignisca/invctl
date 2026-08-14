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
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// setReplaces records that one asset succeeded another.
func (f *projectFixture) setReplaces(t *testing.T, successor, predecessor string) error {
	t.Helper()
	row, err := f.s.GetAsset(f.ctx, f.assets[successor])
	if err != nil {
		t.Fatalf("reading %s: %v", successor, err)
	}
	a := row.Asset
	id := f.assets[predecessor]
	a.ReplacesAssetID = &id
	envIDs := make([]string, len(row.Environments))
	for i, env := range row.Environments {
		envIDs[i] = env.ID
	}
	return f.s.UpdateAsset(f.ctx, testActor, &a, envIDs)
}

// TestAReplacementComparesAcquisitions is the question the CEO asked: what did
// the old one cost, and what does the new one cost.
//
// THE COMPARISON IS BETWEEN ONE-OFFS ON PURPOSE. A monthly support line is not
// a purchase price, and averaging the two produces a percentage nobody can take
// to a supplier. The fixture prices the predecessor with a monthly line as well
// as its acquisition, so a version that totalled everything would report a
// different -- and wrong -- rise.
func TestAReplacementComparesAcquisitions(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)

			// Bought in 2021, replaced in 2026.
			f.priceAssetFrom(t, "sw-core-1", "acquisition", domain.CostOnce, 420000, "2021-03-01")
			// A support contract on the old box, which must NOT enter the
			// comparison: it is what the box cost to keep, not what it cost.
			//
			// DATED BEFORE THE PURCHASE, and that is what makes this assertion
			// bite. An earlier version dated it "now", so the 2021 acquisition
			// was still the earliest line and deleting the period filter changed
			// nothing -- the test passed against the mutation it was written to
			// catch. Sitting in 2020 it becomes the earliest line, so a version
			// that forgets to filter on period reports the monthly rate as the
			// purchase price.
			f.priceAssetFrom(t, "sw-core-1", "operating", domain.CostMonthly, 15000, "2020-01-01")
			f.priceAssetFrom(t, "hv-01", "acquisition", domain.CostOnce, 685000, "2026-03-01")

			if err := f.setReplaces(t, "hv-01", "sw-core-1"); err != nil {
				t.Fatalf("recording the lineage: %v", err)
			}

			got, err := f.s.ReplacementFor(f.ctx, f.assets["hv-01"], costNow)
			if err != nil {
				t.Fatalf("comparing: %v", err)
			}
			if got == nil {
				t.Fatal("no comparison for an asset that replaces another")
			}
			if !got.Comparable() {
				t.Fatal("both ends carry an acquisition price, so it is comparable")
			}
			if got.ThenMinor != 420000 || got.NowMinor != 685000 {
				t.Errorf("then=%d now=%d, want 420000 and 685000: the monthly support "+
					"line on the predecessor must not enter an acquisition comparison",
					got.ThenMinor, got.NowMinor)
			}
			if got.PercentChange() != 63 {
				t.Errorf("PercentChange = %d, want 63", got.PercentChange())
			}
			if !got.Annualisable() {
				t.Fatal("five years apart is long enough to annualise")
			}
			annual := got.AnnualisedPercent()
			if annual < 11 || annual > 14 {
				t.Errorf("annualised = %d%%/yr, want about 12-13 over five years", annual)
			}
		})
	}
}

// TestAnUnpricedEndIsNotAComparison. Half a comparison is worse than none: a
// percentage against a missing number looks authoritative and is invented.
func TestAnUnpricedEndIsNotAComparison(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			f.priceAssetFrom(t, "hv-01", "acquisition", domain.CostOnce, 685000, "2026-03-01")
			if err := f.setReplaces(t, "hv-01", "sw-core-1"); err != nil {
				t.Fatalf("recording the lineage: %v", err)
			}
			got, err := f.s.ReplacementFor(f.ctx, f.assets["hv-01"], costNow)
			if err != nil {
				t.Fatalf("comparing: %v", err)
			}
			if got.Comparable() {
				t.Error("the predecessor has no acquisition price, so nothing is comparable")
			}
			if got.PercentChange() != 0 {
				t.Errorf("PercentChange = %d with an unpriced end; it must not invent one",
					got.PercentChange())
			}
		})
	}
}

// TestAReplacementChainIsAllowedAndACycleIsNot.
//
// A chain is the point -- three generations is three prices -- and a cycle is a
// row that says a box succeeded its own successor. domain.Validate catches the
// one-hop case because it needs no other row; the longer loop needs both ends
// and is refused in the store.
func TestAReplacementChainIsAllowedAndACycleIsNot(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)

			if err := f.setReplaces(t, "vm-app-1", "hv-01"); err != nil {
				t.Fatalf("first link: %v", err)
			}
			if err := f.setReplaces(t, "hv-01", "sw-core-1"); err != nil {
				t.Fatalf("a chain of three must be allowed: %v", err)
			}

			// Closing the loop: sw-core-1 replaced by the box at the far end.
			err := f.setReplaces(t, "sw-core-1", "vm-app-1")
			if err == nil {
				t.Fatal("a replacement cycle was accepted; walking it never terminates")
			}
			if !errors.Is(err, domain.ErrConflict) {
				t.Errorf("error is %v, want a conflict the handler maps to 409", err)
			}
		})
	}
}

// TestAnAssetCannotReplaceItself is the one-hop case, refused in the domain
// because it needs no database to see.
func TestAnAssetCannotReplaceItself(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			if err := f.setReplaces(t, "hv-01", "hv-01"); err == nil {
				t.Fatal("an asset replaced itself")
			}
		})
	}
}
