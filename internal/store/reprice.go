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

	"github.com/madalinignisca/invctl/internal/domain"
)

// Repricing: the verb that makes price history exist (WP-J2).
//
// THE MISSING VERB, AND ITS ABSENCE MADE A COMMENT INTO A FICTION. `cost.go`
// has said since the first release that validity windows exist "because without
// it a renewal at a new price overwrites its predecessor and the history is
// gone". The windows were built. The verb was not: `updateCost` writes
// amount_minor in place, so the obvious operator action -- open the line, type
// the new figure, save -- destroys exactly the number the design set out to
// keep. The seeded estate contained not one entity whose same cost kind was
// priced twice, because nothing had ever produced one.
//
// So a reprice is one transaction with two effects: the line in force is CLOSED
// the day before the new price starts, and a new line opens carrying it. Both
// rows survive, both are audited, and the series they form is what J2 reads.
//
// EDITING IS STILL RIGHT FOR A CORRECTION, and the distinction is the whole
// point. Somebody typing 840 when the invoice said 8400 made a mistake, and the
// wrong figure was never true -- amend it. A supplier raising the rate is not a
// mistake; the old figure was true and stopped being true on a date. Conflating
// them is how an estate loses the only evidence it has that a price moved.

// RepriceSpec is a price change with the date it takes effect.
type RepriceSpec struct {
	// LineID is the line in force that is being superseded.
	LineID string
	// NewAmountMinor is what it costs from EffectiveFrom onward.
	NewAmountMinor int64
	// EffectiveFrom is the day the new price starts. The old line is closed the
	// day before, so the two never overlap and no date falls in both.
	EffectiveFrom string
	// Note travels with the NEW line: it explains the rise, which is what a
	// reader of the series wants ("annual uplift", "moved to subscription").
	Note *string
}

// RepriceAssetCost supersedes a line on an asset with a new price.
func (s *SQLStore) RepriceAssetCost(ctx context.Context, actor domain.Actor, ownerID string, spec RepriceSpec) (*domain.Cost, error) {
	return s.reprice(ctx, actor, costOnAsset, ownerID, spec)
}

// RepriceServiceCost supersedes a line on a service.
func (s *SQLStore) RepriceServiceCost(ctx context.Context, actor domain.Actor, ownerID string, spec RepriceSpec) (*domain.Cost, error) {
	return s.reprice(ctx, actor, costOnService, ownerID, spec)
}

// RepriceCircuitCost supersedes a line on a circuit -- the case this was built
// for, since a circuit is the thing most likely to be renewed at a new rate.
func (s *SQLStore) RepriceCircuitCost(ctx context.Context, actor domain.Actor, ownerID string, spec RepriceSpec) (*domain.Cost, error) {
	return s.reprice(ctx, actor, costOnCircuit, ownerID, spec)
}

// RepriceProjectCost supersedes a line on a project.
func (s *SQLStore) RepriceProjectCost(ctx context.Context, actor domain.Actor, ownerID string, spec RepriceSpec) (*domain.Cost, error) {
	return s.reprice(ctx, actor, costOnProject, ownerID, spec)
}

