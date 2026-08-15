// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain_test

import (
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// TestInflationCompoundsRatherThanSums.
//
// Three years at 5% is 15.76%, not 15%. Over the five-to-seven-year spans this
// estate compares, summing understates what money did and therefore overstates
// how badly a supplier behaved -- which is the wrong way to be wrong when the
// output is an argument with that supplier.
func TestInflationCompoundsRatherThanSums(t *testing.T) {
	s := domain.InflationSeries{2020: 500, 2021: 500, 2022: 500}
	got := s.CumulativeFactor(2020, 2023)
	if got < 1.1576 || got > 1.1577 {
		t.Errorf("factor = %.6f, want ~1.157625 (compounded); 1.15 would be summed", got)
	}
}

// TestRealChangeIsNotNominalMinusInflation. The approximation everybody reaches
// for drifts badly at the sizes this reports.
func TestRealChangeIsNotNominalMinusInflation(t *testing.T) {
	// Five years at 5%: money fell by a factor of 1.2763.
	s := domain.InflationSeries{2020: 500, 2021: 500, 2022: 500, 2023: 500, 2024: 500}

	// A 60% nominal rise over that span.
	got := s.RealPercentChange(60, 2020, 2025)
	// (1.60 / 1.27628) - 1 = 0.2536 -> 25%
	if got != 25 {
		t.Errorf("real change = %d%%, want 25%%; 60-28=32 is the subtraction this "+
			"deliberately does not do", got)
	}
}

// TestAPriceThatTrackedInflationIsFlatInRealTerms. The case that matters most
// for reading the report: a supplier who raised prices exactly as much as money
// fell has not raised them at all.
func TestAPriceThatTrackedInflationIsFlatInRealTerms(t *testing.T) {
	s := domain.InflationSeries{2023: 1000, 2024: 1000}
	// Two years at 10% compounds to 21%.
	if got := s.RealPercentChange(21, 2023, 2025); got != 0 {
		t.Errorf("real change = %d%%, want 0%% for a price that exactly tracked money", got)
	}
}

// TestAGapInTheSeriesIsReportedRatherThanGuessed.
//
// A missing year counts as zero, which UNDERSTATES inflation and so flatters
// the supplier. That is the wrong way to be wrong, which is why Covers exists
// and why the page must ask it before showing a real figure.
func TestAGapInTheSeriesIsReportedRatherThanGuessed(t *testing.T) {
	s := domain.InflationSeries{2020: 500, 2022: 500} // 2021 missing
	ok, missing := s.Covers(2020, 2023)
	if ok {
		t.Fatal("a series with a hole reports as complete")
	}
	if missing != 2021 {
		t.Errorf("names %d as missing, want 2021", missing)
	}
	// And the arithmetic still runs rather than exploding, so a caller that
	// ignores Covers gets a wrong number rather than a panic -- which is why
	// Covers is the guard and not an error return.
	if s.CumulativeFactor(2020, 2023) == 0 {
		t.Error("the factor collapsed to zero on a gap")
	}
}

// TestDeflationRoundTrips. A negative year is real and must not be refused by
// somebody who has only ever seen prices rise.
func TestDeflationRoundTrips(t *testing.T) {
	r := domain.InflationRate{Year: 2015, BasisPoints: -50}
	if err := r.Validate(); err != nil {
		t.Fatalf("deflation was refused: %v", err)
	}
	if r.Percent() != -0.5 {
		t.Errorf("Percent = %v, want -0.5", r.Percent())
	}
	s := domain.InflationSeries{2015: -50}
	if f := s.CumulativeFactor(2015, 2016); f >= 1 {
		t.Errorf("factor = %v; deflation must make money worth MORE later", f)
	}
}

// TestATypoIsRefused. 3200 typed for 320 would report every price in the estate
// as a bargain, silently.
func TestATypoIsRefused(t *testing.T) {
	r := domain.InflationRate{Year: 2024, BasisPoints: 90000}
	if err := r.Validate(); err == nil {
		t.Error("900% inflation was accepted; the typo guard does nothing")
	}
}
