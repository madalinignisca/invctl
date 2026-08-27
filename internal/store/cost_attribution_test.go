// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// costCluster builds a one-host cluster with a declared split and a monthly
// cost, and returns the cluster id.
func (f *projectFixture) costCluster(t *testing.T, splitCPU *int) string {
	t.Helper()
	f.sizeAsset(t, "hv-01", func(a *domain.Asset) {
		cores, mem := 32, 32768
		a.CPUCores, a.MemoryMB = &cores, &mem
	})
	one := 1
	id := mustCluster(t, f.s, f.ctx, "cl", domain.HANone, &one, f.assets["hv-01"])
	c, err := f.s.GetCluster(f.ctx, id)
	if err != nil {
		t.Fatalf("reading the cluster: %v", err)
	}
	c.CostSplitCPU = splitCPU
	if err := f.s.UpdateCluster(f.ctx, testPermit, c); err != nil {
		t.Fatalf("declaring the split: %v", err)
	}
	return id
}

// TestTheBlendUsesTheDeclaredSplit.
//
// THE ARITHMETIC A READER CHECKS BY HAND, which is the only kind worth
// producing: one host at 32 vCPU and 32 GB, a project holding 8 vCPU (25%) and
// 16 GB (50%), a declared 60/40 split. 0.6 x 25 + 0.4 x 50 = 35%, and 35% of
// EUR 1,000 is EUR 350.
//
// Both component shares are carried on the result so the blend can be checked
// rather than taken on trust -- the same rule the physical-fit findings follow.
func TestTheBlendUsesTheDeclaredSplit(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			split := 60
			id := f.costCluster(t, &split)
			if err := f.link(t, "platform", "hv-01", domain.ProjectOwns); err != nil {
				t.Fatalf("linking: %v", err)
			}
			f.allocate(t, "vm-app-1", 8, 16384)
			f.priceAsset(t, "hv-01", "operating", domain.CostMonthly, 100000)

			c, err := f.s.CostAttributionFor(f.ctx, id, domain.FormatDate(costNow))
			if err != nil {
				t.Fatalf("attributing cost: %v", err)
			}
			if c.RunRateMinor != 100000 {
				t.Fatalf("the pool is %d, want 100000", c.RunRateMinor)
			}
			var platform *CostShare
			for i := range c.Shares {
				if c.Shares[i].Subject == "platform" {
					platform = &c.Shares[i]
				}
			}
			if platform == nil {
				t.Fatal("platform holds the cluster and got no share of its cost")
			}
			if platform.CPUPoints != 2500 || platform.MemoryPoints != 5000 {
				t.Fatalf("the component shares are %d/%d, want 2500 CPU and 5000 memory",
					platform.CPUPoints, platform.MemoryPoints)
			}
			// 0.6 x 2500 + 0.4 x 5000 = 3500.
			if platform.BasisPoints != 3500 {
				t.Errorf("the blend is %d basis points, want 3500 -- 60%% of the CPU "+
					"share and 40%% of the memory share", platform.BasisPoints)
			}
			if platform.RunRateMinor != 35000 {
				t.Errorf("platform owes %d of the run rate, want 35000", platform.RunRateMinor)
			}
		})
	}
}

// TestEveryCentOfTheCLusterLandsSomewhere.
//
// §5.3, in money rather than percentages: "a report whose slices do not sum to
// the invoice is worse than no report, because somebody will put it in a board
// pack." Idle capacity is a slice, so headroom somebody is paying for is
// visible rather than quietly absorbed by whoever happens to be biggest.
func TestEveryCentOfTheClusterLandsSomewhere(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			split := 50
			id := f.costCluster(t, &split)
			if err := f.link(t, "platform", "hv-01", domain.ProjectOwns); err != nil {
				t.Fatalf("linking: %v", err)
			}
			f.allocate(t, "vm-app-1", 7, 3333)
			// An awkward figure on purpose: 99999 cents does not divide evenly
			// by anything here.
			f.priceAsset(t, "hv-01", "operating", domain.CostMonthly, 99999)

			c, err := f.s.CostAttributionFor(f.ctx, id, domain.FormatDate(costNow))
			if err != nil {
				t.Fatalf("attributing cost: %v", err)
			}
			var total int64
			for _, sh := range c.Shares {
				total += sh.RunRateMinor
			}
			total += c.Idle.RunRateMinor
			if total != 99999 {
				t.Errorf("the slices sum to %d, want exactly the 99999 that was paid", total)
			}
			if c.Idle.RunRateMinor == 0 {
				t.Error("a cluster 80%% empty shows nobody paying for the headroom")
			}
		})
	}
}

