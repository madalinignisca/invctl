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
	"regexp"
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

// TestANumberCustomFieldRendersOnTheDetailPage closes Ruling AU's first gap:
// every other web-layer test in this file exercises the text kind only, and
// docs/custom-fields-design.md §3's numeric canonicalisation (the trimmed
// original text, not a reparsed float) is a real path CanonicalCustomValue
// takes that this package had never rendered through HTTP and a template.
func TestANumberCustomFieldRendersOnTheDetailPage(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := mustCreateFieldViaHTTP(t, h, "asset", "power_rating", "Power Rating (W)", "number")
	assetID := h.refs.Assets["hv-01"]
	mustSetValueViaHTTP(t, h, assetID, id, "42.50")

	page := body(t, h.get("/assets/"+assetID, false))
	if !strings.Contains(page, customFieldValueMarkup("42.50")) {
		t.Fatal("a number custom field's stored value must render verbatim on the detail page")
	}
}

// TestACustomFieldRendersOnAServiceDetailPage closes Ruling AU's second gap:
// every other web-layer test in this file, including the CSRF/version and
// retirement paths, exercises an asset. Nothing had exercised the identical
// ServiceCustomFields handler through HTTP at all.
func TestACustomFieldRendersOnAServiceDetailPage(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := mustCreateFieldViaHTTP(t, h, "service", "support_channel", "Support Channel", "text")
	serviceID := h.refs.Services["vault"]
	mustSetServiceValueViaHTTP(t, h, serviceID, id, "#vault-oncall")

	page := body(t, h.get("/services/"+serviceID, false))
	heading := strings.Index(page, "Defined by your organisation")
	if heading < 0 {
		t.Fatal("the custom section must render on a service detail page too")
	}
	if !strings.Contains(page, customFieldValueMarkup("#vault-oncall")) {
		t.Fatal("a service's custom value must render verbatim on the detail page")
	}
}

// TestTheValueEditorWarnsThatCustomValuesArePermanentAndMustNotHoldPII is
// FINAL REVIEW AX(a): the creation form's warning (Ruling AD) is opened by
// whoever DEFINES a field, never by whoever later TYPES a value into one on
// an asset or service page -- often somebody who has never seen the
// creation form at all. This is the warning at the point PII actually gets
// typed.
func TestTheValueEditorWarnsThatCustomValuesArePermanentAndMustNotHoldPII(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	mustCreateFieldViaHTTP(t, h, "asset", "cost_centre", "Cost Centre", "text")

	page := body(t, h.get("/assets/"+h.refs.Assets["hv-01"], false))
	if !strings.Contains(page, "Recorded permanently") {
		t.Error("the value editor does not warn that a custom value is recorded permanently")
	}
	if !strings.Contains(page, "personal data") {
		t.Error("the value editor does not warn against typing personal data")
	}
}

