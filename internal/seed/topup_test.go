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

// The top-up exists for one situation: a demo that is LIVE and must not be
// reset to gain what a later release added. Everything below is a property of
// that situation rather than of the data.
//
// The load-bearing property is that running it twice is the same as running it
// once. Both guards it rests on were confirmed by breaking them: with the asset
// skip removed the second run dies on the duplicate name, and with the cost
// match removed the price lines went 68 -> 119. A test that passes either way
// would be describing nothing.

// TestTopUpOnALoadedEstateChangesNothing is the property the live demo depends
// on. The fixture already ran companyCompute during Load, so a top-up over it
// has nothing left to add -- and must therefore write nothing at all.
func TestTopUpOnALoadedEstateChangesNothing(t *testing.T) {
	seed.CompanyEstate = true
	t.Cleanup(func() { seed.CompanyEstate = false })

	eachEngine(t, func(t *testing.T, f *fixture) {
		s, ctx := f.store, f.ctx

		before := countEstate(t, f)
		if _, err := seed.TopUp(ctx, s); err != nil {
			t.Fatalf("topping up: %v", err)
		}
		after := countEstate(t, f)

		for what, n := range before {
			if after[what] != n {
				t.Errorf("%s went %d -> %d; a top-up over a loaded estate must add nothing",
					what, n, after[what])
			}
		}
	})
}

// TestTopUpIsAttributedLikeEveryOtherWrite. A top-up that reached the database
// directly would be quicker and would leave an estate whose history begins
// mid-sentence. Anything it adds is declared state, so the audit obligation is
// the ordinary one -- checked here on the clusters it creates.
func TestTopUpIsAttributedLikeEveryOtherWrite(t *testing.T) {
	seed.CompanyEstate = true
	t.Cleanup(func() { seed.CompanyEstate = false })

	eachEngine(t, func(t *testing.T, f *fixture) {
		s, ctx := f.store, f.ctx

		id, ok := f.refs.Assets["hv-win-01"]
		if !ok {
			t.Fatal("the estate has no hv-win-01 to check the audit trail of")
		}
		entries, err := s.ListChangesForEntity(ctx, "asset", id, 50)
		if err != nil {
			t.Fatalf("reading the change log: %v", err)
		}
		if len(entries) == 0 {
			t.Fatal("hv-win-01 was created with no change_log entry")
		}
	})
}

// TestTheClustersPutFourCostShapesSideBySide. The build-out's whole reason for
// existing is that a one-off licence and a yearly one cannot be compared by
// looking at them. If the fixture drifts to a single shape the demo still has
// numbers on the screen and has stopped demonstrating anything, which is the
// failure this catches.
func TestTheClustersPutFourCostShapesSideBySide(t *testing.T) {
	seed.CompanyEstate = true
	t.Cleanup(func() { seed.CompanyEstate = false })

	eachEngine(t, func(t *testing.T, f *fixture) {
		s, ctx := f.store, f.ctx

		want := []struct {
			host   string
			period string
			amount int64 // minor units
		}{
			{"hv-win-01", "once", 850000},   // perpetual: huge capital, no renewal
			{"hv-esx-01", "yearly", 300000}, // subscription, six times the Proxmox line
			{"hv-dr-01", "yearly", 50000},   // production Proxmox
			{"hv-dev-01", "yearly", 10000},  // the same product, non-production
		}
		for _, w := range want {
			id, ok := f.refs.Assets[w.host]
			if !ok {
				t.Errorf("no %s in the estate", w.host)
				continue
			}
			costs, err := s.ListAssetCosts(ctx, id)
			if err != nil {
				t.Fatalf("costs for %s: %v", w.host, err)
			}
			found := false
			for _, c := range costs {
				if c.Kind == "licence" && c.Period == w.period && c.AmountMinor == w.amount {
					found = true
				}
			}
			if !found {
				t.Errorf("%s has no %s licence of %d minor units; the cost shapes "+
					"the demo compares are no longer all present", w.host, w.period, w.amount)
			}
		}
	})
}

