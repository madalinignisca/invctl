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

// fromSupplier attaches a priced line to an asset, owed to one supplier.
func (f *projectFixture) fromSupplier(t *testing.T, asset, kind, period string,
	minor int64, from, providerID string) string {

	t.Helper()
	c, err := domain.NewCost(NewID(), domain.CostSpec{
		Kind: kind, Period: period, AmountMinor: minor, ValidFrom: &from,
		ProviderID: &providerID,
	}, f.s.Now())
	if err != nil {
		t.Fatalf("building the cost: %v", err)
	}
	if err := f.s.AddAssetCost(f.ctx, testActor, f.assets[asset], c); err != nil {
		t.Fatalf("attaching the cost: %v", err)
	}
	return c.ID
}

// repriceTo supersedes a line, keeping the supplier unless one is given.
func (f *projectFixture) repriceTo(t *testing.T, asset, lineID string,
	minor int64, from string) *domain.Cost {

	t.Helper()
	opened, err := f.s.RepriceAssetCost(f.ctx, testActor, f.assets[asset], RepriceSpec{
		LineID: lineID, NewAmountMinor: minor, EffectiveFrom: from,
	})
	if err != nil {
		t.Fatalf("repricing: %v", err)
	}
	return opened
}

// TestASuppliersRiseIsWeightedByWhatItsLinesCost.
//
// THE CHOICE WORTH ARGUING WITH, and the reason it is not an average. A supplier
// with a €40 line up 50% and a €4,000 line up 2% has not raised its prices by
// 26%. An unweighted mean hands somebody a grievance built out of a rounding
// error on a small invoice, which is the opposite of what a decision to leave a
// supplier needs.
func TestASuppliersRiseIsWeightedByWhatItsLinesCost(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			p := mustProvider(t, f.s, f.ctx, "Big Reseller")

			// A small line that jumped, and a large one that barely moved.
			small := f.fromSupplier(t, "hv-01", "support", domain.CostMonthly, 4000, "2024-01-01", p)
			f.repriceTo(t, "hv-01", small, 6000, "2025-01-01") // +50%
			big := f.fromSupplier(t, "hv-01", "licence", domain.CostMonthly, 400000, "2024-01-01", p)
			f.repriceTo(t, "hv-01", big, 408000, "2025-01-01") // +2%

			rep, err := f.s.SupplierMovements(f.ctx, domain.FormatDate(costNow))
			if err != nil {
				t.Fatalf("gathering: %v", err)
			}
			var got *SupplierMovement
			for i := range rep.Suppliers {
				if rep.Suppliers[i].Provider == "Big Reseller" {
					got = &rep.Suppliers[i]
				}
			}
			if got == nil {
				t.Fatal("the supplier that invoices two lines does not appear")
			}
			if got.Moved != 2 {
				t.Fatalf("%d series moved, want 2", got.Moved)
			}
			// Weighted: (50×6000 + 2×408000) / 414000 ≈ 2.7 → 2.
			// The unweighted mean would be 26.
			if got.NominalPercent > 5 {
				t.Errorf("the rise is reported as %d%%, want about 2%% -- an "+
					"unweighted mean of 50%% and 2%% would give 26%% and would be "+
					"a grievance built from a small invoice", got.NominalPercent)
			}
		})
	}
}

// TestASeriesThatChangedHandsIsNotARise.
//
// Switching reseller at renewal moves the figure. Attributing that to whoever
// happens to be current would manufacture exactly the accusation this report
// exists to test.
func TestASeriesThatChangedHandsIsNotARise(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			old := mustProvider(t, f.s, f.ctx, "Old Reseller")
			new := mustProvider(t, f.s, f.ctx, "New Reseller")

			line := f.fromSupplier(t, "hv-01", "support", domain.CostMonthly, 10000, "2024-01-01", old)
			opened := f.repriceTo(t, "hv-01", line, 20000, "2025-01-01") // +100%
			// The renewal was taken from somebody else.
			opened.ProviderID = &new
			if err := f.s.UpdateAssetCost(f.ctx, testActor, opened); err != nil {
				t.Fatalf("switching supplier: %v", err)
			}

			rep, err := f.s.SupplierMovements(f.ctx, domain.FormatDate(costNow))
			if err != nil {
				t.Fatalf("gathering: %v", err)
			}
			for _, m := range rep.Suppliers {
				if m.Moved != 0 {
					t.Errorf("%s is credited with %d movements across a change of "+
						"supplier; the price moved because the supplier did",
						m.Provider, m.Moved)
				}
				if m.NominalPercent != 0 {
					t.Errorf("%s is reported as having risen %d%%", m.Provider, m.NominalPercent)
				}
				if m.Switched == 0 {
					t.Errorf("%s does not record that a series changed hands", m.Provider)
				}
			}
		})
	}
}

