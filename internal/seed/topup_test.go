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

	"github.com/madalinignisca/invctl/internal/domain"
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
		freeKinds := map[string]bool{"vm": true, "bridge": true, "k8s_node": true}

		// AND THREE NAMED SITES, because kind alone cannot decide this. dc-oslo
		// carries transit and colo-rack-07 carries rack rental, so "a site costs
		// money" is true of the ones you rent space in. A PROVIDER REGION is not
		// that: it is a failure-domain label, you pay per machine, and putting a
		// figure on it would invent a bill nobody receives. Named individually
		// rather than exempting sites wholesale, so dropping dc-oslo's
		// connectivity line still fails this test.
		freeAssets := map[string]string{
			"hz-hel1":  "a Hetzner region: the machines are billed, the region is not",
			"scw-par1": "a Scaleway region: same",
			"ovh-gra":  "an OVHcloud region: same",
		}

		for _, a := range assets {
			if freeKinds[a.Kind] {
				continue
			}
			if _, ok := freeAssets[a.Name]; ok {
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
//
// THE OVERRIDE CASE IS ASSERTED AS A RELATIONSHIP, NOT A DATE, and the first
// version of it was wrong for a reason worth keeping written down. The colo
// machines are rented, so the fixture dates their contract RELATIVE TO NOW --
// b.now.AddDate(1, 2, 0) -- while the catalogue dates are absolute. Writing the
// resulting day down as a literal made the test pass on the afternoon it was
// written and fail the next morning, having caught nothing in between. An
// expectation computed from a moving fixture has to move with it or stop
// mentioning it; this one stops mentioning it and asserts the thing that
// actually matters instead.
func TestAmortisationFollowsTheSameEOLRuleTheAssetPageShows(t *testing.T) {
	seed.CompanyEstate = true
	t.Cleanup(func() { seed.CompanyEstate = false })

	eachEngine(t, func(t *testing.T, f *fixture) {
		s, ctx := f.store, f.ctx

		// amortisedTo is the date the report spreads a one-off over.
		amortisedTo := func(t *testing.T, name string) *string {
			t.Helper()
			id, ok := f.refs.Assets[name]
			if !ok {
				t.Fatalf("no %s in the estate", name)
			}
			costs, err := s.ListAssetCosts(ctx, id)
			if err != nil {
				t.Fatalf("costs for %s: %v", name, err)
			}
			if len(costs) == 0 {
				t.Fatalf("%s carries no cost, so this proves nothing", name)
			}
			return costs[0].OwnerEOLDate
		}

		// Inherited: the asset has no date of its own and the catalogue's is a
		// fixed published one, so a literal here is stable and says what it
		// means.
		t.Run("inherited from the catalogue", func(t *testing.T) {
			for _, tc := range []struct{ asset, want, why string }{
				{"pdu-a1", "2028-06-30", "the AP8853 in the catalogue"},
				{"hv-win-01", "2031-10-31", "the DL380 Gen11"},
			} {
				got := amortisedTo(t, tc.asset)
				if got == nil {
					t.Errorf("%s amortises over no life at all; expected %s from %s",
						tc.asset, tc.want, tc.why)
					continue
				}
				if *got != tc.want {
					t.Errorf("%s amortises to %s, want %s from %s",
						tc.asset, *got, tc.want, tc.why)
				}
			}
		})

		// Overridden: the rented machine carries its own contract date, which
		// must beat the R650's published one.
		t.Run("the asset's own date beats the model's", func(t *testing.T) {
			asset, err := s.GetAsset(ctx, f.refs.Assets["srv-colo-1"])
			if err != nil {
				t.Fatalf("reading srv-colo-1: %v", err)
			}
			// Both halves have to exist or "overrides" is a claim about
			// nothing -- an asset with no date of its own, or a model with
			// none, would satisfy a naive equality check for the wrong reason.
			if asset.EOLDate == nil {
				t.Fatal("srv-colo-1 carries no contract date, so there is no override to check")
			}
			if asset.DeviceTypeEOL == nil {
				t.Fatal("srv-colo-1 has no catalogued model date, so nothing is being overridden")
			}
			if *asset.EOLDate == *asset.DeviceTypeEOL {
				t.Fatalf("srv-colo-1's contract date and the R650's are both %s, so this "+
					"test cannot tell which one the report used", *asset.EOLDate)
			}

			got := amortisedTo(t, "srv-colo-1")
			if got == nil {
				t.Fatal("srv-colo-1 amortises over no life at all")
			}
			if *got != *asset.EOLDate {
				t.Errorf("srv-colo-1 amortises to %s; want its contract date %s, not the "+
					"R650's %s", *got, *asset.EOLDate, *asset.DeviceTypeEOL)
			}
		})
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

// TestATopUpDoesNotResurrectWhatItRetired.
//
// THE BUG THIS EXISTS FOR SHIPPED, briefly, and was invisible until a phase
// retired something. hydrate() read assets with the default filter, which
// excludes retired rows, so a second run could not see the hosts the first had
// withdrawn -- and b.asset, finding no such name in its refs, recreated them.
// The partial unique index permits it, because a name is unique among LIVE rows
// only. Nothing objected: three retired hypervisors came back with fresh cost
// lines, every run.
//
// Counting is the only way this surfaces, which is why the assertion is on the
// totals rather than on any one row.
func TestATopUpDoesNotResurrectWhatItRetired(t *testing.T) {
	seed.CompanyEstate = true
	t.Cleanup(func() { seed.CompanyEstate = false })

	eachEngine(t, func(t *testing.T, f *fixture) {
		s, ctx := f.store, f.ctx

		// The company layer retires the on-prem development hosts when it moves
		// them to rented metal. If it has not, this test proves nothing.
		retired := 0
		assets, err := s.ListAssets(ctx, store.AssetFilter{Limit: 500, IncludeRetired: true})
		if err != nil {
			t.Fatalf("listing: %v", err)
		}
		for _, a := range assets {
			if a.Lifecycle == domain.LifecycleRetired {
				retired++
			}
		}
		if retired == 0 {
			t.Fatal("the estate contains nothing retired, so a resurrection could not " +
				"be detected by this test")
		}

		before := countEstate(t, f)
		if _, err := seed.TopUp(ctx, s); err != nil {
			t.Fatalf("topping up: %v", err)
		}
		after := countEstate(t, f)

		if after["assets"] != before["assets"] {
			t.Errorf("assets went %d -> %d across a top-up. A retired row the "+
				"hydration cannot see is a name the seed recreates, and the partial "+
				"unique index allows it because retired rows do not hold their names",
				before["assets"], after["assets"])
		}
		if after["asset costs"] != before["asset costs"] {
			t.Errorf("asset costs went %d -> %d; resurrected assets get fresh price lines",
				before["asset costs"], after["asset costs"])
		}
	})
}

// TestATopUpCanResolveEveryRefItsPhasesRead.
//
// WRITTEN AFTER A DEPLOY FOUND IT. b.refs.Projects starts empty on a top-up and
// hydrate did not fill it, so every phase resolving a project id -- what an
// engagement was priced for (WP-J7), who shares a machine (WP-J5) -- looked up
// nothing and returned without writing or failing. Both are deliberately
// written to skip an estate that does not contain what they name, which is
// right for a partial deployment and indistinguishable from this.
//
// So the assertion is on the OUTCOME rather than on the map: a top-up must
// leave the estate saying the same things a full seed does. Checking that
// hydrate populates one more map would pass the day somebody adds a phase
// reading a different one.
func TestATopUpCanResolveEveryRefItsPhasesRead(t *testing.T) {
	seed.CompanyEstate = true
	t.Cleanup(func() { seed.CompanyEstate = false })

	eachEngine(t, func(t *testing.T, f *fixture) {
		// AN ESTATE THAT PREDATES THE PHASES, which is the only state that
		// reproduces this. Topping up an already-complete fixture proves
		// nothing: the declarations are already there, so a top-up that
		// resolved no project at all still passes. Clearing them first is what
		// makes the top-up do the work a real upgrade asks of it.
		projectsBefore, err := f.store.ListProjects(f.ctx, store.ProjectFilter{})
		if err != nil {
			t.Fatalf("listing projects: %v", err)
		}
		for _, row := range projectsBefore {
			p := row.Project
			p.PricedForVCPU, p.PricedForMemoryMB = nil, nil
			if err := f.store.UpdateProject(f.ctx, domain.AdministratorPermit(seed.Actor), &p); err != nil {
				t.Fatalf("clearing priced-for: %v", err)
			}
		}
		assetsBefore, err := f.store.ListAssets(f.ctx, store.AssetFilter{})
		if err != nil {
			t.Fatalf("listing assets: %v", err)
		}
		for _, a := range assetsBefore {
			if err := f.store.SetOccupants(f.ctx, domain.AdministratorPermit(seed.Actor), a.ID, nil); err != nil {
				t.Fatalf("clearing occupancy: %v", err)
			}
		}

		if _, err := seed.TopUp(f.ctx, f.store); err != nil {
			t.Fatalf("topping up: %v", err)
		}

		projects, err := f.store.ListProjects(f.ctx, store.ProjectFilter{})
		if err != nil {
			t.Fatalf("listing projects: %v", err)
		}
		priced := 0
		for _, p := range projects {
			if p.PricedForVCPU != nil {
				priced++
			}
		}
		if priced == 0 {
			t.Error("after a top-up no project records what it was priced for, " +
				"so a phase that resolves a project id silently did nothing")
		}

		shared := 0
		assets, err := f.store.ListAssets(f.ctx, store.AssetFilter{})
		if err != nil {
			t.Fatalf("listing assets: %v", err)
		}
		for _, a := range assets {
			occ, err := f.store.OccupancyFor(f.ctx, a.ID)
			if err != nil {
				t.Fatalf("reading occupancy: %v", err)
			}
			if occ.Shared() {
				shared++
			}
		}
		if shared == 0 {
			t.Error("after a top-up no machine is declared as shared, so the " +
				"occupancy phase silently did nothing")
		}
	})
}

// TestTheCapacityPhaseFindsItsClusterAfterARename.
//
// A DEPLOYMENT OUTLIVES THE FIXTURE'S NAMES. The public demo was seeded when
// this fixture's production cluster was called `prod-pve`; it is called
// `prod-virt` today, and the top-up never creates clusters — a host belongs to
// at most one, so `prod-virt` could only be added by taking hv-01 away from
// whatever already holds it. The old name is therefore permanent on that estate.
//
// Matching on the anchor host asks the question that actually matters: not "is
// there a cluster called prod-virt" but "which cluster are the production
// hypervisors in". The symptom this fixes is a silent one — the demo ran a
// successful top-up and declared no capacity at all, and nothing said so.
func TestTheCapacityPhaseFindsItsClusterAfterARename(t *testing.T) {
	seed.CompanyEstate = true
	t.Cleanup(func() { seed.CompanyEstate = false })

	eachEngine(t, func(t *testing.T, f *fixture) {
		clusters, err := f.store.ListClusters(f.ctx)
		if err != nil {
			t.Fatalf("listing clusters: %v", err)
		}
		targetID := ""
		for _, row := range clusters {
			if row.Name == "prod-virt" {
				targetID = row.ID
			}
		}
		if targetID == "" {
			t.Fatal("the fixture no longer builds prod-virt; update this test")
		}
		target, err := f.store.GetCluster(f.ctx, targetID)
		if err != nil {
			t.Fatalf("reading the cluster: %v", err)
		}

		// Rename it the way a long-lived deployment has it, and undeclare the
		// capacity so the top-up has work to do.
		renamed := *target
		renamed.Name = "prod-pve"
		renamed.CPUOvercommit, renamed.CostSplitCPU = nil, nil
		if err := f.store.UpdateCluster(f.ctx, domain.AdministratorPermit(seed.Actor), &renamed); err != nil {
			t.Fatalf("renaming: %v", err)
		}

		if _, err := seed.TopUp(f.ctx, f.store); err != nil {
			t.Fatalf("topping up: %v", err)
		}

		after, err := f.store.GetCluster(f.ctx, targetID)
		if err != nil {
			t.Fatalf("re-reading: %v", err)
		}
		if after.CPUOvercommit == nil {
			t.Error("the capacity phase found no cluster to declare on after a " +
				"rename, so a long-lived estate stays undeclared and says nothing")
		}
		if after.CostSplitCPU == nil {
			t.Error("the cost split was not declared, so that cluster's money " +
				"is not divided at all")
		}
	})
}
