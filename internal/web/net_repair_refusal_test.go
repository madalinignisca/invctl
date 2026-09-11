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

// The two reachability correction paths, converted from flash-and-redirect
// to a 422 that reopens the form on what was typed -- CLAUDE.md's rule,
// mechanically enforced by refusal_status_test.go's AST scan.

// TestARefusedGroupCorrectionReopensOnWhatWasTyped drives NetworkGroupUpdate's
// domain validation: active_active requires a min_healthy.
func TestARefusedGroupCorrectionReopensOnWhatWasTyped(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := firstNetGroup(t, h)

	resp := h.post("/network/groups/"+id, url.Values{
		"csrf_token":   {h.csrfToken("/network/groups/" + id)},
		"code":         {h.lookup(`SELECT code FROM net_group WHERE id = ?`, id)},
		"name":         {"distinctive-group-name-xyz"},
		"kind":         {h.lookup(`SELECT kind FROM net_group WHERE id = ?`, id)},
		"role":         {h.lookup(`SELECT role FROM net_group WHERE id = ?`, id)},
		"availability": {"active_active"}, // min_healthy left blank: refused
		"row_version":  {h.lookup(`SELECT row_version FROM net_group WHERE id = ?`, id)},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("active_active with no min_healthy: got %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
	page := body(t, resp)
	if !strings.Contains(page, "distinctive-group-name-xyz") {
		t.Error("the refused group name does not appear in the response body -- " +
			"the operator's typed input was thrown away")
	}
	if got := h.lookup(`SELECT name FROM net_group WHERE id = ?`, id); got == "distinctive-group-name-xyz" {
		t.Error("the refused name reached the database")
	}
}

// TestARefusedAnchorCorrectionReopensOnWhatWasTyped drives NetworkAnchorUpdate's
// domain validation: code is required.
func TestARefusedAnchorCorrectionReopensOnWhatWasTyped(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := h.lookup(`SELECT id FROM net_anchor WHERE lifecycle = 'active' ORDER BY id LIMIT 1`)

	resp := h.post("/network/anchors/"+id, url.Values{
		"csrf_token":  {h.csrfToken("/network")},
		"code":        {""}, // required, left blank: refused
		"name":        {"distinctive-anchor-name-xyz"},
		"scope":       {h.lookup(`SELECT scope FROM net_anchor WHERE id = ?`, id)},
		"plane":       {h.lookup(`SELECT plane FROM net_anchor WHERE id = ?`, id)},
		"group_id":    {h.lookup(`SELECT group_id FROM net_anchor WHERE id = ?`, id)},
		"row_version": {h.lookup(`SELECT row_version FROM net_anchor WHERE id = ?`, id)},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a blank anchor code: got %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
	page := body(t, resp)
	if !strings.Contains(page, "distinctive-anchor-name-xyz") {
		t.Error("the refused anchor name does not appear in the response body -- " +
			"the operator's typed input was thrown away")
	}
	if got := h.lookup(`SELECT name FROM net_anchor WHERE id = ?`, id); got == "distinctive-anchor-name-xyz" {
		t.Error("the refused name reached the database")
	}
}
