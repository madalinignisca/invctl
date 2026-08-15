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
	"fmt"
	"testing"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
)

var costNow = time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

func (f *projectFixture) priceAsset(t *testing.T, name, kind, period string, minor int64) string {
	t.Helper()
	return f.price(t, func(c *domain.Cost) error {
		return f.s.AddAssetCost(f.ctx, testActor, f.assets[name], c)
	}, kind, period, minor)
}

func (f *projectFixture) priceService(t *testing.T, code, kind, period string, minor int64) string {
	t.Helper()
	return f.price(t, func(c *domain.Cost) error {
		return f.s.AddServiceCost(f.ctx, testActor, f.services[code], c)
	}, kind, period, minor)
}

func (f *projectFixture) priceProject(t *testing.T, code, kind, period string, minor int64) string {
	t.Helper()
	return f.price(t, func(c *domain.Cost) error {
		return f.s.AddProjectCost(f.ctx, testActor, f.projects[code], c)
	}, kind, period, minor)
}

// setAssetEOLOn dates an asset to a fixed day, for tests whose arithmetic is
// stated in absolute dates rather than offsets.
func (f *projectFixture) setAssetEOLOn(t *testing.T, name, date string) {
	t.Helper()
	row, err := f.s.GetAsset(f.ctx, f.assets[name])
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	a := row.Asset
	a.EOLDate = &date
	envIDs := make([]string, len(row.Environments))
	for i, env := range row.Environments {
		envIDs[i] = env.ID
	}
	if err := f.s.UpdateAsset(f.ctx, testActor, &a, envIDs); err != nil {
		t.Fatalf("dating %s: %v", name, err)
	}
}

// priceAssetFrom is priceAsset with an explicit acquisition date -- which for a
// one-off is what ValidFrom means.
func (f *projectFixture) priceAssetFrom(t *testing.T, name, kind, period string, minor int64, from string) string {
	t.Helper()
	c, err := domain.NewCost(NewID(), domain.CostSpec{
		Kind: kind, Period: period, AmountMinor: minor, ValidFrom: &from,
	}, f.s.Now())
	if err != nil {
		t.Fatalf("building the cost: %v", err)
	}
	if err := f.s.AddAssetCost(f.ctx, testActor, f.assets[name], c); err != nil {
		t.Fatalf("attaching the cost: %v", err)
	}
	return c.ID
}

func (f *projectFixture) price(t *testing.T, attach func(*domain.Cost) error,
	kind, period string, minor int64) string {
	t.Helper()
	c, err := domain.NewCost(NewID(), domain.CostSpec{
		Kind: kind, Period: period, AmountMinor: minor,
	}, f.s.Now())
	if err != nil {
		t.Fatalf("building the cost: %v", err)
	}
	if err := attach(c); err != nil {
		t.Fatalf("attaching the cost: %v", err)
	}
	return c.ID
}

func (f *projectFixture) costs(t *testing.T, code string) *ProjectCostSummary {
	t.Helper()
	footprint, err := f.s.ProjectFootprint(f.ctx, f.projects[code])
	if err != nil {
		t.Fatalf("deriving the footprint: %v", err)
	}
	summary, err := f.s.ProjectCosts(f.ctx, footprint, costNow)
	if err != nil {
		t.Fatalf("totalling: %v", err)
	}
	return summary
}

