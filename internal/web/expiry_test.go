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

// The expiry report through the real router, against the seeded fixture. The
// dates in seed_lifetimes.go are relative to the seeding clock, so what the
// page says is stable whenever the suite runs.

func TestExpiryReportShowsWhatRunsOutAndWhoItLandsOn(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	page := body(t, h.get("/reports/expiry", false))

	// Already lapsed, and far enough back to be outside any sane window.
	if !strings.Contains(page, "sw-core-1") {
		t.Error("a core switch whose support lapsed over a year ago is not on the report")
	}
	// Expiring inside the default horizon, and the row a reader acts on.
	if !strings.Contains(page, "hv-01") {
		t.Error("a hypervisor expiring inside the horizon is missing")
	}
	// A licence, not hardware — the other half of what a date means here.
	if !strings.Contains(page, "vault") {
		t.Error("an expiring licence is missing; the report is hardware-only")
	}

	// The finding is the combination, not the date: what rides on it and who
	// has to act. A date with neither is a row nobody can do anything with.
	if !strings.Contains(page, "Platform &amp; Core Services") {
		t.Error("the report does not name the project that owns an expiring asset")
	}
	if !strings.Contains(page, "tier 1") {
		t.Error("the report does not say what tier rides on the expiring hardware")
	}

	// And what it cannot see. An estate where nothing appears to expire is
	// usually one where nobody wrote the dates down.
	if !strings.Contains(page, "What this report cannot see") {
		t.Error("the report does not account for the rows carrying no date")
	}
}

// TestExpiryHorizonNarrowsThroughTheURL. The control is that narrowing drops
// something: a horizon that changes nothing is decoration.
func TestExpiryHorizonNarrowsThroughTheURL(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	wide := body(t, h.get("/reports/expiry?months=24", false))
	if !strings.Contains(wide, "hv-02") {
		t.Fatal("a hypervisor eight months out is missing from a 24-month view")
	}

	narrow := body(t, h.get("/reports/expiry?months=3", false))
	if strings.Contains(narrow, "hv-02") {
		t.Error("narrowing to 3 months still shows something eight months away")
	}
	// ...while the expired row survives every narrowing, because a window is a
	// question about the future.
	if !strings.Contains(narrow, "sw-core-1") {
		t.Error("narrowing the window hid something that has already expired")
	}

	// A nonsense horizon falls back rather than erroring: a bad query string is
	// not worth a 500 on a read-only page.
	if resp := h.get("/reports/expiry?months=-4", false); resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Errorf("a negative horizon returned %d, want 200", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
}

// The date is visible where it is edited and where it is read, and both must
// agree. A field on a form that appears nowhere afterwards is how a column ends
// up written and never looked at.
func TestEOLDateAppearsOnTheDetailPages(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	asset := body(t, h.get("/assets/"+h.refs.Assets["hv-01"], false))
	if !strings.Contains(asset, "Supported until") {
		t.Error("the asset detail page does not show an EOL date")
	}
	svc := body(t, h.get("/services/"+h.refs.Services["vault"], false))
	if !strings.Contains(svc, "Supported until") {
		t.Error("the service detail page does not show an EOL date")
	}

	// An undated asset says so plainly rather than showing nothing at all.
	undated := body(t, h.get("/assets/"+h.refs.Assets["hv-03"], false))
	if !strings.Contains(undated, "Supported until") {
		t.Error("an undated asset hides the field entirely")
	}
}

// A read-only user may look at the report; nothing on it can change anything,
// which is the whole point of a page about dates in a system that never acts.
func TestExpiryReportIsReadableAndAnonymousIsNot(t *testing.T) {
	viewer := newHarness(t)
	viewer.login("viewer", "viewer-password")
	resp := viewer.get("/reports/expiry", false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("a read-only user got %d on the expiry report", resp.StatusCode)
	}

	anon := newHarness(t).get("/reports/expiry", false)
	anon.Body.Close()
	if anon.StatusCode != http.StatusSeeOther {
		t.Errorf("anonymous got %d, want a redirect to login", anon.StatusCode)
	}
}
