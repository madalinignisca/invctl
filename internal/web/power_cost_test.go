// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// TestThePowerSectionSaysWhyItIsEmptyWithNoTariff is D5. The draft had the
// section silently absent when no tariff is configured, by analogy with the
// read-only API staying unmounted. That analogy is wrong: those are surfaces
// that do not exist, this is a section of a page the reader is already
// looking at -- and an administrator who sees nothing cannot tell "not
// configured" from "nothing to show", "I lack the permission" or "this build
// predates the feature".
func TestThePowerSectionSaysWhyItIsEmptyWithNoTariff(t *testing.T) {
	h := newHarness(t) // no tariff, which is the default deployment
	h.login("admin", "admin-password")

	page := body(t, h.get("/reports/cost", false))

	if !strings.Contains(page, "No electricity figure: no tariff is configured.") {
		t.Error("with no tariff configured the cost report does not say so; a reader " +
			"cannot tell an unconfigured rate from a missing permission")
	}
	if !strings.Contains(page, "Electricity") {
		t.Error("the section heading is absent entirely, so there is nothing for the " +
			"explanation to explain")
	}
}

// TestThePowerFigureAppearsWhenATariffIsConfigured is the positive control,
// and it also pins the wording §2.3 requires. 28 minor units per kWh against
// the seeded estate's declared draw.
func TestThePowerFigureAppearsWhenATariffIsConfigured(t *testing.T) {
	h := newHarnessWithTariff(t, 28)
	h.login("admin", "admin-password")

	page := body(t, h.get("/reports/cost", false))

	if strings.Contains(page, "No electricity figure") {
		t.Fatal("a tariff is configured and the report still says none is")
	}
	if !strings.Contains(page, "kWh") {
		t.Error("the report shows no energy figure, so the money beside it cannot be checked")
	}
	// §2.3: the wording is not decoration. A figure correctly scoped to
	// declared IT load WILL be read as "what it costs to keep this on-prem",
	// and understates that by the site's PUE.
	if !strings.Contains(page, "Not comparable to an all-in hosting quote") {
		t.Error("the estimate does not say it is not comparable to a hosting quote, " +
			"which is the exact comparison the whole feature exists to inform")
	}
	if !strings.Contains(page, "1.0") || !strings.Contains(page, "730") {
		t.Error("the assumed power factor and the hours per month are not stated " +
			"beside the figure; an estimate nobody can check is one nobody should believe")
	}
	// "Ceiling" promises the real bill cannot exceed the figure. It can.
	if strings.Contains(strings.ToLower(page), "ceiling") {
		t.Error(`the report calls the estimate a "ceiling"; it has not earned the word ` +
			"(§2.3 -- typed inputs, unmodelled UPS and distribution loss, excluded " +
			"facility overhead)")
	}
}

// TestThePowerFigureIsHiddenFromAnUngrantedObserver. It is money, so it lives
// behind the same grant as every other money surface -- and unlike the
// estate totals it would otherwise leak through a section nobody thought of
// as a cost page. The page still renders 200 with the money withheld.
func TestThePowerFigureIsHiddenFromAnUngrantedObserver(t *testing.T) {
	h := newHarnessWithTariff(t, 28)
	h.login("viewer", "viewer-password")

	resp := h.get("/reports/cost", false)
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("an ungranted Observer got %d, want 200 with the money withheld", resp.StatusCode)
	}
	page := body(t, resp)

	if strings.Contains(page, "kWh") {
		t.Error("an ungranted Observer was shown the estimated energy, which is one " +
			"multiplication away from the money it is derived from")
	}
	if strings.Contains(page, "Electricity, estimated monthly") {
		t.Error("the power figure's label leaked to an ungranted Observer even if the " +
			"amount did not")
	}
}

// TestTheEstateTotalIsUnchangedByThePowerFigure is the page-level half of
// §2.4 -- the store-level half is TestThePowerFigureIsNotInTheEstateTotals.
// Keeping the structs separate stops the ARITHMETIC contamination; this
// checks the page did not quietly do the addition the struct refused to.
func TestTheEstateTotalIsUnchangedByThePowerFigure(t *testing.T) {
	withoutTariff := newHarness(t)
	withoutTariff.login("admin", "admin-password")
	plain := body(t, withoutTariff.get("/reports/cost", false))

	withTariff := newHarnessWithTariff(t, 28)
	withTariff.login("admin", "admin-password")
	priced := body(t, withTariff.get("/reports/cost", false))

	for _, label := range []string{"Capital, spent", "Per month", "Per year"} {
		before, ok := statValue(plain, label)
		if !ok {
			t.Fatalf("no %q figure on the report with no tariff; this test cannot "+
				"compare what it cannot find", label)
		}
		after, ok := statValue(priced, label)
		if !ok {
			t.Fatalf("no %q figure on the report with a tariff configured", label)
		}
		if before != after {
			t.Errorf("the estate's %q total moved from %s to %s when a power tariff was "+
				"configured; the estimate has entered a total of what somebody priced",
				label, before, after)
		}
	}
}

