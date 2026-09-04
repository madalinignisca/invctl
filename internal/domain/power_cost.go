// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import "strconv"

// Power cost is an ESTIMATE, and this file is where the assumptions live so
// that every one of them can be named on the page (docs/power-cost-design.md).
//
// Nothing here is measured. Nothing in this system touches the estate, and a
// metered draw would be observed state with a reporter and an age -- a
// different contract entirely (docs/AUDIT.md).

// PowerHoursPerMonth is the mean hours in a month: 8,760 / 12 = 730.
//
// A constant, but NOT a hidden one -- the report states it beside the figure
// (§4.3). A reader who cannot see the multiplier cannot check the arithmetic,
// and an estimate nobody can check is an estimate nobody should believe.
const PowerHoursPerMonth = 730

// DeclaredDraw is what the estate SAYS it draws, and how much of it said
// nothing at all.
//
// D3 (amended 2026-09-04, docs/power-cost-design.md) is the reason this
// carries exactly these two counts and NOT a ratio. The obvious "N of M
// assets declare a draw" needs an M of "every asset that could plausibly
// carry one", and there is no honest way to compute that: narrowing it by
// asset.kind hits the exact trap internal/domain/asset.go:114-127 already
// documents for CanHostInstances and IsAttachable -- asset_kind is an open
// lookup table that grows by INSERT, so a Go-side hand-listed subset silently
// excludes a kind added after the list was written, with no diagnostic. A
// coverage figure that silently stops counting a new kind is worse than no
// coverage figure.
//
// So this follows powerCoverage's existing precedent
// (internal/store/power_findings.go) instead of inventing a denominator:
// report what was recorded, not a ratio against what wasn't.
type DeclaredDraw struct {
	// TotalVA is the sum over assets of the MAXIMUM draw declared on any one
	// of an asset's inputs -- never the sum of its inputs.
	//
	// THIS IS THE WHOLE POINT AND THE FIRST DRAFT GOT IT BACKWARDS. draw_va is
	// an ALLOCATION figure, not a demand figure: each side of a dual-fed
	// server records the WHOLE load, because a feed is correctly sized only if
	// it can carry its partner's entire load when the other side dies. Summing
	// per asset returns 1,800 VA for a 900 VA server. MAX is right for a
	// redundantly-fed asset (the norm in this estate -- the false-redundancy
	// report exists because it is) and trivially right for a single-fed one.
	//
	// It is wrong for a chassis with two genuinely independent rails feeding
	// different components, where the real draw is the sum; that reads low.
	// Accepted, and the smaller error: rarer than redundant feeding, and
	// understating one unusual chassis beats doubling every correct server.
	// A real per-asset demand figure is a NEW DECLARED FIELD with a migration,
	// a form and an audit surface -- not a smarter query over this column.
	TotalVA int64
	// Declaring is how many assets contributed a figure to TotalVA -- a
	// POSITIVE count, in the same spirit as PowerReport.Assets. An asset
	// contained inside another that already declares a draw is excluded
	// entirely (§2.2 -- its power is already inside its host's wall draw) and
	// is therefore counted neither here nor as a gap: counting it as "failed
	// to declare" would make this number meaningless, and it exists to keep
	// the rest of the figure honest.
	Declaring int
	// UndeclaredDraw is how many LIVE power inputs record no draw_va at all --
	// somebody recorded the supply path but not the number, which is a real
	// gap somebody can go and close. Mirrors PowerReport.UndeclaredDraw
	// exactly, including its scope: every live input, not narrowed by
	// containment or by its asset's lifecycle, because the gap this reports is
	// "this input needs a number", not "this input is missing from the
	// estate total".
	//
	// NO RATIO AGAINST EVERY LIVE ASSET, and no asset.kind allowlist anywhere
	// near this figure -- see the type comment.
	UndeclaredDraw int
}

// PowerEstimate is the declared draw plus the rate, and every assumption in
// between. It carries no currency of its own -- Config.Currency is estate-wide.
type PowerEstimate struct {
	Draw              DeclaredDraw
	TariffMinorPerKWh int64
}

// Configured reports whether a tariff is in force. Zero is unset rather than
// free: nobody has free electricity, and rendering a computed-looking 0.00
// is the measured-looking figure this design refuses.
func (e PowerEstimate) Configured() bool { return e.TariffMinorPerKWh > 0 }

// HoursPerMonth exposes the multiplier to the template, so the page can state
// it rather than imply it.
func (e PowerEstimate) HoursPerMonth() int { return PowerHoursPerMonth }

// MonthlyMinor is the estimated monthly electricity cost.
//
// VA x power factor x hours / 1000 = kWh, and kWh x tariff = money. Power
// factor is assumed 1.0 (VA treated as W), which is conservative FOR THAT STEP
// ONLY -- real power cannot exceed apparent power at the same measurement
// point. It does NOT make the end-to-end figure an upper bound: the input is a
// typed number, the UPS and distribution losses above the declared input are
// unmodelled, and facility overhead is excluded on purpose. Do not call this a
// ceiling; it has not earned the word.
//
// ONE DIVISION, AT THE END, over the estate's summed VA. Dividing per asset
// truncates downward every time and would erode the figure silently -- the
// same reason CostTotals refuses per-line rounding.
//
// Overflow is not a risk at estate scale: a million VA at a EUR 1.00/kWh
// tariff is 7.3e10, eight orders below int64's limit.
func (e PowerEstimate) MonthlyMinor() int64 {
	return divRound(e.Draw.TotalVA*PowerHoursPerMonth*e.TariffMinorPerKWh, 1000)
}

// KWhPerMonthTenths is the energy behind the money, in tenths of a kWh.
//
// Integer tenths rather than a float, for the reason EstateCostSurface.Coverage
// gives: this is a figure for a human to read on a page that is otherwise
// exact, and a float would be the only inexact thing on it.
func (e PowerEstimate) KWhPerMonthTenths() int64 {
	return divRound(e.Draw.TotalVA*PowerHoursPerMonth*10, 1000)
}

// KWhPerMonth renders the energy for the page: "6570.0".
//
// Rendered here rather than in a template helper, and deliberately WITHOUT
// thousands separators -- the money helper in internal/web/render groups its
// output and this does not, which keeps the two visually distinct. That is a
// small win rather than an oversight: the one failure mode this design is
// arranged against is a reader adding a derived figure to a declared one.
func (e PowerEstimate) KWhPerMonth() string {
	tenths := e.KWhPerMonthTenths()
	return strconv.FormatInt(tenths/10, 10) + "." + strconv.FormatInt(tenths%10, 10)
}
