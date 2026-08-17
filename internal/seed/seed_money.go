// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package seed

import (
	"fmt"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// The refresh and the renewals (WP-J1, WP-J2).
//
// WRITTEN BECAUSE THE ESTATE COULD NOT DEMONSTRATE EITHER, which is the fourth
// time this fixture has needed that sentence -- cabling, physical fit and the
// notes panel each shipped before anything in the seed exercised them. J1 asks
// what a box replaced and J2 asks how a price moved, and the estate contained
// no lineage at all and not one cost line whose figure had ever changed. Two
// features rendering an empty panel look identical to two features that do not
// work.
//
// NOTHING HERE IS INVENTED FOR THE DEMO. The pairs and the prices are already
// in the fixture and were already telling this story; all that was missing was
// the two facts that connect them.
//
//	hv-dev-01..03 are retired hypervisors in rack-a2.
//	hv-esx-01..03 are the active ones that took their place, in the same rack.
//	The old boxes cost 3,200 in 2022. The new ones cost 10,200 in 2024.
//
// The price series is the colo rack's rent, and the choice is deliberate. The
// estate's yearly software licence is attributed to a NAMED vendor, and seeding
// two rises onto it would make a public demo assert that a real company raised
// its prices by half -- a specific, checkable-sounding claim about somebody who
// is not here to comment. Colocation rent names nobody, rises everywhere, and
// demonstrates exactly the same arithmetic.

// companyMoney records what replaced what, and what the renewals cost.
func (b *builder) companyMoney() {
	b.refreshLineage()
	b.coloRentRenewals()
	b.inflationSeries()
	b.computeCapacity()
	b.pricedFor()
	b.storagePools()
	b.conditionalLicence()
	b.sharedTenancy()
	b.attributeSuppliers()
}

// pricedFor records what two engagements were quoted on (WP-J7).
//
// ONE PROJECT OVER ITS QUOTE AND ONE INSIDE IT, because a fixture where nothing
// has drifted cannot demonstrate the alert and one where everything has cannot
// demonstrate the calm. `platform` owns the three prod hosts and everything
// inside them, which is 28 vCPU allocated -- priced for 24, it is the CEO's
// alert: nobody is in breach and the margin is eroding.
//
// NOT CALLED CONTRACTED ANYWHERE, including here. See migration 00045.
func (b *builder) pricedFor() {
	if !b.ok() {
		return
	}
	for _, q := range []struct {
		code     string
		vcpu     int
		memoryMB int
	}{
		// Priced for less than it now uses: the engagement grew.
		{"platform", 24, 81920},
		// Priced generously and still inside it: the quiet case, which must be
		// visible or the finding looks like it fires on everything.
		{"orders", 16, 32768},
	} {
		id, ok := b.refs.Projects[q.code]
		if !ok {
			continue
		}
		row, err := b.store.GetProject(b.ctx, id)
		if err != nil {
			b.fail(fmt.Errorf("reading project %s: %w", q.code, err))
			return
		}
		if row.PricedForVCPU != nil {
			continue // already recorded
		}
		p := row.Project
		p.PricedForVCPU, p.PricedForMemoryMB = num(q.vcpu), num(q.memoryMB)
		if err := b.store.UpdateProject(b.ctx, Actor, &p); err != nil {
			b.fail(fmt.Errorf("pricing project %s: %w", q.code, err))
			return
		}
	}
}

// computeCapacity measures the machines, so a cluster can be divided (WP-J3).
//
// THE ESTATE MUST SHOW BOTH ANSWERS, not just the tidy one. A fixture where
// everything is measured and nothing is oversubscribed demonstrates arithmetic
// that always succeeds -- and every capacity finding would have nothing to
// find. So prod-pve is measured, declared at 3:1, and deliberately
// oversubscribed by what its guests are PROVISIONED, while one host elsewhere
// is left unmeasured so "this cluster is not fully measured" has something to
// report.
func (b *builder) computeCapacity() {
	if !b.ok() {
		return
	}

	// The hosts. Sizes are ordinary rather than remarkable: two sockets of
	// sixteen cores and 256 GB is what a mid-range hypervisor has been for
	// several years.
	hosts := []struct {
		name  string
		cores int
		memMB int
	}{
		{"hv-01", 32, 262144},
		{"hv-02", 32, 262144},
		// hv-03 is deliberately LEFT UNMEASURED. An estate that has finished
		// measuring everything is not one anybody recognises, and the gap is
		// what the "not fully measured" finding exists to report.
		{"srv-hz-1", 16, 65536},
		{"srv-hz-2", 16, 65536},
		{"hv-esx-01", 24, 196608},
		{"hv-esx-02", 24, 196608},
		{"hv-esx-03", 24, 196608},
	}
	for _, h := range hosts {
		if err := b.measureAsset(h.name, func(a *domain.Asset) {
			a.CPUCores, a.MemoryMB = num(h.cores), num(h.memMB)
		}); err != nil {
			b.fail(err)
			return
		}
	}

	// The guests. Provisioned deliberately exceeds allocated on two of them:
	// somebody was generous with a limit while the deal was priced on less,
	// which is the gap WP-J4 reports as capacity nobody is paying for.
	guests := []struct {
		name                  string
		vcpuAlloc, vcpuProv   int
		memAllocMB, memProvMB int
	}{
		{"vm-db-1", 8, 16, 32768, 65536},
		{"vm-db-2", 8, 16, 32768, 65536},
		{"vm-app-1", 4, 4, 16384, 16384},
		{"vm-vault-1", 2, 2, 4096, 4096},
		{"vm-vault-2", 2, 2, 4096, 4096},
		{"vm-proxy-1", 4, 4, 8192, 8192},
		// vm-queue-1 is left UNALLOCATED, so "its cost cannot be attributed"
		// has an example.
	}
	for _, g := range guests {
		if err := b.measureAsset(g.name, func(a *domain.Asset) {
			a.VCPUAllocated, a.VCPUProvisioned = num(g.vcpuAlloc), num(g.vcpuProv)
			a.MemoryAllocatedMB, a.MemoryProvisionedMB = num(g.memAllocMB), num(g.memProvMB)
		}); err != nil {
			b.fail(err)
			return
		}
	}

	// What the operators are willing to oversubscribe, declared per cluster.
	// prod-pve must survive losing one of three, which is the case the
	// availability premium is computed from.
	// prod-virt's floor of three is DELIBERATE and load-bearing -- the seed
	// comment says clustering it with spare capacity would break
	// TestContainmentResolvesThroughClosure -- so only the ratio is declared
	// here. dev-hetzner gets an explicit floor of one, which is what a nil
	// floor already meant to the impact engine and which gives the redundancy
	// premium something to demonstrate.
	for _, c := range []struct {
		name       string
		anchorHost string
		overcommit int
		minHosts   int
		splitCPU   int
	}{
		// 60/40 towards CPU: an ordinary judgement for a general-purpose
		// virtualisation cluster, and the point is that somebody made it.
		// hv-01 anchors it: see declareClusterCapacity for why a name is not
		// enough.
		{"prod-virt", "hv-01", 300, 0, 60},
		// dev-hetzner declares NO split, so the estate demonstrates a cluster
		// whose cost cannot be divided and says so.
		{"dev-hetzner", "srv-hz-1", 400, 1, 0},
	} {
		if err := b.declareClusterCapacity(c.name, c.anchorHost, c.overcommit,
			c.minHosts, c.splitCPU); err != nil {
			b.fail(err)
			return
		}
	}
}

// measureAsset applies a measurement, skipping an asset that already carries
// one so a top-up neither rewrites nor fails.
func (b *builder) measureAsset(name string, apply func(*domain.Asset)) error {
	id, ok := b.refs.Assets[name]
	if !ok {
		return nil // a deployment without this layer has no such asset
	}
	row, err := b.store.GetAsset(b.ctx, id)
	if err != nil {
		return fmt.Errorf("reading %s: %w", name, err)
	}
	a := row.Asset
	before := a
	apply(&a)
	if sameCapacity(before, a) {
		return nil
	}
	envIDs := make([]string, len(row.Environments))
	for i, env := range row.Environments {
		envIDs[i] = env.ID
	}
	if err := b.store.UpdateAsset(b.ctx, Actor, &a, envIDs); err != nil {
		return fmt.Errorf("measuring %s: %w", name, err)
	}
	return nil
}

// sameCapacity reports whether a measurement would change nothing, which is
// what makes the phase idempotent.
func sameCapacity(a, b domain.Asset) bool {
	return sameInt(a.CPUCores, b.CPUCores) && sameInt(a.MemoryMB, b.MemoryMB) &&
		sameInt(a.VCPUAllocated, b.VCPUAllocated) && sameInt(a.VCPUProvisioned, b.VCPUProvisioned) &&
		sameInt(a.MemoryAllocatedMB, b.MemoryAllocatedMB) &&
		sameInt(a.MemoryProvisionedMB, b.MemoryProvisionedMB)
}

func sameInt(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// declareClusterCapacity records the overcommit ratio and, when given, how many
// hosts must survive.
func (b *builder) declareClusterCapacity(name, anchorHost string, overcommit, minHosts, splitCPU int) error {
	// Looked up by name rather than from Refs, which carries no cluster map:
	// the phase runs after the clusters exist and this is a handful of rows.
	all, err := b.store.ListClusters(b.ctx)
	if err != nil {
		return fmt.Errorf("listing clusters: %w", err)
	}
	var id string
	for _, row := range all {
		if row.Name == name {
			id = row.ID
			break
		}
	}

	// FALLING BACK TO THE HOST IT WAS MEANT TO DESCRIBE, because a name is the
	// least stable thing about a cluster and this fixture has already renamed
	// one. A demo seeded years ago carries `prod-pve`; today's fixture builds
	// `prod-virt`; the top-up never creates clusters, so the old name is
	// permanent. Matching on the anchor host asks the question that actually
	// matters -- "the cluster the production hypervisors are in" -- and that is
	// true whatever the local name is.
	//
	// It cannot be repaired by creating the missing cluster instead: a host
	// belongs to at most one, so `prod-virt` could only be added by taking
	// hv-01 away from whatever already holds it.
	if id == "" && anchorHost != "" {
		hostID, ok := b.refs.Assets[anchorHost]
		if ok {
			for _, row := range all {
				hosts, err := b.store.ListClusterHosts(b.ctx, row.ID)
				if err != nil {
					return fmt.Errorf("reading hosts of %s: %w", row.Name, err)
				}
				for _, h := range hosts {
					if h.AssetID == hostID {
						id = row.ID
						break
					}
				}
				if id != "" {
					break
				}
			}
		}
	}
	if id == "" {
		b.skip("no cluster named %s and none holding %s, so its capacity is undeclared",
			name, anchorHost)
		return nil
	}
	c, err := b.store.GetCluster(b.ctx, id)
	if err != nil {
		return fmt.Errorf("reading cluster %s: %w", name, err)
	}
	if c.CPUOvercommit != nil && (minHosts == 0 || c.MinHosts != nil) &&
		(splitCPU == 0 || c.CostSplitCPU != nil) {
		return nil // already declared
	}
	c.CPUOvercommit = num(overcommit)
	// What proportion of the cluster's cost is CPU. Declared on one cluster and
	// LEFT UNDECLARED ON THE OTHER, because both states are worth seeing: a
	// reader needs to know what the report looks like before anybody has made
	// the judgement, and an estate where every number is already filled in
	// cannot show that.
	if splitCPU > 0 && c.CostSplitCPU == nil {
		c.CostSplitCPU = num(splitCPU)
	}
	if minHosts > 0 && c.MinHosts == nil {
		c.MinHosts = num(minHosts)
	}
	if err := b.store.UpdateCluster(b.ctx, Actor, c); err != nil {
		return fmt.Errorf("declaring capacity on %s: %w", name, err)
	}
	return nil
}

// inflationSeries records what money did, so a rise can be judged rather than
// merely reported.
//
// ILLUSTRATIVE FIGURES, AND THE SEED SAYS SO IN EVERY ROW. The shape is a
// plausible one -- a couple of quiet years, a spike, then a slow settling --
// and it is deliberately NOT a citation of any index. The whole point of the
// feature is that a person types a figure they can defend from a published
// source, so a fixture pretending to BE that source would teach exactly the
// wrong lesson.
//
// It is also why the years are relative to the clock rather than anchored: the
// shape ages with the demo, so it must not claim to be any particular year's
// history. Each row carries a source field saying it is demo data.
//
// Relative to the seeding clock so the demo ages, like every other date here:
// the series ends at last year, because this year's index is not published
// until it is over.
func (b *builder) inflationSeries() {
	if !b.ok() {
		return
	}
	// Basis points: hundredths of a percent, so 810 is 8.1%.
	shape := []int{240, 810, 540, 290, 240, 220, 210, 200}
	thisYear := b.store.Now().Year()
	source := "illustrative demo data, not a published index"

	for i, bp := range shape {
		year := thisYear - len(shape) + i
		r := &domain.InflationRate{Year: year, BasisPoints: bp, Source: &source}
		// Idempotent by construction: SetInflationRate upserts on the year, so
		// a second run rewrites the same figure rather than adding a row.
		if err := b.store.SetInflationRate(b.ctx, Actor, r); err != nil {
			b.fail(fmt.Errorf("recording inflation for %d: %w", year, err))
			return
		}
	}
}

// refreshLineage records that the ESX hosts took over from the dev ones.
//
// Idempotent: an asset already naming a predecessor is left alone, so a top-up
// run neither rewrites the lineage nor fails on it.
func (b *builder) refreshLineage() {
	pairs := []struct{ successor, predecessor string }{
		{"hv-esx-01", "hv-dev-01"},
		{"hv-esx-02", "hv-dev-02"},
		{"hv-esx-03", "hv-dev-03"},
	}
	for _, p := range pairs {
		if !b.ok() {
			return
		}
		successorID, ok := b.refs.Assets[p.successor]
		if !ok {
			continue // a deployment without the company layer has neither
		}
		predecessorID, ok := b.refs.Assets[p.predecessor]
		if !ok {
			continue
		}
		row, err := b.store.GetAsset(b.ctx, successorID)
		if err != nil {
			b.fail(fmt.Errorf("reading %s: %w", p.successor, err))
			return
		}
		if row.ReplacesAssetID != nil {
			continue // already recorded
		}
		a := row.Asset
		a.ReplacesAssetID = &predecessorID
		envIDs := make([]string, len(row.Environments))
		for i, env := range row.Environments {
			envIDs[i] = env.ID
		}
		if err := b.store.UpdateAsset(b.ctx, Actor, &a, envIDs); err != nil {
			b.fail(fmt.Errorf("recording that %s replaced %s: %w",
				p.successor, p.predecessor, err))
			return
		}
	}
}

// coloRentRenewals moves the colo rack's rent twice, so the estate has a price
// series rather than a price.
//
// TWO STEPS, NOT ONE. A single change gives a before and an after, which any
// audit entry could have shown. Three figures make a TREND -- and a trend is
// what somebody takes to a supplier, because one rise is a negotiation and two
// in a row is a pattern.
//
// The rises are deliberately above any plausible inflation: the whole point of
// WP-J2 is to make "this went up faster than money fell" visible, and a fixture
// where everything tracks inflation demonstrates nothing.
func (b *builder) coloRentRenewals() {
	// Relative to the seeding clock, like every other date in this fixture, so
	// the demo ages instead of looking urgent this year and ancient in three.
	steps := []struct {
		amount   int64 // MAJOR units, as the rest of the cost tables use
		fromDays int
		note     string
	}{
		{700, -365, "energy surcharge applied at renewal"},
		{790, 0, "renewal: power committed unchanged"},
	}

	if !b.ok() {
		return
	}
	id, ok := b.refs.Assets["colo-rack-07"]
	if !ok {
		return // a deployment without the company layer has no colo
	}
	for _, step := range steps {
		line, err := b.currentRent(id)
		if err != nil {
			b.fail(err)
			return
		}
		if line == nil {
			return // nothing to move
		}
		from := domain.FormatDate(b.store.Now().AddDate(0, 0, step.fromDays))
		// IDEMPOTENCY HANGS ON THIS COMPARISON. A second run must not stack
		// another renewal on top: if the line in force already starts on this
		// date, this step has been applied.
		if line.ValidFrom >= from {
			continue
		}
		note := step.note
		if _, err := b.store.RepriceAssetCost(b.ctx, Actor, id, store.RepriceSpec{
			LineID:         line.ID,
			NewAmountMinor: major(step.amount),
			EffectiveFrom:  from,
			Note:           &note,
		}); err != nil {
			b.fail(fmt.Errorf("repricing the colo rent: %w", err))
			return
		}
	}
}

// currentRent returns the monthly operating line in force on an asset, or nil.
func (b *builder) currentRent(assetID string) (*store.CostRow, error) {
	lines, err := b.store.ListAssetCosts(b.ctx, assetID)
	if err != nil {
		return nil, fmt.Errorf("reading costs: %w", err)
	}
	var found *store.CostRow
	for i := range lines {
		l := lines[i]
		if l.Lifecycle == domain.LifecycleRetired || l.Kind != "operating" {
			continue
		}
		if l.Period != domain.CostMonthly || l.ValidUntil != nil {
			continue // superseded already, or not a rate that renews
		}
		if found == nil || l.ValidFrom > found.ValidFrom {
			found = &lines[i]
		}
	}
	return found, nil
}

// storagePools gives the estate two pools at different prices and different
// shapes (WP-J4).
//
// TWO, NOT ONE, AND THAT IS THE WHOLE DEMONSTRATION. §5.7's lesson is that a
// project holding 350 GB of fast media and 300 GB of bulk has two different
// shares of two different pools, and one storage figure would be meaningless.
// A fixture with a single pool could show a percentage but never that.
//
// The ratios differ too: three-times replication on the block pool loses two
// thirds of the array, and a RAID6 bulk pool loses far less. That difference is
// invisible until somebody records the raw figure and lets the model divide.
func (b *builder) storagePools() {
	if !b.ok() {
		return
	}
	// The pools themselves. Parented to the rack they sit in, so containment
	// answers "where is this" like it does for everything else.
	pools := []struct {
		name, kind, parent string
		rawGB              int
	}{
		{"ceph-block", "ceph_3x", "rack-a2", 30720},
		{"nas-bulk", "raid6", "rack-a2", 61440},
	}
	for _, p := range pools {
		b.asset(domain.KindStorage, p.name, p.parent, []string{"prod"}, func(a *domain.Asset) {
			a.StorageKind, a.RawCapacityGB = str(p.kind), num(p.rawGB)
		})
	}
	if !b.ok() {
		return
	}

	// And what they cost, which is what WP-J4 divides by their usable capacity.
	// Two pools at very different prices per usable gigabyte -- fast replicated
	// media against bulk parity -- which is the difference §5.7 says one
	// storage figure would hide.
	b.assetCosts([]costLine{
		{"ceph-block", "acquisition", domain.CostOnce, 46000, -1100, "30 TB raw NVMe across three nodes"},
		{"ceph-block", "support", domain.CostYearly, 5200, -1100, ""},
		{"nas-bulk", "acquisition", domain.CostOnce, 18400, -1600, "60 TB raw, spinning"},
		{"nas-bulk", "support", domain.CostYearly, 1400, -1600, ""},
	})

	// What holds what. The database VMs carry their files on fast media and
	// their backups on bulk, which is the shape that produces two different
	// shares for one project.
	//
	// vm-proxy-1 is DELIBERATELY LEFT WITHOUT A CLAIM, for the same reason
	// hv-03 is left unmeasured: an estate where everything has been recorded
	// is not one anybody recognises, and the gap is what makes the honest
	// "cannot be attributed" line appear rather than being taken on trust.
	claims := []struct {
		asset, pool string
		gb          int
		note        string
	}{
		{"vm-db-1", "ceph-block", 400, "database files"},
		{"vm-db-2", "ceph-block", 400, "database files"},
		{"vm-app-1", "ceph-block", 50, "system disk"},
		{"vm-vault-1", "ceph-block", 50, "system disk"},
		{"vm-vault-2", "ceph-block", 50, "system disk"},
		{"vm-db-1", "nas-bulk", 1200, "nightly dumps, 14 days"},
		{"vm-db-2", "nas-bulk", 1200, "nightly dumps, 14 days"},
		{"vm-queue-1", "nas-bulk", 200, "message spool archive"},
	}
	for _, c := range claims {
		b.storageClaim(c.asset, c.pool, c.gb, c.note)
	}
}

// conditionalLicence puts a per-core licence on one host and names the guests
// it covers (§5.6).
//
// WITHOUT THIS THE FIXTURE COULD NOT SHOW THE RULE THAT BROKE THE DRAFT. Every
// seeded cost is universal, so the estate divided everything evenly and the
// whole of §5.6 was arithmetic nobody could see run. A per-core database
// licence benefits exactly the machines running that database; spread it across
// the cluster and every other workload subsidises them, while the total stays
// right and nothing prompts a reader to look.
//
// ONLY THE GUESTS OF THE HOST IT IS ATTACHED TO. A per-core licence is bought
// for a machine and grants unlimited guests ON THAT MACHINE, so naming a guest
// of a different hypervisor would be wrong twice over: wrong about what the
// licence covers, and unreachable from the form, which offers this host's
// workloads. Saving that form would then silently drop the off-host consumer.
// The first version named both database guests and one of them lives on hv-02.
func (b *builder) conditionalLicence() {
	if !b.ok() {
		return
	}
	host, ok := b.refs.Assets["hv-01"]
	if !ok {
		return
	}
	existing, err := b.store.ListAssetCosts(b.ctx, host)
	if err != nil {
		b.fail(fmt.Errorf("reading the costs on hv-01: %w", err))
		return
	}
	for _, line := range existing {
		if line.AppliesTo != domain.CostUniversal {
			return // already scoped, so a top-up neither rewrites nor duplicates
		}
	}

	b.assetCosts([]costLine{
		{"hv-01", "licence", domain.CostYearly, 7800, -700,
			"per-core database licence, covers the database guests only"},
	})
	if !b.ok() {
		return
	}

	// Find the line just written and scope it.
	lines, err := b.store.ListAssetCosts(b.ctx, host)
	if err != nil {
		b.fail(fmt.Errorf("re-reading the costs on hv-01: %w", err))
		return
	}
	var licence *domain.Cost
	for i := range lines {
		if lines[i].Kind == "licence" {
			c := lines[i].Cost
			licence = &c
			break
		}
	}
	if licence == nil {
		return
	}
	licence.AppliesTo = domain.CostConditional
	if err := b.store.UpdateAssetCost(b.ctx, Actor, licence); err != nil {
		b.fail(fmt.Errorf("scoping the licence: %w", err))
		return
	}

	consumers := []string{}
	for _, name := range []string{"vm-db-1"} {
		if id, ok := b.refs.Assets[name]; ok {
			consumers = append(consumers, id)
		}
	}
	if len(consumers) == 0 {
		return
	}
	if err := b.store.SetCostConsumers(b.ctx, Actor, licence.ID, consumers); err != nil {
		b.fail(fmt.Errorf("naming the licence consumers: %w", err))
	}
}

// sharedTenancy declares one machine as shared between engagements (WP-J5).
//
// THE CASE THE WHOLE WORK PACKAGE EXISTS FOR, and the fixture could not show it
// otherwise: every asset here belongs wholly to one project, so attribution had
// no example where ownership gives the wrong answer. An estate that packs
// tenants together to save on licensing is common enough that the CEO named it,
// and a shared box is exactly where a per-project cost figure quietly stops
// being true.
//
// DELIBERATELY DECLARED AT 90%, not 100. §5.4 says a total that is not a
// hundred is a finding rather than a silent rounding, and a fixture where every
// declaration is complete can demonstrate the arithmetic but never the
// discipline problem it exists to surface. The missing tenth is a real slice of
// a real machine that somebody is paying for and nobody has claimed -- which is
// the conversation the finding is meant to start.
func (b *builder) sharedTenancy() {
	if !b.ok() {
		return
	}
	assetID, ok := b.refs.Assets["vm-proxy-1"]
	if !ok {
		return
	}
	existing, err := b.store.OccupancyFor(b.ctx, assetID)
	if err != nil {
		b.fail(fmt.Errorf("reading occupancy: %w", err))
		return
	}
	if existing.Shared() {
		return // already declared, so a top-up neither rewrites nor duplicates
	}

	shares := []struct {
		project string
		percent int
		note    string
	}{
		{"platform", 50, "the shared ingress everything behind it depends on"},
		{"orders", 40, "agreed at the platform review; revisit when the second region lands"},
		// 90%: the remaining tenth is unclaimed on purpose. See above.
	}
	occupants := make([]domain.Occupant, 0, len(shares))
	for _, sh := range shares {
		id, ok := b.refs.Projects[sh.project]
		if !ok {
			continue
		}
		note := sh.note
		occupants = append(occupants, domain.Occupant{
			ProjectID: id, Percent: sh.percent, Note: &note,
		})
	}
	if len(occupants) == 0 {
		return
	}
	if err := b.store.SetOccupants(b.ctx, Actor, assetID, occupants); err != nil {
		b.fail(fmt.Errorf("declaring shared tenancy: %w", err))
	}
}

// attributeSuppliers records who invoices the estate's cost lines (WP-J6).
//
// WITHOUT THIS THE SUPPLIER REPORT RANKS NOTHING. The fixture priced everything
// and named nobody, so the CEO's third question -- which suppliers raise prices
// beyond inflation -- had a whole engine behind it and an empty table in front.
// Fifth time this fixture has needed that sentence.
//
// DELIBERATELY NOT EXHAUSTIVE. Several lines are left unattributed on purpose,
// because the report's honesty depends on saying what it could not rank, and an
// estate where every line names a supplier cannot demonstrate that. A reader who
// only ever sees a complete ranking will read every ranking as complete.
func (b *builder) attributeSuppliers() {
	if !b.ok() {
		return
	}

	// The suppliers the estate buys from, beyond the telcos the circuits
	// already named. A reseller and two hosting companies -- and the reseller is
	// the one whose renewals the report is meant to catch.
	suppliers := []struct{ name, account string }{
		{"Nordic IT Partner", "NIP-77120"},
		{"Hetzner Online", "HZ-4410932"},
		{"Scaleway", "SCW-88213"},
	}
	ids := map[string]string{}
	existing, err := b.store.ListProviders(b.ctx)
	if err != nil {
		b.fail(fmt.Errorf("listing suppliers: %w", err))
		return
	}
	for _, row := range existing {
		ids[row.Name] = row.ID
	}
	for _, sup := range suppliers {
		if _, ok := ids[sup.name]; ok {
			continue // already seeded, so a top-up neither rewrites nor duplicates
		}
		p, err := domain.NewProvider(store.NewID(), sup.name)
		if err != nil {
			b.fail(fmt.Errorf("building supplier %s: %w", sup.name, err))
			return
		}
		p.AccountRef = str(sup.account)
		if err := b.store.CreateProvider(b.ctx, Actor, p); err != nil {
			b.fail(fmt.Errorf("seeding supplier %s: %w", sup.name, err))
			return
		}
		ids[sup.name] = p.ID
	}

	// Who invoices what. The colo rack is the interesting one: its rent has been
	// repriced twice (see coloRentRenewals), so the reseller carries a real
	// movement to judge against inflation rather than a single figure.
	attribution := []struct{ asset, supplier string }{
		{"colo-rack-07", "Nordic IT Partner"},
		{"hv-01", "Nordic IT Partner"},
		{"hv-02", "Nordic IT Partner"},
		{"srv-hz-1", "Hetzner Online"},
		{"srv-hz-2", "Hetzner Online"},
		{"vps-stg-1", "Scaleway"},
		{"vps-ci-1", "Scaleway"},
		// hv-03, the switches and the firewalls are LEFT UNATTRIBUTED on
		// purpose. See the doc comment.
	}
	b.attributeCircuitCosts(ids)
	for _, a := range attribution {
		assetID, ok := b.refs.Assets[a.asset]
		if !ok {
			continue // a deployment without this layer
		}
		supplierID, ok := ids[a.supplier]
		if !ok {
			continue
		}
		lines, err := b.store.ListAssetCosts(b.ctx, assetID)
		if err != nil {
			b.fail(fmt.Errorf("reading costs of %s: %w", a.asset, err))
			return
		}
		for i := range lines {
			if lines[i].Lifecycle == domain.LifecycleRetired {
				continue
			}
			if lines[i].ProviderID != nil {
				continue // already attributed
			}
			c := lines[i].Cost
			c.ProviderID = &supplierID
			if err := b.store.UpdateAssetCost(b.ctx, Actor, &c); err != nil {
				b.fail(fmt.Errorf("attributing %s: %w", a.asset, err))
				return
			}
		}
	}
}

// attributeCircuitCosts hands each circuit's cost lines to the telco that
// provides it.
//
// THE ONE ATTRIBUTION THAT NEEDS NO JUDGEMENT. A circuit already names its
// provider, and the invoice for that circuit comes from them -- so leaving those
// lines unattributed would be withholding a fact the estate already holds. It is
// also the case that makes the report's meaning clear: the telcos appear beside
// the resellers, because a supplier is a supplier however different the product.
func (b *builder) attributeCircuitCosts(ids map[string]string) {
	if !b.ok() {
		return
	}
	circuits, err := b.store.ListCircuits(b.ctx)
	if err != nil {
		b.fail(fmt.Errorf("listing circuits: %w", err))
		return
	}
	for _, ci := range circuits {
		if ci.Lifecycle == domain.LifecycleRetired || ci.ProviderID == "" {
			continue
		}
		lines, err := b.store.ListCircuitCosts(b.ctx, ci.ID)
		if err != nil {
			b.fail(fmt.Errorf("reading costs of circuit %s: %w", ci.CID, err))
			return
		}
		for i := range lines {
			if lines[i].Lifecycle == domain.LifecycleRetired || lines[i].ProviderID != nil {
				continue
			}
			c := lines[i].Cost
			provider := ci.ProviderID
			c.ProviderID = &provider
			if err := b.store.UpdateCircuitCost(b.ctx, Actor, &c); err != nil {
				b.fail(fmt.Errorf("attributing circuit %s: %w", ci.CID, err))
				return
			}
		}
	}
}
