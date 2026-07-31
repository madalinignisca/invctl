package seed

import (
	"context"
	"fmt"

	"github.com/gabriel/invctl/internal/domain"
	"github.com/gabriel/invctl/internal/store"
)

// ---------- what things cost ----------
//
// Prices that make the estate's ONE structural finding visible: the platform
// team pays for almost everything, and the project that ships the product owns
// a single VM and therefore appears to cost nearly nothing.
//
// That is not a contrivance. It is what the ownership links already say, and it
// is the shape of most real estates -- which is why the summary reports three
// buckets instead of one number. Reading "orders costs EUR 62 a month" next to
// "EUR 1,240 a month sits on this project's footprint that Platform & Core
// Services owns" is the conversation the feature exists to start.
//
// Figures are plausible for a small European colocated estate and are round
// where a real invoice would not be. They are a demo, not a quote.

type costLine struct {
	target string // asset name, service code, or project code
	kind   string
	period string
	amount int64 // MAJOR units; converted below, so the table stays readable
	note   string
}

// major converts a whole-currency figure to the minor units everything is
// stored in. In the table above so nobody has to write 129900 and hope.
func major(v int64) int64 { return v * 100 }

func (b *builder) costs() {
	if !b.ok() {
		return
	}

	assetCosts := []costLine{
		// The site and the racks. Colocation is billed on the space, not on the
		// boxes in it, so it attaches to the rack -- and because the platform
		// project owns the hypervisors but NOT the racks, this is spend the
		// footprint derivation deliberately does not pull in. Somebody has to
		// claim the racks for it to land anywhere, which is itself a finding.
		{"rack-a1", "operating", domain.CostMonthly, 380, "half rack, 2 kW committed"},
		{"rack-b1", "operating", domain.CostMonthly, 380, "half rack, 2 kW committed"},
		{"dc-oslo", "connectivity", domain.CostMonthly, 210, "1 Gbit transit, 95th percentile"},

		// Compute. Bought once, supported yearly -- the two-line shape most
		// hardware actually has, and the reason capital and run rate are
		// reported separately.
		{"hv-01", "acquisition", domain.CostOnce, 8400, "2U, dual socket, 512 GB"},
		{"hv-01", "support", domain.CostYearly, 940, "next business day, ends with the box"},
		{"hv-02", "acquisition", domain.CostOnce, 8400, "2U, dual socket, 512 GB"},
		{"hv-02", "support", domain.CostYearly, 940, "next business day"},
		{"hv-03", "acquisition", domain.CostOnce, 9100, "2U, dual socket, 768 GB"},
		{"hv-03", "support", domain.CostYearly, 1020, "next business day"},

		// Network. Out of support, which is why the expiry report has something
		// to say about them -- and they still cost nothing per month, so they
		// are invisible to a run-rate-only view. Both facts matter.
		{"sw-core-1", "acquisition", domain.CostOnce, 5200, "48x10G, bought 2019"},
		{"sw-core-2", "acquisition", domain.CostOnce, 5200, "48x10G, bought 2019"},
		{"fw-edge-1", "acquisition", domain.CostOnce, 3100, ""},
		{"fw-edge-2", "acquisition", domain.CostOnce, 3100, ""},
		{"fw-edge-1", "support", domain.CostYearly, 610, "lapsed; renewal quoted, not taken"},

		// The stranded box, owned by observability. A small, unremarkable
		// number attached to a host that cannot reach anything -- which is what
		// makes it worth seeing next to the reachability finding.
		{"srv-backup-proxy-1", "acquisition", domain.CostOnce, 2400, ""},
		{"srv-backup-proxy-1", "support", domain.CostYearly, 300, ""},

		// The VM the commerce team owns. VMs are ordinarily free -- they are a
		// share of a hypervisor somebody else bought -- and this one carries a
		// licence-per-guest charge only because the backup agent on it is
		// billed that way. It is the single line that keeps `orders` from
		// totalling to zero, and it is deliberately small.
		{"vm-app-1", "licence", domain.CostMonthly, 62, "backup agent, per protected guest"},
	}

	serviceCosts := []costLine{
		// The two closed-source services, which is where service cost lands in
		// almost every estate.
		{"vault", "licence", domain.CostYearly, 14400, "3 nodes, term ends with the EOL date"},
		{"backup-agent", "licence", domain.CostYearly, 2900, "10 sockets, lapsed"},
		// A support contract on something otherwise free: paying for somebody to
		// call rather than for the right to run it. Separate kinds because they
		// are separately cancellable.
		{"pgsql-core", "support", domain.CostYearly, 6200, "24x7 vendor support on an open source engine"},
	}

	projectCosts := []costLine{
		// The money that attaches to no box and no service anybody here runs.
		// Without a project-level line these are simply absent, and a project
		// total that silently omits its SaaS bill is the kind of number a
		// finance conversation destroys in one question.
		{"orders", "subscription", domain.CostMonthly, 240, "error tracking and session replay, per seat"},
		{"orders", "subscription", domain.CostYearly, 890, "payment gateway, annual platform fee"},
		{"observability", "subscription", domain.CostMonthly, 180, "hosted alerting and on-call rota"},
		{"platform", "subscription", domain.CostYearly, 1200, "certificate authority and domain portfolio"},
	}

	b.addCosts(assetCosts, b.refs.Assets, "asset", b.store.AddAssetCost)
	b.addCosts(serviceCosts, b.refs.Services, "service", b.store.AddServiceCost)
	b.addCosts(projectCosts, b.refs.Projects, "project", b.store.AddProjectCost)
}

// addCosts is shared by the three surfaces. The attach function is passed in
// rather than selected from the entity name, so no string decides a table.
func (b *builder) addCosts(lines []costLine, refs map[string]string, what string,
	attach func(ctx context.Context, actor domain.Actor, ownerID string, c *domain.Cost) error) {

	for _, line := range lines {
		if !b.ok() {
			return
		}
		id, ok := refs[line.target]
		if !ok {
			b.fail(fmt.Errorf("pricing %s %s: unknown", what, line.target))
			return
		}
		spec := domain.CostSpec{
			Kind:        line.kind,
			Period:      line.period,
			AmountMinor: major(line.amount),
		}
		if line.note != "" {
			spec.Note = str(line.note)
		}
		c, err := domain.NewCost(store.NewID(), spec, b.now)
		if err != nil {
			b.fail(fmt.Errorf("building the %s cost for %s: %w", line.kind, line.target, err))
			return
		}
		if err := attach(b.ctx, Actor, id, c); err != nil {
			b.fail(fmt.Errorf("pricing %s %s: %w", what, line.target, err))
			return
		}
	}
}