// TestProjectCostsCountAnAssetOnce is the assertion the whole design exists to
// make true.
//
// The reachable-twice case is specific and it took a mutation to find: an asset
// can be IMPLIED (inside something the project owns) and ALSO declared with a
// `uses` link, which is what a project looks like when somebody wrote the `uses`
// link before the ownership above it existed. Without a dedup the same VM lands
// in two buckets at once and the page reports more spend than the estate has.
//
// An earlier version of this test priced only the owned hypervisor and asserted
// it appeared once. It passed with the dedup deleted -- impliedAssets already
// excludes the project's own declared assets, so nothing was ever reachable
// twice and the test proved nothing. It is written down because a test that
// cannot fail is worse than no test: it reports coverage that is not there.
func TestProjectCostsCountAnAssetOnce(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)

			// Owning the host implies the guest...
			if err := f.link(t, "platform", "hv-01", domain.ProjectOwns); err != nil {
				t.Fatalf("owning hv-01: %v", err)
			}
			// ...and this says it out loud a second time, by another route.
			if err := f.link(t, "platform", "vm-app-1", domain.ProjectUses); err != nil {
				t.Fatalf("also using vm-app-1: %v", err)
			}
			f.priceAsset(t, "hv-01", "support", domain.CostMonthly, 100_00)
			f.priceAsset(t, "vm-app-1", "licence", domain.CostMonthly, 7_00)

			got := f.costs(t, "platform")

			// The guest counts once, in the bucket its ownership puts it in.
			if got.Own.MonthlyMinor != 107_00 {
				t.Errorf("own monthly = %d, want 10700", got.Own.MonthlyMinor)
			}
			if got.Shared.MonthlyMinor != 0 {
				t.Errorf("shared monthly = %d, want 0 — the guest was counted a second "+
					"time through its redundant `uses` link", got.Shared.MonthlyMinor)
			}
			if got.Own.Lines != 2 {
				t.Errorf("lines = %d, want 2", got.Own.Lines)
			}
		})
	}
}

// TestProjectCostsExcludeWhatAnotherProjectOwns. The subsidy case, and the one
// that makes both pages honest: if platform absorbed the VM orders owns, the two
// projects would together report more than the estate costs, and each page would
// look right on its own.
func TestProjectCostsExcludeWhatAnotherProjectOwns(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)

			// platform owns the metal; orders owns a guest inside it.
			if err := f.link(t, "platform", "hv-01", domain.ProjectOwns); err != nil {
				t.Fatalf("owning hv-01: %v", err)
			}
			if err := f.link(t, "orders", "vm-app-1", domain.ProjectOwns); err != nil {
				t.Fatalf("owning vm-app-1: %v", err)
			}
			f.priceAsset(t, "hv-01", "support", domain.CostMonthly, 100_00)
			f.priceAsset(t, "vm-app-1", "licence", domain.CostMonthly, 7_00)

			platform := f.costs(t, "platform")
			if platform.Own.MonthlyMinor != 100_00 {
				t.Errorf("platform's own monthly = %d, want 10000 — it absorbed a VM "+
					"another project owns", platform.Own.MonthlyMinor)
			}
			if platform.Elsewhere.MonthlyMinor != 7_00 {
				t.Errorf("platform's elsewhere monthly = %d, want 700", platform.Elsewhere.MonthlyMinor)
			}
			// And it names who carries it, or the panel says only that somebody
			// does, which nobody can act on.
			if len(platform.ElsewhereOwners) != 1 || platform.ElsewhereOwners[0] != "orders" {
				t.Errorf("elsewhere owners = %v, want [orders]", platform.ElsewhereOwners)
			}

			orders := f.costs(t, "orders")
			if orders.Own.MonthlyMinor != 7_00 {
				t.Errorf("orders' own monthly = %d, want 700 — it inherited the "+
					"hypervisor it merely runs on", orders.Own.MonthlyMinor)
			}

			// The two projects together must not exceed the estate. This is the
			// property the whole feature is arranged around, so it is asserted
			// directly rather than left implied by the two numbers above.
			if total := platform.Own.MonthlyMinor + orders.Own.MonthlyMinor; total != 107_00 {
				t.Errorf("the two projects together claim %d a month; the estate costs 10700", total)
			}
		})
	}
}

// TestProjectCostsNeverDeriveThroughUses. Nothing is derived from a `uses` link
// -- what is inside somebody else's hypervisor is their footprint, and their
// bill.
func TestProjectCostsNeverDeriveThroughUses(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)

			if err := f.link(t, "orders", "hv-01", domain.ProjectUses); err != nil {
				t.Fatalf("using hv-01: %v", err)
			}
			f.priceAsset(t, "hv-01", "support", domain.CostMonthly, 100_00)
			// A guest inside the used hypervisor: reachable only by deriving
			// through `uses`, which must not happen.
			f.priceAsset(t, "vm-app-1", "licence", domain.CostMonthly, 7_00)

			got := f.costs(t, "orders")
			if got.Own.MonthlyMinor != 0 {
				t.Errorf("own monthly = %d, want 0 — cost was derived through a `uses` link",
					got.Own.MonthlyMinor)
			}
			if got.Shared.MonthlyMinor != 100_00 {
				t.Errorf("shared monthly = %d, want 10000", got.Shared.MonthlyMinor)
			}
			// The guest is not in the footprint at all, so its cost appears
			// nowhere -- not even under shared.
			if got.Elsewhere.MonthlyMinor != 0 {
				t.Errorf("elsewhere monthly = %d, want 0", got.Elsewhere.MonthlyMinor)
			}
		})
	}
}

