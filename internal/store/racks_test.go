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
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Rack elevations, and the rule that makes them worth having: two boxes cannot
// be in the same place, and "the same place" depends on depth.

func rackWith(t *testing.T, s *SQLStore, ctx context.Context, units int) (string, string) {
	t.Helper()
	site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
	rack := mustAsset(t, s, ctx, domain.KindRack, "rack-1", &site)
	row, err := s.GetAsset(ctx, rack)
	if err != nil {
		t.Fatalf("reading the rack: %v", err)
	}
	if units > 0 {
		row.UHeight = &units
		if err := s.UpdateAsset(ctx, testPermit, &row.Asset, nil); err != nil {
			t.Fatalf("setting the rack height: %v", err)
		}
	}
	return site, rack
}

// mount places a box, returning the error rather than failing, so refusals can
// be asserted.
func mount(t *testing.T, s *SQLStore, ctx context.Context, rack, name string,
	pos int, face string, deviceType string) error {
	t.Helper()
	a, err := domain.NewAsset(NewID(), domain.KindServer, name, &rack, s.Now())
	if err != nil {
		t.Fatalf("building %s: %v", name, err)
	}
	a.RackPosition = &pos
	if face != "" {
		a.RackFace = &face
	}
	if deviceType != "" {
		a.DeviceTypeID = &deviceType
	}
	return s.CreateAsset(ctx, testPermit, a, nil)
}

func TestTwoBoxesCannotOccupyTheSameUnit(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			_, rack := rackWith(t, s, ctx, 42)

			if err := mount(t, s, ctx, rack, "srv-1", 10, "", ""); err != nil {
				t.Fatalf("placing the first box: %v", err)
			}
			err := mount(t, s, ctx, rack, "srv-2", 10, "", "")
			if err == nil {
				t.Fatal("two boxes were placed at U10")
			}
			if msg := fieldError(err, "rack_position"); msg == "" {
				t.Errorf("error = %v, want a field failure on rack_position so the form "+
					"can render it", err)
			}

			// The control: the next unit up is free, so this is not a rule that
			// refuses everything.
			if err := mount(t, s, ctx, rack, "srv-3", 11, "", ""); err != nil {
				t.Errorf("placing a box at a free unit was refused: %v", err)
			}
		})
	}
}

// TestBackToBackHalfDepthBoxesShareAUnit is the case a single-view elevation
// gets wrong.
func TestBackToBackHalfDepthBoxesShareAUnit(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			_, rack := rackWith(t, s, ctx, 42)
			mf := mustManufacturer(t, s, ctx, "acme", "Acme")

			// Half depth: full_depth defaults false on a spec that does not set it.
			half, err := domain.NewDeviceType(NewID(), domain.DeviceTypeSpec{
				ManufacturerID: mf, Model: "Patch-24", UHeight: intPtr(1),
			}, s.Now())
			if err != nil {
				t.Fatalf("building: %v", err)
			}
			if err := s.CreateDeviceType(ctx, testPermit, half); err != nil {
				t.Fatalf("creating: %v", err)
			}

			if err := mount(t, s, ctx, rack, "patch-front", 20, domain.FaceFront, half.ID); err != nil {
				t.Fatalf("placing the front panel: %v", err)
			}
			// Same unit, other face. This is how a network rack is actually
			// built, and refusing it would make the model wrong about reality.
			if err := mount(t, s, ctx, rack, "patch-rear", 20, domain.FaceRear, half.ID); err != nil {
				t.Fatalf("a half-depth box was refused the REAR of a unit whose front is "+
					"taken: %v\nBack-to-back mounting is ordinary; a model that forbids it "+
					"is a model that disagrees with the rack.", err)
			}

			// And a full-depth box may not go where either face is taken.
			full, err := domain.NewDeviceType(NewID(), domain.DeviceTypeSpec{
				ManufacturerID: mf, Model: "Server-1U", UHeight: intPtr(1), FullDepth: true,
			}, s.Now())
			if err != nil {
				t.Fatalf("building: %v", err)
			}
			if err := s.CreateDeviceType(ctx, testPermit, full); err != nil {
				t.Fatalf("creating: %v", err)
			}
			if err := mount(t, s, ctx, rack, "srv-deep", 20, domain.FaceFront, full.ID); err == nil {
				t.Error("a full-depth box was placed at a unit whose front is occupied")
			}
		})
	}
}

