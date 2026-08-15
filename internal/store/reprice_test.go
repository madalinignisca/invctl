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

// TestARepriceKeepsBothFigures is the assertion the whole verb exists to make
// true, and it was false before it.
//
// Editing a line writes amount_minor in place, so the old price is gone from
// the row and only a change_log diff remembers it -- and change_log is an audit
// trail, not a queryable price series. A reprice closes the line in force and
// opens a new one, so the estate keeps both figures AND the date the change
// took effect.
func TestARepriceKeepsBothFigures(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			id := f.priceAssetFrom(t, "hv-01", "support", domain.CostYearly, 94000, "2023-10-07")

			opened, err := f.s.RepriceAssetCost(f.ctx, testActor, f.assets["hv-01"], RepriceSpec{
				LineID: id, NewAmountMinor: 141000, EffectiveFrom: "2026-10-07",
			})
			if err != nil {
				t.Fatalf("repricing: %v", err)
			}

			lines, err := f.s.ListAssetCosts(f.ctx, f.assets["hv-01"])
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if len(lines) != 2 {
				t.Fatalf("got %d lines, want 2: a reprice keeps the old figure", len(lines))
			}

			var old, cur *CostRow
			for i := range lines {
				if lines[i].ID == id {
					old = &lines[i]
				}
				if lines[i].ID == opened.ID {
					cur = &lines[i]
				}
			}
			if old == nil || cur == nil {
				t.Fatal("both the superseded and the new line must exist")
			}
			if old.AmountMinor != 94000 {
				t.Errorf("the superseded line now reads %d; repricing must not amend it",
					old.AmountMinor)
			}
			// Closed the day BEFORE the new one starts, so no date falls in both
			// windows and a total computed for any day counts one figure.
			if old.ValidUntil == nil || *old.ValidUntil != "2026-10-06" {
				t.Errorf("superseded line closes at %q, want 2026-10-06", derefString(old.ValidUntil))
			}
			if cur.ValidFrom != "2026-10-07" || cur.AmountMinor != 141000 {
				t.Errorf("new line is %d from %s, want 141000 from 2026-10-07",
					cur.AmountMinor, cur.ValidFrom)
			}
			// Same kind and period, or it is not the same cost changing.
			if cur.Kind != old.Kind || cur.Period != old.Period {
				t.Errorf("new line is %s/%s, want %s/%s", cur.Kind, cur.Period, old.Kind, old.Period)
			}
		})
	}
}

// TestARepriceCannotStartBeforeTheLineItReplaces. Overlapping windows mean a
// date that falls in both, and a total computed for that date counts the same
// cost twice.
func TestARepriceCannotStartBeforeTheLineItReplaces(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			id := f.priceAssetFrom(t, "hv-01", "support", domain.CostYearly, 94000, "2023-10-07")

			for _, when := range []string{"2023-10-07", "2020-01-01"} {
				_, err := f.s.RepriceAssetCost(f.ctx, testActor, f.assets["hv-01"], RepriceSpec{
					LineID: id, NewAmountMinor: 141000, EffectiveFrom: when,
				})
				if err == nil {
					t.Fatalf("a reprice effective %s was accepted; the windows overlap", when)
				}
				// THE FIELD ERROR IS THE ASSERTION, not merely that something
				// failed. The CHECK constraint `valid_until >= valid_from`
				// would refuse this anyway -- deleting the Go guard still
				// produces an error, so a test asserting only err != nil passes
				// against the mutation it was written to catch. What the guard
				// buys is a refusal naming the field, which a 422 can redraw
				// beside the input, instead of a driver's constraint text.
				var ve *domain.ValidationError
				if !errors.As(err, &ve) {
					t.Errorf("effective %s failed with %v; want a field error the form "+
						"can show, not a raw constraint violation", when, err)
					continue
				}
				named := false
				for _, fe := range ve.Fields {
					if fe.Field == "effective_from" {
						named = true
					}
				}
				if !named {
					t.Errorf("effective %s: the error is %v, which does not name "+
						"effective_from", when, err)
				}
			}
		})
	}
}

// TestAOneOffCannotBeRepriced. A purchase happened at a price that was paid;
// there is no "from now on" for it. A second purchase is a second line.
func TestAOneOffCannotBeRepriced(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			id := f.priceAssetFrom(t, "hv-01", "acquisition", domain.CostOnce, 840000, "2023-10-07")
			_, err := f.s.RepriceAssetCost(f.ctx, testActor, f.assets["hv-01"], RepriceSpec{
				LineID: id, NewAmountMinor: 900000, EffectiveFrom: "2026-01-01",
			})
			if err == nil {
				t.Fatal("a one-off was repriced")
			}
			if !errors.Is(err, domain.ErrConflict) {
				t.Errorf("error is %v, want a conflict", err)
			}
		})
	}
}

