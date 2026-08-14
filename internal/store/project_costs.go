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
	"sort"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
)

// What a project costs, in the same three buckets its dependencies already use,
// because they are the same three conversations:
//
//	YOURS            what you own, plus what that implies -- your budget
//	ON YOUR ESTATE   things inside yours that another project owns -- shown, never added
//	YOU USE          what you lean on and somebody else pays for -- context, never added
//
// Only the first is a number this project is answerable for. The other two are
// rendered beside it precisely so that nobody adds them: a total that quietly
// absorbed the hypervisor another team owns would double-count it the moment
// that team totalled their own estate, and both pages would look right alone.
//
// EVERY FIGURE HERE IS DERIVED AT READ TIME AND STORED NOWHERE. Writing a rolled
// -up total back would make a derived number indistinguishable from a declared
// one in change_log -- the laundering AUDIT.md rule 7 exists to prevent -- and it
// would go stale the moment a VM moved or a contract renewed.

// ProjectCostSummary is the cost panel on a project overview.
type ProjectCostSummary struct {
	// On is the date the totals are computed for, echoed back so the page can
	// say what "current" meant rather than implying it is timeless.
	On string

	// Own is what the project is answerable for: cost lines on the project
	// itself, on what it owns, and on what owning that implies -- minus
	// anything another project owns outright.
	Own domain.CostTotals
	// Direct is the part of Own attached to the project itself rather than to
	// any asset or service: SaaS, retainers, domains. Broken out because it is
	// the part that appears on no infrastructure page and would otherwise look
	// like a rounding error somebody cannot trace.
	Direct domain.CostTotals

	// Elsewhere is spend on things inside this project's footprint that another
	// project owns. NOT part of Own. This is the subsidy line: it says what is
	// sitting on your estate at somebody else's expense, or vice versa.
	Elsewhere domain.CostTotals
	// ElsewhereOwners names the projects that carry it, so the reader knows who
	// to talk to rather than only that somebody exists.
	ElsewhereOwners []string

	// Shared is spend on things this project declared it USES. Never part of
	// Own -- deriving through `uses` is how one team's estate gets attributed to
	// whoever declared a link first.
	Shared domain.CostTotals

	// Undated counts things in the footprint carrying no cost line at all. The
	// number that makes the rest honest: a total over three priced assets in a
	// footprint of forty is not a budget, it is a sample.
	Priced   int
	Unpriced int
}

// HasAnything reports whether a single figure was recorded anywhere.
func (s *ProjectCostSummary) HasAnything() bool {
	return !s.Own.IsZero() || !s.Elsewhere.IsZero() || !s.Shared.IsZero()
}

