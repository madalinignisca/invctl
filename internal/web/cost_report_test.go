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
func TestCostReportSaysHowMuchItCouldNotSee(t *testing.T) {
	h := newHarness(t)
	h.login("viewer", "viewer-password")

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
// cannot change anything is exactly who asks what the estate costs.
func TestCostReportIsReadableWithoutWriteAccess(t *testing.T) {
	h := newHarness(t)
	h.login("viewer", "viewer-password")
	if code := h.get("/reports/cost", false).StatusCode; code != 200 {
		t.Errorf("a read-only user got %d from the cost report, want 200", code)
	}
}
