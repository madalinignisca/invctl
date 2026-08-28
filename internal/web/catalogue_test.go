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
	"strconv"
	"strings"
	"testing"
)

// The catalogue screens, and the one thing they exist to make visible: where an
// end-of-support date came from.

// catalogueModel adds a manufacturer and a model through the real forms, and
// returns the model's id.
func (h *harness) catalogueModel(t *testing.T, code, name, model, eol string) string {
	t.Helper()
	token := h.csrfToken("/catalogue")
	resp := h.post("/catalogue/manufacturers", url.Values{
		"csrf_token": {token}, "code": {code}, "name": {name},
	}, false)
	resp.Body.Close()

	mfID := h.lookup(`SELECT id FROM manufacturer WHERE code = ?`, code)
	resp = h.post("/catalogue/types", url.Values{
		"csrf_token": {h.csrfToken("/catalogue")}, "manufacturer_id": {mfID},
		"model": {model}, "eol_date": {eol},
	}, false)
	resp.Body.Close()
	return h.lookup(`SELECT id FROM device_type WHERE model = ?`, model)
}

// lookup reads a single string out of the harness database.
func (h *harness) lookup(query string, args ...any) string {
	h.t.Helper()
	var out string
	reader := h.store.DB().Reader
	if err := reader.Get(&out, reader.Rebind(query), args...); err != nil {
		h.t.Fatalf("looking up (%s): %v", query, err)
	}
	return out
}

func TestCataloguingAModelAndPointingAnAssetAtIt(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	dtID := h.catalogueModel(t, "acme", "Acme Systems", "Widget-9000", "2027-09-30")
	if dtID == "" {
		t.Fatal("the model was not catalogued")
	}

	page := body(t, h.get("/catalogue", false))
	if !strings.Contains(page, "Widget-9000") || !strings.Contains(page, "Acme Systems") {
		t.Errorf("the catalogue page does not list what it just created:\n%s", page)
	}

	// THE PICKER IS ACTUALLY POPULATED. An empty picker renders identically to a
	// working one and makes the whole feature unreachable -- which is exactly
	// how the team pickers shipped broken once already.
	form := body(t, h.get("/assets", false))
	if !strings.Contains(form, `name="device_type_id"`) {
		t.Fatal("the asset form has no catalogued-model picker")
	}
	if !strings.Contains(form, dtID) {
		t.Errorf("the model picker does not offer the model that exists. An empty picker " +
			"looks exactly like a working one and makes the feature unreachable.")
	}
}

func TestAnInheritedSupportDateSaysWhereItCameFrom(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	dtID := h.catalogueModel(t, "acme", "Acme Systems", "Widget-9000", "2027-09-30")

	// An asset of that model, with NO date of its own.
	resp := h.post("/assets", url.Values{
		"csrf_token":     {h.csrfToken("/assets")},
		"name":           {"inherits-01"},
		"kind":           {"server"},
		"device_type_id": {dtID},
	}, false)
	resp.Body.Close()
	assetID := h.lookup(`SELECT id FROM asset WHERE name = ?`, "inherits-01")

	page := body(t, h.get("/assets/"+assetID, false))
	if !strings.Contains(page, "2027-09-30") {
		t.Fatalf("the asset page shows no support date, so the inheritance is not "+
			"reaching the screen:\n%s", page)
	}
	if !strings.Contains(page, "inherited from") {
		t.Error("the asset page shows the date without saying it came from the model. " +
			"A date whose origin is unstated renders a manufacturer's claim about a " +
			"MODEL identically to somebody's claim about THIS box.")
	}
	if !strings.Contains(page, "Acme Systems Widget-9000") {
		t.Error("the page does not name the model the date came from")
	}
}

func TestAnAssetsOwnDateIsLabelledAsItsOwn(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	dtID := h.catalogueModel(t, "acme", "Acme Systems", "Widget-9000", "2027-09-30")

	// The magic contract: this box is supported past what the model promises.
	resp := h.post("/assets", url.Values{
		"csrf_token":     {h.csrfToken("/assets")},
		"name":           {"contracted-01"},
		"kind":           {"server"},
		"device_type_id": {dtID},
		"eol_date":       {"2031-12-31"},
	}, false)
	resp.Body.Close()
	assetID := h.lookup(`SELECT id FROM asset WHERE name = ?`, "contracted-01")

	page := body(t, h.get("/assets/"+assetID, false))
	if !strings.Contains(page, "2031-12-31") {
		t.Fatalf("the asset's own date did not win over its model's:\n%s", page)
	}
	if strings.Contains(page, "2027-09-30") {
		t.Error("the model's date is still shown as the support date. The asset's own " +
			"assertion has to win, or the page nags about hardware somebody has paid " +
			"to keep supported.")
	}
	if !strings.Contains(page, "recorded on this asset") {
		t.Error("the page does not say the date is this asset's own rather than inherited")
	}
}

