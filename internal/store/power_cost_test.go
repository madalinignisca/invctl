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

// TestAContainedDrawDoesNotAddToTheEstateTotal is §2.2.
//
// THE ARRANGEMENT IS THE TEST. power_input.asset_id is REFERENCES asset(id)
// with no kind restriction (00023_power.sql), and neither NewPowerInput nor
// the handler nor the asset-detail form limits which kinds may carry one -- so
// a VM declaring its own draw is not a hypothetical, it is what the current UI
// permits. A hypervisor at 900 with a VM inside it at 100 is 1,000 by any
// naive query, even though the VM's power is virtual and already inside the
// host's wall draw.
func TestAContainedDrawDoesNotAddToTheEstateTotal(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			panel := mustPanel(t, s, ctx, site, "panel-1")
			feed := mustFeed(t, s, ctx, panel, "F1", 230, 32)

			// The host declares its wall draw.
			host := mustAsset(t, s, ctx, domain.KindHypervisor, "hv-01", &site)
			mustInput(t, s, ctx, host, feed, "A", intPtr(900))

			// The guest declares one too -- parented to the host, so
			// CreateAsset writes the asset_closure rows the exclusion reads.
			guest := mustAsset(t, s, ctx, domain.KindVM, "vm-01", &host)
			mustInput(t, s, ctx, guest, feed, "A", intPtr(100))

			draw, err := s.DeclaredPowerDraw(ctx)
			if err != nil {
				t.Fatalf("summing declared draw: %v", err)
			}
			if draw.TotalVA != 900 {
				t.Errorf("TotalVA = %d, want 900 -- the VM's 100 VA is already inside "+
					"the hypervisor's wall draw", draw.TotalVA)
			}
			if draw.Declaring != 1 {
				t.Errorf("Declaring = %d, want 1 -- only the host contributed", draw.Declaring)
			}
		})
	}
}

// TestADrawingAssetIsNotItsOwnDrawingAncestor is the depth-0 guard. The
// closure table carries a self-row for every asset (assets.go's
// insertClosureForNewNode), so an exclusion written without `depth > 0`
// excludes EVERY drawing asset and reports an estate that draws nothing --
// a failure that looks like an empty estate rather than a broken query.
func TestADrawingAssetIsNotItsOwnDrawingAncestor(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			panel := mustPanel(t, s, ctx, site, "panel-1")
			feed := mustFeed(t, s, ctx, panel, "F1", 230, 32)
			host := mustAsset(t, s, ctx, domain.KindServer, "srv-1", &site)
			mustInput(t, s, ctx, host, feed, "A", intPtr(450))

			draw, err := s.DeclaredPowerDraw(ctx)
			if err != nil {
				t.Fatalf("summing declared draw: %v", err)
			}
			if draw.TotalVA != 450 {
				t.Fatalf("TotalVA = %d, want 450 -- an asset excluded by its own "+
					"closure self-row reports an estate that draws nothing", draw.TotalVA)
			}
		})
	}
}

// TestTwoInputsOnOneAssetCountOnce is §2.1 at store level -- the seed-fixture
// regression in internal/seed is the primary one, this is the dual-engine half.
func TestTwoInputsOnOneAssetCountOnce(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			pa := mustPanel(t, s, ctx, site, "DB-A")
			pb := mustPanel(t, s, ctx, site, "DB-B")
			fa := mustFeed(t, s, ctx, pa, "A1", 230, 32)
			fb := mustFeed(t, s, ctx, pb, "B1", 230, 32)

			// Properly 2N: the whole load recorded on each side, because that
			// is what feed sizing needs.
			srv := mustAsset(t, s, ctx, domain.KindServer, "srv-1", &site)
			mustInput(t, s, ctx, srv, fa, "A", intPtr(900))
			mustInput(t, s, ctx, srv, fb, "B", intPtr(900))

			draw, err := s.DeclaredPowerDraw(ctx)
			if err != nil {
				t.Fatalf("summing declared draw: %v", err)
			}
			if draw.TotalVA == 1800 {
				t.Fatalf("TotalVA = 1800: the query is summing a dual-fed asset's inputs, " +
					"which doubles every properly-redundant server in the estate")
			}
			if draw.TotalVA != 900 {
				t.Errorf("TotalVA = %d, want 900", draw.TotalVA)
			}
		})
	}
}

