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

// The registry, docs/custom-fields-design.md §4: the page that answers "did
// invctl ship this field, or did someone here add it?" without a phone call
// to the vendor.

func TestTheRegistryShowsWhoDefinedAFieldAndWhy(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	form := url.Values{
		"csrf_token":  {h.csrfToken("/custom-fields")},
		"entity_type": {"asset"}, "code": {"cost_centre"},
		"label": {"Cost Centre"}, "kind": {"text"},
		"description": {"SAP cost centre finance rebills against"},
	}
	if code := h.post("/custom-fields", form, false).StatusCode; code != http.StatusSeeOther {
		t.Fatalf("creating: got %d", code)
	}
	page := body(t, h.get("/custom-fields", false))
	for _, want := range []string{
		"Cost Centre",
		"SAP cost centre finance rebills against",
		"admin", // the display name, resolved from the opaque created_by
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the registry must show %q -- it is what a new hire reads "+
				"instead of telephoning the vendor", want)
		}
	}
}

// TestDefiningACustomFieldIsAdminOnly is FINAL REVIEW F2's rename: this test
// only ever exercised POST /custom-fields, so its old name --
// "TestTheRegistryIsAdminOnly" -- asserted something neither the test nor
// the code does. The registry itself (GET /custom-fields) ships as read()
// and is reachable by any authenticated user, viewer included -- see
// TestTheRegistryIsReadableByAnyAuthenticatedUser below for the coverage
// that actually was missing. Only the mutation this test exercises sits
// behind RequireAdmin.
func TestDefiningACustomFieldIsAdminOnly(t *testing.T) {
	h := newHarness(t)
	h.login("viewer", "viewer-password")
	form := url.Values{
		"csrf_token":  {h.csrfToken("/custom-fields")},
		"entity_type": {"asset"}, "code": {"sneaky"}, "label": {"Sneaky"},
		"kind": {"text"}, "description": {"should never be created"},
	}
	resp := h.post("/custom-fields", form, false)
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusSeeOther {
		t.Fatalf("a viewer must not be able to define a custom field (%d)", resp.StatusCode)
	}
}

// TestTheRegistryIsReadableByAnyAuthenticatedUser is the coverage the old
// name above claimed but never had: docs/custom-fields-design.md §4, as
// corrected -- the support-burden goal this whole feature exists for is
// served by a read-only user being able to open the registry and see who
// defined a field and why, so only the mutating routes sit behind
// RequireAdmin.
func TestTheRegistryIsReadableByAnyAuthenticatedUser(t *testing.T) {
	h := newHarness(t)
	h.login("viewer", "viewer-password")
	resp := h.get("/custom-fields", false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a viewer could not open the custom fields registry: got %d", resp.StatusCode)
	}
}

func TestAFieldWithNoDescriptionIsRefusedWith422(t *testing.T) {
	// Validation failure re-renders the form partial with error state and
	// returns 422 -- never a 200 with the error buried in the body.
	h := newHarness(t)
	h.login("admin", "admin-password")
	form := url.Values{
		"csrf_token":  {h.csrfToken("/custom-fields")},
		"entity_type": {"asset"}, "code": {"nameless"}, "label": {"Nameless"},
		"kind": {"text"}, "description": {"  "},
	}
	resp := h.post("/custom-fields", form, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422", resp.StatusCode)
	}
	if !strings.Contains(body(t, resp), "description") {
		t.Error("the re-rendered form must say which field was rejected")
	}
}

// TestTheCreationFormWarnsThatCustomValuesArePermanentAndMustNotHoldPII is
// Ruling AD's gate. A custom value's text enters change_log permanently and
// redaction is all-or-nothing across every custom field on every entity
// (domain.IsRedacted is keyed by column, and the fold is one opaque key,
// "custom_fields"), so this is the one moment a warning can still change
// anybody's mind: while an administrator is naming the field, not in a help
// page and not behind a tooltip they must hover.
func TestTheCreationFormWarnsThatCustomValuesArePermanentAndMustNotHoldPII(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	page := body(t, h.get("/custom-fields", false))
	// All three clauses, asserted separately: Ruling AH found that asserting
	// only two of the warning's three claims leaves a tripwire that keeps
	// passing if the third -- "cannot be removed" -- is deleted from the
	// template while the other two survive.
	for _, want := range []string{
		"recorded permanently in the audit trail",
		"cannot be removed",
		"must not hold personal data",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the field-creation form does not warn %q", want)
		}
	}
}