// A project's own lines are part of what it costs, and are broken out so the
// figure can be traced. Without them a project total silently omits its SaaS
// bill, which is the kind of gap a finance conversation destroys in one question.
func TestProjectCostsIncludeTheProjectsOwnLines(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)

			if err := f.link(t, "orders", "vm-app-1", domain.ProjectOwns); err != nil {
				t.Fatalf("owning vm-app-1: %v", err)
			}
			// A service the project owns, so all three surfaces are in one
			// total: an asset, a service and the project itself.
			if err := f.linkService(t, "orders", "orders-api", domain.ProjectOwns); err != nil {
				t.Fatalf("owning orders-api: %v", err)
			}
			f.priceAsset(t, "vm-app-1", "licence", domain.CostMonthly, 7_00)
			f.priceService(t, "orders-api", "licence", domain.CostMonthly, 15_00)
			f.priceProject(t, "orders", "subscription", domain.CostMonthly, 24_00)
			f.priceProject(t, "orders", "subscription", domain.CostYearly, 120_00) // 10.00/month

			got := f.costs(t, "orders")
			if got.Direct.MonthlyMinor != 34_00 {
				t.Errorf("direct monthly = %d, want 3400", got.Direct.MonthlyMinor)
			}
			if got.Own.MonthlyMinor != 56_00 {
				t.Errorf("own monthly = %d, want 5600 (700 VM + 1500 service + 3400 direct)",
					got.Own.MonthlyMinor)
			}
			// The year is exact: (700 + 1500 + 2400) * 12 + 12000 = 67200.
			if got.Own.AnnualMinor != 672_00 {
				t.Errorf("own annual = %d, want 67200", got.Own.AnnualMinor)
			}
		})
	}
}

// Retiring a line removes it from the totals and leaves it visible in the list,
// struck through. A store that hid it would make "why did this total change"
// unanswerable from the page.
func TestRetiringACostLineRemovesItFromTotalsButNotFromTheList(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)

			id := f.priceAsset(t, "hv-01", "support", domain.CostMonthly, 100_00)
			f.priceAsset(t, "hv-01", "operating", domain.CostMonthly, 5_00)

			before, err := f.s.ListAssetCosts(f.ctx, f.assets["hv-01"])
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if got := TotalCosts(before, "2026-07-31"); got.MonthlyMinor != 105_00 {
				t.Fatalf("monthly before = %d, want 10500", got.MonthlyMinor)
			}

			if err := f.s.RetireAssetCost(f.ctx, testActor, f.assets["hv-01"], id); err != nil {
				t.Fatalf("retiring: %v", err)
			}
			after, err := f.s.ListAssetCosts(f.ctx, f.assets["hv-01"])
			if err != nil {
				t.Fatalf("listing after: %v", err)
			}
			if len(after) != 2 {
				t.Errorf("the retired line disappeared from the list: %d rows, want 2", len(after))
			}
			if got := TotalCosts(after, "2026-07-31"); got.MonthlyMinor != 5_00 {
				t.Errorf("monthly after = %d, want 500", got.MonthlyMinor)
			}
		})
	}
}