// TestLinesNamingNoSupplierAreCountedNotDropped.
//
// A ranking of four suppliers over a third of the estate's spend is a sample,
// and a reader who cannot see that will read it as the whole book.
func TestLinesNamingNoSupplierAreCountedNotDropped(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			p := mustProvider(t, f.s, f.ctx, "Named Supplier")
			f.fromSupplier(t, "hv-01", "support", domain.CostMonthly, 10000, "2024-01-01", p)
			// And one nobody has attributed.
			f.priceAsset(t, "vm-app-1", "licence", domain.CostMonthly, 250)

			rep, err := f.s.SupplierMovements(f.ctx, domain.FormatDate(costNow))
			if err != nil {
				t.Fatalf("gathering: %v", err)
			}
			if rep.UnattributedLines == 0 {
				t.Error("a line naming no supplier is not counted, so the ranking " +
					"reads as the whole estate")
			}
			if rep.UnattributedMinor != 250 {
				t.Errorf("unattributed spend is %d, want the 250 minor units of "+
					"the unlabelled line", rep.UnattributedMinor)
			}
		})
	}
}

// TestRealTermsIsWithheldWhenInflationDoesNotCoverTheSpan.
//
// Computing it over years treated as zero would understate what money did and
// so flatter the supplier — the direction nobody checks.
func TestRealTermsIsWithheldWhenInflationDoesNotCoverTheSpan(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			p := mustProvider(t, f.s, f.ctx, "Unmeasured Years")
			line := f.fromSupplier(t, "hv-01", "support", domain.CostMonthly, 10000, "2019-01-01", p)
			f.repriceTo(t, "hv-01", line, 15000, "2025-01-01")
			// No inflation rows at all in this fixture.

			rep, err := f.s.SupplierMovements(f.ctx, domain.FormatDate(costNow))
			if err != nil {
				t.Fatalf("gathering: %v", err)
			}
			for _, m := range rep.Suppliers {
				if m.Provider != "Unmeasured Years" {
					continue
				}
				if m.NominalPercent != 50 {
					t.Errorf("nominal is %d%%, want 50", m.NominalPercent)
				}
				if m.Real() {
					t.Error("a real-terms figure is offered for years the inflation " +
						"table does not cover")
				}
				if m.MissingYear == 0 {
					t.Error("the report does not name the year it is missing")
				}
				return
			}
			t.Fatal("the supplier does not appear at all")
		})
	}
}

// TestARenewalKeepsWhoInvoicesItAndWhoBenefits.
//
// TWO THINGS REPRICE USED TO DROP, both found by WP-J6 and both silent.
//
// The supplier: without it every renewal looked like a line that had changed
// hands, and changed hands is deliberately excluded from "did this supplier
// raise its prices" — so the report that exists to judge renewals discounted
// every renewal it had.
//
// The scope: a licence covering three named guests returned to `universal` at
// renewal and was spread across every workload on the host, including the ones
// that derive nothing from it. That is §5.6's subsidy, reintroduced through a
// door §5.6 was not watching.
func TestARenewalKeepsWhoInvoicesItAndWhoBenefits(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			p := mustProvider(t, f.s, f.ctx, "Same Supplier")
			line := f.fromSupplier(t, "hv-01", "licence", domain.CostMonthly, 10000, "2024-01-01", p)

			// Scope it before renewing.
			row, err := f.s.GetAssetCost(f.ctx, line)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			c := row.Cost
			c.AppliesTo = domain.CostConditional
			if err := f.s.UpdateAssetCost(f.ctx, testActor, &c); err != nil {
				t.Fatalf("scoping: %v", err)
			}

			opened := f.repriceTo(t, "hv-01", line, 12000, "2025-01-01")

			after, err := f.s.GetAssetCost(f.ctx, opened.ID)
			if err != nil {
				t.Fatalf("reading the renewal: %v", err)
			}
			if after.ProviderID == nil || *after.ProviderID != p {
				t.Error("the renewal names no supplier, so every renewal reads as " +
					"a line that changed hands and is discounted from the report")
			}
			if after.AppliesTo != domain.CostConditional {
				t.Errorf("the renewal is %q, want conditional: a scoped licence "+
					"returned to universal is spread across workloads that derive "+
					"nothing from it", after.AppliesTo)
			}
		})
	}
}
