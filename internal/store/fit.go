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
	leads, runs, err := s.rackCabling(ctx, contents)
	if err != nil {
		return nil, err
	}
	return rackFitFrom(rack, contents, leads, runs), nil
}

// rackCabling counts what is plugged into each box in one cabinet, and resolves
// the cables running BETWEEN two boxes in it.
//
// SAME CABINET ONLY for the runs, which is a limit and not an omission: two
// racks have no recorded distance between them. There is no floor plan here, so
// the span between cabinets is unknown, and a length check that guessed it
// would be inventing the number the answer turns on.
func (s *SQLStore) rackCabling(ctx context.Context, contents []AssetRow) (map[string]int, []domain.CableRun, error) {
	unitOf := make(map[string]int, len(contents))
	nameOf := make(map[string]string, len(contents))
	ids := make([]string, 0, len(contents))
	for _, row := range contents {
		if row.RackPosition == nil || row.Lifecycle == domain.LifecycleRetired {
			continue
		}
		unitOf[row.ID] = *row.RackPosition
		nameOf[row.ID] = row.Name
		ids = append(ids, row.ID)
	}
	if len(ids) == 0 {
		return map[string]int{}, nil, nil
	}

	// Every active cable with at least one end on a box in this cabinet, with
	// the owning asset of each end resolved. One query rather than one per box.
	faceOf := make(map[string]*string, len(contents))
	for _, row := range contents {
		faceOf[row.ID] = row.DeviceTypePortFace
	}

	var rows []struct {
		LinkID  string `db:"link_id"`
		LengthM *int   `db:"length_m"`
		AAsset  string `db:"a_asset"`
		BAsset  string `db:"b_asset"`
		AName   string `db:"a_name"`
		BName   string `db:"b_name"`
	}
	ph := placeholders(len(ids))
	args := append(append([]any{domain.LifecycleActive}, anySlice(ids)...), anySlice(ids)...)
	if err := s.read(ctx, &rows, `
		SELECT l.id AS link_id, l.length_m AS length_m,
		       ia.asset_id AS a_asset, ib.asset_id AS b_asset,
		       ia.name AS a_name, ib.name AS b_name
		FROM link l
		JOIN interface ia ON ia.id = l.a_interface_id
		JOIN interface ib ON ib.id = l.b_interface_id
		WHERE l.lifecycle = ?
		  AND (ia.asset_id IN (`+ph+`) OR ib.asset_id IN (`+ph+`))`, args...); err != nil {
		return nil, nil, fmt.Errorf("reading rack cabling: %w", err)
	}

	leads := make(map[string]int, len(ids))
	var runs []domain.CableRun
	for _, r := range rows {
		// A cable counts as a lead on each end that is in this cabinet, so a
		// patch between two boxes here is one lead on each rather than two on
		// one -- which is what somebody looking at the channel sees.
		_, aHere := unitOf[r.AAsset]
		_, bHere := unitOf[r.BAsset]
		if aHere {
			leads[r.AAsset]++
		}
		if bHere {
			leads[r.BAsset]++
		}
		// Both ends in this cabinet is what makes a run comparable at all --
		// the crossing check needs two faces, and the length check needs a
		// distance that only exists inside one rack.
		if !aHere || !bHere {
			continue
		}
		run := domain.CableRun{
			LinkID:   r.LinkID,
			Label:    nameOf[r.AAsset] + "/" + r.AName + " to " + nameOf[r.BAsset] + "/" + r.BName,
			FromUnit: unitOf[r.AAsset],
			ToUnit:   unitOf[r.BAsset],
			FromFace: faceOf[r.AAsset], ToFace: faceOf[r.BAsset],
			FromName: nameOf[r.AAsset], ToName: nameOf[r.BAsset],
		}
		// A cable with no length recorded is not a finding: nobody claimed
		// anything about it, and reporting every unmeasured patch lead would
		// bury the one that cannot reach. Zero means the same as absent.
		if r.LengthM != nil && *r.LengthM > 0 {
			run.LengthM = *r.LengthM
		}
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Label < runs[j].Label })
	return leads, runs, nil
}

// rackFitFrom is the pure half, so the estate sweep can reuse it without a
// second query per rack.
func rackFitFrom(rack *AssetRow, contents []AssetRow, leads map[string]int, runs []domain.CableRun) *RackFitReport {
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
			PortFace:    row.DeviceTypePortFace,
			Leads:       leads[row.ID],
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
	r.Problems = append(r.Problems, domain.CheckCabling(r.Fit, boxes, runs)...)
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

	// Cabling (WP-C3).
	WrongFace       int
	FirstWrongFace  string
	DenseLeads      int
	FirstDenseLeads string
	ShortCables     int
	FirstShortCable string
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
		leads, runs, err := s.rackCabling(ctx, byParent[rack.ID])
		if err != nil {
			return nil, err
		}
		report := rackFitFrom(&rack, byParent[rack.ID], leads, runs)
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
			case domain.FitPortsWrongFace:
				out.WrongFace++
				if out.FirstWrongFace == "" {
					// The detail already names both ends, so the summary adds
					// only the cabinet -- "pp-a2-1 in rack-a2 is pp-a2-1 (front
					// ports) to ..." named it twice.
					out.FirstWrongFace = fmt.Sprintf("in %s, %s", rack.Name, p.Detail)
				}
			case domain.FitLeadDensity:
				out.DenseLeads++
				if out.FirstDenseLeads == "" {
					out.FirstDenseLeads = fmt.Sprintf("%s in %s: %s", p.Asset, rack.Name, p.Detail)
				}
			case domain.FitCableTooShort:
				out.ShortCables++
				if out.FirstShortCable == "" {
					out.FirstShortCable = fmt.Sprintf("%s %s", p.Asset, p.Detail)
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
