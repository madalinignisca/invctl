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

// A device type's component template (device-type-templates plan, Task 7):
// the routes wired here are the whole reason Tasks 1-6's store surface --
// ListDeviceTypeComponents, CreateDeviceTypeComponents,
// UpdateDeviceTypeComponent, RetireDeviceTypeComponent -- was reachable from
// nowhere but a Go test until now.

// componentAdd posts the "add by range" form for a device type, filling in a
// fresh CSRF token from the row it is about to open.
func (h *harness) componentAdd(t *testing.T, deviceTypeID string, form url.Values) *http.Response {
	t.Helper()
	form.Set("csrf_token", h.csrfToken("/catalogue?edit="+deviceTypeID))
	return h.post("/catalogue/types/"+deviceTypeID+"/components", form, false)
}

// TestAddingComponentsByARangeCreatesEveryName is the point of the range
// field: a 48-port switch declared as one line, not 48.
func TestAddingComponentsByARangeCreatesEveryName(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	dtID := h.catalogueModel(t, "acme", "Acme Systems", "Switch-48", "2028-01-01")

	resp := h.componentAdd(t, dtID, url.Values{
		"name_spec":   {"Ethernet[1-48]"},
		"form_factor": {"rj45"},
	})
	page := body(t, resp)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("adding a range returned %d, want 303:\n%s", resp.StatusCode, page)
	}

	if got := h.count(`SELECT COUNT(*) FROM device_type_component WHERE device_type_id = ?`, dtID); got != 48 {
		t.Fatalf("adding Ethernet[1-48] created %d rows, want 48", got)
	}
	if got := h.count(`SELECT COUNT(*) FROM change_log WHERE entity_type = 'device_type_component'`); got != 1 {
		t.Errorf("adding 48 ports wrote %d change_log rows, want exactly 1 -- one operator "+
			"action, one audit entry, not 48", got)
	}

	// Ethernet24 and Ethernet48, NOT Ethernet1: "Ethernet1" is a prefix of
	// Ethernet10 through Ethernet19, so a Contains check on it passes even if
	// the only thing rendered is Ethernet10. Two unambiguous names prove the
	// expansion reached the page.
	//
	// These read Ethernet1/1 and Ethernet1/48 until the review found the
	// example string they came from was wrong for the estate's own switches.
	// The input was corrected here and the assertion was not, so the test kept
	// asserting the shape the fix had just removed.
	reopened := body(t, h.get("/catalogue?edit="+dtID, false))
	if !strings.Contains(reopened, "Ethernet24") || !strings.Contains(reopened, "Ethernet48") {
		t.Errorf("the reopened row does not list the range it just created:\n%s", reopened)
	}
}

// TestAMalformedRangeIsRefusedWithTheTypedSpecStillInTheBox is CLAUDE.md's
// rule applied to the one field on this page that can fail in a way no
// domain.ValidationError describes: domain.ExpandRange's own error.
func TestAMalformedRangeIsRefusedWithTheTypedSpecStillInTheBox(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	dtID := h.catalogueModel(t, "acme", "Acme Systems", "Switch-24", "2028-01-01")

	resp := h.componentAdd(t, dtID, url.Values{
		// Counts backwards -- domain.ExpandRange refuses it outright.
		"name_spec":   {"Ethernet1/[9-2]"},
		"form_factor": {"rj45"},
	})
	page := body(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a malformed range returned %d, want 422:\n%s", resp.StatusCode, page)
	}
	if !strings.Contains(page, "Ethernet1/[9-2]") {
		t.Errorf("the refused form does not carry the range the operator typed back:\n%s", page)
	}
	if got := h.count(`SELECT COUNT(*) FROM device_type_component WHERE device_type_id = ?`, dtID); got != 0 {
		t.Errorf("a malformed range created %d rows; a refusal should create none", got)
	}
}

