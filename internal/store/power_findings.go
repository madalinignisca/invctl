// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/madalinignisca/invctl/internal/domain"
)

// What the power model is actually for.
//
// The outage simulation is the cheap half: a feed resolves to the assets that
// lose it, and impact.Request already takes DownAssetIDs. These are the
// expensive half and the reason the tables exist -- three questions nobody can
// answer from a spreadsheet, because each needs the whole chain traced at once.

// PowerFindingKind names the three.
const (
	// FindingFalseRedundancy: two or more inputs, believed to be A and B, whose
	// feeds trace to ONE panel. Not redundancy -- two cables to a single point
	// of failure. Nobody discovers this during normal running.
	FindingFalseRedundancy = "false_redundancy"
	// FindingSingleFed: one input, on something carrying services.
	FindingSingleFed = "single_fed"
	// FindingOverAllocated: declared draw exceeding a feed's derated capacity.
	FindingOverAllocated = "over_allocated"
)

// Severity separates a fault from the design.
//
// Two inputs meeting at a UPS die together and that is a finding. Two inputs
// meeting only at the generator is the ordinary 2N build -- the generator is
// what makes a utility failure survivable, and calling it a single point of
// failure reports the safety measure as the hazard. It is still worth SAYING
// where they converge; it is just not an alarm.
const (
	PowerSeverityFault    = "fault"
	PowerSeverityExpected = "expected"
)

// PowerFinding is one thing worth doing something about.
type PowerFinding struct {
	Kind string
	// EntityType and EntityID are what to open: an asset for the first two, a
	// feed for the third.
	EntityType string
	EntityID   string
	Name       string
	// Detail is the finding in a sentence, already assembled -- templates must
	// not do arithmetic and must not decide what is wrong.
	Detail string
	// ServiceCount and BestTier answer "so what", the same way the expiry report
	// does. A single-fed switch with nothing behind it and a single-fed
	// hypervisor carrying a tier-1 database are not the same problem.
	ServiceCount int
	BestTier     int
	// Panels names what the inputs trace to, for a false-redundancy finding.
	Panels []string
	// Severity is PowerSeverityFault or PowerSeverityExpected. Only convergence
	// findings carry it; the others are always faults.
	Severity string
}

// PowerReport is every finding plus the numbers that keep them honest.
type PowerReport struct {
	Findings []PowerFinding

	// Counts by kind, so a summary need not walk the list.
	FalseRedundancy int
	SingleFed       int
	OverAllocated   int
	// SharedUpstream is the convergences that are the design rather than a
	// fault -- counted separately so they never inflate the alarming number.
	SharedUpstream int

	// The honesty numbers, and they matter more here than in most reports. An
	// estate with three findings over four modelled assets is not a healthy
	// estate; it is an unmodelled one. Without these the report reads as
	// reassurance.
	Assets          int // live assets with at least one power input
	UnmodelledSites int // live sites with no panel at all
	UndeclaredDraw  int // live inputs with no draw recorded
	UnratedFeeds    int // live feeds whose capacity cannot be computed
	// UnsourcedPanels is how many live panels name no supply. It is the number
	// that decides how much the redundancy findings are worth: with nothing
	// above the boards, two panels look independent whether or not they are, and
	// a silent report means "not known" rather than "checked and fine".
	UnsourcedPanels int
}

// powerLink is one input, flattened with everything behind it.
type powerLink struct {
	AssetID   string `db:"asset_id"`
	AssetName string `db:"asset_name"`
	InputName string `db:"input_name"`
	FeedID    string `db:"feed_id"`
	FeedName  string `db:"feed_name"`
	PanelID   string `db:"panel_id"`
	PanelName string `db:"panel_name"`
	// SourceID is the panel's supply, when one is recorded. nil is what makes a
	// pair of panels look independent whether or not they are, which is why the
	// report counts unsourced panels.
	SourceID *string `db:"source_id"`
	DrawVA   *int    `db:"draw_va"`
}

