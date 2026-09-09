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

// Withdrawing a port, through the screen an operator actually uses.
//
// `interface` was the most-referenced table in the schema with no lifecycle
// column at all: nine foreign-key columns point at it, 58 queries read it, and
// a NIC pulled out of a chassis stayed on the asset for ever. The store half is
// covered by internal/store/interface_lifecycle_test.go; this is the half that
// says an operator can reach it.

// TestAPortCanBeWithdrawnFromTheAssetPage.
func TestAPortCanBeWithdrawnFromTheAssetPage(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	// A bare port: one with no cable, which is what the control is offered on.
	id := h.lookup(`SELECT i.id FROM interface i
	                WHERE i.lifecycle = 'active'
	                  AND i.id NOT IN (SELECT a_interface_id FROM link WHERE lifecycle = 'active'
	                                   UNION SELECT b_interface_id FROM link WHERE lifecycle = 'active')
	                  AND i.id NOT IN (SELECT interface_id FROM ip_address WHERE interface_id IS NOT NULL)
	                  AND i.id NOT IN (SELECT interface_id FROM interface_vlan)
	                ORDER BY i.id LIMIT 1`)
	assetID := h.lookup(`SELECT asset_id FROM interface WHERE id = ?`, id)

	// Offered by the page, not just by the router -- this project has shipped a
	// 404 on a button with every handler test green.
	page := body(t, h.get("/assets/"+assetID, false))
	if !strings.Contains(page, "/interfaces/"+id+"/retire") {
		t.Fatalf("the asset page offers no Withdraw control for bare port %s, so the "+
			"route is reachable only by typing a URL nobody would guess", id)
	}

	resp := h.post("/interfaces/"+id+"/retire", url.Values{
		"csrf_token": {h.csrfToken("/assets/" + assetID)},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("withdrawing a bare port returned %d, want 303", resp.StatusCode)
	}
	if got := h.lookup(`SELECT lifecycle FROM interface WHERE id = ?`, id); got != "retired" {
		t.Errorf("lifecycle = %q after withdrawing, want retired", got)
	}
}

// TestAPatchedPortIsNeitherOfferedNorAccepted -- both directions, because
// either alone leaves the door open.
func TestAPatchedPortIsNeitherOfferedNorAccepted(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id := h.lookup(`SELECT a_interface_id FROM link WHERE lifecycle = 'active' ORDER BY id LIMIT 1`)
	assetID := h.lookup(`SELECT asset_id FROM interface WHERE id = ?`, id)

	page := body(t, h.get("/assets/"+assetID, false))
	if strings.Contains(page, "/interfaces/"+id+"/retire") {
		t.Error("the page offers Withdraw on a patched port; the store refuses it, " +
			"so that is a button whose only outcome is a refusal")
	}

	// And a direct request is refused too -- the control's absence is not the
	// enforcement.
	resp := h.post("/interfaces/"+id+"/retire", url.Values{
		"csrf_token": {h.csrfToken("/assets/" + assetID)},
	}, false)
	defer resp.Body.Close()
	if got := h.lookup(`SELECT lifecycle FROM interface WHERE id = ?`, id); got != "active" {
		t.Errorf("lifecycle = %q -- a patched port was withdrawn through a direct "+
			"request, leaving a cable on a port that is no longer there", got)
	}
}

// TestOnlyAWriterCanWithdrawAPort. interface is ScopeSubjectDerived through its
// owning asset, so an Observer is refused at the middleware.
func TestOnlyAWriterCanWithdrawAPort(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := h.lookup(`SELECT id FROM interface WHERE lifecycle = 'active' ORDER BY id LIMIT 1`)
	assetID := h.lookup(`SELECT asset_id FROM interface WHERE id = ?`, id)

	viewer := newHarness(t)
	viewer.login("viewer", "viewer-password")
	resp := viewer.post("/interfaces/"+id+"/retire", url.Values{
		"csrf_token": {viewer.csrfToken("/assets/" + assetID)},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("an Observer got %d withdrawing a port, want 403", resp.StatusCode)
	}
	if got := h.lookup(`SELECT lifecycle FROM interface WHERE id = ?`, id); got != "active" {
		t.Errorf("an Observer's withdrawal reached the database")
	}
}
