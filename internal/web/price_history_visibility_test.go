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

// WP-1.1 Task 4d, item 1: price_movement_panel (web/templates/partials/
// price_movement.html) and asset_detail.html's Replacement panel each render
// money with no CanSeeCosts check at all -- five money calls in the first,
// three in the second, both fetched unconditionally by the handler. These
// tests build REAL fixture data through the real forms rather than trusting
// an empty panel: money_visibility_test.go's broad census already proved
// (before this file existed) that a mutation reverting the gate could pass
// unnoticed if the fixture it exercised had nothing to show -- hv-01 and the
// base circuit both carry zero price history by default, so a gate reverted
// on either would leave the census test green for the wrong reason. Building
// a genuine multi-step price series and a genuine replacement lineage here,
// the same way TestRepricingThroughTheFormKeepsBothFigures and
// TestTheReplacementPanelRendersWithRealNumbers already do, closes that gap.

// repriceHV01Once reprices hv-01's one recurring cost line, producing a
// two-step price series so PriceSeries.Moved() is true -- a single valid_from
// entry never "moved", so the panel would render nothing regardless of the
// CanSeeCosts gate and prove nothing about it. Returns the new amount, so the
// caller can assert on a figure that only this reprice could have produced.
func repriceHV01Once(t *testing.T, h *harness) (assetID, newAmount string) {
	t.Helper()
	assetID = h.refs.Assets["hv-01"]
	page := body(t, h.get("/assets/"+assetID, false))
	costID := firstRepriceID(t, page)
	resp := h.post("/assets/"+assetID+"/costs/"+costID+"/reprice", url.Values{
		"csrf_token":     {h.csrfToken("/assets/" + assetID)},
		"amount":         {"1930.00"},
		"effective_from": {"2027-06-01"},
		"note":           {"uplift for the money-visibility fixture"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("repricing hv-01's recurring line returned %d, want a redirect", resp.StatusCode)
	}
	return assetID, "1,930.00"
}

// TestPriceMovementPanelIsHiddenFromAnUngrantedObserver is item 1's first
// surface: price_movement_panel gates on .Movement/.Moved only and carries
// zero CanSeeCosts references, so an Observer with no can_see_costs grant saw
// every step of a price's history -- on both the asset page (assets.go:697)
// and, through the same shared partial, the circuit page (circuits.go:108,
// circuit_detail.html:110).
func TestPriceMovementPanelIsHiddenFromAnUngrantedObserver(t *testing.T) {
	t.Run("asset", func(t *testing.T) {
		h := newHarness(t)
		h.login("admin", "admin-password")
		id, newAmount := repriceHV01Once(t, h)

		admin := body(t, h.get("/assets/"+id, false))
		if !strings.Contains(admin, "How the price moved") {
			t.Fatal("the price-movement panel did not render for an Administrator after " +
				"a genuine reprice -- the fixture setup is broken, not just the gate")
		}
		if !strings.Contains(admin, newAmount) {
			t.Fatalf("the reprice's own new amount (%s) is not on the page for an "+
				"Administrator", newAmount)
		}

		h.logout()
		h.login("viewer", "viewer-password")
		viewer := body(t, h.get("/assets/"+id, false))
		if strings.Contains(viewer, "How the price moved") {
			t.Error("the price-movement panel heading is visible to an ungranted Observer")
		}
		if strings.Contains(viewer, newAmount) {
			t.Errorf("an ungranted Observer could read the reprice's new amount %q", newAmount)
		}
	})

	t.Run("circuit", func(t *testing.T) {
		h := newHarness(t)
		h.login("admin", "admin-password")
		id := h.lookup(`SELECT id FROM circuit LIMIT 1`)
		if id == "" {
			t.Fatal("no circuit in the fixture")
		}
		// Two priced steps on the same kind, same period: the first through
		// CostAddToCircuit, the second through the reprice route, so the
		// series carries a real movement rather than a lone entry that never
		// "moved" (see repriceHV01Once's doc comment for why that matters).
		add := h.post("/circuits/"+id+"/costs", url.Values{
			"csrf_token": {h.csrfToken("/")},
			"kind":       {"operating"}, "period": {"monthly"}, "amount": {"1450"},
			"valid_from": {"2024-01-01"},
			"note":       {"fixture line for the price-movement test"},
		}, false)
		add.Body.Close()
		if add.StatusCode != http.StatusSeeOther {
			t.Fatalf("adding the fixture circuit cost line returned %d, want a redirect", add.StatusCode)
		}
		costID := firstRepriceID(t, body(t, h.get("/circuits/"+id, false)))
		reprice := h.post("/circuits/"+id+"/costs/"+costID+"/reprice", url.Values{
			"csrf_token":     {h.csrfToken("/")},
			"amount":         {"1780.00"},
			"effective_from": {"2027-06-01"},
			"note":           {"uplift for the money-visibility fixture"},
		}, false)
		reprice.Body.Close()
		if reprice.StatusCode != http.StatusSeeOther {
			t.Fatalf("repricing the circuit line returned %d, want a redirect", reprice.StatusCode)
		}

		admin := body(t, h.get("/circuits/"+id, false))
		if !strings.Contains(admin, "How the price moved") {
			t.Fatal("the price-movement panel did not render for an Administrator on the " +
				"circuit page after a genuine reprice")
		}
		if !strings.Contains(admin, "1,780.00") {
			t.Fatal("the reprice's own new amount is not on the circuit page for an Administrator")
		}

		h.logout()
		h.login("viewer", "viewer-password")
		viewer := body(t, h.get("/circuits/"+id, false))
		if strings.Contains(viewer, "How the price moved") {
			t.Error("the price-movement panel heading is visible to an ungranted Observer " +
				"on the circuit page")
		}
		if strings.Contains(viewer, "1,780.00") {
			t.Error("an ungranted Observer could read the circuit reprice's new amount")
		}
	})
}

// TestReplacementPanelIsHiddenFromAnUngrantedObserver is item 1's second
// surface: asset_detail.html's Replacement panel ({{with .Replacement}},
// fetched unconditionally at assets.go:691) shows Then/Now/Difference in
// plain money with no CanSeeCosts check. Builds the same hv-01-replaces-
// sw-core-1 lineage TestTheReplacementPanelRendersWithRealNumbers uses, so
// the comparison is real (5,200.00 -> 8,400.00, a 3,200.00 difference) rather
// than an empty panel that would pass this test for the wrong reason.
func TestReplacementPanelIsHiddenFromAnUngrantedObserver(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id := h.refs.Assets["hv-01"]
	predecessor := h.refs.Assets["sw-core-1"]
	page := body(t, h.get("/assets/"+id+"?edit="+id, false))
	resp := h.post("/assets/"+id, url.Values{
		"csrf_token":  {h.csrfToken("/assets/" + id)},
		"row_version": {versionInForm(t, page)},
		"name":        {"hv-01"}, "kind": {"hypervisor"}, "lifecycle": {"active"},
		"replaces_asset_id": {predecessor},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("recording the lineage returned %d, want a redirect", resp.StatusCode)
	}

	admin := body(t, h.get("/assets/"+id, false))
	if !strings.Contains(admin, "what this took over from") {
		t.Fatal("the replacement panel did not render for an Administrator after a " +
			"genuine lineage was recorded -- the fixture setup is broken, not just the gate")
	}
	for _, want := range []string{"5,200.00", "8,400.00", "3,200.00"} {
		if !strings.Contains(admin, want) {
			t.Fatalf("the replacement comparison is missing %s for an Administrator", want)
		}
	}

	h.logout()
	h.login("viewer", "viewer-password")
	viewer := body(t, h.get("/assets/"+id, false))
	if strings.Contains(viewer, "what this took over from") {
		t.Error("the replacement panel is visible to an ungranted Observer")
	}
	for _, leaked := range []string{"5,200.00", "8,400.00", "3,200.00"} {
		if strings.Contains(viewer, leaked) {
			t.Errorf("an ungranted Observer could read the replacement figure %q", leaked)
		}
	}
}