// PowerFindings traces the chain and reports what it finds.
//
// One query for the whole chain, then the analysis in Go. Not because SQL could
// not group it, but because "these two feeds share a panel" is a rule somebody
// has to be able to read and change, and a correlated subquery expressing it is
// a rule nobody will touch. The row count here is inputs, not assets, and an
// estate has a few thousand at most.
func (s *SQLStore) PowerFindings(ctx context.Context) (*PowerReport, error) {
	var links []powerLink
	err := s.read(ctx, &links, `
		SELECT i.asset_id, a.name AS asset_name, i.name AS input_name,
		       i.feed_id, f.name AS feed_name, f.panel_id, p.name AS panel_name,
		       p.source_id, i.draw_va
		FROM power_input i
		JOIN asset a       ON a.id = i.asset_id
		JOIN power_feed f  ON f.id = i.feed_id
		JOIN power_panel p ON p.id = f.panel_id
		WHERE i.lifecycle <> ? AND a.lifecycle <> ?
		  AND f.lifecycle <> ? AND p.lifecycle <> ?
		ORDER BY a.name, i.name`,
		domain.LifecycleRetired, domain.LifecycleRetired,
		domain.LifecycleRetired, domain.LifecycleRetired)
	if err != nil {
		return nil, fmt.Errorf("tracing the power chain: %w", err)
	}

	report := &PowerReport{}
	byAsset := map[string][]powerLink{}
	var order []string
	for _, l := range links {
		if _, seen := byAsset[l.AssetID]; !seen {
			order = append(order, l.AssetID)
		}
		byAsset[l.AssetID] = append(byAsset[l.AssetID], l)
	}
	// Not len(byAsset) any more -- assetsWithPowerInput runs the identical
	// definition ("live assets with at least one live power input, feed and
	// panel") as its own SQL statement, factored out exactly like
	// unmodelledSites was, so DeclaredPowerDraw (power_cost.go) can carry the
	// SAME comparator rather than inventing a second definition of "modelled"
	// (§4c.17). This costs one extra round trip PowerFindings did not used to
	// make; single source of truth for a count two different reports quote
	// against each other is worth it.
	assets, err := s.assetsWithPowerInput(ctx)
	if err != nil {
		return nil, err
	}
	report.Assets = assets

	workload, err := s.powerWorkload(ctx, order)
	if err != nil {
		return nil, err
	}

	supplies, err := s.loadSupplyChains(ctx)
	if err != nil {
		return nil, err
	}

	for _, assetID := range order {
		inputs := byAsset[assetID]
		w := workload[assetID]

		if len(inputs) >= 2 {
			if shared, ok := lowestSharedAncestor(inputs, supplies); ok {
				report.Findings = append(report.Findings, shared.finding(assetID, inputs, w))
				if shared.severity == PowerSeverityFault {
					report.FalseRedundancy++
				} else {
					report.SharedUpstream++
				}
			}
			continue
		}

		// SINGLE-FED, BUT ONLY WHERE IT MATTERS. Reporting every single-fed
		// patch panel and office switch would bury the findings above in a list
		// nobody reads, and most things in a real estate are single-fed on
		// purpose. Carrying a service is the signal that somebody chose to
		// depend on it.
		if len(inputs) == 1 && w.services > 0 {
			report.Findings = append(report.Findings, PowerFinding{
				Kind: FindingSingleFed, EntityType: "asset", EntityID: assetID,
				Name:     inputs[0].AssetName,
				Severity: PowerSeverityFault,
				Detail: fmt.Sprintf(
					"one input, on feed %s from panel %s, and %s ride on it",
					inputs[0].FeedName, inputs[0].PanelName,
					countOf(w.services, "service", "services")),
				ServiceCount: w.services, BestTier: w.tier,
			})
			report.SingleFed++
		}
	}

	if err := s.powerUtilisation(ctx, report); err != nil {
		return nil, err
	}
	if err := s.powerCoverage(ctx, report); err != nil {
		return nil, err
	}
	return report, nil
}

type powerWorkloadRow struct {
	services int
	tier     int
}

