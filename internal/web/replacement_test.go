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

// TestTheReplacementPanelRendersWithRealNumbers.
//
// WRITTEN BECAUSE A PANEL THAT COMPILES IS NOT A PANEL THAT RENDERS. This
// codebase has already shipped a page that returned 500 for every request while
// the engine, the store and the seed tests were all green, because nothing
// fetched it. A comparison built from a lineage and two cost lines has more
// moving parts than that one did.
//
// It drives the real editor rather than writing the column directly: the field
// has to survive the round trip through the form, and a version that stored the
// lineage while the form quietly dropped it would pass every store test.
func TestTheReplacementPanelRendersWithRealNumbers(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id := h.refs.Assets["hv-01"]
	predecessor := h.refs.Assets["sw-core-1"]

	page := body(t, h.get("/assets/"+id+"?edit="+id, false))
	if !strings.Contains(page, `name="replaces_asset_id"`) {
		t.Fatal("the asset editor does not offer the lineage field")
	}
	// The predecessor must be ON OFFER, which is the half a store test cannot
	// see: the picker deliberately includes retired assets, because a box that
	// has been replaced usually is one.
	if !strings.Contains(page, `value="`+predecessor+`"`) {
		t.Error("the predecessor is not among the options")
	}

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

	after := body(t, h.get("/assets/"+id, false))
	if !strings.Contains(after, "what this took over from") {
		t.Fatal("the replacement panel is absent from an asset that replaces another")
	}
	if !strings.Contains(after, "sw-core-1") {
		t.Error("the predecessor is not named on the panel")
	}
	// The fixture prices both boxes, so the comparison is real and the numbers
	// are assertable. An earlier draft of this test expected "not comparable"
	// and was simply wrong about the estate -- worth recording, because a test
	// asserting the absence of a feature would have passed for ever while the
	// arithmetic beneath it rotted.
	for _, want := range []string{
		"5,200.00", // paid in 2019
		"8,400.00", // paid in 2023
		"3,200.00", // the difference somebody takes to a supplier
		"61%",      // on the old price
	} {
		if !strings.Contains(after, want) {
			t.Errorf("the comparison does not show %s", want)
		}
	}
	// Four years apart, so the per-year figure must appear: it is the one worth
	// holding against inflation, and the reason the gap is measured at all.
	// Matched on a span with no markup inside it: "15% a year" sits in a
	// <strong> and the words either side of the tag are not contiguous in the
	// HTML, which is how the first version of this assertion failed against a
	// page that was rendering correctly.
	if !strings.Contains(after, "years between the two purchases") {
		t.Error("no annualised figure across a four-year gap")
	}
	// sw-core-1 is not retired, and the panel says so rather than leaving the
	// reader to notice. A live predecessor means either the refresh is
	// unfinished or the lineage was recorded backwards.
	if !strings.Contains(after, "still active") {
		t.Error("a live predecessor is not flagged")
	}
}

// TestAnAssetThatReplacedNothingShowsNoPanel. Most assets replaced nothing, and
// a panel answering a question nobody asked is noise on a page opened during an
// incident.
func TestAnAssetThatReplacedNothingShowsNoPanel(t *testing.T) {
	h := newHarness(t)
	h.login("viewer", "viewer-password")
	page := body(t, h.get("/assets/"+h.refs.Assets["hv-02"], false))
	if strings.Contains(page, "what this took over from") {
		t.Error("an asset replacing nothing shows a replacement panel")
	}
}