func TestARetiredFieldOffersRestoreAndKeepsItsValues(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	create := url.Values{
		"csrf_token":  {h.csrfToken("/custom-fields")},
		"entity_type": {"asset"}, "code": {"retire_me"}, "label": {"Retire Me"},
		"kind": {"text"}, "description": {"a field this test retires"},
	}
	h.post("/custom-fields", create, false).Body.Close()

	page := body(t, h.get("/custom-fields", false))
	id := firstFieldIDFor(t, page, "retire_me")

	retire := h.post("/custom-fields/"+id+"/retire",
		url.Values{"csrf_token": {h.csrfToken("/custom-fields")}}, false)
	retire.Body.Close()
	if retire.StatusCode != http.StatusSeeOther {
		t.Fatalf("retiring returned %d", retire.StatusCode)
	}

	afterRetire := body(t, h.get("/custom-fields", false))
	i := strings.Index(afterRetire, "Retire Me")
	if i < 0 {
		t.Fatal("a retired field disappeared from the registry entirely -- it must appear in its own section")
	}
	// Scoped to this row's own markup, not the whole page: the logged-in
	// user is also named "admin" and appears in the page chrome regardless
	// of whether RetiredByName resolution works at all.
	row := afterRetire[i:]
	if j := strings.Index(row, "</tr>"); j >= 0 {
		row = row[:j]
	}
	if !strings.Contains(row, "admin") {
		t.Errorf("the retired row does not name who retired it: %s", row)
	}

	restore := h.post("/custom-fields/"+id+"/restore",
		url.Values{"csrf_token": {h.csrfToken("/custom-fields")}}, false)
	restore.Body.Close()
	if restore.StatusCode != http.StatusSeeOther {
		t.Fatalf("restoring returned %d", restore.StatusCode)
	}

	afterRestore := body(t, h.get("/custom-fields", false))
	if !strings.Contains(afterRestore, "Retire Me") {
		t.Error("a restored field did not come back")
	}
}

// TestAStaleOptionsSubmissionIsRefusedWith409 is Ruling W at the HTTP layer:
// the options editor carries the field's own row_version, and a second save
// from one token must be refused rather than silently un-retiring or
// re-retiring an option neither administrator saw.
func TestAStaleOptionsSubmissionIsRefusedWith409(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	create := url.Values{
		"csrf_token":  {h.csrfToken("/custom-fields")},
		"entity_type": {"asset"}, "code": {"tier"}, "label": {"Tier"},
		"kind": {"select"}, "description": {"a field this test manages options on"},
	}
	h.post("/custom-fields", create, false).Body.Close()
	id := firstFieldIDFor(t, body(t, h.get("/custom-fields", false)), "tier")

	openPage := body(t, h.get("/custom-fields?options="+id, false))
	stale := versionInForm(t, openPage)

	submit := func(version string) *http.Response {
		return h.post("/custom-fields/"+id+"/options", url.Values{
			"csrf_token": {h.csrfToken("/custom-fields")}, "row_version": {version},
			"option_value": {"gold"}, "option_label": {"Gold"},
		}, false)
	}

	first := submit(stale)
	first.Body.Close()
	if first.StatusCode != http.StatusSeeOther {
		t.Fatalf("the first options save returned %d", first.StatusCode)
	}

	second := submit(stale)
	page := body(t, second)
	if second.StatusCode != http.StatusConflict {
		t.Errorf("a stale options submission returned %d, want 409", second.StatusCode)
	}
	if !strings.Contains(page, "somebody else changed this") {
		t.Errorf("a stale options write was not explained as one: %s", page)
	}
}

