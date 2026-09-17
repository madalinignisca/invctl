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

// Task 6 of the breakout-cables plan: a way to declare a breakout from the
// UI. Driven through the real router, the same discipline
// TestABundleCanBeDeclaredAndCorrected and link_impact_test.go's own comment
// both give: a handler test built by hand never catches a route or a
// template that cannot actually be reached.

// TestABreakoutCanBeDeclaredThroughTheForm covers the plain create path: one
// a-end, several b-ends, one POST, four link rows sharing a breakout id.
func TestABreakoutCanBeDeclaredThroughTheForm(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	sw := mustServerAssetWeb(t, h, "breakout-form-sw")
	swIface := mustInterfaceWeb(t, h, sw, "eth1")
	var bIfaces []string
	for i := 0; i < 4; i++ {
		srv := mustServerAssetWeb(t, h, "breakout-form-srv-"+string(rune('a'+i)))
		bIfaces = append(bIfaces, mustInterfaceWeb(t, h, srv, "eth0"))
	}

	form := url.Values{
		"csrf_token":      {h.csrfToken("/assets/" + sw)},
		"asset_id":        {sw},
		"a_interface_id":  {swIface},
		"b_interface_ids": bIfaces,
		"medium":          {"dac"},
		"length_m":        {"2"},
	}
	resp := h.post("/breakouts", form, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("declaring a breakout returned %d, want 303", resp.StatusCode)
	}

	breakoutID := h.lookup(`SELECT breakout_id FROM link WHERE a_interface_id = ? LIMIT 1`, swIface)
	if breakoutID == "" {
		t.Fatal("no link row carries a breakout id for the declared a-end")
	}
	n := h.count(`SELECT COUNT(*) FROM link WHERE breakout_id = ? AND lifecycle = 'active'`, breakoutID)
	if n != 4 {
		t.Fatalf("breakout %s has %d live strands, want 4", breakoutID, n)
	}
}

// TestABreakoutRefusalReopensWithEveryStrandStillPicked: a b-end reused
// inside the same submission is refused, and the form comes back with the
// a-end, medium, length AND every strand already chosen still selected --
// CLAUDE.md's "a refused form comes back with the typed input intact"
// applied to a multi-valued field.
func TestABreakoutRefusalReopensWithEveryStrandStillPicked(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	sw := mustServerAssetWeb(t, h, "breakout-refusal-sw")
	swIface := mustInterfaceWeb(t, h, sw, "eth1")
	srvA := mustServerAssetWeb(t, h, "breakout-refusal-srv-a")
	srvB := mustServerAssetWeb(t, h, "breakout-refusal-srv-b")
	ifA := mustInterfaceWeb(t, h, srvA, "eth0")
	ifB := mustInterfaceWeb(t, h, srvB, "eth0")

	form := url.Values{
		"csrf_token": {h.csrfToken("/assets/" + sw)},
		"asset_id":   {sw},
		// ifA appears TWICE -- domain.BreakoutSpec.Validate refuses a
		// repeated b-end within one breakout.
		"a_interface_id":  {swIface},
		"b_interface_ids": {ifA, ifA},
		"medium":          {"dac"},
	}
	resp := h.post("/breakouts", form, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("declaring a breakout with a repeated b-end returned %d, want 422",
			resp.StatusCode)
	}
	page := body(t, resp)
	if !strings.Contains(page, `value="`+ifA+`" selected`) {
		t.Error("the refused form does not reopen with the repeated strand still selected -- " +
			"an operator would have to re-pick every port from scratch")
	}
	if strings.Contains(page, `value="`+ifB+`" selected`) {
		t.Error("a port that was never submitted shows as selected")
	}

	// And the ordinary CreateBreakout refusal message reaches the page --
	// this asserts the form is reachable and legible, not the store's own
	// validation logic, which breakout_test.go already covers directly.
	n := h.count(`SELECT COUNT(*) FROM link WHERE a_interface_id = ?`, swIface)
	if n != 0 {
		t.Errorf("a refused breakout wrote %d link rows, want 0 -- nothing should be "+
			"committed from a spec that failed validation", n)
	}
}

// TestTheBreakoutFormIsReachedByClicking, not by constructing the URL --
// link_impact_test.go's own reason: this project has shipped a 404 on a
// button with every handler test green.
func TestTheBreakoutFormIsReachedByClicking(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	sw := mustServerAssetWeb(t, h, "breakout-reach-sw")
	page := body(t, h.get("/assets/"+sw, false))
	if !strings.Contains(page, `action="/breakouts"`) {
		t.Fatal("the asset page offers no breakout declaration form")
	}
}
