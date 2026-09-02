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

// Fix round 1, items 1, 2 and 4's reprice half: costs_scope_test.go's fixture
// (asset_cost, the row half) applied to reprice.go's own verb. reprice was
// missed by the original task -- it runs under the caller's own permit with
// no narrowing at all, so it fails closed for an ordinary project owner
// today (an unnarrowed ScopedPermit has no "asset_cost" bucket), but that
// also means the fourth verb -- reprice -- 403s for a project owner who can
// already add, edit, retire and set consumers on the very same line. This
// file proves the fix: reprice now goes through authorizeCostSubject like
// its three siblings, and does so BEFORE any of reprice's own checks that
// would otherwise leak the foreign line's lifecycle, period or start date
// to an unauthorized caller.

// TestCostScopeRepriceOnTheirOwnAsset is item 1's positive case: a project
// owner scoped to their own asset can reprice a line on it, the fourth verb
// that previously always 403'd for them.
func TestCostScopeRepriceOnTheirOwnAsset(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCostScopeFixture(t, e)
			validFrom := domain.FormatDate(f.s.Now())
			c, err := domain.NewCost(NewID(), domain.CostSpec{
				Kind: "acquisition", Period: domain.CostMonthly, AmountMinor: 1000,
				ValidFrom: &validFrom,
			}, f.s.Now())
			if err != nil {
				t.Fatalf("building the cost: %v", err)
			}
			if err := f.s.AddAssetCost(f.ctx, f.permit, f.a1, c); err != nil {
				t.Fatalf("seeding the cost line on the owner's own asset: %v", err)
			}

			effectiveFrom := domain.FormatDate(f.s.Now().AddDate(0, 1, 0))
			opened, err := f.s.RepriceAssetCost(f.ctx, f.permit, f.a1, RepriceSpec{
				LineID: c.ID, NewAmountMinor: 2000, EffectiveFrom: effectiveFrom,
			})
			if err != nil {
				t.Fatalf("RepriceAssetCost on the owner's own asset = %v, want nil", err)
			}
			if opened == nil || opened.ID == c.ID {
				t.Fatalf("reprice did not open a new, distinct line: %+v", opened)
			}

			// BOTH rows this transaction touches are audited -- the closed
			// original (logUpdate) and the newly opened line (logCreate) --
			// which is only possible if the permit reprice ran under
			// actually covered both ids. A permit scoped to only one of
			// them would have failed the SECOND tx.log call and rolled the
			// whole transaction back.
			closedChanges := mustChangesForCost(t, f, "asset_cost", c.ID)
			if len(closedChanges) != 2 { // create (seeding) + the reprice's close
				t.Errorf("closed line has %d change_log rows, want 2 (create, close)", len(closedChanges))
			}
			openedChanges := mustChangesForCost(t, f, "asset_cost", opened.ID)
			if len(openedChanges) != 1 {
				t.Errorf("opened line has %d change_log rows, want 1 (create)", len(openedChanges))
			}
		})
	}
}

// TestRepriceOnAForeignAssetIsRefused is the ordinary refusal case: a
// project owner scoped to A1 cannot reprice a line on A2.
func TestRepriceOnAForeignAssetIsRefused(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCostScopeFixture(t, e)
			validFrom := domain.FormatDate(f.s.Now())
			c, err := domain.NewCost(NewID(), domain.CostSpec{
				Kind: "acquisition", Period: domain.CostMonthly, AmountMinor: 1000,
				ValidFrom: &validFrom,
			}, f.s.Now())
			if err != nil {
				t.Fatalf("building the cost: %v", err)
			}
			if err := f.s.AddAssetCost(f.ctx, testPermit, f.a2, c); err != nil {
				t.Fatalf("seeding the cost line as an administrator: %v", err)
			}

			effectiveFrom := domain.FormatDate(f.s.Now().AddDate(0, 1, 0))
			_, err = f.s.RepriceAssetCost(f.ctx, f.permit, f.a2, RepriceSpec{
				LineID: c.ID, NewAmountMinor: 2000, EffectiveFrom: effectiveFrom,
			})
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("RepriceAssetCost on a foreign asset = %v, want domain.ErrForbidden", err)
			}
		})
	}
}