// TestAConfiguredTariffOverAnEmptyEstateSaysSoInWords is B1 (§4b.7) at the
// web layer. Configured tests the tariff ALONE, so before this fix the page
// fell through to the figure branch with TotalVA == 0 and printed a
// computed-looking "€0.00" -- on the FIRST DAY of every deployment, since
// the tariff is one environment variable and the draws are hundreds of form
// entries. There was no test between TestThePowerSectionSaysWhyItIsEmpty-
// WithNoTariff (no tariff at all) and TestThePowerFigureAppearsWhenATariff-
// IsConfigured (tariff AND a real draw) -- this is the one the review found
// missing.
func TestAConfiguredTariffOverAnEmptyEstateSaysSoInWords(t *testing.T) {
	h := newHarnessWithTariff(t, 28)
	// Wipe every declared draw so DeclaredPowerDraw's TotalVA is exactly
	// zero, with a tariff still configured -- the state D3's own last
	// sentence names and the state B1 exists to fix.
	h.exec(`UPDATE power_input SET draw_va = NULL`)
	h.login("admin", "admin-password")

	page := body(t, h.get("/reports/cost", false))

	if strings.Contains(page, "No electricity figure: no tariff is configured.") {
		t.Fatal("a tariff IS configured; D5's unconfigured message must not be the one shown")
	}
	if strings.Contains(page, "Electricity, estimated monthly") {
		t.Error("the figure's own stat-row label rendered even though nothing declares a draw; " +
			"HasFigure must gate on both Configured() and a non-zero declared total")
	}
	if !strings.Contains(page, "nothing in the estate currently") &&
		!strings.Contains(strings.ToLower(page), "nothing") {
		t.Error("the page does not say, in words, that nothing declares a draw")
	}
}

// TestTheCoverageSentenceSaysWhatTheGapActuallyIs is V1: "record no draw at
// all" reads two ways, and the WRONG one is plausible -- "this input carries
// no power" -- when the correct reading is "somebody recorded the supply
// path but not the number". Spec D3 states the correct reading outright; the
// template must say it, not hint at it.
func TestTheCoverageSentenceSaysWhatTheGapActuallyIs(t *testing.T) {
	h := newHarnessWithTariff(t, 28)
	h.login("admin", "admin-password")

	page := body(t, h.get("/reports/cost", false))

	if !strings.Contains(page, "supply path recorded but no number") {
		t.Error("the coverage sentence does not say a gap means the supply path was " +
			"recorded but no number was typed in -- the comfortable, wrong reading " +
			"(\"this input carries no power\") is exactly what this wording must rule out")
	}
	if strings.Contains(page, "record no draw at all") {
		t.Error("the ambiguous phrasing V1 found is still present verbatim")
	}
}

// TestTheCoverageSentenceExplainsWhyThereIsNoPercentage is V2/B3: the reader
// is trained by the three rows above (Priced/Coverage%/Unpriced) to expect a
// ratio, and this section renders a bare count with no denominator. It must
// say why, not just omit the ratio silently.
func TestTheCoverageSentenceExplainsWhyThereIsNoPercentage(t *testing.T) {
	h := newHarnessWithTariff(t, 28)
	h.login("admin", "admin-password")

	page := body(t, h.get("/reports/cost", false))

	if !strings.Contains(page, "no percentage") && !strings.Contains(page, "no honest denominator") {
		t.Error("the page never explains why there is no coverage percentage beside " +
			"this figure, unlike the three rows above it")
	}
	if !strings.Contains(page, "open lookup set") && !strings.Contains(page, "open set") {
		t.Error("the reason given for no percentage does not name the actual cause " +
			"(asset.kind is an open lookup set) -- D3's own cited reasoning")
	}
}

