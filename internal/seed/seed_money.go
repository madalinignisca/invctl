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
