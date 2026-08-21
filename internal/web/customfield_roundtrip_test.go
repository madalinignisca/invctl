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
// trip through its own form widget unchanged. Three independent bugs have
// now violated this, all with the same shape -- the store accepted a value
// its rendering widget could not represent, so the browser silently drew a
// blank, and the next unrelated save on that entity posted the blank back as
// an explicit clear:
//
//   - a select value naming an option that has since been retired: the form
//     offered only LIVE options, so nothing matched the stored value and the
//     browser fell back to its own blank "not set" choice.
//   - a number value like "5." or "+42": neither is a valid floating-point
//     number under the HTML value-sanitisation algorithm an
//     <input type="number"> applies, so the browser renders the widget
//     EMPTY. (".5", found in the same round, is a DIFFERENT case: the
//     WHATWG grammar explicitly allows the integer part to be absent when a
//     fraction part follows, so ".5" IS valid and DOES round-trip -- see
//     isDecimalNumber's comment for the history of getting this backwards.)
//   - a date value of "0000-01-01": a real calendar date as far as Go's
//     time.Parse is concerned, but HTML's valid-date-string grammar
//     requires a year greater than zero, so <input type="date"> renders it
//     EMPTY too.
//
// This is written as a property over every kind, not as a set of
// case-specific patches -- that is what stops a tenth instance the next
// time a kind's validator and its widget's grammar quietly diverge.
func TestEveryStoredCustomValueSurvivesARoundTripThroughItsWidget(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	assetID := h.refs.Assets["hv-01"]

	// LOAD-BEARING VS CLOSED BY CONSTRUCTION. The two loops below do
	// different jobs and carry different amounts of the test's power.
	//
	// `text` and `boolean` are CLOSED BY CONSTRUCTION: CanonicalCustomValue's
	// `boolean` branch has exactly two successful exits, "true" and "false",
	// full stop -- there is no third string it can ever hand a widget that
	// a three-state <select> cannot already represent. `text` is plain text:
	// an <input type="text"> applies no sanitisation algorithm to its value
	// at all, so anything customTextBounds accepts renders back
	// byte-for-byte. A bare equality check against what was submitted is
	// already a COMPLETE proof for these two kinds, and the cases loop below
	// checks exactly that.
	//
	// `number`, `date` and `select` are LOAD-BEARING: each has its own
	// widget-side rule -- a sanitisation algorithm for `number` and `date`,
	// an offering rule for `select` -- that is a SEPARATE specification from
	// the domain validator, and it is exactly the gap between those two
	// specifications that produced every B1 instance found so far: the
	// retired select option, "5."/"+42", and year "0000". The edgeCases loop
	// further down checks `number` and `date` against an INDEPENDENT model
	// of their widget's own grammar rather than trusting the server's
	// literal rendered attribute; the dedicated subtest at the bottom does
	// the same for `select`. Anyone adding a sixth kind should work out
	// which side of this line it falls on before assuming a plain equality
	// check is enough.
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

	// EDGE CASES for the two kinds whose widget applies its own
	// sanitisation algorithm on render, `number` and `date`: each has two
	// equally valid closures. CanonicalCustomValue may refuse an
	// unrepresentable value outright, in which case there is nothing to
	// round-trip and the 422 IS the closure (this is the path this branch
	// currently takes for every case below); or it may be accepted, in
	// which case the stored text must be exactly what the widget renders
	// back, checked against an INDEPENDENT model of the widget's grammar --
	// widgetGrammar, below -- rather than trusting the server's own literal
	// `value="..."` attribute, which has no browser to disagree with it.
	//
	// ".5" and "1e3" are included here even though both are ACCEPTED
	// (neither is refused): the WHATWG grammar's "one or both" clause and
	// its exponent form are exactly the kind of easy-to-get-backwards rule
	// that produced the first correction of this test being wrong in the
	// opposite direction -- checking them against the independent oracle
	// here is what would have caught that.
	edgeCases := []struct{ name, code, kind, value string }{
		{"a leading decimal point (WHATWG allows an absent integer part)",
			"rt_num_leading_dot", "number", ".5"},
		{"an exponent form", "rt_num_exponent", "number", "1e3"},
		{"a trailing decimal point", "rt_num_trailing_dot", "number", "5."},
		{"an explicit plus sign", "rt_num_plus", "number", "+42"},
		{"year 0000", "rt_date_zero_year", "date", "0000-01-01"},
	}
	for _, c := range edgeCases {
		t.Run(c.name, func(t *testing.T) {
			id := mustCreateFieldViaHTTP(t, h, "asset", c.code, c.name, c.kind)
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
				t.Fatalf("setting %q for kind %s: got %d, want 303 or 422", c.value, c.kind, resp.StatusCode)
			}
			after := body(t, h.get("/assets/"+assetID, false))
			got := widgetValueForField(t, after, id, c.kind)
			sanitise := widgetGrammar(c.kind)
			sanitised := sanitise(got)
			if sanitised != c.value {
				t.Errorf("the store accepted %q for kind %s, but its own widget cannot represent it: "+
					"an independent model of the widget's own rendering grammar renders it back as %q, "+
					"which is what an unrelated save on this entity would silently resubmit as a clear",
					c.value, c.kind, sanitised)
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

// widgetGrammar returns the INDEPENDENT oracle for one kind's widget-side
// rendering grammar, or nil for a kind edgeCases never exercises.
func widgetGrammar(kind string) func(string) string {
	switch kind {
	case "number":
		return sanitiseHTMLNumberWidgetValue
	case "date":
		return sanitiseHTMLDateWidgetValue
	default:
		return nil
	}
}

// sanitiseHTMLNumberWidgetValue reproduces the outcome of the HTML "algorithm
// for converting a string to a number" that a real browser applies to an
// <input type="number">'s value attribute on render: a string that is not a
// "valid floating-point number" per the WHATWG grammar renders as an EMPTY
// widget, not as the string typed. https://html.spec.whatwg.org/#number-state
//
// THE GRAMMAR, RESTATED PRECISELY BECAUSE GETTING THIS WRONG IS WHAT B1's
// FIRST CORRECTION DID: optional "-" (never "+"), then ONE OR BOTH of (a
// series of digits) and ("." followed by a series of digits) -- the integer
// part is OPTIONAL when a fraction part is present, so ".5" IS valid --
// then an optional exponent ("e"/"E", an optional sign, a series of
// digits), so "1e3" IS valid too. No browser is invoked here -- this is the
// grammar, hand-rolled, the way this codebase prefers a small direct
// implementation over a dependency for a rule this narrow.
//
// MUST STAY INDEPENDENT OF internal/domain.isDecimalNumber, AND THIS IS NOT
// A STYLE PREFERENCE. This function is the test's ORACLE for "what would a
// browser actually render back", and isDecimalNumber is the thing under
// test's answer to "may this be stored". If a future refactor extracts one
// shared implementation and calls it from both, TestEveryStoredCustomValue-
// SurvivesARoundTripThroughItsWidget becomes tautological: the store's
// validator and the test's oracle would always agree with EACH OTHER,
// including when they are both wrong, and the test could never again catch
// a divergence between them -- which is the entire reason this test exists.
// Do not "clean this up" into one function. Two independently written
// implementations of the same public grammar, and the fact that they agree,
// is the proof.
func sanitiseHTMLNumberWidgetValue(s string) string {
	if htmlValidFloatRe.MatchString(s) {
		return s
	}
	return ""
}

// htmlValidFloatRe is a direct transcription of the WHATWG grammar restated
// on sanitiseHTMLNumberWidgetValue's comment, written as a regular
// expression rather than a hand-scanned state machine -- a genuinely
// different implementation technique from isDecimalNumber's, not merely a
// renamed copy of it. See that function's comment for why the two must
// never converge.
var htmlValidFloatRe = regexp.MustCompile(`^-?([0-9]+(\.[0-9]+)?|\.[0-9]+)([eE][-+]?[0-9]+)?$`)

// sanitiseHTMLDateWidgetValue reproduces the outcome of the HTML "algorithm
// to parse a date string" applied to an <input type="date">'s value
// attribute on render. https://html.spec.whatwg.org/#valid-date-string --
// the part that matters here is that the year component must represent a
// number GREATER THAN ZERO. "0000-01-01" is a real calendar date as far as
// Go's time.Parse is concerned (year 0 is a valid *year*, it is simply not
// a valid HTML date-string *year component*), so the domain package's own
// "is this a real calendar date" check cannot see this failure -- only a
// model of the WIDGET's own grammar can, which is what this function is.
//
// MUST STAY INDEPENDENT of internal/domain's date validation, for the exact
// reason given on sanitiseHTMLNumberWidgetValue: it is the test's oracle,
// not a second call site for the thing under test.
func sanitiseHTMLDateWidgetValue(s string) string {
	m := htmlValidDateRe.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	allZero := true
	for _, r := range m[1] {
		if r != '0' {
			allZero = false
			break
		}
	}
	if allZero {
		return ""
	}
	return s
}

// htmlValidDateRe matches YYYY-MM-DD shape only -- month/day RANGE
// validity (a real 30th of February, a real leap day) is not this
// function's job. Every value edgeCases feeds it has already passed
// CanonicalCustomValue's own real-calendar-date check by the time it gets
// here; the only gap this oracle exists to catch is the year-zero one.
var htmlValidDateRe = regexp.MustCompile(`^([0-9]{4,})-([0-9]{2})-([0-9]{2})$`)

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