// The validity window decides what "current" means, and a closed line stops
// counting without being retired -- which is what an operator should reach for
// when a contract simply ends.
func TestTotalsRespectTheValidityWindow(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)

			from, until := "2026-01-01", "2026-06-30"
			old, err := domain.NewCost(NewID(), domain.CostSpec{
				Kind: "support", Period: domain.CostMonthly, AmountMinor: 100_00,
				ValidFrom: &from, ValidUntil: &until,
			}, f.s.Now())
			if err != nil {
				t.Fatalf("building the closed line: %v", err)
			}
			if err := f.s.AddAssetCost(f.ctx, testActor, f.assets["hv-01"], old); err != nil {
				t.Fatalf("adding the closed line: %v", err)
			}

			renewed := "2026-07-01"
			next, err := domain.NewCost(NewID(), domain.CostSpec{
				Kind: "support", Period: domain.CostMonthly, AmountMinor: 120_00,
				ValidFrom: &renewed,
			}, f.s.Now())
			if err != nil {
				t.Fatalf("building the renewal: %v", err)
			}
			if err := f.s.AddAssetCost(f.ctx, testActor, f.assets["hv-01"], next); err != nil {
				t.Fatalf("adding the renewal: %v", err)
			}

			rows, err := f.s.ListAssetCosts(f.ctx, f.assets["hv-01"])
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			// Today: only the renewal. Both lines survive, which is the point of
			// keeping the window -- the old price is still answerable for.
			if got := TotalCosts(rows, "2026-07-31"); got.MonthlyMinor != 120_00 {
				t.Errorf("monthly today = %d, want 12000", got.MonthlyMinor)
			}
			if got := TotalCosts(rows, "2026-03-01"); got.MonthlyMinor != 100_00 {
				t.Errorf("monthly in March = %d, want 10000 — the history was overwritten",
					got.MonthlyMinor)
			}
		})
	}
}

// Every cost mutation is audited. A price is declared state: somebody read an
// invoice and typed it, and "who changed this number and when" is the first
// question anybody asks of a total they disagree with.
func TestCostMutationsAreAudited(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)

			before := f.changeRows(t, "asset_cost")
			id := f.priceAsset(t, "hv-01", "support", domain.CostMonthly, 100_00)
			if after := f.changeRows(t, "asset_cost"); after != before+1 {
				t.Errorf("creating: change_log went %d -> %d, want one more", before, after)
			}

			row, err := f.s.GetAssetCost(f.ctx, id)
			if err != nil {
				t.Fatalf("reading the line back: %v", err)
			}
			updated := row.Cost
			updated.AmountMinor = 110_00
			if err := f.s.UpdateAssetCost(f.ctx, testActor, &updated); err != nil {
				t.Fatalf("updating: %v", err)
			}
			if after := f.changeRows(t, "asset_cost"); after != before+2 {
				t.Errorf("updating: change_log = %d, want %d", after, before+2)
			}

			if err := f.s.RetireAssetCost(f.ctx, testActor, f.assets["hv-01"], id); err != nil {
				t.Fatalf("retiring: %v", err)
			}
			if after := f.changeRows(t, "asset_cost"); after != before+3 {
				t.Errorf("retiring: change_log = %d, want %d", after, before+3)
			}
		})
	}
}

// An unknown kind is refused by the lookup table, the same way every other
// vocabulary is -- and the error names the field so a form can show it.
func TestACostKindMustExist(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)

			c, err := domain.NewCost(NewID(), domain.CostSpec{
				Kind: "vibes", Period: domain.CostMonthly, AmountMinor: 100,
			}, f.s.Now())
			if err != nil {
				t.Fatalf("the constructor rejected the shape rather than the store rejecting the value: %v", err)
			}
			if err := f.s.AddAssetCost(f.ctx, testActor, f.assets["hv-01"], c); err == nil {
				t.Error("a cost kind that is not in the lookup table was accepted")
			}
		})
	}
}

