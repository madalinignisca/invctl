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

// The power chain's five refusal paths, converted from flash-and-redirect to
// a 422 that reopens the form on what was typed -- CLAUDE.md's rule ("a
// validation failure returns 422 with the form partial re-rendered"), which
// refusal_status_test.go's AST scan now enforces mechanically.
//
// Each test types a DISTINCTIVE name into the field that survives the
// refusal, so the assertion is not just "422 came back" -- a redirect would
// also eventually show a 200 on /power, but with the operator's typed name
// nowhere on the page, because a redirect discards the POST body and refills
// the row from storage.

// TestARefusedPanelCorrectionReopensOnWhatWasTyped drives PowerPanelUpdate's
// validation-error branch: a voltage without an amperage is refused by
// domain.Rating's own check ("an amperage without a voltage cannot give a
// capacity").
func TestARefusedPanelCorrectionReopensOnWhatWasTyped(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	site := h.lookup(`SELECT id FROM asset WHERE kind = 'site' LIMIT 1`)
	panel := h.addPanel(t, site, "refusal-board")

	resp := h.post("/power/panels/"+panel, url.Values{
		"csrf_token":  {h.csrfToken("/power")},
		"name":        {"distinctive-panel-name-xyz"},
		"amperage":    {"32"}, // voltage left blank: refused as "an amperage without a voltage..."
		"row_version": {h.lookup(`SELECT row_version FROM power_panel WHERE id = ?`, panel)},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an amperage with no voltage: got %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
	page := body(t, resp)
	if !strings.Contains(page, "distinctive-panel-name-xyz") {
		t.Error("the refused panel name does not appear in the response body -- " +
			"the operator's typed input was thrown away")
	}
	if got := h.lookup(`SELECT name FROM power_panel WHERE id = ?`, panel); got == "distinctive-panel-name-xyz" {
		t.Error("the refused name reached the database")
	}
}

// TestARefusedFeedCorrectionReopensOnWhatWasTyped drives PowerFeedUpdate's
// refusalMessages branch via a max_utilisation outside 1-100.
func TestARefusedFeedCorrectionReopensOnWhatWasTyped(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	site := h.lookup(`SELECT id FROM asset WHERE kind = 'site' LIMIT 1`)
	panel := h.addPanel(t, site, "refusal-feed-board")
	feed := h.addFeed(t, panel, "refusal-feed")

	resp := h.post("/power/feeds/"+feed, url.Values{
		"csrf_token":      {h.csrfToken("/power")},
		"name":            {"distinctive-feed-name-xyz"},
		"voltage":         {"230"},
		"amperage":        {"32"},
		"max_utilisation": {"150"}, // out of range: refused
		"row_version":     {h.lookup(`SELECT row_version FROM power_feed WHERE id = ?`, feed)},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a max_utilisation of 150: got %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
	page := body(t, resp)
	if !strings.Contains(page, "distinctive-feed-name-xyz") {
		t.Error("the refused feed name does not appear in the response body -- " +
			"the operator's typed input was thrown away")
	}
	if got := h.lookup(`SELECT name FROM power_feed WHERE id = ?`, feed); got == "distinctive-feed-name-xyz" {
		t.Error("the refused name reached the database")
	}
}

// TestARefusedSupplyCorrectionReopensOnWhatWasTyped drives PowerSourceUpdate's
// cycle refusal (requireNoSupplyCycle): a supply cannot feed itself.
func TestARefusedSupplyCorrectionReopensOnWhatWasTyped(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	site := h.lookup(`SELECT id FROM asset WHERE kind = 'site' LIMIT 1`)
	ups := h.addSource(t, site, "refusal-ups", "ups")

	resp := h.post("/power/sources/"+ups, url.Values{
		"csrf_token":  {h.csrfToken("/power")},
		"name":        {"distinctive-supply-name-xyz"},
		"kind":        {"ups"},
		"parent_id":   {ups}, // its own id: refused as a cycle
		"row_version": {h.lookup(`SELECT row_version FROM power_source WHERE id = ?`, ups)},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a supply fed by itself: got %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
	page := body(t, resp)
	if !strings.Contains(page, "distinctive-supply-name-xyz") {
		t.Error("the refused supply name does not appear in the response body -- " +
			"the operator's typed input was thrown away")
	}
	if got := h.lookup(`SELECT name FROM power_source WHERE id = ?`, ups); got == "distinctive-supply-name-xyz" {
		t.Error("the refused name reached the database")
	}
}

// TestARefusedPowerInputCreateReopensOnWhatWasTyped drives PowerInputCreate's
// validation branch: checkDraw refuses a draw over 100,000 VA ("is larger
// than any single input; check the units").
func TestARefusedPowerInputCreateReopensOnWhatWasTyped(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	site := h.lookup(`SELECT id FROM asset WHERE kind = 'site' LIMIT 1`)
	panel := h.addPanel(t, site, "input-create-board")
	feed := h.addFeed(t, panel, "input-create-feed")
	asset := h.lookup(`SELECT id FROM asset WHERE kind <> 'site' AND lifecycle = 'active' LIMIT 1`)

	resp := h.post("/assets/"+asset+"/power", url.Values{
		"csrf_token": {h.csrfToken("/assets/" + asset)},
		"feed_id":    {feed},
		"name":       {"distinctive-input-name-xyz"},
		"draw_va":    {"999999"}, // over the 100,000 VA bound: refused
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a draw of 999999 VA: got %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
	page := body(t, resp)
	if !strings.Contains(page, "distinctive-input-name-xyz") {
		t.Error("the refused input name does not appear in the response body -- " +
			"the operator's typed input was thrown away")
	}
	if h.lookup(`SELECT COUNT(*) FROM power_input WHERE asset_id = ? AND name = ?`,
		asset, "distinctive-input-name-xyz") != "0" {
		t.Error("the refused power input reached the database")
	}
}

// TestARefusedPowerInputUpdateReopensOnWhatWasTyped is the same shape for
// PowerInputUpdate, correcting a row that already exists.
func TestARefusedPowerInputUpdateReopensOnWhatWasTyped(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	site := h.lookup(`SELECT id FROM asset WHERE kind = 'site' LIMIT 1`)
	panel := h.addPanel(t, site, "input-update-board")
	feed := h.addFeed(t, panel, "input-update-feed")
	asset := h.lookup(`SELECT id FROM asset WHERE kind <> 'site' AND lifecycle = 'active' LIMIT 1`)

	resp := h.post("/assets/"+asset+"/power", url.Values{
		"csrf_token": {h.csrfToken("/assets/" + asset)},
		"feed_id":    {feed}, "name": {"PSU-refusal"}, "draw_va": {"300"},
	}, false)
	resp.Body.Close()
	input := h.lookup(`SELECT id FROM power_input WHERE asset_id = ? AND name = ?`, asset, "PSU-refusal")

	resp = h.post("/assets/"+asset+"/power/"+input, url.Values{
		"csrf_token":  {h.csrfToken("/assets/" + asset)},
		"name":        {"distinctive-input-update-xyz"},
		"feed_id":     {feed},
		"draw_va":     {"999999"}, // over the bound: refused
		"row_version": {h.lookup(`SELECT row_version FROM power_input WHERE id = ?`, input)},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("correcting a draw to 999999 VA: got %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
	page := body(t, resp)
	if !strings.Contains(page, "distinctive-input-update-xyz") {
		t.Error("the refused input name does not appear in the response body -- " +
			"the operator's typed input was thrown away")
	}
	if got := h.lookup(`SELECT name FROM power_input WHERE id = ?`, input); got == "distinctive-input-update-xyz" {
		t.Error("the refused name reached the database")
	}
}
