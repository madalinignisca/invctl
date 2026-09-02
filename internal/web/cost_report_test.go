// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"strings"
	"testing"
)

// TestCostReportSaysHowMuchItCouldNotSee.
//
// THE TOTAL IS THE POINT AND THE COVERAGE IS WHY IT CAN BE BELIEVED. A run rate
// on a page is the number most likely to reach a meeting and least likely to be
// questioned, and it is a floor rather than a total whenever anything carries
// no price. The fixture estate is only partly priced -- deliberately, since a
// fully priced fixture would let this page ship without ever rendering the
// caveat that makes it honest.
//
// Asserting the words rather than a figure: the amounts move whenever the
// fixture gains a cost line, but "these totals are a floor" must survive every
// one of those changes.
//
// LOGGED IN AS ADMIN, NOT "viewer". This report is gated behind CanSeeCosts
// (WP-1.1 Task 4c closed the leak: an ungranted Observer used to get the same
// 200 this test checks, money and all). The caveat this test exists to prove
// only renders to someone who can see the totals it caveats, so the account
// has to be one that actually has the grant -- an Administrator always does.
// See TestCostReportIsHiddenFromAnUngrantedObserver for the ungranted case.
func TestCostReportSaysHowMuchItCouldNotSee(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	page := body(t, h.get("/reports/cost", false))

	if !strings.Contains(page, "floor, not a total") {
		t.Error("the report does not warn that its totals are a floor; the fixture " +
			"has unpriced entities, so the caveat is required")
	}
	if !strings.Contains(page, "unpriced") {
		t.Error("no surface reports what it could not price")
	}
	for _, surface := range []string{"assets", "services", "circuits", "projects"} {
		if !strings.Contains(page, surface) {
			t.Errorf("surface %q is missing: money attaches there and the totals "+
				"would silently omit it", surface)
		}
	}
}

// TestCostReportIsReadableWithoutWriteAccess. It is a report, and a reader who
// cannot change anything is exactly who asks what the estate costs. This is
// about REACHING the page, not about what it shows once there -- the "viewer"
// fixture account has no can_see_costs grant, so it gets the page but not the
// money on it; see TestCostReportIsHiddenFromAnUngrantedObserver for that.
func TestCostReportIsReadableWithoutWriteAccess(t *testing.T) {
	h := newHarness(t)
	h.login("viewer", "viewer-password")
	if code := h.get("/reports/cost", false).StatusCode; code != 200 {
		t.Errorf("a read-only user got %d from the cost report, want 200", code)
	}
}

// TestCostReportIsHiddenFromAnUngrantedObserver is the defect WP-1.1 Task 4c
// closed: /reports/cost had no CanSeeCosts check at all, on the handler or in
// the template, so the "viewer" fixture account -- an Observer with no
// can_see_costs grant -- got a 200 with every figure the estate carries,
// including capital and monthly totals in the tens of thousands. The page
// still renders (200, not 403 or a redirect): what changes is that the money
// is gone, replaced by the same "Not for you" panel supplier_report.html
// already uses for the identical case.
func TestCostReportIsHiddenFromAnUngrantedObserver(t *testing.T) {
	h := newHarness(t)
	h.login("viewer", "viewer-password")

	resp := h.get("/reports/cost", false)
	if resp.StatusCode != 200 {
		resp.Body.Close()
		t.Fatalf("an ungranted Observer got %d from the cost report, want 200 "+
			"with the money withheld, not a hard refusal", resp.StatusCode)
	}
	page := body(t, resp)

	if !strings.Contains(page, "Not for you") {
		t.Error("an ungranted Observer was not shown the cost-visibility panel")
	}
	if !strings.Contains(page, "your account does not see money") {
		t.Error("the withheld report carries no explanation of why")
	}
	for _, figure := range []string{"€56,100.00", "€4,006.17"} {
		if strings.Contains(page, figure) {
			t.Errorf("an ungranted Observer could read %q on the cost report", figure)
		}
	}
	if strings.Contains(page, "capital committed") || strings.Contains(page, "Capital, spent") {
		t.Error("a cost figure's label leaked even though the amount did not")
	}
}