// TestTheShowPanelLinksToTheRegistryAndRendersTheDescription is FINAL REVIEW
// ATTRIBUTION: the registry and the value editor both attribute properly,
// but the read-only show panel -- the one an operator most often actually
// lands on -- carried no link back to the registry and rendered none of a
// field's own "why this field exists" description, leaving "who added this
// and why" a question this panel could not itself answer.
func TestTheShowPanelLinksToTheRegistryAndRendersTheDescription(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := mustCreateFieldViaHTTP(t, h, "asset", "cost_centre", "Cost Centre", "text")
	assetID := h.refs.Assets["hv-01"]
	mustSetValueViaHTTP(t, h, assetID, id, "IT-42")

	page := body(t, h.get("/assets/"+assetID, false))
	if !strings.Contains(page, `href="/custom-fields"`) {
		t.Error("the show panel does not link to the custom fields registry")
	}
	if !strings.Contains(page, "a fixture field for the web test suite") {
		t.Error("the show panel does not render the field's own description")
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

// TestABooleanFieldStaysUnsetWhenAnUnrelatedFieldIsSaved is the Critical a
// reviewer found: the widget must never fabricate a declaration as a side
// effect of an unrelated edit. This is a SHARED multi-field form -- every
// live field is posted on every save -- so a checkbox (which cannot
// represent "no assertion": unticked and never-recorded are the same state
// on the wire) would record "false" against every boolean the entity had
// never held, the moment an operator saved a fix to something else
// entirely. The three-state select must not do that: its blank state posts
// an empty string, which clears a field that already holds nothing, a
// correct no-op.
func TestABooleanFieldStaysUnsetWhenAnUnrelatedFieldIsSaved(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	boolID := mustCreateFieldViaHTTP(t, h, "asset", "in_scope", "In Scope", "boolean")
	noteID := mustCreateFieldViaHTTP(t, h, "asset", "note", "Note", "text")
	assetID := h.refs.Assets["hv-01"]

	page := body(t, h.get("/assets/"+assetID, false))
	if got := selectedCustomFieldOption(t, page, boolID); got != "" {
		t.Fatalf("the boolean must start unset, or this test proves nothing: got %q", got)
	}

	// Exactly what a real submission of this shared form sends: the field
	// the operator actually edited, and the boolean carrying whatever its
	// own widget drew -- here, blank, because it was never set.
	resp := h.post("/assets/"+assetID+"/custom-fields", url.Values{
		"csrf_token":   {h.csrfToken("/assets/" + assetID)},
		"row_version":  {versionInForm(t, page)},
		"cf_" + noteID: {"a note about this box"},
		"cf_" + boolID: {""},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("saving: got %d", resp.StatusCode)
	}

	after := body(t, h.get("/assets/"+assetID, false))
	if got := selectedCustomFieldOption(t, after, boolID); got != "" {
		t.Errorf("saving an unrelated field recorded the boolean as %q -- "+
			"it must stay unset until somebody actually asserts it", got)
	}
}

// TestABooleanCanBeSetToYesToNoAndClearedBackToUnset exercises every state
// transition the three-state widget offers, and that the widget always
// redraws the state that is actually stored.
func TestABooleanCanBeSetToYesToNoAndClearedBackToUnset(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := mustCreateFieldViaHTTP(t, h, "asset", "in_scope", "In Scope", "boolean")
	assetID := h.refs.Assets["hv-01"]

	setBoolean := func(value string) string {
		t.Helper()
		page := body(t, h.get("/assets/"+assetID, false))
		resp := h.post("/assets/"+assetID+"/custom-fields", url.Values{
			"csrf_token":  {h.csrfToken("/assets/" + assetID)},
			"row_version": {versionInForm(t, page)},
			"cf_" + id:    {value},
		}, false)
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("setting the boolean to %q: got %d", value, resp.StatusCode)
		}
		return selectedCustomFieldOption(t, body(t, h.get("/assets/"+assetID, false)), id)
	}

	if got := setBoolean("true"); got != "true" {
		t.Errorf("setting to yes: the widget redrew %q, not the stored state", got)
	}
	if got := setBoolean("false"); got != "false" {
		t.Errorf("setting to no: the widget redrew %q, not the stored state", got)
	}
	if got := setBoolean(""); got != "" {
		t.Errorf("clearing back to unset: the widget redrew %q, not blank", got)
	}
}

// TestAFieldRetiredBetweenRenderAndSubmitIsSilentlyDropped is Ruling AJ: a
// field retired between the form being rendered and the submission arriving
// is a field the operator was not shown at the moment they clicked Save, so
// the submission must not name it at all -- the first half of the
// submission contract applied to the race. Before the fix this value
// reached the store, which correctly refused it but as a generic error page
// rather than the styled per-field 422 every sibling refusal in this
// handler produces; the observable difference from here is that the SAME
// submission, with every other field unchanged, now succeeds.
func TestAFieldRetiredBetweenRenderAndSubmitIsSilentlyDropped(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := mustCreateFieldViaHTTP(t, h, "asset", "cost_centre", "Cost Centre", "text")
	assetID := h.refs.Assets["hv-01"]

	page := body(t, h.get("/assets/"+assetID, false))
	token := versionInForm(t, page)

	retire := h.post("/custom-fields/"+id+"/retire",
		url.Values{"csrf_token": {h.csrfToken("/custom-fields")}}, false)
	retire.Body.Close()
	if retire.StatusCode != http.StatusSeeOther {
		t.Fatalf("retiring: got %d", retire.StatusCode)
	}

	resp := h.post("/assets/"+assetID+"/custom-fields", url.Values{
		"csrf_token":  {h.csrfToken("/assets/" + assetID)},
		"row_version": {token},
		"cf_" + id:    {"a value for a field that just retired"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("a value for a field retired mid-flight must be dropped, not refused: got %d", resp.StatusCode)
	}
}

// selectedCustomFieldOption reads which <option> the value editor's select
// for one field currently carries selected, scoped to that field's own
// <select id="cf-<field id>">...</select> block so two boolean fields on
// the same page cannot be confused with each other.
func selectedCustomFieldOption(t *testing.T, page, fieldID string) string {
	t.Helper()
	i := strings.Index(page, `id="cf-`+fieldID+`"`)
	if i < 0 {
		t.Fatalf("no widget rendered for field %s", fieldID)
	}
	end := strings.Index(page[i:], "</select>")
	if end < 0 {
		t.Fatalf("no closing </select> for field %s", fieldID)
	}
	block := page[i : i+end]
	m := regexp.MustCompile(`value="([^"]*)" selected`).FindStringSubmatch(block)
	if m == nil {
		t.Fatalf("no selected option drawn for field %s in:\n%s", fieldID, block)
	}
	return m[1]
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

// mustSetServiceValueViaHTTP is the service half of mustSetValueViaHTTP,
// posting to ServiceCustomFields's own route (POST /services/{id}/custom-
// fields) rather than reusing the asset helper against the wrong path.
func mustSetServiceValueViaHTTP(t *testing.T, h *harness, serviceID, fieldID, value string) {
	t.Helper()
	page := body(t, h.get("/services/"+serviceID, false))
	resp := h.post("/services/"+serviceID+"/custom-fields", url.Values{
		"csrf_token":    {h.csrfToken("/services/" + serviceID)},
		"row_version":   {versionInForm(t, page)},
		"cf_" + fieldID: {value},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("setting service custom value: got %d", resp.StatusCode)
	}
}
