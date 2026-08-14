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
	"sort"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
)

// What the estate costs, gathered from everywhere money can be attached.
//
// THE COVERAGE FIGURES ARE NOT A FOOTNOTE, they are the reason this page can be
// believed. Every other report here reports what it cannot see -- the power
// report names the sites with no board, expiry names the things with no date --
// and a total is the number most likely to be quoted and least likely to be
// questioned. "€8,400 a month" reads as what the estate costs. It is what the
// estate costs *that somebody has priced*, and when eleven of ninety assets
// carry a figure those are different sentences.
//
// So the totals and the counts are one type, returned together, and the page
// renders them together. Splitting them would let a caller take the first
// without the second, which is exactly how the misreading happens.

// EstateCostSurface is one place money attaches, with what was found there.
type EstateCostSurface struct {
	// Name is what a reader calls these -- "assets", "circuits".
	Name string
	// Totals over the ACTIVE lines on this surface, at the date asked for.
	Totals domain.CostTotals
	// Priced is how many entities of this kind carry at least one live cost
	// line; Total is how many exist. The difference is the report's own
	// uncertainty, and it belongs beside the money rather than under it.
	Priced int
	Total  int
}

// Unpriced is what carries no cost line at all.
func (s EstateCostSurface) Unpriced() int { return s.Total - s.Priced }

// Coverage is the share of this surface that has been priced, 0-100.
//
// Integer arithmetic on purpose: this is a proportion for a human to read, not
// a figure anything computes from, and a float here would be the only one on a
// page that is otherwise exact.
func (s EstateCostSurface) Coverage() int {
	if s.Total == 0 {
		return 0
	}
	return s.Priced * 100 / s.Total
}

// EstateCostReport is what the whole estate costs, and how much of it is known.
type EstateCostReport struct {
	// On is the date the totals were computed for, echoed back so the page can
	// say what "current" meant rather than implying it is timeless. Same
	// reasoning as ProjectCostSummary.On.
	On string
	// Totals across every surface. A sum of sums, and it inherits every
	// surface's uncertainty -- which is why Surfaces travels with it.
	Totals domain.CostTotals
	// Surfaces in a fixed order, so the page does not reorder itself between
	// visits as counts change.
	Surfaces []EstateCostSurface
	// Largest recurring lines, biggest first. What dominates the run rate is
	// the first question anybody asks of a total, and without it the reader
	// goes hunting through four list pages to find it.
	Largest []LargestCostLine
}

// PricedEntities and TotalEntities roll the surfaces up for a headline.
func (r EstateCostReport) PricedEntities() int {
	n := 0
	for _, s := range r.Surfaces {
		n += s.Priced
	}
	return n
}

func (r EstateCostReport) TotalEntities() int {
	n := 0
	for _, s := range r.Surfaces {
		n += s.Total
	}
	return n
}

// FullyPriced reports whether every cost-bearing thing carries a figure, which
// is the only state in which the totals are a total rather than a floor.
func (r EstateCostReport) FullyPriced() bool {
	return r.TotalEntities() > 0 && r.PricedEntities() == r.TotalEntities()
}

// LargestCostLine is one recurring cost with enough context to act on it.
type LargestCostLine struct {
	Surface     string
	OwnerID     string
	OwnerLabel  string
	Kind        string
	MonthlyCost int64
	// Href is where the reader goes to see it, built here because the surface
	// decides the path and the template must not learn four of them.
	Href string
}

// largestCostLines is how many the page shows. Enough to reveal what dominates
// a run rate, few enough that it stays a summary rather than a fifth list page.
const largestCostLines = 10

// EstateCosts totals every live cost line and reports what it could not see.
func (s *SQLStore) EstateCosts(ctx context.Context, now time.Time) (*EstateCostReport, error) {
	on := domain.FormatDate(now)
	report := &EstateCostReport{On: on}

	// Fixed order, and it is the order money is usually thought about: the
	// hardware, what runs on it, the connectivity, then what was bought
	// outright for a project.
	surfaces := []struct {
		name    string
		table   costTable
		hrefFmt string
		labels  func(context.Context) (map[string]string, error)
	}{
		{"assets", costOnAsset, "/assets/%s", s.assetLabels},
		{"services", costOnService, "/services/%s", s.serviceLabels},
		{"circuits", costOnCircuit, "/circuits/%s", s.circuitLabels},
		{"projects", costOnProject, "/projects/%s", s.projectLabels},
	}

	var largest []LargestCostLine
	for _, sf := range surfaces {
		labels, err := sf.labels(ctx)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(labels))
		for id := range labels {
			ids = append(ids, id)
		}
		byOwner, err := s.costsFor(ctx, sf.table, ids)
		if err != nil {
			return nil, err
		}

		surface := EstateCostSurface{Name: sf.name, Total: len(labels)}
		for id, lines := range byOwner {
			if len(lines) == 0 {
				continue
			}
			surface.Priced++
			totals := TotalCosts(lines, on)
			surface.Totals = surface.Totals.Plus(totals)
			for _, line := range lines {
				monthly := line.MonthlyMinor()
				if monthly <= 0 {
					continue
				}
				largest = append(largest, LargestCostLine{
					Surface: sf.name, OwnerID: id, OwnerLabel: labels[id],
					Kind: line.KindLabel, MonthlyCost: monthly,
					Href: fmt.Sprintf(sf.hrefFmt, id),
				})
			}
		}
		report.Totals = report.Totals.Plus(surface.Totals)
		report.Surfaces = append(report.Surfaces, surface)
	}

	// Biggest first, then by label so the order is stable when two lines cost
	// the same -- otherwise the page reshuffles between visits for no reason a
	// reader can see.
	sort.Slice(largest, func(i, j int) bool {
		if largest[i].MonthlyCost != largest[j].MonthlyCost {
			return largest[i].MonthlyCost > largest[j].MonthlyCost
		}
		return largest[i].OwnerLabel < largest[j].OwnerLabel
	})
	if len(largest) > largestCostLines {
		largest = largest[:largestCostLines]
	}
	report.Largest = largest
	return report, nil
}

// The label lookups. Each returns every LIVE entity of its kind, which is what
// makes the denominator honest: a retired asset is not something anybody failed
// to price.

func (s *SQLStore) assetLabels(ctx context.Context) (map[string]string, error) {
	return s.labelsFrom(ctx, `SELECT id, name AS label FROM asset WHERE lifecycle <> ?`, "assets")
}

func (s *SQLStore) serviceLabels(ctx context.Context) (map[string]string, error) {
	return s.labelsFrom(ctx, `SELECT id, code AS label FROM service WHERE lifecycle <> ?`, "services")
}

func (s *SQLStore) circuitLabels(ctx context.Context) (map[string]string, error) {
	return s.labelsFrom(ctx, `SELECT id, cid AS label FROM circuit WHERE lifecycle <> ?`, "circuits")
}

func (s *SQLStore) projectLabels(ctx context.Context) (map[string]string, error) {
	return s.labelsFrom(ctx, `SELECT id, code AS label FROM project WHERE lifecycle <> ?`, "projects")
}

func (s *SQLStore) labelsFrom(ctx context.Context, query, what string) (map[string]string, error) {
	var rows []struct {
		ID    string `db:"id"`
		Label string `db:"label"`
	}
	if err := s.read(ctx, &rows, query, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("listing %s for the cost report: %w", what, err)
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.ID] = r.Label
	}
	return out, nil
}
