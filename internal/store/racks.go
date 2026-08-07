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
	"errors"
	"fmt"
	"sort"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Rack elevations. The rules are in domain/rack.go; this resolves them against
// the estate.

// RackUnit is one shelf of the drawing, front and rear.
type RackUnit struct {
	Number int
	// Front and Rear are what occupies this unit on each face, or nil. A
	// full-depth box appears in BOTH, which is what makes "is there space"
	// answerable rather than a guess.
	Front *domain.Placement
	Rear  *domain.Placement
	// FrontStart is true when this unit is where the front box begins, so a
	// multi-unit box is labelled once rather than on every shelf it covers.
	FrontStart bool
	RearStart  bool
}

// RackElevation is everything the diagram and the capacity line need.
type RackElevation struct {
	RackID   string
	RackName string
	// Units is the whole rack, TOP FIRST, because that is how a rack is looked
	// at. The Number on each is still counted from the floor.
	Units []RackUnit
	// Height is what somebody recorded, or the assumed default.
	Height int
	// HeightKnown separates "this is a 42U rack" from "nobody said, so we drew
	// 42". The page says which, because capacity computed from a guess is a
	// number somebody will otherwise plan with.
	HeightKnown bool
	// Unpositioned is everything in this rack whose position nobody recorded.
	// It is the ordinary starting state and it is listed rather than hidden --
	// a diagram of three boxes in a rack of forty is misleading on its own.
	Unpositioned []AssetRow
	FreeUnits    int
}

// Elevation resolves what is in a rack and where.
func (s *SQLStore) Elevation(ctx context.Context, rackID string) (*RackElevation, error) {
	rack, err := s.GetAsset(ctx, rackID)
	if err != nil {
		return nil, err
	}
	contents, err := s.ListAssets(ctx, AssetFilter{ParentID: rackID})
	if err != nil {
		return nil, fmt.Errorf("listing rack contents: %w", err)
	}

	height, known := domain.DefaultRackUnits, false
	if rack.UHeight != nil && *rack.UHeight > 0 {
		height, known = *rack.UHeight, true
	}

	e := &RackElevation{
		RackID: rackID, RackName: rack.Name,
		Height: height, HeightKnown: known,
	}

	placements := make([]domain.Placement, 0, len(contents))
	for _, row := range contents {
		if row.RackPosition == nil {
			e.Unpositioned = append(e.Unpositioned, row)
			continue
		}
		placements = append(placements, placementOf(row))
	}
	sort.Slice(placements, func(i, j int) bool { return placements[i].Position < placements[j].Position })

	// Top first: a rack is read from the top down even though its units are
	// numbered from the floor.
	e.Units = make([]RackUnit, 0, height)
	for n := height; n >= 1; n-- {
		unit := RackUnit{Number: n}
		for i := range placements {
			p := &placements[i]
			if p.Occupies(n, domain.FaceFront) && unit.Front == nil {
				unit.Front, unit.FrontStart = p, p.Position == n
			}
			if p.Occupies(n, domain.FaceRear) && unit.Rear == nil {
				unit.Rear, unit.RearStart = p, p.Position == n
			}
		}
		if unit.Front == nil && unit.Rear == nil {
			e.FreeUnits++
		}
		e.Units = append(e.Units, unit)
	}
	return e, nil
}

// placementOf resolves one row's occupancy.
//
// Height comes from the catalogued model. An uncatalogued box is treated as 1U
// and SAYS SO: assuming one unit is right far more often than it is wrong, and
// assuming zero would draw an empty rack full of servers.
func placementOf(row AssetRow) domain.Placement {
	p := domain.Placement{
		AssetID: row.ID, Name: row.Name, Kind: row.Kind,
		Position: *row.RackPosition, Height: 1,
		Face: domain.FaceFront, FullDepth: true,
	}
	if row.RackFace != nil && *row.RackFace != "" {
		p.Face = *row.RackFace
		// A face was chosen, so depth is what the model says rather than the
		// safe assumption below.
		p.FullDepth = row.DeviceTypeFullDepth
	}
	if row.DeviceTypeUHeight != nil && *row.DeviceTypeUHeight > 0 {
		p.Height, p.HeightKnown = *row.DeviceTypeUHeight, true
	}
	return p
}

// requireFreeRackSpace enforces the placement rules for one asset.
func (s *SQLStore) requireFreeRackSpace(ctx context.Context, a *domain.Asset) error {
	if a.RackPosition == nil || a.ParentID == nil {
		return nil
	}
	rack, err := s.GetAsset(ctx, *a.ParentID)
	if errors.Is(err, domain.ErrNotFound) {
		// No such parent. Not this check's business: the insert's foreign key
		// and requireUniqueSiblingName both answer it, with a message about the
		// parent rather than about rack space.
		return nil
	}
	if err != nil {
		// A REAL failure, propagated. Swallowing it would place a box without
		// checking anything whenever the database was unhappy -- an error
		// discarded and replaced with an answer indistinguishable from a
		// legitimate one.
		return fmt.Errorf("reading the rack: %w", err)
	}
	contents, err := s.ListAssets(ctx, AssetFilter{ParentID: *a.ParentID})
	if err != nil {
		return fmt.Errorf("reading the rack: %w", err)
	}

	units := 0
	if rack.UHeight != nil {
		units = *rack.UHeight
	}
	existing := make([]domain.Placement, 0, len(contents))
	for _, row := range contents {
		if row.RackPosition == nil || row.ID == a.ID {
			continue
		}
		existing = append(existing, placementOf(row))
	}

	// The asset being placed, resolved the same way as everything already there
	// -- so a full-depth server and a half-depth panel are judged by one rule.
	var moving AssetRow
	moving.Asset = *a
	if a.DeviceTypeID != nil {
		if dt, err := s.GetDeviceType(ctx, *a.DeviceTypeID); err == nil {
			moving.DeviceTypeUHeight, moving.DeviceTypeFullDepth = dt.UHeight, dt.FullDepth
		}
	}
	return domain.CheckPlacement(placementOf(moving), units, existing)
}