// TestAnUnknownDrawIsExcludedFromTheFigureAndCountedInCoverage is D3.
// "Not recorded" must stay distinguishable from zero -- the same rule
// Rating's nullable fields and EstateCostSurface.Unpriced already keep.
func TestAnUnknownDrawIsExcludedFromTheFigureAndCountedInCoverage(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			panel := mustPanel(t, s, ctx, site, "panel-1")
			feed := mustFeed(t, s, ctx, panel, "F1", 230, 32)

			declared := mustAsset(t, s, ctx, domain.KindServer, "srv-1", &site)
			mustInput(t, s, ctx, declared, feed, "A", intPtr(450))

			// An input, but nobody typed a figure into it.
			silent := mustAsset(t, s, ctx, domain.KindServer, "srv-2", &site)
			mustInput(t, s, ctx, silent, feed, "A", nil)

			// No input at all -- also a gap, but it is not the one this
			// figure counts: D3 refuses a denominator of "every live asset",
			// so an asset with no input at all is simply absent from both
			// counts, not tallied as a third kind of gap.
			mustAsset(t, s, ctx, domain.KindServer, "srv-3", &site)

			draw, err := s.DeclaredPowerDraw(ctx)
			if err != nil {
				t.Fatalf("summing declared draw: %v", err)
			}
			if draw.TotalVA != 450 {
				t.Errorf("TotalVA = %d, want 450 -- an unknown draw must not be read "+
					"as a zero one", draw.TotalVA)
			}
			if draw.Declaring != 1 {
				t.Errorf("Declaring = %d, want 1", draw.Declaring)
			}
			if draw.UndeclaredDraw != 1 {
				t.Errorf("UndeclaredDraw = %d, want 1 -- exactly the one live input "+
					"that recorded no draw_va", draw.UndeclaredDraw)
			}
		})
	}
}

// TestLifecycleGatesTheAssetAndTheInputAndNothingElse is §4.4, and the second
// half is the one worth having: a retired FEED under a running server is a
// data inconsistency, not a reason to believe the server stopped drawing
// power. PowerFindings filters all four because a finding is about the supply
// path; this follows AssetsLosingPower instead, which filters exactly two.
//
// RetirePowerFeed refuses while the feed still carries a live input
// (internal/store/power.go), so the feed under test is retired via
// UpdatePowerFeed directly rather than weakening the arrangement.
func TestLifecycleGatesTheAssetAndTheInputAndNothingElse(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			panel := mustPanel(t, s, ctx, site, "panel-1")
			live := mustFeed(t, s, ctx, panel, "F1", 230, 32)
			doomed := mustFeed(t, s, ctx, panel, "F2", 230, 32)

			keeps := mustAsset(t, s, ctx, domain.KindServer, "srv-1", &site)
			mustInput(t, s, ctx, keeps, doomed, "A", intPtr(450))

			retiredInput := mustAsset(t, s, ctx, domain.KindServer, "srv-2", &site)
			gone := mustInput(t, s, ctx, retiredInput, live, "A", intPtr(700))

			// Retire the FEED the first server hangs off, directly -- it
			// still carries srv-1's live input, so RetirePowerFeed would
			// refuse. Its draw must stay regardless.
			doomedFeed, err := s.GetPowerFeed(ctx, doomed)
			if err != nil {
				t.Fatalf("reading the doomed feed: %v", err)
			}
			doomedFeed.Lifecycle = domain.LifecycleRetired
			if err := s.UpdatePowerFeed(ctx, testPermit, &doomedFeed.PowerFeed); err != nil {
				t.Fatalf("retiring the feed: %v", err)
			}
			// Retire the second server's INPUT. Its draw must go.
			if err := s.RetirePowerInput(ctx, testPermit, gone); err != nil {
				t.Fatalf("retiring the input: %v", err)
			}

			draw, err := s.DeclaredPowerDraw(ctx)
			if err != nil {
				t.Fatalf("summing declared draw: %v", err)
			}
			if draw.TotalVA != 450 {
				t.Errorf("TotalVA = %d, want 450 -- a retired input drops out, a retired "+
					"feed under a running server does not", draw.TotalVA)
			}
		})
	}
}

