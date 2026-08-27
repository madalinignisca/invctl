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
	"database/sql"
	"errors"
	"fmt"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Supplies: what sits above a panel. See migration 00024.

// PowerSourceRow is a source with its parent and what hangs off it.
type PowerSourceRow struct {
	domain.PowerSource
	SiteName   string  `db:"site_name"`
	ParentName *string `db:"parent_name"`
	AssetName  *string `db:"asset_name"`
	Panels     int     `db:"panels"`
	Children   int     `db:"children"`
}

const powerSourceSelect = `
	SELECT s.*, a.name AS site_name, par.name AS parent_name, ast.name AS asset_name,
	       (SELECT COUNT(*) FROM power_panel p
	        WHERE p.source_id = s.id AND p.lifecycle <> '` + domain.LifecycleRetired + `') AS panels,
	       (SELECT COUNT(*) FROM power_source c
	        WHERE c.parent_id = s.id AND c.lifecycle <> '` + domain.LifecycleRetired + `') AS children
	FROM power_source s
	JOIN asset a ON a.id = s.site_id
	LEFT JOIN power_source par ON par.id = s.parent_id
	LEFT JOIN asset ast ON ast.id = s.asset_id`

// CreatePowerSource inserts a supply and its audit row.
func (s *SQLStore) CreatePowerSource(ctx context.Context, permit domain.Permit, p *domain.PowerSource) error {
	p.RowVersion = 1
	if err := p.Validate(); err != nil {
		return err
	}
	return s.write(ctx, permit, func(t *tx) error {
		if err := requireLiveAsset(ctx, t, "site_id", p.SiteID); err != nil {
			return err
		}
		if p.AssetID != nil {
			if err := requireLiveAsset(ctx, t, "asset_id", *p.AssetID); err != nil {
				return err
			}
		}
		if p.ParentID != nil {
			if err := requireLiveSource(ctx, t, *p.ParentID); err != nil {
				return err
			}
			if err := requireNoSupplyCycle(ctx, t, p.ID, *p.ParentID); err != nil {
				return err
			}
		}
		if err := requireUniquePowerName(ctx, t,
			`power_source`, `site_id`, p.SiteID, p.Name, p.Lifecycle, p.ID); err != nil {
			return err
		}
		_, err := t.exec(ctx, `
			INSERT INTO power_source (id, parent_id, site_id, asset_id, name, kind, notes,
			                          lifecycle, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ID, p.ParentID, p.SiteID, p.AssetID, p.Name, p.Kind, p.Notes,
			p.Lifecycle, p.CreatedAt, p.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating power source")
		}
		return t.logCreate(ctx, "power_source", p.ID, p)
	})
}

// UpdatePowerSource persists field changes, including re-parenting.
//
// The parent IS editable, unlike a feed's panel: getting the supply chain wrong
// is the commonest reason to be on this screen at all, and correcting it is
// exactly what somebody does after reading a finding. The cycle guard is what
// makes that safe.
func (s *SQLStore) UpdatePowerSource(ctx context.Context, permit domain.Permit, p *domain.PowerSource) error {
	before, err := s.GetPowerSource(ctx, p.ID)
	if err != nil {
		return err
	}
	p.SiteID = before.SiteID
	if err := p.Validate(); err != nil {
		return err
	}
	p.CreatedAt = before.CreatedAt
	p.UpdatedAt = domain.FormatTime(s.now())

	return s.write(ctx, permit, func(t *tx) error {
		if p.ParentID != nil {
			if err := requireLiveSource(ctx, t, *p.ParentID); err != nil {
				return err
			}
			if err := requireNoSupplyCycle(ctx, t, p.ID, *p.ParentID); err != nil {
				return err
			}
		}
		if p.AssetID != nil {
			if err := requireLiveAsset(ctx, t, "asset_id", *p.AssetID); err != nil {
				return err
			}
		}
		if err := requireUniquePowerName(ctx, t,
			`power_source`, `site_id`, p.SiteID, p.Name, p.Lifecycle, p.ID); err != nil {
			return err
		}
		res, err := t.exec(ctx, `
			UPDATE power_source SET parent_id = ?, asset_id = ?, name = ?, kind = ?, notes = ?,
			                        lifecycle = ?, updated_at = ?, row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			p.ParentID, p.AssetID, p.Name, p.Kind, p.Notes, p.Lifecycle, p.UpdatedAt,
			p.ID, p.RowVersion)
		if err != nil {
			return translateWriteErr(err, "updating power source")
		}
		if err := requireVersion(res, "power_source", p.ID, &p.RowVersion); err != nil {
			return err
		}
		return t.logUpdate(ctx, "power_source", p.ID, &before.PowerSource, p)
	})
}

