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

	"github.com/madalinignisca/invctl/internal/domain"
)

// How a price moved (WP-J2).
//
// ONE SERIES PER COST KIND, NEVER ONE PER ENTITY. A box carries an acquisition,
// a support contract and a subscription at once; putting them on one line would
// compare a purchase against an annual rate and call the difference a price
// rise. The question "did this get more expensive" only means anything within a
// kind.
//
// READ FROM THE LINES THEMSELVES, not from change_log. The audit trail does
// record every amendment, but it is an append-only record of who changed what,
// not a queryable price series -- and `CLAUDE.md` is explicit that it is never
// to become a data source for reports. What makes this readable is the reprice
// verb: two rows, the older one closed on a date.

// PriceStep is one figure in force over a window.
type PriceStep struct {
	AmountMinor int64
	From        string
	// Until is empty for the line in force, which is the ordinary case for the
	// last step of any series.
	Until string
	Note  string
	// ChangeMinor and PercentChange are against the PREVIOUS step, and are zero
	// on the first -- there is nothing before it to have moved from.
	ChangeMinor   int64
	PercentChange int64
}

// IsCurrent reports whether this step is the one in force.
func (s PriceStep) IsCurrent() bool { return s.Until == "" }

// PriceSeries is one cost kind's history on one thing.
type PriceSeries struct {
	Kind   string
	Label  string
	Period string
	Steps  []PriceStep
}

// Moved reports whether there is more than one figure to compare. A single step
// is a price nobody has changed, which is not a movement and must not be
// rendered as one.
func (s PriceSeries) Moved() bool { return len(s.Steps) > 1 }

// FirstMinor and CurrentMinor bracket the series.
func (s PriceSeries) FirstMinor() int64 {
	if len(s.Steps) == 0 {
		return 0
	}
	return s.Steps[0].AmountMinor
}

func (s PriceSeries) CurrentMinor() int64 {
	if len(s.Steps) == 0 {
		return 0
	}
	return s.Steps[len(s.Steps)-1].AmountMinor
}

// TotalPercentChange is the movement across the whole series, first to current.
func (s PriceSeries) TotalPercentChange() int64 {
	if !s.Moved() || s.FirstMinor() == 0 {
		return 0
	}
	return (s.CurrentMinor() - s.FirstMinor()) * 100 / s.FirstMinor()
}

// PriceMovementFor returns one series per cost kind that has moved, plus the
// ones that have not, so a reader can tell "steady" from "unrecorded".
func (s *SQLStore) PriceMovementFor(ctx context.Context, t costTable, ownerID string) ([]PriceSeries, error) {
	lines, err := s.listCosts(ctx, t, ownerID)
	if err != nil {
		return nil, err
	}

	byKind := map[string][]CostRow{}
	labels := map[string]string{}
	periods := map[string]string{}
	for _, l := range lines {
		// Retired lines are excluded: a withdrawn figure is not a price that
		// was in force, it is one somebody took back. Including it would show a
		// rise or fall that never happened to anybody.
		if l.Lifecycle == domain.LifecycleRetired {
			continue
		}
		byKind[l.Kind] = append(byKind[l.Kind], l)
		labels[l.Kind] = l.KindLabel
		periods[l.Kind] = l.Period
	}

	out := make([]PriceSeries, 0, len(byKind))
	for kind, rows := range byKind {
		sort.Slice(rows, func(i, j int) bool { return rows[i].ValidFrom < rows[j].ValidFrom })
		series := PriceSeries{Kind: kind, Label: labels[kind], Period: periods[kind]}
		for i, r := range rows {
			step := PriceStep{
				AmountMinor: r.AmountMinor,
				From:        r.ValidFrom,
				Until:       derefString(r.ValidUntil),
				Note:        derefString(r.Note),
			}
			if i > 0 {
				prev := rows[i-1].AmountMinor
				step.ChangeMinor = r.AmountMinor - prev
				if prev != 0 {
					step.PercentChange = (r.AmountMinor - prev) * 100 / prev
				}
			}
			series.Steps = append(series.Steps, step)
		}
		out = append(out, series)
	}
	// Moved first, then by label. What changed is what somebody came to see,
	// and a stable secondary key stops the panel reshuffling between visits.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Moved() != out[j].Moved() {
			return out[i].Moved()
		}
		return out[i].Label < out[j].Label
	})
	return out, nil
}

// PriceMovementForAsset and friends name the surface, so a handler never passes
// a costTable around and cannot pass the wrong one.
func (s *SQLStore) PriceMovementForAsset(ctx context.Context, id string) ([]PriceSeries, error) {
	return s.PriceMovementFor(ctx, costOnAsset, id)
}

func (s *SQLStore) PriceMovementForService(ctx context.Context, id string) ([]PriceSeries, error) {
	return s.PriceMovementFor(ctx, costOnService, id)
}

func (s *SQLStore) PriceMovementForCircuit(ctx context.Context, id string) ([]PriceSeries, error) {
	return s.PriceMovementFor(ctx, costOnCircuit, id)
}

func (s *SQLStore) PriceMovementForProject(ctx context.Context, id string) ([]PriceSeries, error) {
	return s.PriceMovementFor(ctx, costOnProject, id)
}
