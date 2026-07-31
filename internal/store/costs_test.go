package store

import (
	"testing"
	"time"

	"github.com/gabriel/invctl/internal/domain"
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
	footprint, err := f.s.ProjectFootprint(f.ctx, f.projects[code], 0)
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

			if err := f.s.RetireAssetCost(f.ctx, testActor, id); err != nil {
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

			if err := f.s.RetireAssetCost(f.ctx, testActor, id); err != nil {
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
