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

// The detail-page half of WP-A4: the grouped display and the value editor,
// docs/custom-fields-design.md §4, §6 and §8. Task 5 built the registry;
// this is the last user-facing piece.

func TestCustomFieldsRenderInTheirOwnSection(t *testing.T) {
	// Grouped and labelled as the organisation's own, never interleaved with
	// built-in fields: a new hire must be able to tell at a glance which of
	// these invctl shipped.
	h := newHarness(t)
	h.login("admin", "admin-password")
	mustCreateFieldViaHTTP(t, h, "asset", "cost_centre", "Cost Centre", "text")

	page := body(t, h.get("/assets/"+h.refs.Assets["hv-01"], false))
	heading := strings.Index(page, "Defined by your organisation")
	label := strings.Index(page, "Cost Centre")
	if heading < 0 {
		t.Fatal("the custom section must carry a heading naming the organisation, " +
			"not the product")
	}
	if label < heading {
		t.Fatal("the custom field appears above its own heading, which means it is " +
			"interleaved with built-in fields -- the thing a new hire cannot tell apart")
	}
}

func TestTheSectionIsAbsentWhenNoFieldIsDefined(t *testing.T) {
	// An estate that never uses the feature never sees it.
	h := newHarness(t)
	h.login("admin", "admin-password")
	page := body(t, h.get("/assets/"+h.refs.Assets["hv-01"], false))
	if strings.Contains(page, "Defined by your organisation") {
		t.Fatal("the custom section renders with no fields defined")
	}
}

// customFieldValueMarkup is the exact fragment custom_fields_show renders
// for one field's stored value. Scoped assertions use this rather than a
// bare substring check on the whole page: docs/custom-fields-design.md §5
// folds a custom value into the audit trail PERMANENTLY, by design, and the
// Timeline panel on the very same detail page renders that folded diff
// verbatim -- so a bare "the page must not contain this value" assertion
// would fail for the wrong reason, against a guarantee this work package
// does not own and must not weaken.
func customFieldValueMarkup(value string) string {
	return `<dd class="mono">` + value + `</dd>`
}

func TestARetiredFieldDisappearsFromTheDetailPage(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := mustCreateFieldViaHTTP(t, h, "asset", "cost_centre", "Cost Centre", "text")
	assetID := h.refs.Assets["hv-01"]
	mustSetValueViaHTTP(t, h, assetID, id, "IT-42")

	if !strings.Contains(body(t, h.get("/assets/"+assetID, false)), customFieldValueMarkup("IT-42")) {
		t.Fatal("the value must show before retirement, or this test proves nothing")
	}
	if code := h.post("/custom-fields/"+id+"/retire", url.Values{
		"csrf_token": {h.csrfToken("/custom-fields")},
	}, false).StatusCode; code != http.StatusSeeOther {
		t.Fatalf("retiring: got %d", code)
	}
	after := body(t, h.get("/assets/"+assetID, false))
	if strings.Contains(after, customFieldValueMarkup("IT-42")) {
		t.Fatal("a retired field still renders on the detail page")
	}
	if strings.Contains(after, "Defined by your organisation") {
		t.Fatal("the custom fields section still renders once its only field is retired")
	}
}

func TestAnInvalidValueReturns422AndKeepsTheOthers(t *testing.T) {
	// One bad value must not discard the good ones the operator also typed.
	h := newHarness(t)
	h.login("admin", "admin-password")
	tag := mustCreateFieldViaHTTP(t, h, "asset", "asset_tag", "Asset Tag", "text")
	due := mustCreateFieldViaHTTP(t, h, "asset", "warranty_ends", "Warranty ends", "date")
	assetID := h.refs.Assets["hv-01"]

	page := body(t, h.get("/assets/"+assetID, false))
	resp := h.post("/assets/"+assetID+"/custom-fields", url.Values{
		"csrf_token":  {h.csrfToken("/assets/" + assetID)},
		"row_version": {versionInForm(t, page)},
		"cf_" + tag:   {"ABC-1234"},
		"cf_" + due:   {"march next year"},
	}, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422", resp.StatusCode)
	}
	if !strings.Contains(body(t, resp), "ABC-1234") {
		t.Error("the re-rendered form dropped the value that was valid")
	}
}

