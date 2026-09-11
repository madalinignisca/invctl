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

// AssetOccupants and AssetReparent, converted from flash-and-redirect to a
// 422 that reopens the asset page on what was typed -- CLAUDE.md's rule,
// mechanically enforced by refusal_status_test.go's AST scan.

// TestARefusedOccupantSetReopensOnWhatWasTyped drives
// domain.ValidateOccupants's own bound: a share is between 1 and 100.
func TestARefusedOccupantSetReopensOnWhatWasTyped(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	vm := h.refs.Assets["vm-app-1"]
	platform := h.refs.Projects["platform"]

	resp := h.post("/assets/"+vm+"/occupants", url.Values{
		"csrf_token":          {h.csrfToken("/assets/" + vm)},
		"project_id":          {platform},
		"percent_" + platform: {"150"}, // out of range: refused
		"note_" + platform:    {"distinctive-occupant-note-xyz"},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a 150%% share: got %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
	page := body(t, resp)
	if !strings.Contains(page, "distinctive-occupant-note-xyz") {
		t.Error("the refused occupant note does not appear in the response body -- " +
			"the operator's typed input was thrown away")
	}
	if h.lookup(`SELECT COUNT(*) FROM asset_occupant WHERE asset_id = ? AND project_id = ?`,
		vm, platform) != "0" {
		t.Error("the refused occupant set reached the database")
	}
}

// TestARefusedReparentReopensWithTheAttemptedMove drives AssetReparent's own
// cycle guard: an asset cannot contain itself.
func TestARefusedReparentReopensWithTheAttemptedMove(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	asset := h.refs.Assets["hv-01"]

	resp := h.post("/assets/"+asset+"/parent", url.Values{
		"csrf_token": {h.csrfToken("/assets/" + asset)},
		"parent_id":  {asset}, // itself: refused as a cycle
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an asset made its own parent: got %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
	page := body(t, resp)
	if !strings.Contains(page, "cannot contain itself") {
		t.Error("the refusal reason does not appear on the reopened page")
	}
	// The exact echo AssetReparent's rejected() call adds: proof the attempted
	// value survived the refusal rather than being silently discarded the way
	// a flash-and-redirect would have.
	if !strings.Contains(page, `name="parent_id" value="`+asset+`"`) {
		t.Error("the attempted parent_id was not echoed back on the refused page")
	}
	if got := h.lookup(`SELECT COALESCE(parent_id, '') FROM asset WHERE id = ?`, asset); got == asset {
		t.Error("the asset was made its own parent in the database")
	}
}