// TestARepriceRefusesALineOnAnotherOwner. A line id arriving in a URL must not
// reach a line on something the caller was not looking at -- the same rule
// retireCost follows.
func TestARepriceRefusesALineOnAnotherOwner(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			id := f.priceAssetFrom(t, "hv-01", "support", domain.CostYearly, 94000, "2023-10-07")
			_, err := f.s.RepriceAssetCost(f.ctx, testActor, f.assets["sw-core-1"], RepriceSpec{
				LineID: id, NewAmountMinor: 141000, EffectiveFrom: "2026-10-07",
			})
			if !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("error is %v, want not-found for a line on another owner", err)
			}
		})
	}
}

// TestARepriceWritesBothAuditRows. Two facts changed, so two entries: the old
// line was closed and a new one was opened. One entry would leave the estate
// unable to say when the price moved.
func TestARepriceWritesBothAuditRows(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			id := f.priceAssetFrom(t, "hv-01", "support", domain.CostYearly, 94000, "2023-10-07")
			before := f.changeRows(t, "asset_cost")

			if _, err := f.s.RepriceAssetCost(f.ctx, testActor, f.assets["hv-01"], RepriceSpec{
				LineID: id, NewAmountMinor: 141000, EffectiveFrom: "2026-10-07",
			}); err != nil {
				t.Fatalf("repricing: %v", err)
			}
			if got := f.changeRows(t, "asset_cost") - before; got != 2 {
				t.Errorf("a reprice wrote %d audit rows, want 2 (one closed, one opened)", got)
			}
		})
	}
}

// TestPriceMovementReadsTheSeriesARepriceCreated. The verb and the view are
// one feature: without the reprice there is nothing to read, and without the
// view the reprice is invisible.
func TestPriceMovementReadsTheSeriesARepriceCreated(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			id := f.priceAssetFrom(t, "hv-01", "support", domain.CostYearly, 94000, "2023-10-07")
			// An unrelated kind, which must form its OWN series: comparing a
			// purchase against an annual support rate would call the difference
			// a price rise.
			f.priceAssetFrom(t, "hv-01", "acquisition", domain.CostOnce, 840000, "2023-10-07")

			second, err := f.s.RepriceAssetCost(f.ctx, testActor, f.assets["hv-01"], RepriceSpec{
				LineID: id, NewAmountMinor: 141000, EffectiveFrom: "2026-10-07",
			})
			if err != nil {
				t.Fatalf("repricing: %v", err)
			}
			if _, err := f.s.RepriceAssetCost(f.ctx, testActor, f.assets["hv-01"], RepriceSpec{
				LineID: second.ID, NewAmountMinor: 155000, EffectiveFrom: "2027-10-07",
			}); err != nil {
				t.Fatalf("repricing again: %v", err)
			}

			series, err := f.s.PriceMovementForAsset(f.ctx, f.assets["hv-01"])
			if err != nil {
				t.Fatalf("reading movement: %v", err)
			}
			if len(series) != 2 {
				t.Fatalf("got %d series, want one per kind (support, acquisition)", len(series))
			}
			// Moved first: what changed is what somebody came to see.
			support := series[0]
			if support.Kind != "support" {
				t.Fatalf("first series is %q; the one that moved must lead", support.Kind)
			}
			if !support.Moved() {
				t.Fatal("support was repriced twice and does not report as moved")
			}
			if len(support.Steps) != 3 {
				t.Fatalf("support has %d steps, want 3", len(support.Steps))
			}
			if support.FirstMinor() != 94000 || support.CurrentMinor() != 155000 {
				t.Errorf("series runs %d..%d, want 94000..155000",
					support.FirstMinor(), support.CurrentMinor())
			}
			if got := support.TotalPercentChange(); got != 64 {
				t.Errorf("total change %d%%, want 64%%", got)
			}
			// Only the last step is in force; the earlier two are closed.
			if !support.Steps[2].IsCurrent() {
				t.Error("the newest step is not the one in force")
			}
			if support.Steps[0].IsCurrent() || support.Steps[1].IsCurrent() {
				t.Error("a superseded step still reports as in force")
			}
			// Step-to-step change, which is what a supplier is argued with.
			if support.Steps[1].PercentChange != 50 {
				t.Errorf("first rise %d%%, want 50%%", support.Steps[1].PercentChange)
			}
			// The acquisition never moved, and must say so rather than be hidden.
			if series[1].Moved() {
				t.Error("a single-line kind reports as moved")
			}
		})
	}
}
