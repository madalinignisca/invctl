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
	// UnmodelledSites is how many LIVE sites carry no power panel at all --
	// added by the stage-7 review (§4b.9), after D3's amendment over-
	// generalised its own objection and dropped it. D3 refuses a hand-listed
	// `kind IN (...)` allowlist because asset_kind is an open lookup table
	// that grows by INSERT; `a.kind = ?` against the single, closed
	// domain.KindSite is not that shape, and it is the ONE count that answers
	// how much of the estate this figure could not see at all. Reused, not
	// reimplemented -- internal/store/power_findings.go's powerCoverage
	// computes the identical fact for PowerReport.UnmodelledSites, and this
	// runs the same query rather than a second copy of it.
	//
	// Deliberately still not a ratio: three sites with one power-modelled is
	// not "33% coverage", it is a figure covering a third of the estate,
	// wrong in the direction that makes staying look cheap.
	UnmodelledSites int
	// Assets is PowerReport.Assets (internal/store/power_findings.go): live
	// assets carrying at least one live power input, full stop -- REGARDLESS
	// of whether any of those inputs declared a draw. Added by the round-2
	// re-review (§4c.17) after it found a fourth render state the three
	// counts above cannot detect: model one rack where a single server
	// declares a draw and two hundred other assets have no power_input row
	// at all (every site already has a panel; panels get created early,
	// inputs late). TotalVA > 0 so a real figure renders, UndeclaredDraw is
	// 0 -- it counts input ROWS with a NULL draw_va, and an asset with no
	// input row produces no row to count -- and UnmodelledSites is 0. The
	// page would print money under three green coverage signals over an
	// estate that is half a percent modelled.
	//
	// Declaring is a subset of Assets, never the other way round, and the
	// page reports it as "N of M assets that have a power input declared a
	// draw" -- a real ratio, unlike the ones D3 refuses, because both halves
	// come from the identical query (assetsWithPowerInput, reused rather
	// than reimplemented, the same precedent UnmodelledSites already set).
	Assets int
}

// PowerEstimate is the declared draw plus the rate, and every assumption in
// between. It carries no currency of its own -- Config.Currency is estate-wide.
type PowerEstimate struct {
	Draw DeclaredDraw
	// TariffHundredthsMinorPerKWh is the configured rate, in hundredths of a
	// minor unit -- widened from whole minor units by item 21 before
	// anything deployed, because a real rate like 0.2847 could otherwise
	// only be entered as 28, an error an order of magnitude larger than the
	// truncation §4.3 exists to prevent. The extra digit is folded into the
	// same end-of-chain division MonthlyMinor already performs, the same
	// technique effectivePUEHundredths uses for its own extra digit.
	TariffHundredthsMinorPerKWh int64
	// PUEHundredths is the operator-DECLARED facility Power Usage
	// Effectiveness, in integer hundredths (140 = a PUE of 1.40) -- D6,
	// added by the stage-7 review after §1 and §5 were found in
	// contradiction (§1 promised a keep-or-move figure; §5 forbade the one
	// multiplier that makes one comparable to a hosting quote).
	//
	// ZERO MEANS UNDECLARED, not "PUE 0" -- a facility using less power than
	// the load inside it is physically impossible, so zero can never be a
	// real value and is free to mean "not set" the way TariffHundredthsMinorPerKWh
	// reuses zero for "no tariff". PUEDeclared reports which case this is;
	// effectivePUEHundredths is what the arithmetic actually uses.
	//
	// Never invented, never defaulted from site metadata, never implied to be
	// measured -- it is declared exactly as the tariff is (docs/power-cost-
	// design.md §5's "No invented PUE", amended for D6).
	PUEHundredths int64
}

// Configured reports whether a tariff is in force. Zero is unset rather than
// free: nobody has free electricity, and rendering a computed-looking 0.00
// is the measured-looking figure this design refuses.
func (e PowerEstimate) Configured() bool { return e.TariffHundredthsMinorPerKWh > 0 }