// TestThePowerFigureIsNotInTheEstateTotals is §2.4 where it cannot be reworded
// away. The page's layout is what stops a HUMAN adding the two figures; this
// is what stops the CODE doing it -- a later refactor that folds power into
// EstateCosts would make that total part-declared and part-derived with no way
// for a reader to tell which half they were quoting.
func TestThePowerFigureIsNotInTheEstateTotals(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			panel := mustPanel(t, s, ctx, site, "panel-1")
			feed := mustFeed(t, s, ctx, panel, "F1", 230, 32)
			srv := mustAsset(t, s, ctx, domain.KindServer, "srv-1", &site)

			before, err := s.EstateCosts(ctx, s.Now())
			if err != nil {
				t.Fatalf("estate costs before: %v", err)
			}

			mustInput(t, s, ctx, srv, feed, "A", intPtr(900))

			after, err := s.EstateCosts(ctx, s.Now())
			if err != nil {
				t.Fatalf("estate costs after: %v", err)
			}
			if before.Totals != after.Totals {
				t.Errorf("declaring a power draw moved the estate totals from %+v to %+v; "+
					"an estimate must never enter a total of what somebody priced",
					before.Totals, after.Totals)
			}

			draw, err := s.DeclaredPowerDraw(ctx)
			if err != nil {
				t.Fatalf("summing declared draw: %v", err)
			}
			if draw.TotalVA != 900 {
				t.Fatalf("TotalVA = %d, want 900 -- without this the comparison above "+
					"passes because nothing was declared at all", draw.TotalVA)
			}
		})
	}
}

// TestDeclaredPowerDrawCarriesUnmodelledSites is B3 / §4b.9. D3's amendment
// over-generalised its own objection and dropped this exact count from the
// cost figure; it is reused from PowerFindings' powerCoverage rather than
// reimplemented, so this test also pins that the two never disagree.
func TestDeclaredPowerDrawCarriesUnmodelledSites(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			// One site with a full power model.
			modelled := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			panel := mustPanel(t, s, ctx, modelled, "panel-1")
			feed := mustFeed(t, s, ctx, panel, "F1", 230, 32)
			srv := mustAsset(t, s, ctx, domain.KindServer, "srv-1", &modelled)
			mustInput(t, s, ctx, srv, feed, "A", intPtr(900))

			// Two sites with no power panel at all.
			mustAsset(t, s, ctx, domain.KindSite, "dc-b", nil)
			mustAsset(t, s, ctx, domain.KindSite, "dc-c", nil)

			draw, err := s.DeclaredPowerDraw(ctx)
			if err != nil {
				t.Fatalf("summing declared draw: %v", err)
			}
			if draw.UnmodelledSites != 2 {
				t.Errorf("UnmodelledSites = %d, want 2 -- one of three sites carries a power model",
					draw.UnmodelledSites)
			}

			report, err := s.PowerFindings(ctx)
			if err != nil {
				t.Fatalf("PowerFindings: %v", err)
			}
			if report.UnmodelledSites != draw.UnmodelledSites {
				t.Errorf("PowerFindings.UnmodelledSites = %d but DeclaredPowerDraw's is %d; "+
					"B3 requires the cost figure to REUSE powerCoverage's existing computation, "+
					"not run a second copy of it that could drift", report.UnmodelledSites, draw.UnmodelledSites)
			}
		})
	}
}