// TestWithoutADeclaredSplitNoMoneyIsDivided.
//
// Unlike the overcommit ratio, which defaults to a pessimistic 1:1 because
// there is a safe direction to be wrong in, half and half is not cautious --
// it is arbitrary. So the report says what is missing and divides nothing,
// while the CAPACITY shares carry on: "who holds 12% of this cluster" needs no
// money at all.
func TestWithoutADeclaredSplitNoMoneyIsDivided(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			id := f.costCluster(t, nil)
			if err := f.link(t, "platform", "hv-01", domain.ProjectOwns); err != nil {
				t.Fatalf("linking: %v", err)
			}
			f.allocate(t, "vm-app-1", 8, 16384)
			f.priceAsset(t, "hv-01", "operating", domain.CostMonthly, 100000)

			c, err := f.s.CostAttributionFor(f.ctx, id, domain.FormatDate(costNow))
			if err != nil {
				t.Fatalf("attributing cost: %v", err)
			}
			if c.Divisible() {
				t.Fatal("a cluster with no declared split reports itself divisible")
			}
			for _, sh := range c.Shares {
				if sh.RunRateMinor != 0 {
					t.Errorf("%s was charged %d with no declared split",
						sh.Subject, sh.RunRateMinor)
				}
			}
			if len(c.Gaps) == 0 {
				t.Error("nothing was divided and the report does not say why")
			}
			if !strings.Contains(strings.Join(c.Gaps, " "), "CPU") {
				t.Errorf("the gap does not name what is missing: %v", c.Gaps)
			}
			// And the capacity shares are unaffected.
			a, err := f.s.AttributionFor(f.ctx, id)
			if err != nil {
				t.Fatalf("attributing capacity: %v", err)
			}
			if len(divisionFor(t, a, "CPU").Shares) == 0 {
				t.Error("an undeclared cost split also silenced the capacity shares")
			}
		})
	}
}

// TestAConditionalLicenceReachesOnlyTheGuestsItCovers.
//
// §5.6, and the failure it prevents: divide a per-core operating-system licence
// evenly and every workload running something else silently subsidises the ones
// that do. The total stays right, so nothing prompts a reader to look.
func TestAConditionalLicenceReachesOnlyTheGuestsItCovers(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			split := 50
			id := f.costCluster(t, &split)
			// platform owns the host and therefore vm-app-1 inside it; orders
			// owns a second guest that the licence does NOT cover.
			if err := f.link(t, "platform", "hv-01", domain.ProjectOwns); err != nil {
				t.Fatalf("linking the host: %v", err)
			}
			hv := f.assets["hv-01"]
			f.assets["vm-other"] = mustAsset(t, f.s, f.ctx, domain.KindVM, "vm-other", &hv, f.env)
			if err := f.link(t, "orders", "vm-other", domain.ProjectOwns); err != nil {
				t.Fatalf("linking the guest: %v", err)
			}
			f.allocate(t, "vm-app-1", 8, 8192)
			f.allocate(t, "vm-other", 8, 8192)

			costID := f.priceAsset(t, "hv-01", "licence", domain.CostMonthly, 60000)
			line, err := f.s.GetAssetCost(f.ctx, costID)
			if err != nil {
				t.Fatalf("reading the line: %v", err)
			}
			c := line.Cost
			c.AppliesTo = domain.CostConditional
			if err := f.s.UpdateAssetCost(f.ctx, testPermit, &c); err != nil {
				t.Fatalf("scoping the licence: %v", err)
			}
			// It covers only platform's guest.
			if err := f.s.SetCostConsumers(f.ctx, testPermit, costID,
				[]string{f.assets["vm-app-1"]}); err != nil {
				t.Fatalf("naming the consumers: %v", err)
			}

			got, err := f.s.CostAttributionFor(f.ctx, id, domain.FormatDate(costNow))
			if err != nil {
				t.Fatalf("attributing: %v", err)
			}
			byName := map[string]CostShare{}
			for _, sh := range got.Shares {
				byName[sh.Subject] = sh
			}
			if byName["platform"].DirectMinor != 60000 {
				t.Errorf("platform carries %d of the licence, want all 60000",
					byName["platform"].DirectMinor)
			}
			if byName["orders"].DirectMinor != 0 {
				t.Errorf("orders was charged %d for a licence its workload does not "+
					"run -- which is the subsidy §5.6 exists to prevent",
					byName["orders"].DirectMinor)
			}
			// And it never entered the divided pool.
			if got.RunRateMinor != 0 {
				t.Errorf("the licence also went into the shared pool (%d), so it "+
					"has been charged twice", got.RunRateMinor)
			}
		})
	}
}

// TestAScopedLineNamingNobodyIsReportedNotSpread. The fallback would be a
// default wearing a declaration's clothes.
func TestAScopedLineNamingNobodyIsReportedNotSpread(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			split := 50
			id := f.costCluster(t, &split)
			if err := f.link(t, "platform", "hv-01", domain.ProjectOwns); err != nil {
				t.Fatalf("linking: %v", err)
			}
			f.allocate(t, "vm-app-1", 8, 8192)

			costID := f.priceAsset(t, "hv-01", "licence", domain.CostMonthly, 50000)
			line, _ := f.s.GetAssetCost(f.ctx, costID)
			c := line.Cost
			c.AppliesTo = domain.CostConditional
			if err := f.s.UpdateAssetCost(f.ctx, testPermit, &c); err != nil {
				t.Fatalf("scoping: %v", err)
			}
			// Deliberately naming nobody.

			got, err := f.s.CostAttributionFor(f.ctx, id, domain.FormatDate(costNow))
			if err != nil {
				t.Fatalf("attributing: %v", err)
			}
			if got.UnattributableMinor != 50000 {
				t.Errorf("unattributable is %d, want the whole 50000", got.UnattributableMinor)
			}
			if got.RunRateMinor != 0 {
				t.Error("a conditional line naming nobody was spread across everybody, " +
					"which is a default wearing a declaration's clothes")
			}
			if len(got.Gaps) == 0 {
				t.Error("the report does not say a line reached nobody")
			}
		})
	}
}