// TestTheOptionsEditorDrawsExistingOptionsNotBlanks is Ruling AF: the render
// path must populate a field's live options before the options editor draws
// them, or an admin who opens the editor on a field that already has options
// and clicks Save without retyping every one silently retires them all. The
// submission side was already correct -- the bug was entirely in what the
// form showed, the converse half of design.md §6's contract.
func TestTheOptionsEditorDrawsExistingOptionsNotBlanks(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id := mustCreateCustomField(t, h, "asset", "tier", "select")
	first := h.post("/custom-fields/"+id+"/options", url.Values{
		"csrf_token":   {h.csrfToken("/custom-fields")},
		"row_version":  {versionInForm(t, body(t, h.get("/custom-fields?options="+id, false)))},
		"option_value": {"gold"}, "option_label": {"Gold"},
	}, false)
	first.Body.Close()
	if first.StatusCode != http.StatusSeeOther {
		t.Fatalf("seeding the gold option returned %d", first.StatusCode)
	}

	reopened := body(t, h.get("/custom-fields?options="+id, false))
	if !strings.Contains(reopened, `name="option_value" value="gold"`) {
		t.Errorf("reopening the options editor on a field that already has an option "+
			"drew a blank instead of its stored value:\n%s", reopened)
	}
	if !strings.Contains(reopened, `name="option_label" value="Gold"`) {
		t.Errorf("reopening the options editor did not draw the option's stored label:\n%s", reopened)
	}
}

// TestAnOversizedOrControlCharacterOptionIsRefusedWith422 is FINAL REVIEW AY
// at the HTTP layer: an option's value and label must obey the same bounds a
// `text` custom value does, and the refusal must come back as the styled
// per-field 422 every sibling refusal in this handler produces -- never
// err.Error() echoed verbatim, which is how a raw driver error would have
// reached the browser for the same input against PostgreSQL.
func TestAnOversizedOrControlCharacterOptionIsRefusedWith422(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	id := mustCreateCustomField(t, h, "asset", "tier", "select")

	submit := func(value, label string) *http.Response {
		return h.post("/custom-fields/"+id+"/options", url.Values{
			"csrf_token":   {h.csrfToken("/custom-fields")},
			"row_version":  {versionInForm(t, body(t, h.get("/custom-fields?options="+id, false)))},
			"option_value": {value}, "option_label": {label},
		}, false)
	}

	oversized := strings.Repeat("a", 600)
	resp := submit(oversized, "Gold")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an oversized option value: got %d, want 422", resp.StatusCode)
	}
	resp.Body.Close()

	withNUL := "gold\x00"
	resp = submit(withNUL, "Gold")
	page := body(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an option value with an embedded NUL byte: got %d, want 422", resp.StatusCode)
	}
	if strings.Contains(page, "invalid byte sequence") || strings.Contains(page, "SQLSTATE") {
		t.Errorf("a raw driver error reached the browser: %s", page)
	}
}

// firstFieldIDFor finds the id of the edit link for one field's code on the
// registry page. Custom fields carry no detail page of their own, so this is
// how a test gets from "the field it just created" to its id, the way
// firstEditID does for every other resource in this suite.
func firstFieldIDFor(t *testing.T, page, code string) string {
	t.Helper()
	i := strings.Index(page, ">"+code+"<")
	if i < 0 {
		t.Fatalf("the registry does not list a field with code %q", code)
	}
	return firstEditID(t, page[i:])
}

// mustCreateCustomField defines a field through the real handler and returns
// its id, for tests that need one to already exist -- exercising the same
// path a form submission does rather than reaching into the store.
func mustCreateCustomField(t *testing.T, h *harness, entityType, code, kind string) string {
	t.Helper()
	form := url.Values{
		"csrf_token":  {h.csrfToken("/custom-fields")},
		"entity_type": {entityType}, "code": {code}, "label": {code},
		"kind": {kind}, "description": {"a fixture field for the web test suite"},
	}
	resp := h.post("/custom-fields", form, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("defining custom field %s: got %d", code, resp.StatusCode)
	}
	return firstFieldIDFor(t, body(t, h.get("/custom-fields", false)), code)
}
