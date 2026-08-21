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

// FINAL REVIEW B1 (BLOCKING): a value the store accepts must survive a round
// trip through its own form widget unchanged. Two independent bugs violated
// this before the fix, both with the same shape -- the store accepted a value
// its rendering widget could not represent, so the browser silently drew a
// blank, and the next unrelated save on that entity posted the blank back as
// an explicit clear:
//
//   - a select value naming an option that has since been retired: the form
//     offered only LIVE options, so nothing matched the stored value and the
//     browser fell back to its own blank "not set" choice.
//   - a number value like ".5" or "5.": CanonicalCustomValue's isDecimalNumber
//     accepted it, but it is not a valid floating-point number under the
//     HTML value-sanitisation algorithm an <input type="number"> applies, so
//     the browser renders the widget EMPTY.
//
// This is written as a property over every kind, not as two case-specific
// patches -- that is what stops a ninth instance the next time a kind's
// validator and its widget's grammar quietly diverge.
func TestEveryStoredCustomValueSurvivesARoundTripThroughItsWidget(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	assetID := h.refs.Assets["hv-01"]

	cases := []struct {
		name, code, kind, value string
	}{
		{"text", "rt_text", "text", "ABC-1234"},
		{"a whole number", "rt_num_whole", "number", "42"},
		{"a decimal", "rt_num_decimal", "number", "42.50"},
		{"a negative", "rt_num_neg", "number", "-7"},
		{"a real date", "rt_date", "date", "2027-03-01"},
		{"boolean true", "rt_bool_true", "boolean", "true"},
		{"boolean false", "rt_bool_false", "boolean", "false"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := mustCreateFieldViaHTTP(t, h, "asset", c.code, c.name, c.kind)
			mustSetValueViaHTTP(t, h, assetID, id, c.value)

			page := body(t, h.get("/assets/"+assetID, false))
			got := widgetValueForField(t, page, id, c.kind)
			if got != c.value {
				t.Errorf("stored %q for kind %s, the widget round-tripped %q -- "+
					"the store accepted a value its own widget cannot represent",
					c.value, c.kind, got)
			}
		})
	}

	// THE .5 CASE, run separately because it has two equally valid closures
	// (design.md, as amended, allows either): CanonicalCustomValue may refuse
	// an unrepresentable number outright, in which case there is nothing to
	// round-trip and the 422 IS the closure; or it may be accepted, in which
	// case the stored text must be exactly what an <input type="number">
	// renders back, simulating the browser's own value-sanitisation algorithm
	// rather than trusting the server's literal `value="..."` attribute --
	// the server has no browser to disagree with it.
	unrepresentable := []struct{ name, code, value string }{
		{"a leading decimal point", "rt_num_leading_dot", ".5"},
		{"a trailing decimal point", "rt_num_trailing_dot", "5."},
		{"an explicit plus sign", "rt_num_plus", "+42"},
	}
	for _, c := range unrepresentable {
		t.Run(c.name, func(t *testing.T) {
			id := mustCreateFieldViaHTTP(t, h, "asset", c.code, c.name, "number")
			page := body(t, h.get("/assets/"+assetID, false))
			resp := h.post("/assets/"+assetID+"/custom-fields", url.Values{
				"csrf_token":  {h.csrfToken("/assets/" + assetID)},
				"row_version": {versionInForm(t, page)},
				"cf_" + id:    {c.value},
			}, false)
			resp.Body.Close()
			if resp.StatusCode == http.StatusUnprocessableEntity {
				return // refused outright: nothing was stored, nothing to round-trip
			}
			if resp.StatusCode != http.StatusSeeOther {
				t.Fatalf("setting %q: got %d, want 303 or 422", c.value, resp.StatusCode)
			}
			after := body(t, h.get("/assets/"+assetID, false))
			got := widgetValueForField(t, after, id, "number")
			sanitised := sanitiseHTMLNumberWidgetValue(got)
			if sanitised != c.value {
				t.Errorf("the store accepted %q as a number, but a real <input type=\"number\"> "+
					"cannot represent it: its own value-sanitisation algorithm renders it back as %q, "+
					"which is what an unrelated save on this entity would silently resubmit as a clear",
					c.value, sanitised)
			}
		})
	}

	// The B1 reproduction, verbatim: set a select value, retire the option it
	// names, and confirm the value editor still offers it, selected, so the
	// operator sees exactly what the next save will resubmit.
	t.Run("select value on an option retired after it was set", func(t *testing.T) {
		id := mustCreateFieldViaHTTP(t, h, "asset", "rt_tier", "Tier", "select")
		mustSetCustomFieldOptions(t, h, id, []string{"gold", "silver"}, []string{"Gold", "Silver"})
		mustSetValueViaHTTP(t, h, assetID, id, "silver")

		// Retire "silver" by offering only "gold".
		mustSetCustomFieldOptions(t, h, id, []string{"gold"}, []string{"Gold"})

		page := body(t, h.get("/assets/"+assetID, false))
		got := widgetValueForField(t, page, id, "select")
		if got != "silver" {
			t.Errorf("stored value %q on a since-retired option did not round-trip through "+
				"the select widget: got %q -- resubmitting the form as drawn would clear it", "silver", got)
		}
	})
}

