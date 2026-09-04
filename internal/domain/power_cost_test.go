// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import "testing"

// TestTheEstimateDividesOnceAtTheEnd is §4.3 as a test. Per-asset division
// truncates downward every time, always in the same direction, which is the
// kind of error that survives review because it looks like rounding.
//
// Five assets of 1 VA each at 28 minor/kWh: per-asset arithmetic gives
// divRound(1*730*28, 1000) = divRound(20440, 1000) = 20, five times = 100.
// Summed first: divRound(5*730*28, 1000) = divRound(102200, 1000) = 102.
// The two differ, which is what makes the test worth having.
func TestTheEstimateDividesOnceAtTheEnd(t *testing.T) {
	summedFirst := PowerEstimate{
		Draw:              DeclaredDraw{TotalVA: 5, Declaring: 5},
		TariffMinorPerKWh: 28,
	}.MonthlyMinor()

	var perAsset int64
	for i := 0; i < 5; i++ {
		perAsset += PowerEstimate{
			Draw:              DeclaredDraw{TotalVA: 1},
			TariffMinorPerKWh: 28,
		}.MonthlyMinor()
	}

	if summedFirst == perAsset {
		t.Fatalf("summed-first and per-asset arithmetic both gave %d; this fixture no "+
			"longer distinguishes them, so it proves nothing about the rule it guards",
			summedFirst)
	}
	if summedFirst != 102 {
		t.Errorf("MonthlyMinor = %d, want 102 -- the estate's VA must be summed raw "+
			"and divided once", summedFirst)
	}
}

func TestThePowerEstimateArithmetic(t *testing.T) {
	tests := []struct {
		name        string
		va          int64
		tariff      int64
		wantMonthly int64
		wantKWh     string
	}{
		// 900 VA, 730 h -> 657 kWh; at 28 minor/kWh -> 18,396 minor.
		{"one dual-fed server", 900, 28, 18396, "657.0"},
		{"nothing declared", 0, 28, 0, "0.0"},
		{"no tariff", 900, 0, 0, "657.0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := PowerEstimate{Draw: DeclaredDraw{TotalVA: tc.va}, TariffMinorPerKWh: tc.tariff}
			if got := e.MonthlyMinor(); got != tc.wantMonthly {
				t.Errorf("MonthlyMinor = %d, want %d", got, tc.wantMonthly)
			}
			if got := e.KWhPerMonth(); got != tc.wantKWh {
				t.Errorf("KWhPerMonth = %q, want %q", got, tc.wantKWh)
			}
		})
	}
}

// TestAZeroTariffIsUnsetRatherThanFree. Zero renders D5's explanation, not a
// computed-looking EUR 0.00 beside "per month".
func TestAZeroTariffIsUnsetRatherThanFree(t *testing.T) {
	if (PowerEstimate{Draw: DeclaredDraw{TotalVA: 900}}).Configured() {
		t.Error("a zero tariff reported itself configured")
	}
	if !(PowerEstimate{TariffMinorPerKWh: 1}).Configured() {
		t.Error("a one-minor-unit tariff reported itself unconfigured")
	}
}

// TestDeclaredDrawCarriesNoRatio is D3 as amended: the type has exactly the
// two counts powerCoverage's precedent already answers with -- how many
// assets contributed, and how many live inputs said nothing -- and nothing
// that could be read as "N of M assets", because M cannot be computed
// honestly (docs/power-cost-design.md D3).
func TestDeclaredDrawCarriesNoRatio(t *testing.T) {
	d := DeclaredDraw{TotalVA: 900, Declaring: 3, UndeclaredDraw: 8}
	if d.Declaring != 3 {
		t.Errorf("Declaring = %d, want 3", d.Declaring)
	}
	if d.UndeclaredDraw != 8 {
		t.Errorf("UndeclaredDraw = %d, want 8", d.UndeclaredDraw)
	}
}