func (s *SQLStore) reprice(ctx context.Context, actor domain.Actor, t costTable,
	ownerID string, spec RepriceSpec) (*domain.Cost, error) {

	before, err := s.getCost(ctx, t, spec.LineID)
	if err != nil {
		return nil, err
	}
	// The owner is checked rather than trusted, like retireCost: a line id
	// arriving in a URL must not let somebody reprice a line on a thing they
	// were not looking at.
	if before.OwnerID != ownerID {
		return nil, fmt.Errorf("cost line %s does not belong to %s: %w",
			spec.LineID, ownerID, domain.ErrNotFound)
	}
	if before.Lifecycle == domain.LifecycleRetired {
		return nil, fmt.Errorf("cost line %s is retired and cannot be repriced: %w",
			spec.LineID, domain.ErrConflict)
	}
	// A ONE-OFF CANNOT BE REPRICED, and refusing is the honest answer. A
	// purchase happened once at a price that was paid; there is no "from now
	// on" for it. Somebody wanting to record a second purchase is recording a
	// second acquisition, which is a new line, not a supersession of the first.
	if before.Period == domain.CostOnce {
		return nil, fmt.Errorf("a one-off cost is a payment that happened, not a "+
			"rate that can change; add a second line instead: %w", domain.ErrConflict)
	}

	day, err := domain.ParseDate(spec.EffectiveFrom)
	if err != nil {
		ve := &domain.ValidationError{}
		ve.Add("effective_from", "must be a date, as YYYY-MM-DD")
		return nil, ve
	}
	// Strictly after the line it supersedes. Equal would leave the old line
	// open for zero days and both rows claiming the same start; earlier would
	// mean the new price was in force before the old one began.
	//
	// Lexical comparison is correct here and everywhere else in this codebase:
	// dates are RFC3339 text and sort correctly as strings, which is why they
	// are stored that way.
	if spec.EffectiveFrom <= before.ValidFrom {
		ve := &domain.ValidationError{}
		ve.Add("effective_from", "the new price must start after %s, when the line "+
			"it replaces began", before.ValidFrom)
		return nil, ve
	}
	closeOn := domain.FormatDate(day.AddDate(0, 0, -1))

	now := domain.FormatTime(s.now())
	closed := before.Cost
	closed.ValidUntil = &closeOn
	closed.UpdatedAt = now

	opened := domain.Cost{
		ID: NewID(), Kind: before.Kind, Period: before.Period,
		AmountMinor: spec.NewAmountMinor, Note: spec.Note,
		ValidFrom: spec.EffectiveFrom, Lifecycle: domain.LifecycleActive,
		CreatedAt: now, UpdatedAt: now, RowVersion: 1,
		// THE SUPPLIER CARRIES FORWARD, because a renewal is from the same
		// supplier unless somebody says otherwise -- and if it does not, the
		// series looks to WP-J6 like a line that changed hands, which is
		// deliberately excluded from "did this supplier raise its prices".
		// Every renewal would have been discounted from the report that exists
		// to judge renewals.
		ProviderID: before.ProviderID,
		// So does the scope. A licence that covered three guests still covers
		// them at the new price; losing it would silently return the line to
		// universal and spread it over workloads that derive nothing from it.
		AppliesTo: before.AppliesTo,
	}
	if err := opened.Validate(); err != nil {
		return nil, err
	}

	err = s.write(ctx, domain.AdministratorPermit(actor), func(tx *tx) error {
		res, err := tx.exec(ctx,
			`UPDATE `+t.name+` SET valid_until = ?, updated_at = ?,
			                       row_version = row_version + 1
			 WHERE id = ? AND row_version = ?`,
			closeOn, now, before.ID, before.RowVersion)
		if err != nil {
			return translateWriteErr(err, "closing the superseded cost line")
		}
		if err := requireVersion(res, t.entity, before.ID, &before.RowVersion); err != nil {
			return err
		}
		if err := tx.logUpdate(ctx, t.entity, before.ID, &before.Cost, &closed); err != nil {
			return err
		}
		_, err = tx.exec(ctx, `
			INSERT INTO `+t.name+` (id, `+t.column+`, kind, period, amount_minor, note,
			                        valid_from, valid_until, lifecycle, created_at, updated_at,
			                        provider_id`+scopeColumn(t)+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?`+scopeValue(t)+`)`,
			append([]any{
				opened.ID, ownerID, opened.Kind, opened.Period, opened.AmountMinor, opened.Note,
				opened.ValidFrom, opened.ValidUntil, opened.Lifecycle, opened.CreatedAt,
				opened.UpdatedAt, opened.ProviderID,
			}, scopeArg(t, opened.AppliesTo)...)...)
		if err != nil {
			return translateWriteErr(err, "opening the new cost line")
		}
		return tx.logCreate(ctx, t.entity, opened.ID, &opened)
	})
	if err != nil {
		return nil, err
	}
	return &opened, nil
}

// scopeColumn, scopeValue and scopeArg add applies_to to a statement only for
// the one cost table that has it. Three tiny helpers rather than a second
// INSERT: two statements that must stay in step is how one of them stops
// carrying a column somebody added to the other.
func scopeColumn(t costTable) string {
	if t.scoped {
		return ", applies_to"
	}
	return ""
}

func scopeValue(t costTable) string {
	if t.scoped {
		return ", ?"
	}
	return ""
}

func scopeArg(t costTable, appliesTo string) []any {
	if t.scoped {
		return []any{appliesTo}
	}
	return nil
}
