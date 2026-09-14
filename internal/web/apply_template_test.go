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

// The apply-template control (device-type-templates plan, Task 8): the
// reason the whole feature was built. Tasks 1-6 instantiate a device type's
// template onto an asset CREATED from it; nothing before this task did
// anything for the four DCS-7050SX3-48YC8 switches the demo already has,
// which predate their model's template and are the case that matters.
//
// Every test here declares the device type and creates the asset from it
// BEFORE adding any template component -- instantiateComponents reads the
// template at creation time and finds zero rows, so the asset is created
// bare, exactly like a real box that existed before anybody catalogued its
// ports. Components are added to the device type only afterwards, so
// ApplyTemplate has something to backfill.

func TestApplyTemplateAddsMissingPortsAndReportsHowMany(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	dtID := h.catalogueModel(t, "acme", "Acme Systems", "Switch-48", "2028-01-01")
	resp := h.post("/assets", url.Values{
		"csrf_token":     {h.csrfToken("/assets")},
		"name":           {"sw-backfill-01"},
		"kind":           {"switch"},
		"device_type_id": {dtID},
	}, false)
	resp.Body.Close()
	assetID := h.lookup(`SELECT id FROM asset WHERE name = ?`, "sw-backfill-01")

	if got := h.count(`SELECT COUNT(*) FROM interface WHERE asset_id = ?`, assetID); got != 0 {
		t.Fatalf("the asset was created with %d interfaces before the model carried any "+
			"template -- it should have been created bare", got)
	}

	// The model gains a template AFTER the asset already exists.
	resp = h.componentAdd(t, dtID, url.Values{
		"name_spec":   {"Ethernet1/[1-4]"},
		"form_factor": {"rj45"},
	})
	resp.Body.Close()

	// The control names what it will do before doing it.
	page := body(t, h.get("/assets/"+assetID, false))
	applyAction := "/assets/" + assetID + "/apply-template"
	if !strings.Contains(page, "Add 4 missing port") {
		t.Fatalf("the asset page does not say how many ports the control would add:\n%s", page)
	}
	if !strings.Contains(page, "Acme Systems Switch-48") {
		t.Errorf("the control does not name the model the missing ports come from:\n%s", page)
	}
	if !strings.Contains(page, `hx-post="`+applyAction+`"`) {
		t.Fatalf("no hx-post apply-template action on the asset page:\n%s", page)
	}
	if !strings.Contains(page, "hx-confirm=") {
		t.Error("the apply-template control carries no hx-confirm -- adding several ports at " +
			"once needs a confirmation the same way every other bulk action on this page has one")
	}

	before := h.count(`SELECT COUNT(*) FROM change_log`)
	resp = h.post(applyAction, url.Values{
		"csrf_token": {h.csrfToken("/assets/" + assetID)},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("applying the template returned %d, want 303", resp.StatusCode)
	}

	if got := h.count(`SELECT COUNT(*) FROM interface WHERE asset_id = ? AND lifecycle = 'active'`,
		assetID); got != 4 {
		t.Fatalf("applying the template left %d active interfaces on the asset, want 4", got)
	}
	for _, name := range []string{"Ethernet1/1", "Ethernet1/2", "Ethernet1/3", "Ethernet1/4"} {
		if got := h.count(`SELECT COUNT(*) FROM interface WHERE asset_id = ? AND name = ?`,
			assetID, name); got != 1 {
			t.Errorf("interface %q was not created by the apply, got %d rows", name, got)
		}
	}
	// One change_log row per interface created, the same audit shape
	// instantiateComponents writes at create time -- ApplyTemplate reuses
	// instantiateInterfaceComponents rather than a second code path.
	if after := h.count(`SELECT COUNT(*) FROM change_log`); after-before != 4 {
		t.Errorf("applying the template wrote %d change_log rows, want 4", after-before)
	}

	after := body(t, h.get("/assets/"+assetID, false))
	if !strings.Contains(after, "Added 4 ports") {
		t.Errorf("no flash confirming how many ports were added:\n%s", after)
	}
	// Complete now: the control should have nothing left to offer.
	if strings.Contains(after, applyAction) {
		t.Errorf("the apply-template control is still offered after the asset already has "+
			"every port its template names:\n%s", after)
	}
}

// TestApplyTemplateControlAbsentWithNoDeviceType is the "a control that can
// only do nothing is worse than no control" half of the brief: an asset with
// no device type at all must not even render the button.
func TestApplyTemplateControlAbsentWithNoDeviceType(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	resp := h.post("/assets", url.Values{
		"csrf_token": {h.csrfToken("/assets")},
		"name":       {"no-model-01"},
		"kind":       {"server"},
	}, false)
	resp.Body.Close()
	assetID := h.lookup(`SELECT id FROM asset WHERE name = ?`, "no-model-01")

	page := body(t, h.get("/assets/"+assetID, false))
	if strings.Contains(page, "apply-template") {
		t.Errorf("an asset with no device type at all offers the apply-template control:\n%s", page)
	}
}

// TestApplyTemplateControlAbsentWithNoTemplate covers the second way the
// control must not appear: a device type exists, but nobody has declared a
// template for it yet.
func TestApplyTemplateControlAbsentWithNoTemplate(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	dtID := h.catalogueModel(t, "acme", "Acme Systems", "Bare-1", "2028-01-01")
	resp := h.post("/assets", url.Values{
		"csrf_token":     {h.csrfToken("/assets")},
		"name":           {"bare-model-01"},
		"kind":           {"server"},
		"device_type_id": {dtID},
	}, false)
	resp.Body.Close()
	assetID := h.lookup(`SELECT id FROM asset WHERE name = ?`, "bare-model-01")

	page := body(t, h.get("/assets/"+assetID, false))
	if strings.Contains(page, "apply-template") {
		t.Errorf("an asset whose device type carries no template offers the apply-template "+
			"control:\n%s", page)
	}
}

// TestApplyTemplateOnACompleteAssetAddsNothing is the third outcome
// ApplyTemplate can report: not an error, just nothing to do.
func TestApplyTemplateOnACompleteAssetAddsNothing(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	dtID := h.catalogueModel(t, "acme", "Acme Systems", "Switch-8", "2028-01-01")
	resp := h.post("/assets", url.Values{
		"csrf_token":     {h.csrfToken("/assets")},
		"name":           {"sw-complete-01"},
		"kind":           {"switch"},
		"device_type_id": {dtID},
	}, false)
	resp.Body.Close()
	assetID := h.lookup(`SELECT id FROM asset WHERE name = ?`, "sw-complete-01")

	// The template arrives AFTER creation, same as the other tests, then the
	// operator applies it once so the asset is genuinely complete.
	resp = h.componentAdd(t, dtID, url.Values{
		"name_spec":   {"eth0"},
		"form_factor": {"rj45"},
	})
	resp.Body.Close()
	applyAction := "/assets/" + assetID + "/apply-template"
	resp = h.post(applyAction, url.Values{
		"csrf_token": {h.csrfToken("/assets/" + assetID)},
	}, false)
	resp.Body.Close()
	if got := h.count(`SELECT COUNT(*) FROM interface WHERE asset_id = ?`, assetID); got != 1 {
		t.Fatalf("setup: expected 1 interface after the first apply, got %d", got)
	}

	before := h.count(`SELECT COUNT(*) FROM change_log`)
	resp = h.post(applyAction, url.Values{
		"csrf_token": {h.csrfToken("/assets/" + assetID)},
	}, false)
	page := body(t, resp)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("applying an already-satisfied template returned %d, want 303 (nothing to do "+
			"is not an error):\n%s", resp.StatusCode, page)
	}
	if got := h.count(`SELECT COUNT(*) FROM interface WHERE asset_id = ?`, assetID); got != 1 {
		t.Errorf("re-applying the template changed the interface count to %d, want unchanged 1", got)
	}
	if after := h.count(`SELECT COUNT(*) FROM change_log`); after != before {
		t.Errorf("re-applying an already-satisfied template wrote %d change_log rows, want 0",
			after-before)
	}

	after := body(t, h.get("/assets/"+assetID, false))
	if !strings.Contains(after, "Nothing to add") {
		t.Errorf("no flash saying there was nothing to add:\n%s", after)
	}
}
