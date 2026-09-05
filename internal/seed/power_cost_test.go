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

	"github.com/madalinignisca/invctl/internal/seed"
)

// TestTheFixtureCountsADualFedServerOnce is the regression the power-cost
// spec was rewritten for, and it is asserted against the SEEDED estate rather
// than a hypothetical one because the fixture is where the claim was checked:
//
//	// PROPERLY 2N: one lead per side. Converges only at the generator, which
//	// is the design and must not read as a fault.
//	{"hv-02", "DB-A/A2", "A", 900},
//	{"hv-02", "DB-B/B1", "B", 900},
//
// A ~900 VA server with 900 declared on EACH side, because each side must be
// able to carry the whole load when the other dies. The first draft of the
// design summed per asset and would report 1,800 for this box -- the identical
// 100% overstatement it existed to prevent, moved from feed scope to asset
// scope.
//
// WRITTEN AS A DELTA, not as a hardcoded estate total. A total pinned to a
// fixed VA figure would fail the next time somebody adds a row to the
// fixture, and would be "fixed" by editing the number -- which is how a
// regression test stops guarding anything. Retiring one of hv-02's two inputs
// must change NOTHING (that is MAX); retiring both must remove exactly 900
// (that is the contribution).
func TestTheFixtureCountsADualFedServerOnce(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		s, ctx := f.store, f.ctx

		hv2 := f.refs.Assets["hv-02"]
		if hv2 == "" {
			t.Fatal("the fixture has no hv-02; it is the properly-2N box this test is about")
		}
		inputs, err := s.PowerInputsFor(ctx, hv2)
		if err != nil {
			t.Fatalf("reading hv-02's inputs: %v", err)
		}
		if len(inputs) != 2 {
			t.Fatalf("hv-02 has %d inputs, want 2 -- the fixture no longer demonstrates "+
				"a dual-fed asset, so this test proves nothing about MAX vs SUM", len(inputs))
		}
		for _, in := range inputs {
			if in.DrawVA == nil || *in.DrawVA != 900 {
				t.Fatalf("hv-02 input %s declares %v, want 900 on each side -- the whole "+
					"point is that BOTH sides carry the whole load", in.Name, in.DrawVA)
			}
		}

		before, err := s.DeclaredPowerDraw(ctx)
		if err != nil {
			t.Fatalf("summing declared draw: %v", err)
		}

		// Retire the B side. A SUM would drop 900 here; MAX drops nothing.
		if err := s.RetirePowerInput(ctx, seed.Permit, inputs[1].ID); err != nil {
			t.Fatalf("retiring hv-02's second input: %v", err)
		}
		oneSide, err := s.DeclaredPowerDraw(ctx)
		if err != nil {
			t.Fatalf("summing declared draw after retiring one side: %v", err)
		}
		if oneSide.TotalVA != before.TotalVA {
			t.Fatalf("retiring one of hv-02's two 900 VA inputs moved the estate total "+
				"from %d to %d VA; the query is SUMMING a dual-fed asset's inputs, which "+
				"doubles every properly-redundant server in the estate",
				before.TotalVA, oneSide.TotalVA)
		}

		// Retire the A side too. Now hv-02 contributes nothing, and the drop
		// is exactly what it was contributing: 900, not 1,800.
		if err := s.RetirePowerInput(ctx, seed.Permit, inputs[0].ID); err != nil {
			t.Fatalf("retiring hv-02's first input: %v", err)
		}
		none, err := s.DeclaredPowerDraw(ctx)
		if err != nil {
			t.Fatalf("summing declared draw after retiring both sides: %v", err)
		}
		if got := before.TotalVA - none.TotalVA; got != 900 {
			t.Errorf("hv-02 contributed %d VA to the estate total, want 900 -- it is a "+
				"900 VA server whose whole load is recorded on each of two sides", got)
		}
		// Coverage moves with it: the box stops declaring, it does not stop
		// existing. There is no "qualifying" denominator to keep in step with
		// it (D3, amended) -- only the positive count of what contributed.
		if none.Declaring != before.Declaring-1 {
			t.Errorf("Declaring went from %d to %d, want a drop of exactly 1",
				before.Declaring, none.Declaring)
		}
	})
}
