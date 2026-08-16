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

// Who shares a machine (WP-J5, migration 00049).
//
// IT DOES NOT REPLACE OWNERSHIP. An occupied asset still has an owner -- who is
// answerable for it and who is called when it breaks -- and occupancy changes
// only how its capacity and cost DIVIDE. Conflating the two would mean a
// machine four projects share belongs to nobody.

// OccupancyFor returns who shares one asset.
func (s *SQLStore) OccupancyFor(ctx context.Context, assetID string) (*domain.Occupancy, error) {
	var occupants []domain.Occupant
	if err := s.read(ctx, &occupants, `
		SELECT o.asset_id, o.project_id, o.percent, o.note, o.created_at, o.updated_at,
		       p.code AS project_code
		FROM asset_occupant o
		JOIN project p ON p.id = o.project_id
		WHERE o.asset_id = ? AND p.lifecycle <> ?
		ORDER BY o.percent DESC, p.code`, assetID, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("reading the occupants of %s: %w", assetID, err)
	}
	out := &domain.Occupancy{AssetID: assetID, Occupants: occupants}
	domain.SortOccupants(out.Occupants)
	return out, nil
}

// AllOccupancy returns every declared occupancy, keyed by asset.
//
// ONE QUERY RATHER THAN ONE PER WORKLOAD. The attribution walks every guest of
// every member host, and a per-asset lookup there turned a single report into a
// query per machine -- the shape internal/store/scaletest exists to catch.
func (s *SQLStore) AllOccupancy(ctx context.Context) (map[string]*domain.Occupancy, error) {
	var rows []domain.Occupant
	if err := s.read(ctx, &rows, `
		SELECT o.asset_id, o.project_id, o.percent, o.note, o.created_at, o.updated_at,
		       p.code AS project_code
		FROM asset_occupant o
		JOIN project p ON p.id = o.project_id
		JOIN asset a ON a.id = o.asset_id
		WHERE p.lifecycle <> ? AND a.lifecycle <> ?
		ORDER BY o.asset_id, o.percent DESC, p.code`,
		domain.LifecycleRetired, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("reading declared occupancy: %w", err)
	}
	out := map[string]*domain.Occupancy{}
	for _, r := range rows {
		o, seen := out[r.AssetID]
		if !seen {
			o = &domain.Occupancy{AssetID: r.AssetID}
			out[r.AssetID] = o
		}
		o.Occupants = append(o.Occupants, r)
	}
	for _, o := range out {
		domain.SortOccupants(o.Occupants)
	}
	return out, nil
}

// SetOccupants replaces who shares an asset.
//
// A SET OWNED BY THE ASSET, replaced wholesale inside its transaction and
// folded into its audited value. An empty list means nobody shares it, which is
// the ordinary state and is recorded as a change like any other -- somebody
// deciding a machine is no longer shared is exactly the sort of thing a reader
// needs to find later.
func (s *SQLStore) SetOccupants(ctx context.Context, actor domain.Actor,
	assetID string, occupants []domain.Occupant) error {

	if err := domain.ValidateOccupants(occupants); err != nil {
		return err
	}
	asset, err := s.GetAsset(ctx, assetID)
	if err != nil {
		return err
	}
	if asset.IsRetired() {
		return fmt.Errorf("a retired asset houses nobody: %w", domain.ErrInvalid)
	}
	at := domain.FormatTime(s.Now())
	return s.write(ctx, actor, func(t *tx) error {
		before, err := occupancyAudit(ctx, t, &asset.Asset)
		if err != nil {
			return err
		}
		if _, err := t.exec(ctx,
			`DELETE FROM asset_occupant WHERE asset_id = ?`, assetID); err != nil {
			return translateWriteErr(err, "clearing the occupants")
		}
		for _, x := range occupants {
			if _, err := t.exec(ctx, `
				INSERT INTO asset_occupant
					(asset_id, project_id, percent, note, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?)`,
				assetID, x.ProjectID, x.Percent, x.Note, at, at); err != nil {
				return translateWriteErr(err, "recording an occupant")
			}
		}
		after, err := occupancyAudit(ctx, t, &asset.Asset)
		if err != nil {
			return err
		}
		return t.logUpdate(ctx, "asset", assetID, before, after)
	})
}

// assetOccupancyAudit is the audited shape when the occupancy changes.
type assetOccupancyAudit struct {
	domain.Asset
	Occupancy string `db:"occupancy"`
}

// occupancyAudit reads the audited value inside the transaction, because the
// reader pool cannot see uncommitted writes and the "after" snapshot would
// otherwise equal the "before" one.
func occupancyAudit(ctx context.Context, t *tx, a *domain.Asset) (*assetOccupancyAudit, error) {
	var rows []struct {
		Code    string `db:"code"`
		Percent int    `db:"percent"`
	}
	if err := t.selectAll(ctx, &rows, `
		SELECT p.code, o.percent
		FROM asset_occupant o
		JOIN project p ON p.id = o.project_id
		WHERE o.asset_id = ?
		ORDER BY o.percent DESC, p.code`, a.ID); err != nil {
		return nil, fmt.Errorf("reading occupancy for the audit trail: %w", err)
	}
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		parts = append(parts, fmt.Sprintf("%s %d%%", r.Code, r.Percent))
	}
	return &assetOccupancyAudit{Asset: *a, Occupancy: strings.Join(parts, ", ")}, nil
}
