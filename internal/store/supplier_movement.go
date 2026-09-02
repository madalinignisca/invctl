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

	"github.com/madalinignisca/invctl/internal/domain"
)

// Which suppliers raise prices beyond inflation (WP-J6).
//
// THE THIRD QUESTION, AND THE ONE THE ESTATE COULD ONLY ANSWER ITEM BY ITEM.
// docs/COST-ATTRIBUTION.md §1 has it in the CEO's own words: the company would
// rather leave a supplier that abuses pricing than absorb it. J2 built every
// piece of the arithmetic -- a price series per cost kind, a recorded inflation
// series, nominal against real -- and answered it for one thing at a time. What
// was missing was the dimension, and migration 00050 added it to the cost line.
//
// AGGREGATED FROM THE LINES, NEVER STORED. A supplier's movement is whatever its
// lines did. A maintained per-supplier figure would drift from the lines it
// claims to summarise the first time one was repriced -- the same argument §5.1
// makes for unit prices.
//
// WEIGHTED BY WHAT EACH LINE COSTS, and this is the choice worth arguing with. A
// supplier with one €40 line up 50% and one €4,000 line up 2% has not raised its
// prices by 26%. It has raised them by about 2%, because the large line is
// almost all of the money. An unweighted mean hands somebody a grievance built
// from a rounding error on a small invoice, which is the opposite of what a
// decision to leave a supplier needs.
//
// A SERIES WHOSE SUPPLIER CHANGED IS NOT A SUPPLIER RAISING ITS PRICE. Switching
// reseller at renewal moves the figure, and attributing that move to whoever
// happens to be current would manufacture exactly the accusation this report
// exists to test. Such a series is counted and excluded from the percentages.

// SupplierMovement is one supplier's book and how it moved.
type SupplierMovement struct {
	ProviderID string
	Provider   string
	AccountRef *string

	// Series is every price history wholly owed to this supplier, moved first.
	Series []PriceSeries

	// MonthlyMinor is the current run rate across every line of theirs.
	MonthlyMinor int64
	// Lines, Moved and Switched say what the percentages are computed over. A
	// supplier with nine steady lines and one that jumped is a different
	// conversation from one whose whole book rose.
	Lines int
	Moved int
	// Switched is series that changed hands mid-history, excluded above.
	Switched int

	// NominalPercent and RealPercent are whole percent, weighted by line cost --
	// the same unit PriceSeries.TotalPercentChange uses.
	NominalPercent int64
	RealPercent    int64
	// MissingYear names the first year the inflation table does not cover, or
	// zero. While it is set, no real-terms figure is shown: computing one over
	// years treated as zero would understate what money did and so flatter the
	// supplier.
	MissingYear int
}

// Real reports whether a real-terms figure can honestly be shown.
func (m SupplierMovement) Real() bool { return m.MissingYear == 0 && m.Moved > 0 }

// BeyondInflation reports whether this supplier rose faster than money lost
// value — the question as it was actually asked.
func (m SupplierMovement) BeyondInflation() bool { return m.Real() && m.RealPercent > 0 }

// SupplierReport is every supplier, worst first, and what it could not count.
type SupplierReport struct {
	On        string
	Suppliers []SupplierMovement
	// UnattributedLines and UnattributedMinor are the lines naming no supplier.
	//
	// THE NUMBER THAT MAKES THE REST HONEST. A ranking of four suppliers over a
	// third of the estate's spend is a sample, and a reader who cannot see that
	// will read it as the whole book.
	UnattributedLines int
	UnattributedMinor int64
}

// Attributed reports whether anything could be attributed at all.
func (r SupplierReport) Attributed() bool { return len(r.Suppliers) > 0 }

// supplierOwner is one thing that carries priced lines.
type supplierOwner struct {
	table costTable
	id    string
}