// TestTheComponentEditFormCarriesItsVersion is the behavioural half of
// TestEveryEditFormCarriesItsVersion's static scan: this drives the real
// route and proves the token round-trips into an actual correction, not just
// that the markup happens to contain the right input name.
func TestTheComponentEditFormCarriesItsVersion(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	dtID := h.catalogueModel(t, "acme", "Acme Systems", "Widget-1", "2028-01-01")

	resp := h.componentAdd(t, dtID, url.Values{"name_spec": {"mgmt0"}, "form_factor": {"rj45"}})
	resp.Body.Close()
	componentID := h.lookup(`SELECT id FROM device_type_component WHERE device_type_id = ? AND name = ?`,
		dtID, "mgmt0")

	editPage := body(t, h.get("/catalogue?edit="+dtID+"&component="+componentID, false))
	if !strings.Contains(editPage, `name="row_version" value="1"`) {
		t.Fatalf("the component's edit form does not carry its row_version:\n%s", editPage)
	}

	token := h.csrfToken("/catalogue?edit=" + dtID + "&component=" + componentID)
	resp = h.post("/catalogue/types/"+dtID+"/components/"+componentID, url.Values{
		"csrf_token":  {token},
		"name":        {"mgmt0-renamed"},
		"form_factor": {"sfp28"},
		"row_version": {"1"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("correcting a component returned %d, want 303", resp.StatusCode)
	}
	if got := h.lookup(`SELECT name FROM device_type_component WHERE id = ?`, componentID); got != "mgmt0-renamed" {
		t.Errorf("the component's name is %q, want %q", got, "mgmt0-renamed")
	}
	if got := h.lookup(`SELECT form_factor FROM device_type_component WHERE id = ?`, componentID); got != "sfp28" {
		t.Errorf("the component's form factor is %q, want %q", got, "sfp28")
	}

	// A stale token is refused, not silently accepted -- the guard the
	// carried version exists for.
	resp = h.post("/catalogue/types/"+dtID+"/components/"+componentID, url.Values{
		"csrf_token":  {h.csrfToken("/catalogue?edit=" + dtID + "&component=" + componentID)},
		"name":        {"mgmt0-renamed-again"},
		"form_factor": {"sfp28"},
		"row_version": {"1"}, // stale: the row is on version 2 after the save above
	}, false)
	page := body(t, resp)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("a stale correction returned %d, want 409:\n%s", resp.StatusCode, page)
	}
	if got := h.lookup(`SELECT name FROM device_type_component WHERE id = ?`, componentID); got != "mgmt0-renamed" {
		t.Errorf("a stale write changed the name to %q", got)
	}
}

// TestWithdrawingAComponentRetiresItAndConfirmsFirst covers both halves of
// Task 7's withdrawal path: the confirmation on the button (it changes what
// every future asset of the model gets), and that the store call it drives
// actually withdraws the row rather than deleting it.
func TestWithdrawingAComponentRetiresItAndConfirmsFirst(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	dtID := h.catalogueModel(t, "acme", "Acme Systems", "Widget-2", "2028-01-01")

	resp := h.componentAdd(t, dtID, url.Values{"name_spec": {"eth0"}, "form_factor": {"rj45"}})
	resp.Body.Close()
	componentID := h.lookup(`SELECT id FROM device_type_component WHERE device_type_id = ? AND name = ?`,
		dtID, "eth0")

	listed := body(t, h.get("/catalogue?edit="+dtID, false))
	retireAction := "/catalogue/types/" + dtID + "/components/" + componentID + "/retire"
	if !strings.Contains(listed, `hx-post="`+retireAction+`"`) {
		t.Fatalf("no hx-post withdrawal action for the component that was just added:\n%s", listed)
	}
	if !strings.Contains(listed, "hx-confirm=") {
		t.Error("the withdraw button carries no hx-confirm -- withdrawing a component changes " +
			"what every future asset of this model gets, and that needs a confirmation the same " +
			"way every other withdrawal on this codebase has one")
	}

	resp = h.post(retireAction, url.Values{
		"csrf_token": {h.csrfToken("/catalogue?edit=" + dtID)},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("withdrawing a component returned %d, want 303", resp.StatusCode)
	}

	if got := h.lookup(`SELECT lifecycle FROM device_type_component WHERE id = ?`, componentID); got != "retired" {
		t.Errorf("the component's lifecycle is %q, want retired -- soft delete only", got)
	}

	reopened := body(t, h.get("/catalogue?edit="+dtID, false))
	if strings.Contains(reopened, "eth0") {
		t.Error("a withdrawn component is still listed as active on the reopened row")
	}
}
