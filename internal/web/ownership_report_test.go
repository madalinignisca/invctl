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

// TestTheOwnershipReportRendersAndFindsTheSeededGap.
//
// Fetched over real HTTP rather than trusted to compile, the same reasoning
// supplier_test.go gives: a template can build and still 404 or panic on
// render, and only a request through the router proves otherwise. The base
// seed (seed_projects.go, seed_services.go) leaves haproxy-edge owned by
// nobody deliberately -- "the most realistic finding in the fixture" -- so a
// correct report must show it without any extra fixture wiring here.
func TestTheOwnershipReportRendersAndFindsTheSeededGap(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	page := body(t, h.get("/reports/ownership", false))
	if !strings.Contains(page, "Ownership") {
		t.Fatal("the ownership report does not render")
	}
	if !strings.Contains(page, "haproxy-edge") {
		t.Error("the seeded unowned service (haproxy-edge) does not appear in the report")
	}
	// Reachable, not just routable.
	if !strings.Contains(page, `href="/reports/ownership"`) {
		t.Error("the report is not in the navigation rail")
	}
}

// TestTheOwnershipReportIsBehindAuthentication mirrors the other reports:
// unauthenticated access is refused, and a read-only account can still see
// it -- this is not money, so it carries no CanSeeCosts gate.
func TestTheOwnershipReportIsBehindAuthentication(t *testing.T) {
	h := newHarness(t)

	resp := h.get("/reports/ownership", false)
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK && !strings.Contains(body(t, resp), "Sign in") {
		t.Error("the ownership report is served to somebody who has not signed in")
	}

	h.login("viewer", "viewer-password")
	if page := body(t, h.get("/reports/ownership", false)); !strings.Contains(page, "Ownership") {
		t.Error("a read-only account cannot reach the ownership report")
	}
}
