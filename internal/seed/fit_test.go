// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package seed_test

import (
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/seed"
	"github.com/madalinignisca/invctl/internal/store"
)

// The physical checks, against the fixture rather than against a mock.
//
// The unit tests in internal/domain prove the RULES. These prove the estate
// actually contains the arrangements those rules are for -- which is the half
// that rots. Three times in this repository a test has been satisfied by a
// fixture that no longer held the case it named, and every one of them was
// found by counting rather than by reading.

// TestTheFixtureHoldsEveryPhysicalCase.
//
// Each check needs something to find AND something to stay quiet about. A
// fixture with only bad cabinets would pass a check that fired on everything.
func TestTheFixtureHoldsEveryPhysicalCase(t *testing.T) {
	seed.CompanyEstate = true
	t.Cleanup(func() { seed.CompanyEstate = false })

	eachEngine(t, func(t *testing.T, f *fixture) {
		s, ctx := f.store, f.ctx

		fit, err := s.EstateFit(ctx)
		if err != nil {
			t.Fatalf("sweeping the estate: %v", err)
		}

		for _, tc := range []struct {
			what string
			got  int
			why  string
		}{
			{"too deep", fit.TooDeep,
				"rack-a2 is 600mm usable and holds a 772mm server"},
			{"overloaded", fit.Overloaded,
				"colo-rack-07 is rated 600kg and holds more"},
			{"side starved", fit.SideStarved,
				"fw-branch-1 draws from the sides in a 600mm cabinet"},
			{"opposed airflow", fit.OpposedAirflow,
				"a rear-to-front switch sits directly above a front-to-rear server"},
			{"unmeasured racks", fit.UnmeasuredRacks,
				"rack-b1 has no depth recorded and holds something"},
			{"undeclared airflow", fit.UndeclaredAirflow,
				"not every placed box has a catalogued model"},
		} {
			if tc.got == 0 {
				t.Errorf("the estate reports no %q findings, so any test naming that case "+
					"proves nothing — expected because %s", tc.what, tc.why)
			}
		}
	})
}

// TestAProperlySpecifiedCabinetReportsNothing.
//
// THE NEGATIVE CONTROL, and the reason rack-a1 is measured at all. Without a
// cabinet that comes back clean, a check that reported every rack in the estate
// would satisfy every other assertion in this file.
func TestAProperlySpecifiedCabinetReportsNothing(t *testing.T) {
	seed.CompanyEstate = true
	t.Cleanup(func() { seed.CompanyEstate = false })

	eachEngine(t, func(t *testing.T, f *fixture) {
		s, ctx := f.store, f.ctx

		id, ok := f.refs.Assets["rack-a1"]
		if !ok {
			t.Fatal("no rack-a1 in the estate")
		}
		report, err := s.RackFit(ctx, id)
		if err != nil {
			t.Fatalf("resolving rack-a1: %v", err)
		}
		if !report.Measured() {
			t.Fatal("rack-a1 carries no measurements, so it cannot be the clean case")
		}
		for _, p := range report.Problems {
			if p.Kind == domain.FitTooDeep || p.Kind == domain.FitOverloaded ||
				p.Kind == domain.FitSideStarved {
				t.Errorf("rack-a1 is specified for what is in it and reported %q: %s",
					p.Kind, p.Detail)
			}
		}
	})
}

// TestTheShallowCabinetReportsBothItsProblemsSeparately.
//
// rack-a2 is wrong in two independent ways -- too shallow for a server and too
// narrow for a side-breather -- and the two must not collapse into one row. A
// single "this rack is bad" finding would be true and useless: they are fixed
// differently, one by moving a box and one by buying a cabinet.
func TestTheShallowCabinetReportsBothItsProblemsSeparately(t *testing.T) {
	seed.CompanyEstate = true
	t.Cleanup(func() { seed.CompanyEstate = false })

	eachEngine(t, func(t *testing.T, f *fixture) {
		s, ctx := f.store, f.ctx

		id, ok := f.refs.Assets["rack-a2"]
		if !ok {
			t.Fatal("no rack-a2 in the estate")
		}
		report, err := s.RackFit(ctx, id)
		if err != nil {
			t.Fatalf("resolving rack-a2: %v", err)
		}
		seen := map[string]string{}
		for _, p := range report.Problems {
			seen[p.Kind] = p.Detail
		}
		if _, ok := seen[domain.FitSideStarved]; !ok {
			t.Errorf("rack-a2 holds fw-branch-1, which draws from the sides in a 600mm "+
				"cabinet, and reported no side-clearance finding. Got: %v", seen)
		}
		if _, ok := seen[domain.FitTooDeep]; !ok {
			t.Errorf("rack-a2 is 600mm usable and holds a full-depth server, and reported "+
				"no depth finding. Got: %v", seen)
		}
	})
}

// TestNothingPhysicalEverRefusesAPlacement.
//
// The rule the whole work package rests on. A box that does not fit is IN the
// rack with the door open, and an inventory that refuses to record it has made
// itself less useful to make its validator tidier.
//
// Proved by doing the thing: place a 772mm server in the 600mm cabinet and
// require the write to succeed.
func TestNothingPhysicalEverRefusesAPlacement(t *testing.T) {
	seed.CompanyEstate = true
	t.Cleanup(func() { seed.CompanyEstate = false })

	eachEngine(t, func(t *testing.T, f *fixture) {
		s, ctx := f.store, f.ctx

		rackID, ok := f.refs.Assets["rack-a2"]
		if !ok {
			t.Fatal("no rack-a2 in the estate")
		}
		modelID, ok := f.refs.DeviceTypes["PowerEdge R650"]
		if !ok {
			t.Fatal("no R650 in the catalogue, so this proves nothing about a deep box")
		}

		a, err := domain.NewAsset(store.NewID(), domain.KindServer, "srv-does-not-fit", &rackID, s.Now())
		if err != nil {
			t.Fatalf("building the asset: %v", err)
		}
		a.DeviceTypeID = &modelID
		// A free unit, so nothing REFUSES it for a reason other than depth --
		// otherwise an overlap rejection would look like the physical check
		// refusing, and this test would pass for the wrong reason.
		a.RackPosition, a.RackFace = intPtr(37), strPtr(domain.FaceFront)

		if err := s.CreateAsset(ctx, seed.Actor, a, nil); err != nil {
			t.Fatalf("a box too deep for its cabinet was REFUSED: %v\n"+
				"Physical fit warns; it never refuses. The estate has to be able to "+
				"describe a bad reality or people stop recording it.", err)
		}

		// And having accepted it, the rack must say so.
		report, err := s.RackFit(ctx, rackID)
		if err != nil {
			t.Fatalf("resolving the rack: %v", err)
		}
		found := false
		for _, p := range report.Problems {
			if p.Kind == domain.FitTooDeep && p.AssetID == a.ID {
				found = true
			}
		}
		if !found {
			t.Error("the box was accepted and then not reported, which is the worst of " +
				"both: recorded and silent")
		}
	})
}

func intPtr(n int) *int       { return &n }
func strPtr(s string) *string { return &s }