// widgetValueForField extracts what one field's own widget currently carries
// as its value, scoped to that field's own element the way
// selectedCustomFieldOption already scopes a <select> -- so two fields on the
// same page can never be confused with each other.
func widgetValueForField(t *testing.T, page, fieldID, kind string) string {
	t.Helper()
	switch kind {
	case "boolean", "select":
		return selectedCustomFieldOption(t, page, fieldID)
	default:
		i := strings.Index(page, `id="cf-`+fieldID+`"`)
		if i < 0 {
			t.Fatalf("no widget rendered for field %s", fieldID)
		}
		end := strings.Index(page[i:], ">")
		if end < 0 {
			t.Fatalf("no closing tag for field %s's widget", fieldID)
		}
		tag := page[i : i+end]
		m := regexp.MustCompile(`\bvalue="([^"]*)"`).FindStringSubmatch(tag)
		if m == nil {
			t.Fatalf("no value attribute on field %s's widget:\n%s", fieldID, tag)
		}
		return m[1]
	}
}

// sanitiseHTMLNumberWidgetValue reproduces the outcome of the HTML "algorithm
// for converting a string to a number" that a real browser applies to an
// <input type="number">'s value attribute on render: a string that is not a
// "valid floating-point number" per the WHATWG grammar renders as an EMPTY
// widget, not as the string typed. https://html.spec.whatwg.org/#number-state
// (the grammar, restated): optional "-" (never "+"), one or more digits,
// optionally "." followed by one or more digits, optionally an exponent. No
// browser is invoked here -- this is the same grammar, hand-rolled, the way
// this codebase prefers a small direct implementation over a dependency for
// a rule this narrow.
func sanitiseHTMLNumberWidgetValue(s string) string {
	i := 0
	if i < len(s) && s[i] == '-' {
		i++
	}
	start := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == start {
		return ""
	}
	if i < len(s) && s[i] == '.' {
		i++
		fracStart := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == fracStart {
			return ""
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		j := i + 1
		if j < len(s) && (s[j] == '-' || s[j] == '+') {
			j++
		}
		expStart := j
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j == expStart {
			return "" // "e" with no exponent digits is not valid either
		}
		i = j
	}
	if i != len(s) {
		return ""
	}
	return s
}

// mustSetCustomFieldOptions replaces a select field's live option vocabulary
// through the real handler, carrying the field's own row_version off the
// registry page the way an operator's browser would.
func mustSetCustomFieldOptions(t *testing.T, h *harness, fieldID string, values, labels []string) {
	t.Helper()
	page := body(t, h.get("/custom-fields?options="+fieldID, false))
	form := url.Values{
		"csrf_token":  {h.csrfToken("/custom-fields")},
		"row_version": {versionInForm(t, page)},
	}
	form["option_value"] = values
	form["option_label"] = labels
	resp := h.post("/custom-fields/"+fieldID+"/options", form, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("setting options of field %s: got %d", fieldID, resp.StatusCode)
	}
}