// TestUnmodelledSitesAppearsBesideTheFigure is B3 / §4b.9. D3's amendment
// over-generalised its own objection and dropped this exact count from the
// page; the fixture already carries an unmodelled site or two through the
// seed's own topology, but this test forces the count itself so it does not
// depend on incidental seed shape.
func TestUnmodelledSitesAppearsBesideTheFigure(t *testing.T) {
	h := newHarnessWithTariff(t, 28)
	// Add one live site with no power panel at all, directly -- the shape
	// UnmodelledSites exists to count.
	h.exec(`INSERT INTO asset (id, kind, name, lifecycle, created_at, updated_at, row_version)
	        VALUES (?, ?, ?, ?, ?, ?, 1)`,
		"01a06f55-0000-7000-8000-000000000abc", "site", "unmodelled-site-fixture",
		"active", h.store.Now().UTC().Format("2006-01-02T15:04:05Z"),
		h.store.Now().UTC().Format("2006-01-02T15:04:05Z"))
	h.login("admin", "admin-password")

	page := body(t, h.get("/reports/cost", false))

	if !strings.Contains(page, "live site(s) carry no power model at all") {
		t.Error("the page does not carry the UnmodelledSites count beside the electricity figure")
	}
	if !strings.Contains(page, "missing from this figure") && !strings.Contains(page, "entirely") {
		t.Error("the page does not say the unmodelled site is missing entirely, not merely undercounted")
	}
}

// TestTheDirectionOfErrorIsStatedOnThePage is W3 / §4b.10: two things
// understate the IT-load figure and neither reached the page before this --
// the independent-rail chassis §2.1 accepted, and an unmodelled site.
// powerUtilisation already appends "and N of its inputs declare no draw at
// all" for the analogous over-allocation finding; this is the same posture
// applied to the money page.
func TestTheDirectionOfErrorIsStatedOnThePage(t *testing.T) {
	h := newHarnessWithTariff(t, 28)
	h.login("admin", "admin-password")

	page := body(t, h.get("/reports/cost", false))

	if !strings.Contains(strings.ToLower(page), "low") {
		t.Error("the page never states the figure reads LOW, which is the direction of " +
			"every unmodelled gap it names")
	}
	if !strings.Contains(page, "independent") {
		t.Error("the page does not name the independent-rail chassis as a source of " +
			"understatement (§2.1's accepted cost)")
	}
}

// TestTheTariffResolutionIsStatedBesideTheRate is W4 / §4b.14: whole minor
// units per kWh means a real rate of 0.2847 is entered as 28, a systematic
// ~1.7% understatement -- larger than the truncation §4.3 agonises over, and
// nothing on the page said so before this.
func TestTheTariffResolutionIsStatedBesideTheRate(t *testing.T) {
	h := newHarnessWithTariff(t, 28)
	h.login("admin", "admin-password")

	page := body(t, h.get("/reports/cost", false))

	if !strings.Contains(page, "0.2847") {
		t.Error("the page does not name the concrete example rate (0.2847 entered as 28) " +
			"that makes the resolution's cost concrete")
	}
	if !strings.Contains(page, "whole minor units") {
		t.Error("the page does not say the tariff's resolution is whole minor units per kWh")
	}
}

// TestADeclaredPUEShowsBothFigures is D6 / §4b.11. Unset must change nothing
// (proved at the domain layer by TestAnUndeclaredPUEReproducesTheUnmultiplied-
// FigureExactly); this is the positive control at the page layer -- set, it
// must show BOTH the IT-load figure and the facility figure, and state the
// PUE is declared, not measured.
func TestADeclaredPUEShowsBothFigures(t *testing.T) {
	h := newHarnessWithTariffAndPUE(t, 28, 140)
	h.login("admin", "admin-password")

	page := body(t, h.get("/reports/cost", false))

	if !strings.Contains(page, "Electricity, estimated monthly") {
		t.Fatal("the IT-load figure is missing when a PUE is declared")
	}
	if !strings.Contains(page, "Facility, including PUE 1.40") {
		t.Error("the facility figure's label, naming the PUE in force, is missing")
	}
	if !strings.Contains(page, "declared") || !strings.Contains(page, "not measured") {
		t.Error("the page does not say the PUE is declared, not measured")
	}
}

// statValue reads one figure out of the estate's stat row by its LABEL, which
// is also why the power section must not reuse those labels: two figures under
// one label on one page is the misreading this design is arranged against, and
// this helper would silently read the wrong one.
func statValue(page, label string) (string, bool) {
	re := regexp.MustCompile(`stat-label">` + regexp.QuoteMeta(label) +
		`</div>\s*<div class="stat-value">([^<]+)</div>`)
	m := re.FindStringSubmatch(page)
	if m == nil {
		return "", false
	}
	return m[1], true
}
