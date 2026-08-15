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
	"net/url"
	"strings"
	"testing"
)

// TestInflationRoundTripsThroughTheForm, including the decimal shapes somebody
// actually types off a statistics page.
func TestInflationRoundTripsThroughTheForm(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	// Explicit years, because an earlier version derived them from the typed
	// string and gave "3.2" and "2" the same one -- so the second silently
	// corrected the first and the test failed against correct code.
	for _, tc := range []struct{ year, typed, want string }{
		{"1990", "3.2", "3.20%"},
		{"1991", "3,4", "3.40%"},   // comma, as most of Europe writes it
		{"1992", "-0.5", "-0.50%"}, // deflation must round-trip
		{"1993", "2", "2.00%"},
	} {
		resp := h.post("/inflation", url.Values{
			"csrf_token": {h.csrfToken("/inflation")},
			"year":       {tc.year},
			"percent":    {tc.typed},
			"source":     {"test"},
		}, false)
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("recording %q returned %d", tc.typed, resp.StatusCode)
		}
	}

	page := body(t, h.get("/inflation", false))
	for _, want := range []string{"3.20%", "3.40%", "-0.50%", "2.00%"} {
		if !strings.Contains(page, want) {
			t.Errorf("the series does not show %s", want)
		}
	}
}

// TestATypoIsRefusedWithA422. 900% would report every price in the estate as a
// bargain, and silently.
func TestATypoIsRefusedWithA422(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	resp := h.post("/inflation", url.Values{
		"csrf_token": {h.csrfToken("/inflation")},
		"year":       {"2024"}, "percent": {"900"},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("900%% inflation returned %d, want 422", resp.StatusCode)
	}
}

// TestAReadOnlyUserCannotRecordARate. Reference data is declared state and
// follows the same rule as everything else.
func TestAReadOnlyUserCannotRecordARate(t *testing.T) {
	h := newHarness(t)
	h.login("viewer", "viewer-password")
	resp := h.post("/inflation", url.Values{
		"csrf_token": {h.csrfToken("/inflation")},
		"year":       {"2024"}, "percent": {"3.2"},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a read-only user recording a rate got %d, want 403", resp.StatusCode)
	}
}