// TestAnEmbeddedNewlineIsToldApartFromAnEmbeddedNUL is Task 2's carried
// rule: the domain layer's refusal is the same generic message for both, and
// somebody who pasted a two-line value is told about the line break, not
// about "control characters" in the abstract -- special-cased where the
// error is rendered.
func TestAnEmbeddedNewlineIsToldApartFromAnEmbeddedNUL(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := mustCreateFieldViaHTTP(t, h, "asset", "notes_field", "Notes", "text")
	assetID := h.refs.Assets["hv-01"]

	page := body(t, h.get("/assets/"+assetID, false))
	resp := h.post("/assets/"+assetID+"/custom-fields", url.Values{
		"csrf_token":  {h.csrfToken("/assets/" + assetID)},
		"row_version": {versionInForm(t, page)},
		"cf_" + id:    {"first line\nsecond line"},
	}, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422", resp.StatusCode)
	}
	got := body(t, resp)
	if !strings.Contains(got, "must be a single line") {
		t.Errorf("a pasted newline was not told apart from a generic control character: %s", got)
	}
}

// TestClearingAValueRemovesIt is the "present, blank or space" half of the
// submission contract (design.md §6): an explicit blank is a deliberate
// clear, distinct from the field being absent from the form altogether.
func TestClearingAValueRemovesIt(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := mustCreateFieldViaHTTP(t, h, "asset", "cost_centre", "Cost Centre", "text")
	assetID := h.refs.Assets["hv-01"]
	mustSetValueViaHTTP(t, h, assetID, id, "IT-42")

	page := body(t, h.get("/assets/"+assetID, false))
	resp := h.post("/assets/"+assetID+"/custom-fields", url.Values{
		"csrf_token":  {h.csrfToken("/assets/" + assetID)},
		"row_version": {versionInForm(t, page)},
		"cf_" + id:    {""},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("clearing: got %d", resp.StatusCode)
	}
	if strings.Contains(body(t, h.get("/assets/"+assetID, false)), customFieldValueMarkup("IT-42")) {
		t.Error("an explicit blank did not clear the stored value")
	}
}

// TestTheValueEditorDrawsTheStoredValueNotABlank is the converse half of the
// submission contract (design.md §6), the same Critical Task 5's options
// editor had: a form that draws a blank over an existing value produces a
// submission that commits that blank faithfully on the very next save. The
// widget itself must carry the stored value, not merely the display list
// beside it.
func TestTheValueEditorDrawsTheStoredValueNotABlank(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := mustCreateFieldViaHTTP(t, h, "asset", "cost_centre", "Cost Centre", "text")
	assetID := h.refs.Assets["hv-01"]
	mustSetValueViaHTTP(t, h, assetID, id, "IT-42")

	page := body(t, h.get("/assets/"+assetID, false))
	want := `name="cf_` + id + `" value="IT-42"`
	if !strings.Contains(page, want) {
		t.Errorf("the value editor's input did not carry the stored value verbatim:\n%s", page)
	}
}

// mustCreateFieldViaHTTP defines a field through the real handler and
// returns its id, for tests that need one to already exist.
func mustCreateFieldViaHTTP(t *testing.T, h *harness, entityType, code, label, kind string) string {
	t.Helper()
	form := url.Values{
		"csrf_token":  {h.csrfToken("/custom-fields")},
		"entity_type": {entityType}, "code": {code}, "label": {label},
		"kind": {kind}, "description": {"a fixture field for the web test suite"},
	}
	resp := h.post("/custom-fields", form, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("defining custom field %s: got %d", code, resp.StatusCode)
	}
	return firstFieldIDFor(t, body(t, h.get("/custom-fields", false)), code)
}

// mustSetValueViaHTTP sets one asset's value for one field through the
// real handler, carrying the asset's own row_version off the page it
// rendered -- the same token the operator's browser would have carried.
func mustSetValueViaHTTP(t *testing.T, h *harness, assetID, fieldID, value string) {
	t.Helper()
	page := body(t, h.get("/assets/"+assetID, false))
	resp := h.post("/assets/"+assetID+"/custom-fields", url.Values{
		"csrf_token":    {h.csrfToken("/assets/" + assetID)},
		"row_version":   {versionInForm(t, page)},
		"cf_" + fieldID: {value},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("setting custom value: got %d", resp.StatusCode)
	}
}
