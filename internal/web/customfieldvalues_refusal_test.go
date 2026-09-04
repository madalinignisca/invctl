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

// WP-A4 follow-up item 3: a refused "+42" used to echo back into
// <input type="number">, and the browser silently blanks it -- "+42" fails
// the WHATWG "valid floating-point number" grammar an <input type="number">
// applies to its own value attribute (never a leading '+'), so the
// operator's rejected text simply vanished, contradicting this repo's "your
// text is still here" rule. Same shape for <input type="date">. The fix:
// custom_fields_form.html falls back to a plain text input whenever THIS
// field is the one being re-rendered after a refusal ($.Edit.Err $name),
// regardless of whether the stored value would otherwise have been
// Representable -- the ordinary, unrefused path is untouched and keeps its
// native widget (see TestAValidNumberAndDateKeepTheirNativeWidget below).

// customFieldWidgetTag extracts the full opening <input ...> (or <select
// ...>) tag for one field, scoped by its "cf-<field id>" id attribute --
// the same anchoring widgetValueForField already uses, generalised to
// return the whole tag rather than just its value attribute, since this
// file needs to inspect `type=` too.
func customFieldWidgetTag(t *testing.T, page, fieldID string) string {
	t.Helper()
	i := strings.Index(page, `id="cf-`+fieldID+`"`)
	if i < 0 {
		t.Fatalf("no widget rendered for field %s", fieldID)
	}
	end := strings.Index(page[i:], ">")
	if end < 0 {
		t.Fatalf("no closing tag for field %s's widget", fieldID)
	}
	return page[i : i+end]
}

// TestARefusedNumberSurvivesReRenderingAsText is item 3's main case for
// `number`: the operator's own rejected text must still be visible, not
// silently dropped by the browser's own value-sanitisation algorithm.
func TestARefusedNumberSurvivesReRenderingAsText(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	assetID := h.refs.Assets["hv-01"]
	id := mustCreateFieldViaHTTP(t, h, "asset", "wp_a4_refused_num", "WPA4 Refused Num", "number")

	page := body(t, h.get("/assets/"+assetID, false))
	resp := h.post("/assets/"+assetID+"/custom-fields", url.Values{
		"csrf_token":  {h.csrfToken("/assets/" + assetID)},
		"row_version": {versionInForm(t, page)},
		"cf_" + id:    {"+42"},
	}, false)
	refused := body(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("submitting a leading-plus number: got %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}

	tag := customFieldWidgetTag(t, refused, id)
	if !strings.Contains(tag, `type="text"`) {
		t.Errorf("a refused number must fall back to a text input so the rejected text survives "+
			"rendering, got: %s", tag)
	}
	// html/template escapes "+" to "&#43;" in an attribute -- the same
	// reason TestAnUnrepresentableStoredValueRendersAsTextInstead and
	// h.csrfToken both already have to account for it.
	if !strings.Contains(tag, `value="&#43;42"`) {
		t.Errorf("the operator's rejected text %q must still be visible after the refusal, got: %s", "+42", tag)
	}
}

// TestARefusedDateSurvivesReRenderingAsText is the same shape for `date`:
// an unparseable date used to hit exactly the same fate through
// <input type="date">.
func TestARefusedDateSurvivesReRenderingAsText(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	assetID := h.refs.Assets["hv-01"]
	id := mustCreateFieldViaHTTP(t, h, "asset", "wp_a4_refused_date", "WPA4 Refused Date", "date")

	page := body(t, h.get("/assets/"+assetID, false))
	resp := h.post("/assets/"+assetID+"/custom-fields", url.Values{
		"csrf_token":  {h.csrfToken("/assets/" + assetID)},
		"row_version": {versionInForm(t, page)},
		"cf_" + id:    {"not-a-date"},
	}, false)
	refused := body(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("submitting an unparseable date: got %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}

	tag := customFieldWidgetTag(t, refused, id)
	if !strings.Contains(tag, `type="text"`) {
		t.Errorf("a refused date must fall back to a text input so the rejected text survives "+
			"rendering, got: %s", tag)
	}
	if !strings.Contains(tag, `value="not-a-date"`) {
		t.Errorf("the operator's rejected text %q must still be visible after the refusal, got: %s",
			"not-a-date", tag)
	}
}

// TestAValidNumberAndDateKeepTheirNativeWidget is the ordinary-path half of
// item 3: nothing about the refusal fallback may touch a field that was
// never refused. Losing the spinner and the date picker on every ordinary
// save to fix the refusal path would be a bad trade.
func TestAValidNumberAndDateKeepTheirNativeWidget(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	assetID := h.refs.Assets["hv-01"]

	numID := mustCreateFieldViaHTTP(t, h, "asset", "wp_a4_valid_num", "WPA4 Valid Num", "number")
	dateID := mustCreateFieldViaHTTP(t, h, "asset", "wp_a4_valid_date", "WPA4 Valid Date", "date")
	mustSetValueViaHTTP(t, h, assetID, numID, "42")
	mustSetValueViaHTTP(t, h, assetID, dateID, "2027-03-01")

	page := body(t, h.get("/assets/"+assetID, false))

	numTag := customFieldWidgetTag(t, page, numID)
	if !strings.Contains(numTag, `type="number"`) {
		t.Errorf("a valid, never-refused number must keep its native <input type=\"number\">, got: %s", numTag)
	}
	if !strings.Contains(numTag, `value="42"`) {
		t.Errorf("the valid number must still render its own value, got: %s", numTag)
	}

	dateTag := customFieldWidgetTag(t, page, dateID)
	if !strings.Contains(dateTag, `type="date"`) {
		t.Errorf("a valid, never-refused date must keep its native <input type=\"date\">, got: %s", dateTag)
	}
	if !strings.Contains(dateTag, `value="2027-03-01"`) {
		t.Errorf("the valid date must still render its own value, got: %s", dateTag)
	}
}
