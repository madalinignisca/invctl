// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"strings"
	"testing"
)

// WP-A4 follow-up item 1: custom_fields_show.html used to render a select
// field's stored VALUE verbatim -- which is the option's CODE ("p1"), not
// what design.md §3 gives every option as "what a reader sees" (its LABEL,
// "Priority One"). This file is that panel's own read view, proven
// separately from the editor's (which already renders the code correctly,
// since that is exactly what a submission needs to post back).

// showPanelValueForField extracts what custom_fields_show currently renders
// for the field named by label -- scoped to that field's own <dt>/<dd> pair
// so a neighbouring field's value can never satisfy an assertion meant for
// this one, the same anchoring customfieldvalues_test.go's editor
// assertions already use.
func showPanelValueForField(t *testing.T, page, label string) string {
	t.Helper()
	i := strings.Index(page, ">"+label+"<")
	if i < 0 {
		t.Fatalf("the show panel did not render a field labelled %q; this test would prove nothing", label)
	}
	rest := page[i:]
	const ddOpen = `<dd class="mono">`
	ddStart := strings.Index(rest, ddOpen)
	if ddStart < 0 {
		t.Fatalf("no <dd class=\"mono\"> follows the %q label", label)
	}
	rest = rest[ddStart+len(ddOpen):]
	ddEnd := strings.Index(rest, "</dd>")
	if ddEnd < 0 {
		t.Fatalf("no closing </dd> follows the %q label", label)
	}
	return rest[:ddEnd]
}

// TestCustomFieldsShowRendersASelectValuesLabelNotItsCode is item 1's main
// case: it must be impossible to pass this test without DisplayValue
// actually resolving a select value's code to its label.
func TestCustomFieldsShowRendersASelectValuesLabelNotItsCode(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	assetID := h.refs.Assets["hv-01"]

	id := mustCreateFieldViaHTTP(t, h, "asset", "wp_a4_priority", "WPA4 Priority", "select")
	mustSetCustomFieldOptions(t, h, id, []string{"p1"}, []string{"Priority One"})
	mustSetValueViaHTTP(t, h, assetID, id, "p1")

	page := body(t, h.get("/assets/"+assetID, false))
	got := showPanelValueForField(t, page, "WPA4 Priority")
	if got != "Priority One" {
		t.Errorf("show panel rendered %q for a select value, want the option's LABEL %q -- "+
			"the stored code %q is what the FORM posts back, not what a reader should be shown",
			got, "Priority One", "p1")
	}
}

// TestCustomFieldsShowStillRendersALabelForARetiredOption is
// docs/custom-fields-design.md §3's "a value already set keeps displaying",
// applied to the label fix: a value naming an option retired since it was
// set must not fall back to the bare code, and must not disappear.
func TestCustomFieldsShowStillRendersALabelForARetiredOption(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	assetID := h.refs.Assets["hv-02"]

	id := mustCreateFieldViaHTTP(t, h, "asset", "wp_a4_tier", "WPA4 Tier", "select")
	mustSetCustomFieldOptions(t, h, id, []string{"gold", "silver"}, []string{"Gold", "Silver"})
	mustSetValueViaHTTP(t, h, assetID, id, "silver")

	// Retire "silver" by offering only "gold" from here on.
	mustSetCustomFieldOptions(t, h, id, []string{"gold"}, []string{"Gold"})

	page := body(t, h.get("/assets/"+assetID, false))
	got := showPanelValueForField(t, page, "WPA4 Tier")
	if got != "Silver" {
		t.Errorf("a value naming a since-retired option rendered %q, want its label %q still -- "+
			"design.md §3 says a value already set keeps displaying", got, "Silver")
	}
}

// TestCustomFieldsShowNonSelectValuesAreUnaffected proves the label
// resolution is scoped to `select` alone: every other kind's show-panel
// value is still exactly what CustomValuesFor returned, untouched.
func TestCustomFieldsShowNonSelectValuesAreUnaffected(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	assetID := h.refs.Assets["hv-01"]

	cases := []struct{ code, label, kind, value string }{
		{"wp_a4_notes", "WPA4 Notes", "text", "ABC-9"},
		{"wp_a4_watts", "WPA4 Watts", "number", "42.5"},
		{"wp_a4_installed", "WPA4 Installed", "date", "2027-03-01"},
		{"wp_a4_flag", "WPA4 Flag", "boolean", "true"},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			id := mustCreateFieldViaHTTP(t, h, "asset", c.code, c.label, c.kind)
			mustSetValueViaHTTP(t, h, assetID, id, c.value)

			page := body(t, h.get("/assets/"+assetID, false))
			got := showPanelValueForField(t, page, c.label)
			if got != c.value {
				t.Errorf("a %s value rendered %q in the show panel, want the stored value %q "+
					"verbatim -- the select label fix must not touch any other kind", c.kind, got, c.value)
			}
		})
	}
}
