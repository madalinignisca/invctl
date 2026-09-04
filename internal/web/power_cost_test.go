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