func TestAMultiUnitBoxOccupiesEveryUnitItCovers(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			_, rack := rackWith(t, s, ctx, 42)
			mf := mustManufacturer(t, s, ctx, "acme", "Acme")
			big, err := domain.NewDeviceType(NewID(), domain.DeviceTypeSpec{
				ManufacturerID: mf, Model: "Chassis-4U", UHeight: intPtr(4), FullDepth: true,
			}, s.Now())
			if err != nil {
				t.Fatalf("building: %v", err)
			}
			if err := s.CreateDeviceType(ctx, testPermit, big); err != nil {
				t.Fatalf("creating: %v", err)
			}

			if err := mount(t, s, ctx, rack, "chassis", 10, domain.FaceFront, big.ID); err != nil {
				t.Fatalf("placing the chassis: %v", err)
			}
			// U10-U13 are taken. U12 is in the middle, which a naive check that
			// compared only start positions would miss.
			if err := mount(t, s, ctx, rack, "srv-mid", 12, domain.FaceFront, big.ID); err == nil {
				t.Error("a box was placed inside the space a 4U chassis occupies")
			}
			// AND FROM BELOW. A 4U box at U8 covers U8-U11, so it collides at
			// U10 and U11 -- units that are neither box's first. A check that
			// compared only starting positions, or that walked only the first
			// unit of the box being placed, would allow this.
			if err := mount(t, s, ctx, rack, "srv-below", 8, domain.FaceFront, big.ID); err == nil {
				t.Error("a 4U box was placed at U8, overlapping a chassis at U10-U13 in " +
					"its own upper units")
			}

			if err := mount(t, s, ctx, rack, "srv-above", 14, domain.FaceFront, ""); err != nil {
				t.Errorf("the unit above the chassis was refused: %v", err)
			}
		})
	}
}

func TestARackRefusesWhatWouldStickOutOfTheTop(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			_, rack := rackWith(t, s, ctx, 12)

			if err := mount(t, s, ctx, rack, "too-high", 13, "", ""); err == nil {
				t.Error("a box was placed above the top of a 12U rack")
			}
			if err := mount(t, s, ctx, rack, "just-fits", 12, "", ""); err != nil {
				t.Errorf("the top unit of the rack was refused: %v", err)
			}
		})
	}
}

// TestAnUnmeasuredRackRefusesNothing covers a guess being enforced as a rule.
func TestAnUnmeasuredRackRefusesNothing(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			_, rack := rackWith(t, s, ctx, 0) // height not recorded

			// 50 is above the assumed 42. Refusing it would be enforcing a
			// default nobody stated.
			if err := mount(t, s, ctx, rack, "high-up", 50, "", ""); err != nil {
				t.Errorf("a position was refused against a height nobody recorded: %v\n"+
					"Racks are usually 42U; that is a display default, not a rule.", err)
			}
		})
	}
}

func TestAnElevationDrawsWhatIsPlacedAndListsWhatIsNot(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			_, rack := rackWith(t, s, ctx, 10)

			if err := mount(t, s, ctx, rack, "placed", 3, "", ""); err != nil {
				t.Fatalf("placing: %v", err)
			}
			// In the rack, position unrecorded: the ordinary starting state.
			a, err := domain.NewAsset(NewID(), domain.KindServer, "somewhere", &rack, s.Now())
			if err != nil {
				t.Fatalf("building: %v", err)
			}
			if err := s.CreateAsset(ctx, testPermit, a, nil); err != nil {
				t.Fatalf("creating: %v", err)
			}

			e2, err := s.Elevation(ctx, rack)
			if err != nil {
				t.Fatalf("elevation: %v", err)
			}
			if len(e2.Units) != 10 {
				t.Fatalf("drew %d units, want 10", len(e2.Units))
			}
			// Top first: a rack is read from the top even though U1 is the floor.
			if e2.Units[0].Number != 10 || e2.Units[9].Number != 1 {
				t.Errorf("units run %d..%d, want 10..1", e2.Units[0].Number, e2.Units[9].Number)
			}
			if !e2.HeightKnown {
				t.Error("a recorded height is reported as assumed")
			}
			if len(e2.Unpositioned) != 1 || e2.Unpositioned[0].Name != "somewhere" {
				t.Errorf("unpositioned = %+v, want the one box nobody has located. "+
					"A diagram of one box in a rack of ten is misleading without it.",
					e2.Unpositioned)
			}
			if e2.FreeUnits != 9 {
				t.Errorf("free units = %d, want 9", e2.FreeUnits)
			}
		})
	}
}
