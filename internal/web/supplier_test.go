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
	"strings"
	"testing"
)

// TestTheSupplierReportRendersAndSaysWhatItCannotRank.
//
// The page is fetched rather than trusted to compile, for the reason the circuit
// impact page taught this codebase. It also asserts the honesty line: a ranking
// over part of the book reads exactly like a ranking over all of it, so the part
// it misses has to be on the page.
func TestTheSupplierReportRendersAndSaysWhatItCannotRank(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	page := body(t, h.get("/reports/suppliers", false))
	if !strings.Contains(page, "Suppliers") {
		t.Fatal("the supplier report does not render")
	}
	// The base fixture names no supplier on any cost line, so the page must say
	// so rather than showing an empty table that reads as "no rises".
	if !strings.Contains(page, "No supplier is named") &&
		!strings.Contains(page, "name no supplier") {
		t.Error("with nothing attributed the report does not say so, so an empty " +
			"ranking reads as a clean bill of health")
	}
	// And it is reachable, not just routable.
	if !strings.Contains(body(t, h.get("/reports/cost", false)), "/reports/suppliers") {
		t.Log("note: the supplier report is not linked from the cost report")
	}
	if !strings.Contains(page, `href="/reports/suppliers"`) {
		t.Error("the report is not in the navigation rail")
	}
}

// TestTheSupplierReportIsBehindAuthentication.
//
// NOT a test that a read-only user is refused: auth.go states as a decision that
// CanSeeCosts returns exactly what CanRead does, because gating commercial
// figures now would invent a requirement, and "a permission nobody has thought
// through is worse than none, because it looks like protection". A test asserting
// the opposite would have quietly contradicted that decision.
//
// What IS guaranteed is that the report is not public, and that it renders
// through the same permission the other money pages use — so the day
// CanSeeCosts stops equalling CanRead, this page follows without being touched.
func TestTheSupplierReportIsBehindAuthentication(t *testing.T) {
	h := newHarness(t)

	resp := h.get("/reports/suppliers", false)
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK && !strings.Contains(body(t, resp), "Sign in") {
		t.Error("the supplier report is served to somebody who has not signed in")
	}

	// A read-only account sees it, which is today's declared policy.
	h.login("viewer", "viewer-password")
	if page := body(t, h.get("/reports/suppliers", false)); !strings.Contains(page, "Suppliers") {
		t.Error("a read-only account cannot reach the supplier report, which " +
			"contradicts CanSeeCosts returning what CanRead does")
	}
}