// TestAmortisationReadsTheEOLDateOfWhatWasBought.
//
// The join is the whole feature at this layer: a cost line has no end date of
// its own, and the life it is spread over belongs to the asset or service it is
// attached to. Without the join every one-off would count as unamortisable and
// the figure would be silently zero on a page that still rendered it.
func TestAmortisationReadsTheEOLDateOfWhatWasBought(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)

			// Two years of life, bought a year ago: 240000/24 = 10000 a month.
			f.setAssetEOLOn(t, "hv-01", "2027-08-01")
			f.priceAssetFrom(t, "hv-01", "acquisition", domain.CostOnce, 240_00, "2025-08-01")
			// The control: same purchase, no EOL date on what it bought.
			f.priceAssetFrom(t, "sw-core-1", "acquisition", domain.CostOnce, 240_00, "2025-08-01")

			hv, err := f.s.ListAssetCosts(f.ctx, f.assets["hv-01"])
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if len(hv) != 1 || hv[0].OwnerEOLDate == nil {
				t.Fatalf("the asset's EOL date did not reach the cost row: %+v", hv)
			}
			got := TotalCosts(hv, "2026-07-31")
			if got.AmortisedMonthlyMinor != 10_00 {
				t.Errorf("amortised = %d, want 1000", got.AmortisedMonthlyMinor)
			}
			if got.Amortisable != 1 || got.Unamortisable != 0 {
				t.Errorf("counters = %d/%d, want 1/0", got.Amortisable, got.Unamortisable)
			}
			// And the run rate is untouched: a one-off is not a run rate.
			if got.MonthlyMinor != 0 {
				t.Errorf("run rate = %d, want 0", got.MonthlyMinor)
			}

			sw, err := f.s.ListAssetCosts(f.ctx, f.assets["sw-core-1"])
			if err != nil {
				t.Fatalf("listing the control: %v", err)
			}
			if sw[0].OwnerEOLDate != nil {
				t.Fatalf("an undated asset reported an EOL date: %v", *sw[0].OwnerEOLDate)
			}
			control := TotalCosts(sw, "2026-07-31")
			if control.Amortisable != 0 || control.Unamortisable != 1 {
				t.Errorf("control counters = %d/%d, want 0/1",
					control.Amortisable, control.Unamortisable)
			}
		})
	}
}

// A project's own cost lines can never be amortised: a project has no
// end-of-support, and a setup fee for a SaaS subscription bought nothing with a
// life. The join is absent for project_cost by construction, so this asserts the
// absence is deliberate rather than an oversight.
func TestProjectCostsAreNeverAmortised(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)

			f.priceProject(t, "orders", "subscription", domain.CostOnce, 500_00)
			rows, err := f.s.ListProjectCosts(f.ctx, f.projects["orders"])
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if rows[0].OwnerEOLDate != nil {
				t.Errorf("a project cost carried an EOL date: %v", *rows[0].OwnerEOLDate)
			}
			got := TotalCosts(rows, "2026-07-31")
			if got.Amortisable != 0 {
				t.Errorf("a project cost was amortised")
			}
			if got.Unamortisable != 1 {
				t.Errorf("unamortisable = %d, want 1 — the one-off was not counted at all",
					got.Unamortisable)
			}
		})
	}
}

// TestCostsCoverTheWholeFootprintNotJustWhatFitsOnADiagram.
//
// Regression, from a review. ProjectFootprint used to take the neighbourhood
// node budget -- 60, chosen so an SVG stays legible -- and truncate its implied
// assets to it before conflicts, implied services or costs were computed. A
// project owning a rack with eighty priced guests therefore reported EUR 60 a
// month against a real EUR 80: the twenty past the budget were not costed, not
// counted as unpriced, and mentioned nowhere in the summary.
//
// Eighty is deliberately above the old default. At sixty-one this test would
// pass against the bug for anyone who later raised the constant, and a
// regression test that a constant can silence is not a regression test.
func TestCostsCoverTheWholeFootprintNotJustWhatFitsOnADiagram(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)

			rack := mustAsset(t, f.s, f.ctx, domain.KindRack, "rack-z", nil, f.env)
			f.assets["rack-z"] = rack
			const guests = 80
			for i := 0; i < guests; i++ {
				name := fmt.Sprintf("vm-z-%02d", i)
				f.assets[name] = mustAsset(t, f.s, f.ctx, domain.KindVM, name, &rack, f.env)
				f.priceAsset(t, name, "operating", domain.CostMonthly, 1_00)
			}
			if err := f.link(t, "platform", "rack-z", domain.ProjectOwns); err != nil {
				t.Fatalf("owning the rack: %v", err)
			}

			got := f.costs(t, "platform")
			if got.Own.MonthlyMinor != guests*1_00 {
				t.Errorf("monthly = %d, want %d — the rollup inherited a drawing budget",
					got.Own.MonthlyMinor, guests*1_00)
			}
			if got.Priced != guests {
				t.Errorf("priced = %d, want %d", got.Priced, guests)
			}
		})
	}
}