// SupplierMovements answers the third question across the whole estate.
func (s *SQLStore) SupplierMovements(ctx context.Context, on string) (*SupplierReport, error) {
	if on == "" {
		on = domain.FormatDate(s.Now())
	}
	out := &SupplierReport{On: on}

	owners, err := s.pricedOwners(ctx)
	if err != nil {
		return nil, err
	}

	byProvider := map[string]*SupplierMovement{}
	names, err := s.providerNames(ctx)
	if err != nil {
		return nil, err
	}
	get := func(id string) *SupplierMovement {
		m, ok := byProvider[id]
		if !ok {
			m = &SupplierMovement{
				ProviderID: id, Provider: names[id].name, AccountRef: names[id].accountRef,
			}
			byProvider[id] = m
		}
		return m
	}

	for _, owner := range owners {
		lines, err := s.listCosts(ctx, owner.table, owner.id)
		if err != nil {
			return nil, fmt.Errorf("reading lines for the supplier report: %w", err)
		}
		// Who invoices each kind on this owner, and whether they agree.
		providersByKind := map[string]map[string]bool{}
		for _, l := range lines {
			if l.Lifecycle == domain.LifecycleRetired {
				continue // a withdrawn figure was never in force; see PriceMovementFor
			}
			id := ""
			if l.ProviderID != nil {
				id = *l.ProviderID
			}
			if providersByKind[l.Kind] == nil {
				providersByKind[l.Kind] = map[string]bool{}
			}
			providersByKind[l.Kind][id] = true
			if id == "" {
				out.UnattributedLines++
				out.UnattributedMinor += l.MonthlyMinor()
				continue
			}
			get(id).Lines++
			get(id).MonthlyMinor += l.MonthlyMinor()
		}

		series, err := s.PriceMovementFor(ctx, owner.table, owner.id)
		if err != nil {
			return nil, err
		}
		for _, sr := range series {
			ids := providersByKind[sr.Kind]
			if len(ids) != 1 {
				// Either nobody named a supplier, or the series changed hands.
				// A move across a change of supplier is a switch, not a rise.
				if len(ids) > 1 {
					for id := range ids {
						if id != "" {
							get(id).Switched++
						}
					}
				}
				continue
			}
			var only string
			for id := range ids {
				only = id
			}
			if only == "" {
				continue
			}
			m := get(only)
			m.Series = append(m.Series, sr)
			if !sr.Moved() {
				continue
			}
			m.Moved++
		}
	}

	// The weighted percentages, computed once per supplier over the series that
	// are wholly theirs.
	for _, m := range byProvider {
		var weight, nominal, real int64
		for _, sr := range m.Series {
			if !sr.Moved() {
				continue
			}
			w := sr.CurrentMinor()
			if w <= 0 {
				w = 1 // a one-off still counts, just not by a run rate
			}
			weight += w
			nominal += sr.TotalPercentChange() * w
			switch {
			case sr.RealKnown():
				real += sr.Real.PercentChange * w
			case sr.Real != nil && sr.Real.MissingYear != 0 && m.MissingYear == 0:
				m.MissingYear = sr.Real.MissingYear
			}
		}
		if weight > 0 {
			m.NominalPercent = nominal / weight
			m.RealPercent = real / weight
		}
		sort.SliceStable(m.Series, func(a, b int) bool {
			if m.Series[a].Moved() != m.Series[b].Moved() {
				return m.Series[a].Moved()
			}
			return m.Series[a].Label < m.Series[b].Label
		})
		out.Suppliers = append(out.Suppliers, *m)
	}

	// Worst first: the supplier whose real-terms rise is largest is the one the
	// question was asked about. Suppliers with no judgeable movement sort last
	// rather than to the top with a zero.
	sort.SliceStable(out.Suppliers, func(a, b int) bool {
		x, y := out.Suppliers[a], out.Suppliers[b]
		if x.Real() != y.Real() {
			return x.Real()
		}
		if x.Real() && x.RealPercent != y.RealPercent {
			return x.RealPercent > y.RealPercent
		}
		if x.MonthlyMinor != y.MonthlyMinor {
			return x.MonthlyMinor > y.MonthlyMinor
		}
		return x.Provider < y.Provider
	})
	return out, nil
}

// pricedOwners is every entity carrying at least one live cost line.
func (s *SQLStore) pricedOwners(ctx context.Context) ([]supplierOwner, error) {
	out := []supplierOwner{}
	// Item 4 (2026-09-02 group-a-1-1 round): was its own hand-typed
	// []costTable{...} literal here, a second copy of the same four entries
	// costs.go's allCostTables already declares -- so a fifth costTable
	// added there would silently never reach the supplier report. Now
	// consumes that one canonical list; see allCostTables' own comment.
	for _, t := range allCostTables {
		var ids []string
		// DISTINCT: one owner is visited once however many lines it carries.
		if err := s.read(ctx, &ids, `
			SELECT DISTINCT `+t.column+` FROM `+t.name+`
			WHERE lifecycle <> ?`, domain.LifecycleRetired); err != nil {
			return nil, fmt.Errorf("listing priced %s: %w", t.entity, err)
		}
		for _, id := range ids {
			out = append(out, supplierOwner{table: t, id: id})
		}
	}
	return out, nil
}

type providerName struct {
	name       string
	accountRef *string
}

// providerNames resolves ids for display, because a report is read by people.
func (s *SQLStore) providerNames(ctx context.Context) (map[string]providerName, error) {
	var rows []struct {
		ID         string  `db:"id"`
		Name       string  `db:"name"`
		AccountRef *string `db:"account_ref"`
	}
	if err := s.read(ctx, &rows,
		`SELECT id, name, account_ref FROM provider`); err != nil {
		return nil, fmt.Errorf("resolving supplier names: %w", err)
	}
	out := make(map[string]providerName, len(rows))
	for _, r := range rows {
		out[r.ID] = providerName{name: r.Name, accountRef: r.AccountRef}
	}
	return out, nil
}