func TestTheCatalogueIsAdminOnly(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	h.catalogueModel(t, "acme", "Acme Systems", "Widget-9000", "2027-09-30")
	before := h.count(`SELECT COUNT(*) FROM device_type`)
	mfID := h.lookup(`SELECT id FROM manufacturer WHERE code = ?`, "acme")

	h.logout()
	h.login("viewer", "viewer-password")

	// Readable: the catalogue is part of the inventory, and a read-only user
	// answering "what model is that box" is the audience this is written for.
	resp := h.get("/catalogue", false)
	page := body(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /catalogue as a read-only user returned %d, want 200", resp.StatusCode)
	}
	if strings.Contains(page, "Catalogue a model") {
		t.Error("a read-only user is offered the add-a-model form, whose only outcome is a 403")
	}

	// Not writable -- and the submission has to be one that WOULD have worked.
	//
	// This test previously posted a model with no manufacturer_id. That is
	// refused by VALIDATION, not by authorization, so it passed unchanged when
	// the route was moved off RequireWrite: nothing was created either way.
	// Caught by mutating the route and watching the test stay green.
	resp = h.post("/catalogue/types", url.Values{
		"csrf_token":      {h.csrfToken("/catalogue")},
		"manufacturer_id": {mfID},
		"model":           {"sneaky"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST /catalogue/types as a read-only user returned %d, want 403",
			resp.StatusCode)
	}
	if h.count(`SELECT COUNT(*) FROM device_type WHERE model = 'sneaky'`) != 0 {
		t.Error("a read-only user catalogued a model")
	}
	if got := h.count(`SELECT COUNT(*) FROM device_type`); got != before {
		t.Errorf("device types went from %d to %d under a read-only session", before, got)
	}
}

func TestARefusedModelKeepsWhatWasTyped(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	h.catalogueModel(t, "acme", "Acme Systems", "Widget-9000", "2027-09-30")
	mfID := h.lookup(`SELECT id FROM manufacturer WHERE code = ?`, "acme")

	// A rack height that is not a number. Refused rather than silently stored
	// as zero, which would put the model into every elevation calculation as
	// occupying nothing.
	resp := h.post("/catalogue/types", url.Values{
		"csrf_token": {h.csrfToken("/catalogue")}, "manufacturer_id": {mfID},
		"model": {"R750"}, "u_height": {"tall"},
	}, false)
	page := body(t, resp)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", resp.StatusCode)
	}
	if h.count(`SELECT COUNT(*) FROM device_type WHERE model = 'R750'`) != 0 {
		t.Error("a model with an unreadable rack height was stored anyway")
	}
	// The refusal must have somewhere to render. A 422 whose message has no
	// rendering site is a blank screen, which this project has shipped three
	// times.
	if !strings.Contains(page, "field-error") {
		t.Errorf("the refusal has no visible field error:\n%s", page)
	}
	if !strings.Contains(page, "R750") {
		t.Error("the refused form came back blank; what was typed has to survive a refusal")
	}
}