// HasFigure is B1 (§4b.7): Configured tests the tariff ALONE, and a tariff
// set over an estate with no declared draw prints a computed-looking 0.00 --
// day one of every real deployment, because the tariff is one environment
// variable and the draws are hundreds of form entries. D3's own last sentence
// already required this and it was not built the first time: "if nothing at
// all declares a draw, say that in words rather than showing a zero."
//
// The template branches on this, not on Configured, before it renders an
// amount.
func (e PowerEstimate) HasFigure() bool { return e.Configured() && e.Draw.TotalVA > 0 }

// PUEDeclared reports whether an operator declared a facility PUE. Unset
// must render EXACTLY today's output -- this is what lets the template show
// the facility figure only when there is a second assumption behind it to
// name.
func (e PowerEstimate) PUEDeclared() bool { return e.PUEHundredths > 0 }

// effectivePUEHundredths is what the arithmetic actually multiplies by: the
// declared value, or 100 (a no-op PUE of 1.0) when nothing was declared. This
// is the one place "undeclared" turns into a number -- everywhere else it
// stays a question ("PUEDeclared?") so nothing downstream can mistake an
// unset PUE for a declared 1.0.
func (e PowerEstimate) effectivePUEHundredths() int64 {
	if e.PUEHundredths <= 0 {
		return 100
	}
	return e.PUEHundredths
}

// PUE renders the declared multiplier for the page, e.g. "1.40". Operators
// know a PUE as a decimal, not as hundredths -- the same reason the tariff is
// entered in whole minor units rather than the reverse.
func (e PowerEstimate) PUE() string {
	h := e.effectivePUEHundredths()
	return strconv.FormatInt(h/100, 10) + "." + pad2(h%100)
}

func pad2(n int64) string {
	if n < 10 {
		return "0" + strconv.FormatInt(n, 10)
	}
	return strconv.FormatInt(n, 10)
}

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
	return divRound(e.Draw.TotalVA*PowerHoursPerMonth*e.TariffHundredthsMinorPerKWh, 1000*100)
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

// FacilityMonthlyMinor is D6's second figure: the IT-load estimate above,
// multiplied by the declared PUE, for a facility-inclusive comparison against
// a hosting quote.
//
// THE PUE IS FOLDED INTO THE SAME END-OF-CHAIN DIVISION, not applied as a
// second division over an already-rounded MonthlyMinor -- §4.3's one-division
// property (sum raw VA first, divide once) would otherwise be reintroduced by
// the back door the moment a second assumption was added. Multiplying the
// numerator by effectivePUEHundredths and the divisor by 100 is arithmetically
// identical to dividing by 100 twice, so an undeclared PUE (effective 100,
// i.e. 1.00) reproduces MonthlyMinor's result bit-for-bit -- verified in
// TestAnUndeclaredPUEReproducesTheUnmultipliedFigureExactly, because "unset
// changes nothing" is a claim worth a test, not just a comment.
func (e PowerEstimate) FacilityMonthlyMinor() int64 {
	return divRound(
		e.Draw.TotalVA*PowerHoursPerMonth*e.TariffHundredthsMinorPerKWh*e.effectivePUEHundredths(),
		1000*100*100)
}

// FacilityKWhPerMonthTenths is the energy behind FacilityMonthlyMinor, on the
// same one-division footing as KWhPerMonthTenths.
func (e PowerEstimate) FacilityKWhPerMonthTenths() int64 {
	return divRound(
		e.Draw.TotalVA*PowerHoursPerMonth*10*e.effectivePUEHundredths(),
		1000*100)
}

// FacilityKWhPerMonth renders the facility energy figure for the page, in the
// same "6570.0" shape as KWhPerMonth.
func (e PowerEstimate) FacilityKWhPerMonth() string {
	tenths := e.FacilityKWhPerMonthTenths()
	return strconv.FormatInt(tenths/10, 10) + "." + strconv.FormatInt(tenths%10, 10)
}