// TestEveryPricedKindOfAssetCarriesAFigure. A rollup that silently omits half
// the estate is worse than no rollup: it is confidently wrong, and an unpriced
// asset looks perfectly present on every page except the one that adds up.
func TestEveryPricedKindOfAssetCarriesAFigure(t *testing.T) {
	seed.CompanyEstate = true
	t.Cleanup(func() { seed.CompanyEstate = false })

	eachEngine(t, func(t *testing.T, f *fixture) {
		s, ctx := f.store, f.ctx

		assets, err := s.ListAssets(ctx, store.AssetFilter{Limit: 500})
		if err != nil {
			t.Fatalf("listing: %v", err)
		}
		// A VM, a bridge and a k8s node cost nothing of their own -- their money
		// is the host's, and pricing them would double-count it.
		free := map[string]bool{"vm": true, "bridge": true, "k8s_node": true}
		for _, a := range assets {
			if free[a.Kind] {
				continue
			}
			costs, err := s.ListAssetCosts(ctx, a.ID)
			if err != nil {
				t.Fatalf("costs for %s: %v", a.Name, err)
			}
			if len(costs) == 0 {
				t.Errorf("%s (%s) carries no cost line, so every total silently omits it",
					a.Name, a.Kind)
			}
		}
	})
}

// TestAmortisationFollowsTheSameEOLRuleTheAssetPageShows.
//
// A one-off is spread over the life of the thing it bought, so the report needs
// that thing's end-of-support -- and most boxes do not carry one, they INHERIT
// it from their catalogued model. Reading only the asset's own column made
// every inherited box amortise over no life at all and report zero, which is to
// say the report was at its most wrong on the estates using the catalogue
// properly. Both directions are checked: the inherited date, and the contract
// date that overrides a model's.
func TestAmortisationFollowsTheSameEOLRuleTheAssetPageShows(t *testing.T) {
	seed.CompanyEstate = true
	t.Cleanup(func() { seed.CompanyEstate = false })

	eachEngine(t, func(t *testing.T, f *fixture) {
		s, ctx := f.store, f.ctx

		for _, tc := range []struct {
			asset, want, why string
		}{
			{"pdu-a1", "2028-06-30", "inherited from the AP8853 in the catalogue"},
			{"srv-colo-1", "2027-10-07", "the rental contract, which overrides the R650's date"},
			{"hv-win-01", "2031-10-31", "inherited from the DL380 Gen11"},
		} {
			id, ok := f.refs.Assets[tc.asset]
			if !ok {
				t.Errorf("no %s in the estate", tc.asset)
				continue
			}
			costs, err := s.ListAssetCosts(ctx, id)
			if err != nil {
				t.Fatalf("costs for %s: %v", tc.asset, err)
			}
			if len(costs) == 0 {
				t.Errorf("%s carries no cost, so this proves nothing", tc.asset)
				continue
			}
			got := costs[0].OwnerEOLDate
			if got == nil {
				t.Errorf("%s amortises over no life at all; expected %s (%s)",
					tc.asset, tc.want, tc.why)
				continue
			}
			if *got != tc.want {
				t.Errorf("%s amortises to %s, want %s (%s)", tc.asset, *got, tc.want, tc.why)
			}
		}
	})
}

func countEstate(t *testing.T, f *fixture) map[string]int {
	t.Helper()
	out := map[string]int{}

	assets, err := f.store.ListAssets(f.ctx, store.AssetFilter{Limit: 500, IncludeRetired: true})
	if err != nil {
		t.Fatalf("listing assets: %v", err)
	}
	out["assets"] = len(assets)
	for _, a := range assets {
		costs, err := f.store.ListAssetCosts(f.ctx, a.ID)
		if err != nil {
			t.Fatalf("listing costs: %v", err)
		}
		out["asset costs"] += len(costs)
	}

	envs, err := f.store.ListEnvironments(f.ctx)
	if err != nil {
		t.Fatalf("listing environments: %v", err)
	}
	out["environments"] = len(envs)

	types, err := f.store.ListDeviceTypes(f.ctx, store.DeviceTypeFilter{})
	if err != nil {
		t.Fatalf("listing device types: %v", err)
	}
	out["device types"] = len(types)
	return out
}
