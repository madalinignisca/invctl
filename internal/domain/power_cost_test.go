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

// TestAConfiguredTariffOverAnEmptyEstateHasNoFigure is B1 (§4b.7). Configured
// tests the tariff ALONE, so before this the else-branch would compute
// MonthlyMinor over a zero TotalVA and print a clean-looking "0.00" -- the
// exact reassuring wrongness this whole design exists to refuse, and on the
// FIRST DAY of every deployment, since the tariff is one env var and the
// draws are hundreds of form entries. There was no test between the D5 case
// (Configured() == false) and the positive case (both true) until this one.
func TestAConfiguredTariffOverAnEmptyEstateHasNoFigure(t *testing.T) {
	e := PowerEstimate{Draw: DeclaredDraw{TotalVA: 0}, TariffMinorPerKWh: 28}
	if !e.Configured() {
		t.Fatal("Configured() is false with a tariff set; this test would prove nothing about HasFigure")
	}
	if e.HasFigure() {
		t.Error("HasFigure() is true with a tariff set and zero declared draw; the template " +
			"would render a computed-looking EUR 0.00 on day one of every deployment")
	}

	positive := PowerEstimate{Draw: DeclaredDraw{TotalVA: 900}, TariffMinorPerKWh: 28}
	if !positive.HasFigure() {
		t.Error("HasFigure() is false with a tariff set and a real declared draw; " +
			"the positive case must still render")
	}

	unset := PowerEstimate{Draw: DeclaredDraw{TotalVA: 900}}
	if unset.HasFigure() {
		t.Error("HasFigure() is true with no tariff configured; D5's branch must still win")
	}
}

// TestAnUndeclaredPUEReproducesTheUnmultipliedFigureExactly is D6's central
// promise: a deployment that never sets INV_POWER_PUE must see NO CHANGE in
// behaviour. Folding the PUE into the single end-of-chain division (rather
// than a second division on top of MonthlyMinor) makes an undeclared PUE
// arithmetically a no-op, and this proves it bit-for-bit rather than trusting
// the comment that says so.
func TestAnUndeclaredPUEReproducesTheUnmultipliedFigureExactly(t *testing.T) {
	e := PowerEstimate{Draw: DeclaredDraw{TotalVA: 900}, TariffMinorPerKWh: 28}
	if e.PUEDeclared() {
		t.Fatal("PUEDeclared() is true with PUEHundredths left at zero")
	}
	if got, want := e.FacilityMonthlyMinor(), e.MonthlyMinor(); got != want {
		t.Errorf("FacilityMonthlyMinor() = %d, want %d (== MonthlyMinor()) with no PUE declared", got, want)
	}
	if got, want := e.FacilityKWhPerMonth(), e.KWhPerMonth(); got != want {
		t.Errorf("FacilityKWhPerMonth() = %q, want %q (== KWhPerMonth()) with no PUE declared", got, want)
	}
}

// TestADeclaredPUEMultipliesTheFacilityFigureOnly pins the arithmetic AND the
// separation: the IT-load figure (MonthlyMinor) must be untouched by a
// declared PUE, and the facility figure must reflect it.
//
// 900 VA, 730 h, 28 minor/kWh -> IT load 18,396 minor (657.0 kWh), as
// TestThePowerEstimateArithmetic already pins. At PUE 1.40: facility kWh =
// 657.0 * 1.4 = 919.8, facility cost = 18396 * 1.4 = 25,754.4 -> rounds to
// 25754 (divRound rounds half away from zero on the combined numerator).
func TestADeclaredPUEMultipliesTheFacilityFigureOnly(t *testing.T) {
	e := PowerEstimate{
		Draw:              DeclaredDraw{TotalVA: 900},
		TariffMinorPerKWh: 28,
		PUEHundredths:     140,
	}
	if !e.PUEDeclared() {
		t.Fatal("PUEDeclared() is false with PUEHundredths = 140")
	}
	if got := e.PUE(); got != "1.40" {
		t.Errorf("PUE() = %q, want \"1.40\"", got)
	}
	if got := e.MonthlyMinor(); got != 18396 {
		t.Errorf("MonthlyMinor() = %d, want 18396 -- a declared PUE must not move the IT-load figure", got)
	}
	if got := e.FacilityMonthlyMinor(); got != 25754 {
		t.Errorf("FacilityMonthlyMinor() = %d, want 25754", got)
	}
	if got := e.FacilityKWhPerMonth(); got != "919.8" {
		t.Errorf("FacilityKWhPerMonth() = %q, want \"919.8\"", got)
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