// TestRetiringACostRequiresTheRightOwner.
//
// From a security review. The route is /assets/{id}/costs/{costID}/retire and
// the store used to select on {costID} alone, so an admin could retire a cost
// belonging to one asset through a URL naming another: the change_log entry
// would be correct and the operator's belief about what they had just done
// would not.
func TestRetiringACostRequiresTheRightOwner(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)

			id := f.priceAsset(t, "hv-01", "support", domain.CostMonthly, 100_00)

			// The same cost, reached through a different asset's URL.
			err := f.s.RetireAssetCost(f.ctx, testActor, f.assets["sw-core-1"], id)
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("retiring through the wrong owner returned %v, want ErrNotFound", err)
			}
			rows, err := f.s.ListAssetCosts(f.ctx, f.assets["hv-01"])
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if rows[0].Lifecycle != domain.LifecycleActive {
				t.Error("the cost was retired through another asset's URL")
			}

			// The control: through its own owner it retires.
			if err := f.s.RetireAssetCost(f.ctx, testActor, f.assets["hv-01"], id); err != nil {
				t.Fatalf("retiring through the right owner: %v", err)
			}
		})
	}
}

// TestAForeignOwnedAssetIsCountedElsewhereNotShared.
//
// The last untested dedup path, from a review: an asset that is implied (inside
// something this project owns), owned outright by ANOTHER project, and also
// declared with a `uses` link from this one. All three are true at once, and the
// fold order decides which bucket wins.
//
// It must be Elsewhere. "Somebody else owns this and it is sitting on my
// estate" is a different conversation from "I declared that I use this", and
// counting it under Shared would file the subsidy under things already
// accounted for -- where nobody looks.
func TestAForeignOwnedAssetIsCountedElsewhereNotShared(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)

			if err := f.link(t, "platform", "hv-01", domain.ProjectOwns); err != nil {
				t.Fatalf("owning hv-01: %v", err)
			}
			// orders owns the guest inside it...
			if err := f.link(t, "orders", "vm-app-1", domain.ProjectOwns); err != nil {
				t.Fatalf("owning vm-app-1: %v", err)
			}
			// ...and platform ALSO declared that it uses that guest.
			if err := f.link(t, "platform", "vm-app-1", domain.ProjectUses); err != nil {
				t.Fatalf("also using vm-app-1: %v", err)
			}
			f.priceAsset(t, "vm-app-1", "licence", domain.CostMonthly, 7_00)

			got := f.costs(t, "platform")
			if got.Elsewhere.MonthlyMinor != 7_00 {
				t.Errorf("elsewhere = %d, want 700", got.Elsewhere.MonthlyMinor)
			}
			if got.Shared.MonthlyMinor != 0 {
				t.Errorf("shared = %d, want 0 — a subsidy was filed under things already "+
					"accounted for, where nobody looks", got.Shared.MonthlyMinor)
			}
			if got.Own.MonthlyMinor != 0 {
				t.Errorf("own = %d, want 0 — it absorbed another project's asset",
					got.Own.MonthlyMinor)
			}
		})
	}
}

// TestAFootprintLargerThanTheDriverParameterLimit.
//
// From a database review. Removing the node budget from the footprint was right
// -- a rendering cap must not truncate a sum -- but it left the id lists fed to
// costsFor, footprintConflicts, ownersOfServices, projectsFor and
// neighbourhoodWorkloads bounded only by the estate. Both drivers refuse past a
// limit, at different points and with opaque errors: SQLite at 32,767 bound
// parameters, pgx at 65,536.
//
// 600 guests is above idChunkSize and far below either limit, so this exercises
// the chunk boundary without a minute of setup. The assertion that matters is
// the total: chunking must not lose or double-count a row at the seam.
func TestAFootprintLargerThanTheDriverParameterLimit(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)

			rack := mustAsset(t, f.s, f.ctx, domain.KindRack, "rack-big", nil, f.env)
			f.assets["rack-big"] = rack
			const guests = 600
			for i := 0; i < guests; i++ {
				name := fmt.Sprintf("vm-big-%03d", i)
				f.assets[name] = mustAsset(t, f.s, f.ctx, domain.KindVM, name, &rack, f.env)
				f.priceAsset(t, name, "operating", domain.CostMonthly, 1_00)
			}
			if err := f.link(t, "platform", "rack-big", domain.ProjectOwns); err != nil {
				t.Fatalf("owning the rack: %v", err)
			}

			got := f.costs(t, "platform")
			if got.Own.MonthlyMinor != guests*1_00 {
				t.Errorf("monthly = %d, want %d — a chunk boundary lost or repeated rows",
					got.Own.MonthlyMinor, guests*1_00)
			}
			if got.Priced != guests {
				t.Errorf("priced = %d, want %d", got.Priced, guests)
			}
		})
	}
}

