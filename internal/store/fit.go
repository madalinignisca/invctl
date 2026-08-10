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

// Physical fit, resolved against the estate. The rules are in domain/fit.go;
// this feeds them.
//
// NOTHING HERE REFUSES ANYTHING. Every function returns findings, and no caller
// turns one into a validation error -- see the header of domain/fit.go for why
// a box that does not fit must still be recordable.

// RackFitReport is one cabinet's answer.
type RackFitReport struct {
	RackID   string
	RackName string
	Fit      domain.RackFit
	Load     domain.RackLoad
	Problems []domain.FitProblem
}

// Measured reports whether the cabinet carries any measurement at all. The
// rack page branches on it to show a prompt rather than an empty verdict.
func (r RackFitReport) Measured() bool {
	return r.Fit.UsableDepthMM != nil || r.Fit.WidthMM != nil || r.Fit.MaxLoadGrams != nil
}

// LoadPercent is how full the rack is against its rating, or -1 when either
// half is unknown. A percentage of an unknown rating would be a number
// somebody plans with.
func (r RackFitReport) LoadPercent() int {
	if r.Fit.MaxLoadGrams == nil || *r.Fit.MaxLoadGrams <= 0 || r.Load.Weighed == 0 {
		return -1
	}
	return r.Load.TotalGrams * 100 / *r.Fit.MaxLoadGrams
}

// RackFit resolves one cabinet and everything placed in it.
func (s *SQLStore) RackFit(ctx context.Context, rackID string) (*RackFitReport, error) {
	rack, err := s.GetAsset(ctx, rackID)
	if err != nil {
		return nil, err
	}
	contents, err := s.ListAssets(ctx, AssetFilter{ParentID: rackID})
	if err != nil {
		return nil, fmt.Errorf("reading rack contents: %w", err)
	}
	return rackFitFrom(rack, contents), nil
}

// rackFitFrom is the pure half, so the estate sweep can reuse it without a
// second query per rack.
func rackFitFrom(rack *AssetRow, contents []AssetRow) *RackFitReport {
	r := &RackFitReport{
		RackID: rack.ID, RackName: rack.Name,
		Fit: domain.RackFit{
			UsableDepthMM: rack.UsableDepthMM,
			WidthMM:       rack.WidthMM,
			MaxLoadGrams:  rack.MaxLoadGrams,
		},
	}
	boxes := make([]domain.FitInput, 0, len(contents))
	for _, row := range contents {
		// ONLY WHAT IS PLACED AT A UNIT. An asset sitting in the rack with no
		// recorded position is not being claimed to be anywhere, so it cannot
		// be checked and must not be counted in the load -- a total that
		// silently included it would be a different number depending on whether
		// somebody had got round to recording positions.
		if row.RackPosition == nil {
			continue
		}
		if row.Lifecycle == domain.LifecycleRetired {
			continue
		}
		box := domain.FitInput{
			AssetID: row.ID, Name: row.Name,
			Position:    *row.RackPosition,
			Height:      1,
			Face:        domain.FaceFront,
			DepthMM:     row.DeviceTypeDepthMM,
			WeightGrams: row.DeviceTypeWeightGrams,
			Airflow:     row.DeviceTypeAirflow,
		}
		if row.RackFace != nil && *row.RackFace != "" {
			box.Face = *row.RackFace
		}
		if row.DeviceTypeUHeight != nil && *row.DeviceTypeUHeight > 0 {
			box.Height = *row.DeviceTypeUHeight
		}
		boxes = append(boxes, box)
	}

	r.Load = domain.Load(boxes)
	r.Problems = append(r.Problems, domain.CheckDepth(r.Fit, boxes)...)
	r.Problems = append(r.Problems, domain.CheckLoad(r.Fit, boxes)...)
	r.Problems = append(r.Problems, domain.CheckAirflow(r.Fit, boxes)...)
	return r
}

// FitFindings counts physical problems across every rack, for the overview.
type FitFindings struct {
	TooDeep         int
	FirstTooDeep    string
	Overloaded      int
	FirstOverloaded string
	SideStarved     int
	FirstSideStarve string
	OpposedAirflow  int
	FirstOpposed    string
	// UnmeasuredRacks counts cabinets holding something with no depth recorded.
	// A rack nobody has put anything in is not a gap -- there is nothing to
	// check and nothing to go and measure.
	UnmeasuredRacks int
	// UndeclaredAirflow counts PLACED boxes whose model does not say which way
	// the air goes.
	UndeclaredAirflow int
}

// EstateFit sweeps every rack.
//
// Two queries for the whole estate rather than two per rack: racks are assets
// of kind rack, and their contents are every asset whose parent is one of them.
// A per-rack loop would be a query per cabinet on a page that already runs
// several.
func (s *SQLStore) EstateFit(ctx context.Context) (*FitFindings, error) {
	racks, err := s.ListAssets(ctx, AssetFilter{Kind: domain.KindRack, Limit: fitRackLimit})
	if err != nil {
		return nil, fmt.Errorf("listing racks: %w", err)
	}
	out := &FitFindings{}
	if len(racks) == 0 {
		return out, nil
	}

	// Everything placed anywhere, indexed by parent. One read, then grouped in
	// Go -- the alternative is a query per rack.
	all, err := s.ListAssets(ctx, AssetFilter{Limit: fitAssetLimit})
	if err != nil {
		return nil, fmt.Errorf("listing placed assets: %w", err)
	}
	byParent := map[string][]AssetRow{}
	for _, row := range all {
		if row.ParentID == nil {
			continue
		}
		byParent[*row.ParentID] = append(byParent[*row.ParentID], row)
	}

	for i := range racks {
		rack := racks[i]
		report := rackFitFrom(&rack, byParent[rack.ID])
		if rack.UsableDepthMM == nil && len(byParent[rack.ID]) > 0 {
			out.UnmeasuredRacks++
		}
		for _, p := range report.Problems {
			// Counted by KIND, never by reading Detail. The first draft matched
			// on the prose and would have stopped counting the moment somebody
			// improved a sentence.
			switch p.Kind {
			case domain.FitTooDeep:
				out.TooDeep++
				if out.FirstTooDeep == "" {
					out.FirstTooDeep = fmt.Sprintf("%s in %s: %s", p.Asset, rack.Name, p.Detail)
				}
			case domain.FitOverloaded:
				out.Overloaded++
				if out.FirstOverloaded == "" {
					out.FirstOverloaded = fmt.Sprintf("%s is %s", rack.Name, p.Detail)
				}
			case domain.FitSideStarved:
				out.SideStarved++
				if out.FirstSideStarve == "" {
					out.FirstSideStarve = fmt.Sprintf("%s in %s %s", p.Asset, rack.Name, p.Detail)
				}
			case domain.FitOpposedAir:
				out.OpposedAirflow++
				if out.FirstOpposed == "" {
					out.FirstOpposed = fmt.Sprintf("%s in %s %s", p.Asset, rack.Name, p.Detail)
				}
			}
		}
		for _, b := range byParent[rack.ID] {
			if b.RackPosition != nil && b.Lifecycle != domain.LifecycleRetired &&
				b.DeviceTypeAirflow == nil {
				out.UndeclaredAirflow++
			}
		}
	}
	return out, nil
}

// Limits on the estate sweep. Generous enough that no real estate reaches them
// and bounded so a runaway query cannot page the whole table into memory.
const (
	fitRackLimit  = 5000
	fitAssetLimit = 100000
)
