// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import "math"

// What money did, against which a price rise is judged (WP-J2).
//
// A DECLARED FACT FROM OUTSIDE THE ESTATE. Somebody reads a published index and
// types it. Nothing observes it, nothing derives it, and nothing fetches it --
// invariant 7 forbids the outbound call, and a rate that arrived on its own
// would be a figure nobody chose and nobody could date.

// InflationRate is one year's figure.
type InflationRate struct {
	Year int `db:"year"`
	// BasisPoints is hundredths of a percent: 320 is 3.2%. Integer for the
	// reason money is -- these get compounded, and three floats multiplied
	// together drift in the last place.
	BasisPoints int     `db:"basis_points"`
	Source      *string `db:"source"`
	CreatedAt   string  `db:"created_at"`
	UpdatedAt   string  `db:"updated_at"`
	RowVersion  int64   `db:"row_version"`
}

// Percent renders the rate for a reader.
func (r InflationRate) Percent() float64 { return float64(r.BasisPoints) / 100 }

// Validate checks a rate before it is stored.
func (r *InflationRate) Validate() error {
	ve := &ValidationError{}
	if r.Year < 1900 || r.Year > 2200 {
		ve.Add("year", "must be a four-digit year")
	}
	// The bound is the typo guard the CHECK also enforces: 3200 typed for 320
	// would report every price in the estate as a bargain, and silently.
	if r.BasisPoints < -5000 || r.BasisPoints > 50000 {
		ve.Add("basis_points", "must be between -50%% and 500%%, as hundredths of a percent")
	}
	return ve.OrNil()
}

// InflationSeries is the whole table, keyed by year.
type InflationSeries map[int]int

// CumulativeFactor is what one unit of money at the START of `from` is worth in
// the money of `to`, as a multiplier.
//
// COMPOUNDED, NOT SUMMED. Three years at 5% is 15.76%, not 15%, and over the
// five-to-seven-year spans this estate compares the difference is not academic.
//
// Years with no recorded rate are treated as ZERO and reported as missing by
// Covers, never guessed at. Interpolating would invent the exact figure the
// whole feature exists to check against, and a silent 0% understates inflation,
// which flatters the supplier -- the wrong way to be wrong here.
func (s InflationSeries) CumulativeFactor(from, to int) float64 {
	if to <= from {
		return 1
	}
	factor := 1.0
	for y := from; y < to; y++ {
		factor *= 1 + float64(s[y])/10000
	}
	return factor
}

// Covers reports whether every year in the span has a rate, and names the first
// that does not.
//
// The caller shows the real figure only when this is true. A partial series
// produces a number that looks authoritative and is arithmetic over zeros.
func (s InflationSeries) Covers(from, to int) (bool, int) {
	for y := from; y < to; y++ {
		if _, ok := s[y]; !ok {
			return false, y
		}
	}
	return true, 0
}

// RealPercentChange strips inflation out of a nominal rise.
//
// real = ((1 + nominal) / (1 + inflation) - 1) * 100
//
// NOT nominal minus inflation, which is the approximation everybody reaches for
// and which drifts badly at the sizes this reports: a 60% rise over five years
// of 5% inflation is 25% real, not 35%.
//
// FLOAT HERE IS DELIBERATE AND BOUNDED. Every monetary amount in this codebase
// is an integer in minor units and that is not negotiable; this is a ratio for
// a person to read, computed once at render time and stored nowhere. Returned
// as a whole number of percent, because a tenth of a percent of an estimate
// against a published index is false precision.
func (s InflationSeries) RealPercentChange(nominalPercent int64, from, to int) int64 {
	factor := s.CumulativeFactor(from, to)
	if factor <= 0 {
		return nominalPercent
	}
	real := ((1 + float64(nominalPercent)/100) / factor) - 1
	return int64(math.Round(real * 100))
}