// ProjectCosts totals a project's footprint.
//
// It takes the footprint rather than recomputing it, so the cost panel and the
// membership panel above it can never disagree about what the project consists
// of -- two derivations of the same set is how a page starts contradicting
// itself.
func (s *SQLStore) ProjectCosts(ctx context.Context, f *ProjectFootprint, asOf time.Time) (*ProjectCostSummary, error) {
	on := domain.FormatDate(asOf)
	summary := &ProjectCostSummary{On: on}

	// Which implied things another project owns. An implied asset with a
	// declared owner elsewhere is already computed as a footprint conflict;
	// implied SERVICES need the same question asked, because a service somebody
	// else owns running on your hardware is the same situation one layer up.
	foreignAssets := make(map[string]bool, len(f.Conflicts))
	ownerNames := map[string]bool{}
	for _, c := range f.Conflicts {
		foreignAssets[c.AssetID] = true
		ownerNames[c.ProjectName] = true
	}

	impliedServiceIDs := make([]string, len(f.ImpliedServices))
	for i, svc := range f.ImpliedServices {
		impliedServiceIDs[i] = svc.ID
	}
	serviceOwners, err := s.ownersOfServices(ctx, impliedServiceIDs)
	if err != nil {
		return nil, err
	}
	foreignServices := make(map[string]bool, len(serviceOwners))
	for id, owner := range serviceOwners {
		if owner.ProjectID != f.ProjectID {
			foreignServices[id] = true
			ownerNames[owner.ProjectName] = true
		}
	}

	// Assets: owned outright, plus implied, minus the ones somebody else owns.
	ownAssetIDs := append([]string{}, f.OwnedAssetIDs...)
	var elsewhereAssetIDs []string
	for _, a := range f.ImpliedAssets {
		if foreignAssets[a.ID] {
			elsewhereAssetIDs = append(elsewhereAssetIDs, a.ID)
			continue
		}
		ownAssetIDs = append(ownAssetIDs, a.ID)
	}

	// Services: the same shape. An implied service another project owns is
	// their licence to pay, however much of your hardware it is running on.
	ownServiceIDs := append([]string{}, f.OwnedServiceIDs...)
	var elsewhereServiceIDs []string
	for _, svc := range f.ImpliedServices {
		if foreignServices[svc.ID] {
			elsewhereServiceIDs = append(elsewhereServiceIDs, svc.ID)
			continue
		}
		ownServiceIDs = append(ownServiceIDs, svc.ID)
	}

	assetCosts, err := s.costsFor(ctx, costOnAsset,
		concatIDs(ownAssetIDs, elsewhereAssetIDs, f.UsedAssetIDs))
	if err != nil {
		return nil, err
	}
	serviceCosts, err := s.costsFor(ctx, costOnService,
		concatIDs(ownServiceIDs, elsewhereServiceIDs, f.UsedServiceIDs))
	if err != nil {
		return nil, err
	}
	// Circuits. No Elsewhere split, and that is not an omission: the partial
	// unique index in 00041 allows only one owner, so a circuit this project
	// does not own is one it merely uses -- there is no third case where
	// another project owns something inside our footprint, because a circuit
	// contains nothing.
	circuitCosts, err := s.costsFor(ctx, costOnCircuit,
		concatIDs(f.OwnedCircuitIDs, f.UsedCircuitIDs))
	if err != nil {
		return nil, err
	}
	projectCosts, err := s.ListProjectCosts(ctx, f.ProjectID)
	if err != nil {
		return nil, err
	}

	// Deduplicated by id before totalling. An asset can be reachable as both
	// owned and implied, and a service can be both owned and running on owned
	// hardware -- counting either twice is the exact failure this whole design
	// is arranged to avoid, and it would look like a plausible number.
	counted := map[string]bool{}
	fold := func(ids []string, costs map[string][]CostRow, into *domain.CostTotals) {
		for _, id := range ids {
			if counted[id] {
				continue
			}
			counted[id] = true
			lines := costs[id]
			if len(lines) == 0 {
				summary.Unpriced++
				continue
			}
			summary.Priced++
			totals := TotalCosts(lines, on)
			*into = into.Plus(totals)
		}
	}

	fold(ownAssetIDs, assetCosts, &summary.Own)
	fold(ownServiceIDs, serviceCosts, &summary.Own)
	fold(elsewhereAssetIDs, assetCosts, &summary.Elsewhere)
	fold(elsewhereServiceIDs, serviceCosts, &summary.Elsewhere)
	fold(f.UsedAssetIDs, assetCosts, &summary.Shared)
	fold(f.UsedServiceIDs, serviceCosts, &summary.Shared)
	fold(f.OwnedCircuitIDs, circuitCosts, &summary.Own)
	fold(f.UsedCircuitIDs, circuitCosts, &summary.Shared)

	summary.Direct = TotalCosts(projectCosts, on)
	summary.Own = summary.Own.Plus(summary.Direct)

	for name := range ownerNames {
		summary.ElsewhereOwners = append(summary.ElsewhereOwners, name)
	}
	sort.Strings(summary.ElsewhereOwners)
	return summary, nil
}

// concatIDs joins id lists for a single lookup.
func concatIDs(lists ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range lists {
		for _, id := range list {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}