// TestFilteringAssetsByModelActuallyFilters covers a link that was a no-op.
//
// The catalogue lists "3 assets" against a model and links to
// /assets?device_type_id=<id>. AssetFilter had no such field, so the parameter
// was accepted, ignored, and the page showed the WHOLE inventory under a
// heading that implied otherwise -- the silent-fallback shape, in a link.
//
// Nothing on screen distinguishes "every asset is of this model" from "the
// filter did nothing", which is why this asserts on what is ABSENT.
func TestFilteringAssetsByModelActuallyFilters(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	dtID := h.catalogueModel(t, "acme", "Acme Systems", "Widget-9000", "2027-09-30")

	resp := h.post("/assets", url.Values{
		"csrf_token":     {h.csrfToken("/assets")},
		"name":           {"of-the-model-01"},
		"kind":           {"server"},
		"device_type_id": {dtID},
	}, false)
	resp.Body.Close()
	resp = h.post("/assets", url.Values{
		"csrf_token": {h.csrfToken("/assets")},
		"name":       {"not-of-the-model-01"},
		"kind":       {"server"},
	}, false)
	resp.Body.Close()

	// Asserted on the ROW LINKS, not on the names appearing anywhere in the
	// page. The same page renders a parent picker listing every asset, so a
	// bare substring check finds every name in an <option> and can never fail.
	inID := h.lookup(`SELECT id FROM asset WHERE name = ?`, "of-the-model-01")
	outID := h.lookup(`SELECT id FROM asset WHERE name = ?`, "not-of-the-model-01")

	page := body(t, h.get("/assets?device_type_id="+dtID, false))
	if !strings.Contains(page, `href="/assets/`+inID+`"`) {
		t.Error("the filtered list omits an asset that IS of that model")
	}
	if strings.Contains(page, `href="/assets/`+outID+`"`) {
		t.Error("the filtered list includes an asset that is not of that model, so the " +
			"parameter is being ignored and the page is showing everything")
	}
	// And the seeded estate is excluded too, so this cannot pass on a database
	// where "everything" and "the filtered set" happen to coincide.
	seeded := h.asset("hv-01")
	if strings.Contains(page, `href="/assets/`+seeded+`"`) {
		t.Error("the filtered list includes seeded assets with no model at all")
	}
}

// TestARackDrawsItsElevationAndSaysWhatItCannotPlace covers the two halves of
// the rack page: what is drawn, and what is admitted to be missing from it.
func TestARackDrawsItsElevationAndSaysWhatItCannotPlace(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	rack := h.asset("rack-a1")
	// Give the rack a height through the real form.
	h.setRackHeight(t, rack, 12)

	// One box placed, one in the rack with no position -- the ordinary state.
	resp := h.post("/assets", url.Values{
		"csrf_token": {h.csrfToken("/assets")}, "name": {"placed-1"}, "kind": {"server"},
		"parent_id": {rack}, "rack_position": {"4"},
	}, false)
	resp.Body.Close()
	resp = h.post("/assets", url.Values{
		"csrf_token": {h.csrfToken("/assets")}, "name": {"somewhere-1"}, "kind": {"server"},
		"parent_id": {rack},
	}, false)
	resp.Body.Close()

	page := body(t, h.get("/assets/"+rack, false))
	if !strings.Contains(page, "Elevation") {
		t.Fatalf("the rack page has no elevation:\n%s", page)
	}
	if !strings.Contains(page, "placed-1") {
		t.Error("the elevation does not show the box that has a position")
	}
	// The admission, which is the half that keeps the diagram honest.
	if !strings.Contains(page, "no\n      position recorded") &&
		!strings.Contains(page, "no position recorded") {
		t.Errorf("the page does not say an asset is in the rack without a position. A "+
			"diagram of one box in a rack of twelve is misleading on its own:\n%s", page)
	}
	if !strings.Contains(page, "somewhere-1") {
		t.Error("the unpositioned asset is not named")
	}

	// And a collision is refused with a message on the field.
	resp = h.post("/assets", url.Values{
		"csrf_token": {h.csrfToken("/assets")}, "name": {"collide-1"}, "kind": {"server"},
		"parent_id": {rack}, "rack_position": {"4"},
	}, false)
	collide := body(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 for a unit that is already taken", resp.StatusCode)
	}
	if !strings.Contains(collide, "placed-1 is already there") {
		t.Errorf("the refusal does not name what is in the way:\n%s", collide)
	}
}

// setRackHeight records a rack's capacity through the asset form.
func (h *harness) setRackHeight(t *testing.T, rackID string, units int) {
	t.Helper()
	row := h.lookup(`SELECT name FROM asset WHERE id = ?`, rackID)
	version := h.lookup(`SELECT row_version FROM asset WHERE id = ?`, rackID)
	resp := h.post("/assets/"+rackID, url.Values{
		"csrf_token": {h.csrfToken("/assets/" + rackID)},
		"name":       {row}, "kind": {"rack"}, "lifecycle": {"active"},
		"u_height": {strconv.Itoa(units)}, "row_version": {version},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("setting the rack height returned %d, want 303", resp.StatusCode)
	}
}