// powerWorkload counts what rides on each asset, THROUGH CONTAINMENT.
//
// The same closure join the expiry report uses, and the same reason: losing
// power to a rack takes everything inside it, so a rack's workload is the sum of
// its contents. Reusing the shape rather than inventing a second answer to "does
// anything depend on this".
func (s *SQLStore) powerWorkload(ctx context.Context, assetIDs []string) (map[string]powerWorkloadRow, error) {
	out := map[string]powerWorkloadRow{}
	if len(assetIDs) == 0 {
		return out, nil
	}
	var rows []struct {
		AssetID  string `db:"asset_id"`
		Services int    `db:"service_count"`
		BestTier int    `db:"best_tier"`
	}
	args := append(anySlice(assetIDs), domain.LifecycleRetired, domain.LifecycleRetired)
	err := s.read(ctx, &rows, `
		SELECT c.ancestor_id AS asset_id,
		       COUNT(DISTINCT s.id) AS service_count,
		       MIN(s.tier) AS best_tier
		FROM asset_closure c
		JOIN service_instance si ON si.host_asset_id = c.descendant_id
		JOIN service s           ON s.id = si.service_id
		WHERE c.ancestor_id IN (`+placeholders(len(assetIDs))+`)
		  AND si.lifecycle <> ? AND s.lifecycle <> ?
		GROUP BY c.ancestor_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("counting what rides on powered assets: %w", err)
	}
	for _, r := range rows {
		out[r.AssetID] = powerWorkloadRow{services: r.Services, tier: r.BestTier}
	}
	return out, nil
}

// powerUtilisation reports feeds whose declared load exceeds their derating.
//
// A feed with no rating is NOT a finding -- it is counted as unrated instead.
// Reporting it would be reporting a gap in the record as a fault in the estate,
// and the two need different work from different people.
func (s *SQLStore) powerUtilisation(ctx context.Context, report *PowerReport) error {
	feeds, err := s.ListPowerFeeds(ctx, PowerFeedFilter{})
	if err != nil {
		return err
	}
	for _, f := range feeds {
		usable, ok := f.UsableVA()
		if !ok {
			report.UnratedFeeds++
			continue
		}
		if f.AllocatedVA <= usable {
			continue
		}
		detail := fmt.Sprintf("%d VA allocated against %d VA usable (%d%% of %d VA)",
			f.AllocatedVA, usable, f.MaxUtilisation, mustCapacity(f))
		if f.UndeclaredInputs > 0 {
			// The number is already over, and it is over by AT LEAST this much:
			// saying so stops a reader treating the figure as the whole story.
			detail += fmt.Sprintf(", and %d of its inputs declare no draw at all",
				f.UndeclaredInputs)
		}
		report.Findings = append(report.Findings, PowerFinding{
			Kind: FindingOverAllocated, EntityType: "power_feed", EntityID: f.ID,
			Name:   f.PanelName + " / " + f.Name,
			Detail: detail,
		})
		report.OverAllocated++
	}
	return nil
}

func mustCapacity(f PowerFeedRow) int {
	c, _ := f.CapacityVA()
	return c
}

// powerCoverage counts what is NOT modelled, which is what stops a short report
// reading as a clean bill of health.
func (s *SQLStore) powerCoverage(ctx context.Context, report *PowerReport) error {
	if err := s.readOne(ctx, &report.UndeclaredDraw, `
		SELECT COUNT(*) FROM power_input WHERE draw_va IS NULL AND lifecycle <> ?`,
		domain.LifecycleRetired); err != nil {
		return fmt.Errorf("counting inputs with no declared draw: %w", err)
	}
	// Sites with no panel at all. Not "assets with no input" -- almost nothing
	// in a rack has its own input modelled and never will; the question worth
	// asking is whether a LOCATION has any power model behind it.
	if err := s.readOne(ctx, &report.UnsourcedPanels, `
		SELECT COUNT(*) FROM power_panel WHERE source_id IS NULL AND lifecycle <> ?`,
		domain.LifecycleRetired); err != nil {
		return fmt.Errorf("counting panels with no supply: %w", err)
	}
	n, err := s.unmodelledSites(ctx)
	if err != nil {
		return err
	}
	report.UnmodelledSites = n
	return nil
}

// unmodelledSites counts live sites with no live power panel at all --
// factored out of powerCoverage above so DeclaredPowerDraw (power_cost.go)
// can carry the identical count without a second copy of this query. B3
// (§4b.9) found the cost report had dropped this exact fact after D3's
// amendment over-generalised its own objection to a DIFFERENT allowlist
// shape; the query stays here, next to the report it was written for, and
// power_cost.go calls it rather than restating it.
//
// Not "assets with no input" -- almost nothing in a rack has its own input
// modelled and never will; the question worth asking is whether a LOCATION
// has any power model behind it at all.
func (s *SQLStore) unmodelledSites(ctx context.Context) (int, error) {
	var n int
	if err := s.readOne(ctx, &n, `
		SELECT COUNT(*) FROM asset a
		WHERE a.kind = ? AND a.lifecycle <> ?
		  AND NOT EXISTS (SELECT 1 FROM power_panel p
		                  WHERE p.site_id = a.id AND p.lifecycle <> ?)`,
		domain.KindSite, domain.LifecycleRetired, domain.LifecycleRetired); err != nil {
		return 0, fmt.Errorf("counting sites with no power model: %w", err)
	}
	return n, nil
}

// assetsWithPowerInput counts live assets carrying at least one live power
// input on a live feed under a live panel -- exactly the join PowerFindings
// already runs to build byAsset, factored out here for the same reason
// unmodelledSites was: so DeclaredPowerDraw (power_cost.go) can carry the
// IDENTICAL count as a comparator rather than a second, possibly-drifting
// definition of "modelled" (§4c.17). "3 of 47 assets that have a power input
// declared a draw" only means what it says if both halves of that sentence
// come from the same query.
func (s *SQLStore) assetsWithPowerInput(ctx context.Context) (int, error) {
	var n int
	if err := s.readOne(ctx, &n, `
		SELECT COUNT(DISTINCT i.asset_id)
		FROM power_input i
		JOIN asset a       ON a.id = i.asset_id
		JOIN power_feed f  ON f.id = i.feed_id
		JOIN power_panel p ON p.id = f.panel_id
		WHERE i.lifecycle <> ? AND a.lifecycle <> ?
		  AND f.lifecycle <> ? AND p.lifecycle <> ?`,
		domain.LifecycleRetired, domain.LifecycleRetired,
		domain.LifecycleRetired, domain.LifecycleRetired); err != nil {
		return 0, fmt.Errorf("counting assets with a power input: %w", err)
	}
	return n, nil
}

// AssetsLosingPower resolves a set of failed feeds to the assets that actually
// go dark.
//
// AN ASSET GOES DOWN ONLY WHEN EVERY ONE OF ITS LIVE INPUTS IS ON A FAILED FEED.
// This is the whole point of modelling redundancy: an A+B asset losing one feed
// keeps running, and a resolver that returned everything attached to the feed
// would model redundancy and then ignore it -- producing an impact simulation
// that is wrong in the reassuring direction for single-fed kit and wrong in the
// alarming direction for everything else.
//
// The result is DownAssetIDs for impact.Request. Containment is not expanded
// here: the engine already does that, so a rack losing power takes its contents
// through the closure table rather than through a second walk.
func (s *SQLStore) AssetsLosingPower(ctx context.Context, feedIDs []string) ([]string, error) {
	if len(feedIDs) == 0 {
		return nil, nil
	}
	var links []powerLink
	err := s.read(ctx, &links, `
		SELECT i.asset_id, a.name AS asset_name, i.name AS input_name,
		       i.feed_id, f.name AS feed_name, f.panel_id, p.name AS panel_name,
		       p.source_id, i.draw_va
		FROM power_input i
		JOIN asset a       ON a.id = i.asset_id
		JOIN power_feed f  ON f.id = i.feed_id
		JOIN power_panel p ON p.id = f.panel_id
		WHERE i.lifecycle <> ? AND a.lifecycle <> ?
		ORDER BY i.asset_id`,
		domain.LifecycleRetired, domain.LifecycleRetired)
	if err != nil {
		return nil, fmt.Errorf("resolving what loses power: %w", err)
	}

	failed := make(map[string]bool, len(feedIDs))
	for _, id := range feedIDs {
		failed[id] = true
	}

	total := map[string]int{}
	lost := map[string]int{}
	var order []string
	for _, l := range links {
		if total[l.AssetID] == 0 {
			order = append(order, l.AssetID)
		}
		total[l.AssetID]++
		if failed[l.FeedID] {
			lost[l.AssetID]++
		}
	}

	var down []string
	for _, id := range order {
		if lost[id] > 0 && lost[id] == total[id] {
			down = append(down, id)
		}
	}
	return down, nil
}

// countOf renders "1 service" / "3 services" for a finding sentence.
func countOf(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// joinNames renders a list for a sentence: "A and B", "A, B and C".
func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return "no inputs"
	case 1:
		return "input " + names[0]
	case 2:
		return "inputs " + names[0] + " and " + names[1]
	}
	return "inputs " + strings.Join(names[:len(names)-1], ", ") +
		" and " + names[len(names)-1]
}

// ---------- tracing the supply chain ----------

// supplyNode is one link in the chain above a panel.
type supplyNode struct {
	ID     string  `db:"id"`
	Parent *string `db:"parent_id"`
	Name   string  `db:"name"`
	Kind   string  `db:"kind"`
}

// loadSupplyChains reads every live supply once.
func (s *SQLStore) loadSupplyChains(ctx context.Context) (map[string]supplyNode, error) {
	var rows []supplyNode
	err := s.read(ctx, &rows,
		`SELECT id, parent_id, name, kind FROM power_source WHERE lifecycle <> ?`,
		domain.LifecycleRetired)
	if err != nil {
		return nil, fmt.Errorf("reading the supply chain: %w", err)
	}
	out := make(map[string]supplyNode, len(rows))
	for _, r := range rows {
		out[r.ID] = r
	}
	return out, nil
}

// sharedAncestor is where every input of one asset converges.
type sharedAncestor struct {
	label    string
	what     string // "feed", "panel" or a source kind
	severity string
}

func (a sharedAncestor) finding(assetID string, inputs []powerLink, w powerWorkloadRow) PowerFinding {
	names := make([]string, 0, len(inputs))
	for _, l := range inputs {
		names = append(names, l.InputName)
	}
	verdict := "one failure takes all of them"
	if a.severity == PowerSeverityExpected {
		verdict = "which is the usual design rather than a fault"
	}
	return PowerFinding{
		Kind: FindingFalseRedundancy, EntityType: "asset", EntityID: assetID,
		Name:     inputs[0].AssetName,
		Severity: a.severity,
		Detail: fmt.Sprintf("%s converge on %s %s — %s",
			joinNames(sortedStrings(names)), a.what, a.label, verdict),
		ServiceCount: w.services, BestTier: w.tier,
		Panels: []string{a.label},
	}
}

// lowestSharedAncestor walks every input up to the top of its chain and finds
// the most specific thing they ALL pass through.
//
// ALL, not some. An asset with three inputs where two share a panel survives
// losing that panel, because the third is still live -- so the question is only
// ever whether there is a single thing whose failure takes every input at once.
//
// The path for one input is [feed, panel, source, source's parent, …]. Because
// each is a chain, the shared portion is a suffix, and the first node of the
// first input's path that appears in every other path is the lowest one.
func lowestSharedAncestor(inputs []powerLink, supplies map[string]supplyNode) (sharedAncestor, bool) {
	paths := make([]map[string]bool, len(inputs))
	for i, l := range inputs {
		paths[i] = map[string]bool{}
		for _, id := range supplyPath(l, supplies) {
			paths[i][id] = true
		}
	}

	for _, id := range supplyPath(inputs[0], supplies) {
		shared := true
		for i := 1; i < len(paths); i++ {
			if !paths[i][id] {
				shared = false
				break
			}
		}
		if !shared {
			continue
		}
		switch id {
		case inputs[0].FeedID:
			// Every input on ONE feed. Not two cables to one board -- two cables
			// to one breaker.
			return sharedAncestor{label: inputs[0].FeedName, what: "feed",
				severity: PowerSeverityFault}, true
		case inputs[0].PanelID:
			return sharedAncestor{label: inputs[0].PanelName, what: "panel",
				severity: PowerSeverityFault}, true
		}
		node, ok := supplies[id]
		if !ok {
			continue
		}
		severity := PowerSeverityExpected
		if domain.SharingIsAFault(node.Kind) {
			severity = PowerSeverityFault
		}
		return sharedAncestor{label: node.Name, what: strings.ReplaceAll(node.Kind, "_", " "),
			severity: severity}, true
	}
	return sharedAncestor{}, false
}

// supplyPath is one input's chain, most specific first.
//
// Bounded, like every walk up this tree. A cycle is refused on the way in and
// the depth guard is what stops a report hanging if that is ever wrong -- a hung
// page is worse than a wrong one, because nobody can see what it was going to
// say.
func supplyPath(l powerLink, supplies map[string]supplyNode) []string {
	path := []string{l.FeedID, l.PanelID}
	cur := l.SourceID
	for depth := 0; cur != nil && depth < supplyDepthLimit; depth++ {
		node, ok := supplies[*cur]
		if !ok {
			break
		}
		path = append(path, node.ID)
		cur = node.Parent
	}
	return path
}

// AssetsLosingSupply resolves a failed supply to the assets that go dark.
//
// A supply failing takes everything BENEATH it: the boards it feeds, the boards
// fed by supplies it feeds, and the feeds off all of them. So this walks the
// chain DOWN, collects the feeds, and hands them to AssetsLosingPower -- which
// already knows that an asset survives while any one of its inputs is on a live
// feed. Losing UPS-A takes a server dual-fed across two of its boards; it does
// not take one fed from UPS-A and UPS-B.
//
// Written as a resolver over the existing one rather than a second traversal,
// because the redundancy rule must have exactly one implementation. A supply
// outage that honoured redundancy differently from a feed outage would be two
// answers to one question.
func (s *SQLStore) AssetsLosingSupply(ctx context.Context, sourceIDs []string) ([]string, error) {
	if len(sourceIDs) == 0 {
		return nil, nil
	}
	supplies, err := s.loadSupplyChains(ctx)
	if err != nil {
		return nil, err
	}

	// Everything at or below the failed supplies. Bounded by the same depth
	// limit as every other walk here: a cycle is refused on the way in, and a
	// guard that trusts that is one that hangs a page if it is ever wrong.
	failed := map[string]bool{}
	for _, id := range sourceIDs {
		failed[id] = true
	}
	for depth := 0; depth < supplyDepthLimit; depth++ {
		grew := false
		for id, node := range supplies {
			if failed[id] || node.Parent == nil || !failed[*node.Parent] {
				continue
			}
			failed[id] = true
			grew = true
		}
		if !grew {
			break
		}
	}

	// Filtered in Go against the closure computed above, rather than as a
	// recursive query the two engines would have to agree on.
	var panelFeeds []struct {
		FeedID   string `db:"feed_id"`
		SourceID string `db:"source_id"`
	}
	err = s.read(ctx, &panelFeeds, `
		SELECT f.id AS feed_id, p.source_id
		FROM power_feed f
		JOIN power_panel p ON p.id = f.panel_id
		WHERE f.lifecycle <> ? AND p.lifecycle <> ? AND p.source_id IS NOT NULL`,
		domain.LifecycleRetired, domain.LifecycleRetired)
	if err != nil {
		return nil, fmt.Errorf("resolving feeds below a supply: %w", err)
	}
	var lost []string
	for _, pf := range panelFeeds {
		if failed[pf.SourceID] {
			lost = append(lost, pf.FeedID)
		}
	}
	if len(lost) == 0 {
		return nil, nil
	}
	return s.AssetsLosingPower(ctx, lost)
}
