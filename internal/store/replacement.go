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
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
)

// What a refresh cost, against what it succeeded (WP-J1).
//
// THE COMPARISON IS BETWEEN ACQUISITIONS, NOT BETWEEN TOTALS. A one-off is what
// was paid for the box; a monthly line is what it costs to keep running, and
// they answer different questions. Comparing "everything this ever cost"
// against "everything that cost" would put a five-year support contract beside
// a purchase price and produce a percentage nobody can act on.
//
// THE PERCENTAGE IS DELIBERATELY NOT INFLATION-ADJUSTED HERE. Whether a rise
// beat inflation is a second question needing a series this system does not yet
// hold (WP-J2), and folding an adjustment in silently would make the raw figure
// unavailable. What is reported is the cash difference and the years between,
// which is what somebody negotiating actually quotes.

// ReplacementComparison is one box against the one it replaced.
type ReplacementComparison struct {
	// The successor and its predecessor, named so a page needs no second read.
	AssetID       string
	AssetName     string
	PredecessorID string
	// PredecessorName survives the predecessor being retired, which is the
	// ordinary state of a thing that has been replaced -- and the reason soft
	// delete is load-bearing rather than tidy.
	PredecessorName string
	// PredecessorRetired says so plainly, because a predecessor still active is
	// worth a second look: either the refresh has not finished or somebody
	// recorded the lineage the wrong way round.
	PredecessorRetired bool

	// Acquisition costs, in minor units. Zero means no one-off line was
	// recorded, which is different from "it was free" -- Known says which.
	NowMinor   int64
	ThenMinor  int64
	NowKnown   bool
	ThenKnown  bool
	NowDate    string
	ThenDate   string
	YearsApart float64
	VendorNow  string
	VendorThen string
}

// Comparable reports whether both ends carry an acquisition price. Without
// both, the page shows what it has and says what it lacks rather than printing
// a percentage computed from a missing number.
func (c ReplacementComparison) Comparable() bool { return c.NowKnown && c.ThenKnown }

// DifferenceMinor is what the refresh cost over its predecessor. Negative is a
// real and welcome answer.
func (c ReplacementComparison) DifferenceMinor() int64 { return c.NowMinor - c.ThenMinor }

// PercentChange is the rise as a whole-number percentage of the old price.
//
// Integer arithmetic, like EstateCostSurface.Coverage: a figure for a human to
// read and quote, never one anything computes from.
func (c ReplacementComparison) PercentChange() int64 {
	if !c.Comparable() || c.ThenMinor == 0 {
		return 0
	}
	return (c.NowMinor - c.ThenMinor) * 100 / c.ThenMinor
}

// Annualisable reports whether the two purchases are far enough apart for a
// per-year figure to mean anything.
//
// TWO METHODS RATHER THAN A VALUE AND AN OK, because html/template cannot
// destructure a multi-value return and the only caller is a template. A single
// method returning 0 for "do not show" would have been worse: zero is a real
// answer -- a price that did not move -- and the page must be able to tell that
// apart from a gap too short to annualise.
func (c ReplacementComparison) Annualisable() bool {
	return c.Comparable() && c.ThenMinor != 0 && c.YearsApart >= 1
}

// AnnualisedPercent spreads the change over the years between the two
// purchases, which is the figure worth comparing against an inflation rate.
//
// Only meaningful when Annualisable; over three months it is arithmetic noise
// magnified, and printing it would invite exactly the comparison it cannot
// support.
func (c ReplacementComparison) AnnualisedPercent() int64 {
	if !c.Annualisable() {
		return 0
	}
	return int64(float64(c.PercentChange()) / c.YearsApart)
}

// ReplacementFor returns the comparison for one asset, or nil when it replaced
// nothing.
func (s *SQLStore) ReplacementFor(ctx context.Context, assetID string, now time.Time) (*ReplacementComparison, error) {
	var row struct {
		AssetName       string  `db:"asset_name"`
		VendorNow       *string `db:"vendor_now"`
		PredecessorID   *string `db:"predecessor_id"`
		PredecessorName *string `db:"predecessor_name"`
		PredecessorLife *string `db:"predecessor_lifecycle"`
		VendorThen      *string `db:"vendor_then"`
	}
	err := s.readOne(ctx, &row, `
		SELECT a.name AS asset_name, a.vendor AS vendor_now,
		       p.id AS predecessor_id, p.name AS predecessor_name,
		       p.lifecycle AS predecessor_lifecycle, p.vendor AS vendor_then
		FROM asset a
		LEFT JOIN asset p ON p.id = a.replaces_asset_id
		WHERE a.id = ?`, assetID)
	if err != nil {
		return nil, fmt.Errorf("reading the replacement of %s: %w", assetID, err)
	}
	if row.PredecessorID == nil {
		return nil, nil
	}

	c := &ReplacementComparison{
		AssetID: assetID, AssetName: row.AssetName,
		PredecessorID:      *row.PredecessorID,
		PredecessorName:    derefString(row.PredecessorName),
		PredecessorRetired: derefString(row.PredecessorLife) == domain.LifecycleRetired,
		VendorNow:          derefString(row.VendorNow),
		VendorThen:         derefString(row.VendorThen),
	}

	c.NowMinor, c.NowDate, c.NowKnown, err = s.acquisitionOf(ctx, assetID)
	if err != nil {
		return nil, err
	}
	c.ThenMinor, c.ThenDate, c.ThenKnown, err = s.acquisitionOf(ctx, *row.PredecessorID)
	if err != nil {
		return nil, err
	}
	if c.NowKnown && c.ThenKnown {
		then, errThen := domain.ParseDate(c.ThenDate)
		nowAt, errNow := domain.ParseDate(c.NowDate)
		if errThen == nil && errNow == nil {
			c.YearsApart = nowAt.Sub(then).Hours() / 24 / 365.25
		}
	}
	return c, nil
}

// acquisitionOf finds what was paid once for an asset.
//
// THE EARLIEST ONE-OFF, not the largest and not the sum. A box bought once may
// later carry a one-off upgrade or a migration fee, and totalling them would
// report the purchase as more expensive than it was. For a one-off, valid_from
// IS the acquisition date -- cost.go says so, and it is why no acquired_on
// column exists.
func (s *SQLStore) acquisitionOf(ctx context.Context, assetID string) (int64, string, bool, error) {
	lines, err := s.ListAssetCosts(ctx, assetID)
	if err != nil {
		return 0, "", false, err
	}
	var (
		best  int64
		when  string
		found bool
	)
	for _, l := range lines {
		if l.Lifecycle == domain.LifecycleRetired || l.Period != domain.CostOnce {
			continue
		}
		if !found || l.ValidFrom < when {
			best, when, found = l.AmountMinor, l.ValidFrom, true
		}
	}
	return best, when, found, nil
}