// TestRepriceAuthorizationRunsBeforeEveryOracleCheck is items 2 and 4
// together: authorization must run BEFORE the retired check, the one-off
// check, and the effective-date check -- each of which, run first against a
// foreign line, hands an unauthorized caller back a fact about a line they
// are not allowed to know exists: its lifecycle (ErrConflict, "is
// retired"), its period (ErrConflict, "a one-off cost"), or its own
// valid_from date (a *domain.ValidationError whose message echoes the date
// back -- afterCostWrite flashes this to the user). Every subtest sets up
// the EXACT condition that check exists to catch, on a line the calling
// permit does not cover, and asserts domain.ErrForbidden specifically: if a
// future refactor moves authorization below any one of these three checks,
// that subtest goes red with the leaking error instead.
func TestRepriceAuthorizationRunsBeforeEveryOracleCheck(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			t.Run("retired", func(t *testing.T) {
				f := newCostScopeFixture(t, e)
				c, err := domain.NewCost(NewID(), newCostSpec(), f.s.Now())
				if err != nil {
					t.Fatalf("building the cost: %v", err)
				}
				if err := f.s.AddAssetCost(f.ctx, testPermit, f.a2, c); err != nil {
					t.Fatalf("seeding the cost line: %v", err)
				}
				if err := f.s.RetireAssetCost(f.ctx, testPermit, f.a2, c.ID); err != nil {
					t.Fatalf("retiring the cost line as an administrator: %v", err)
				}
				effectiveFrom := domain.FormatDate(f.s.Now().AddDate(0, 1, 0))
				_, err = f.s.RepriceAssetCost(f.ctx, f.permit, f.a2, RepriceSpec{
					LineID: c.ID, NewAmountMinor: 2000, EffectiveFrom: effectiveFrom,
				})
				if !errors.Is(err, domain.ErrForbidden) {
					t.Fatalf("reprice of a retired, out-of-scope line = %v, want domain.ErrForbidden "+
						"(if this is domain.ErrConflict, authorization moved back below the retired check)", err)
				}
			})

			t.Run("one-off", func(t *testing.T) {
				f := newCostScopeFixture(t, e)
				c, err := domain.NewCost(NewID(), domain.CostSpec{
					Kind: "acquisition", Period: domain.CostOnce, AmountMinor: 1000,
				}, f.s.Now())
				if err != nil {
					t.Fatalf("building the cost: %v", err)
				}
				if err := f.s.AddAssetCost(f.ctx, testPermit, f.a2, c); err != nil {
					t.Fatalf("seeding the cost line: %v", err)
				}
				effectiveFrom := domain.FormatDate(f.s.Now().AddDate(0, 1, 0))
				_, err = f.s.RepriceAssetCost(f.ctx, f.permit, f.a2, RepriceSpec{
					LineID: c.ID, NewAmountMinor: 2000, EffectiveFrom: effectiveFrom,
				})
				if !errors.Is(err, domain.ErrForbidden) {
					t.Fatalf("reprice of a one-off, out-of-scope line = %v, want domain.ErrForbidden "+
						"(if this is domain.ErrConflict, authorization moved back below the one-off check)", err)
				}
			})

			t.Run("bad-date", func(t *testing.T) {
				f := newCostScopeFixture(t, e)
				validFrom := domain.FormatDate(f.s.Now())
				c, err := domain.NewCost(NewID(), domain.CostSpec{
					Kind: "acquisition", Period: domain.CostMonthly, AmountMinor: 1000,
					ValidFrom: &validFrom,
				}, f.s.Now())
				if err != nil {
					t.Fatalf("building the cost: %v", err)
				}
				if err := f.s.AddAssetCost(f.ctx, testPermit, f.a2, c); err != nil {
					t.Fatalf("seeding the cost line: %v", err)
				}
				// Equal to valid_from, not strictly after: the exact condition
				// the date check exists to catch, and its message echoes
				// before.ValidFrom back verbatim.
				_, err = f.s.RepriceAssetCost(f.ctx, f.permit, f.a2, RepriceSpec{
					LineID: c.ID, NewAmountMinor: 2000, EffectiveFrom: validFrom,
				})
				if !errors.Is(err, domain.ErrForbidden) {
					t.Fatalf("reprice with a foreign line's own start date echoed back = %v, "+
						"want domain.ErrForbidden (if this is a *domain.ValidationError, "+
						"authorization moved back below the date check)", err)
				}
			})
		})
	}
}