// priceCircuit attaches a cost to a circuit.
func (f *projectFixture) priceCircuit(t *testing.T, cid, kind, period string, minor int64) string {
	t.Helper()
	return f.price(t, func(c *domain.Cost) error {
		return f.s.AddCircuitCost(f.ctx, testActor, f.circuit(t, cid), c)
	}, kind, period, minor)
}

// TestProjectCostsIncludeCircuits is the assertion that was false until
// migration 00041.
//
// THIS IS A WRONG NUMBER, NOT A MISSING FEATURE, and that distinction is why
// the test asserts a total rather than the presence of a row. The rollup
// gathered cost lines from assets and services and stopped. Circuits carry a
// monthly rate -- often the largest single recurring line a project has -- and
// nothing recorded which project a circuit served, so every project paying for
// connectivity reported less than it costs. The page was not wrong about what
// it gathered; it was wrong about what it implied.
//
// Both relations are asserted because they land in different buckets and the
// difference is the whole point of having two. An OWNED circuit is the
// project's own cost. A USED one is somebody else's cost that this project
// depends on -- the transit link serving a whole rack -- and counting it as Own
// would have every tenant of that rack reporting the same monthly rate.
func TestProjectCostsIncludeCircuits(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)

			if err := f.linkCircuit(t, "orders", "DIA-1", domain.ProjectOwns); err != nil {
				t.Fatalf("linking the owned circuit: %v", err)
			}
			if err := f.linkCircuit(t, "orders", "TRANSIT-1", domain.ProjectUses); err != nil {
				t.Fatalf("linking the used circuit: %v", err)
			}
			f.priceCircuit(t, "DIA-1", "operating", domain.CostMonthly, 90000)
			f.priceCircuit(t, "TRANSIT-1", "operating", domain.CostMonthly, 40000)

			got := f.costs(t, "orders")
			if got.Own.MonthlyMinor != 90000 {
				t.Errorf("Own monthly = %d, want 90000: an owned circuit's rate is the "+
					"project's own cost, and before 00041 it reached no total at all",
					got.Own.MonthlyMinor)
			}
			if got.Shared.MonthlyMinor != 40000 {
				t.Errorf("Shared monthly = %d, want 40000: a used circuit is somebody "+
					"else's cost this project depends on", got.Shared.MonthlyMinor)
			}
		})
	}
}

// TestOneProjectMayOwnACircuit refuses the second owner.
//
// The partial unique index in 00041 is the authority. Two owners would land one
// monthly rate in two rollups, and the estate would report more spend than it
// has -- the same failure TestProjectCostsCountAnAssetOnce exists to prevent,
// arriving by a different route.
func TestOneProjectMayOwnACircuit(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			if err := f.linkCircuit(t, "orders", "DIA-1", domain.ProjectOwns); err != nil {
				t.Fatalf("first owner: %v", err)
			}
			err := f.linkCircuit(t, "platform", "DIA-1", domain.ProjectOwns)
			if err == nil {
				t.Fatal("a second project owned the same circuit; one rate would be " +
					"counted twice")
			}
			if !errors.Is(err, domain.ErrConflict) {
				t.Errorf("error is %v, want a conflict the handler maps to 409", err)
			}
			// Using it is not owning it, and must still be allowed.
			if err := f.linkCircuit(t, "platform", "DIA-1", domain.ProjectUses); err != nil {
				t.Errorf("a second project could not USE the circuit: %v", err)
			}
		})
	}
}