// GetPowerSource loads one by id.
func (s *SQLStore) GetPowerSource(ctx context.Context, id string) (*PowerSourceRow, error) {
	var row PowerSourceRow
	if err := s.readOne(ctx, &row, powerSourceSelect+` WHERE s.id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting power source %s: %w", id, err)
	}
	return &row, nil
}

// ListPowerSources returns supplies, by site then name.
func (s *SQLStore) ListPowerSources(ctx context.Context, includeRetired bool) ([]PowerSourceRow, error) {
	query := powerSourceSelect
	var args []any
	if !includeRetired {
		query += ` WHERE s.lifecycle <> ?`
		args = append(args, domain.LifecycleRetired)
	}
	query += ` ORDER BY a.name, s.name`
	var rows []PowerSourceRow
	if err := s.read(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("listing power sources: %w", err)
	}
	return rows, nil
}

// RetirePowerSource soft-deletes a supply.
//
// Refused while panels or downstream supplies still hang off it: they would be
// left pointing at something no list shows, and the chain behind them could no
// longer be traced -- which is the one thing the supply layer exists to do.
func (s *SQLStore) RetirePowerSource(ctx context.Context, p domain.Permit, id string) error {
	before, err := s.GetPowerSource(ctx, id)
	if err != nil {
		return err
	}
	if before.Panels > 0 || before.Children > 0 {
		return fmt.Errorf("supply %s still feeds %d %s and %d downstream %s: %w",
			before.Name, before.Panels, pluralWord(before.Panels, "panel", "panels"),
			before.Children, pluralWord(before.Children, "supply", "supplies"), domain.ErrConflict)
	}
	at := domain.FormatTime(s.now())
	return s.write(ctx, p, func(t *tx) error {
		_, err := t.exec(ctx,
			`UPDATE power_source SET lifecycle = ?, updated_at = ?, row_version = row_version + 1
			 WHERE id = ?`, domain.LifecycleRetired, at, id)
		if err != nil {
			return translateWriteErr(err, "retiring power source")
		}
		after := before.PowerSource
		after.Lifecycle, after.UpdatedAt = domain.LifecycleRetired, at
		return t.logUpdate(ctx, "power_source", id, &before.PowerSource, &after)
	})
}

func requireLiveSource(ctx context.Context, t *tx, id string) error {
	var lifecycle string
	if err := t.get(ctx, &lifecycle, `SELECT lifecycle FROM power_source WHERE id = ?`, id); err != nil {
		ve := &domain.ValidationError{}
		ve.Add("parent_id", "choose a supply")
		return ve
	}
	if lifecycle == domain.LifecycleRetired {
		ve := &domain.ValidationError{}
		ve.Add("parent_id", "that supply has been retired")
		return ve
	}
	return nil
}

// supplyDepthLimit bounds every walk up a supply chain.
//
// A cycle is already refused on the way in, but a guard that trusts that is a
// guard that hangs the findings page if it is ever wrong -- and a hung report is
// worse than a wrong one, because nobody can see what it was going to say. No
// real chain is anywhere near this deep.
const supplyDepthLimit = 32

// requireNoSupplyCycle refuses a parent that is fed, however indirectly, by the
// source being edited.
func requireNoSupplyCycle(ctx context.Context, t *tx, id, parentID string) error {
	cur := parentID
	for depth := 0; depth < supplyDepthLimit; depth++ {
		if cur == id {
			ve := &domain.ValidationError{}
			ve.Add("parent_id", "that supply is fed by this one; the chain would loop")
			return ve
		}
		var next *string
		err := t.get(ctx, &next, `SELECT parent_id FROM power_source WHERE id = ?`, cur)
		if errors.Is(err, sql.ErrNoRows) {
			// The chain ends at a row that is not there. Not an error to report
			// here -- requireLiveSource already refused an unknown parent, and a
			// dangling link further up is a broken chain rather than a loop, so
			// there is nothing left to walk.
			return nil
		}
		if err != nil {
			// A REAL failure, propagated. Swallowing it would let a cycle through
			// whenever the database was unhappy, which is the shape this codebase
			// keeps finding: an error discarded and replaced with an answer
			// indistinguishable from a legitimate one.
			return fmt.Errorf("walking the supply chain: %w", err)
		}
		if next == nil {
			return nil
		}
		cur = *next
	}
	ve := &domain.ValidationError{}
	ve.Add("parent_id", "that supply chain is longer than this system will follow")
	return ve
}
