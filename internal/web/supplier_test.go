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
// NOT a test that a read-only user is refused the PAGE: this route is gated on
// CanRead, not CanSeeCosts -- it names suppliers, not amounts, so an Observer
// without the cost grant still reaches it. WP-G1 Task 4 narrowed CanSeeCosts
// off CanRead (Administrator implicit, Observer/ProjectOwner only when
// granted, docs/rbac-design.md §3), which is exactly why this test asserts
// page reachability rather than any figure on it: a claim about a specific
// amount here would need to track the grant, and this route deliberately
// carries no such claim.
//
// What IS guaranteed is that the report is not public, and that it renders
// through CanRead the same way every other read path does.
func TestTheSupplierReportIsBehindAuthentication(t *testing.T) {
	h := newHarness(t)

	resp := h.get("/reports/suppliers", false)
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK && !strings.Contains(body(t, resp), "Sign in") {
		t.Error("the supplier report is served to somebody who has not signed in")
	}

	// A read-only account sees the report page itself.
	h.login("viewer", "viewer-password")
	if page := body(t, h.get("/reports/suppliers", false)); !strings.Contains(page, "Suppliers") {
		t.Error("a read-only account cannot reach the supplier report")
	}
}

// TestSupplierReportIsHiddenFromAnUngrantedObserver mirrors
// TestCostReportIsHiddenFromAnUngrantedObserver (cost_report_test.go).
// SupplierReport (internal/web/handlers/cost_report.go) skips SupplierMovements
// entirely when the viewer lacks CanSeeCosts, and supplier_report.html falls
// back to the same "Not for you" panel cost_report.html uses -- both money
// figures on this page (the per-line total and every per-supplier monthly
// amount) and the "name no supplier" line that names an amount alongside a
// count must be withheld together, not just the numbers on their own; a
// mutant that deletes either the handler gate or the template panel leaves
// the rest of internal/web green and still leaks "26 cost line(s) name no
// supplier, worth €4,006.17 a month" to an ungranted Observer.
func TestSupplierReportIsHiddenFromAnUngrantedObserver(t *testing.T) {
	h := newHarness(t)
	h.login("viewer", "viewer-password")

	resp := h.get("/reports/suppliers", false)
	if resp.StatusCode != 200 {
		resp.Body.Close()
		t.Fatalf("an ungranted Observer got %d from the supplier report, want 200 "+
			"with the money withheld, not a hard refusal", resp.StatusCode)
	}
	page := body(t, resp)

	if !strings.Contains(page, "Not for you") {
		t.Error("an ungranted Observer was not shown the cost-visibility panel")
	}
	if !strings.Contains(page, "your account does not see money") {
		t.Error("the withheld report carries no explanation of why")
	}
	if strings.Contains(page, "€") {
		t.Error("an ungranted Observer could read a money figure on the supplier report")
	}
	if strings.Contains(page, "name no supplier") {
		t.Error("the unattributed-lines callout leaked to an ungranted Observer, " +
			"naming both a count and an amount")
	}
}
