// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import (
	"strings"
	"testing"
	"time"
)

var customFieldNow = time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

func TestACustomFieldRefusesAnEmptyDescription(t *testing.T) {
	// The description is what a new hire reads instead of telephoning the
	// vendor. An administrator who cannot say why a field exists is the
	// origin of that call, and creation is the cheapest moment to ask.
	_, err := NewCustomField("id", CustomFieldEntityAsset, "cost_centre",
		"Cost Centre", CustomFieldText, "   ", "user-1", "team-1", customFieldNow)
	if err == nil {
		t.Fatal("a field with no description must be refused")
	}
}

// TestACustomFieldRefusesAnEmptyOwnerTeam is the senior review's own finding,
// made concrete: "who defined this" (created_by) is the wrong answer to "who
// do I ask" the moment that person leaves, and owner_team_id is required with
// no escape hatch precisely so a new field cannot join the eleven
// pre-existing ones a migration could not backfill an owner for.
func TestACustomFieldRefusesAnEmptyOwnerTeam(t *testing.T) {
	_, err := NewCustomField("id", CustomFieldEntityAsset, "cost_centre",
		"Cost Centre", CustomFieldText, "SAP cost centre", "user-1", "   ", customFieldNow)
	if err == nil {
		t.Fatal("a field with no owner team must be refused")
	}
	ve, ok := AsValidation(err)
	if !ok {
		t.Fatalf("got %v, want a ValidationError naming owner_team_id", err)
	}
	found := false
	for _, f := range ve.Fields {
		if f.Field == "owner_team_id" {
			found = true
		}
	}
	if !found {
		t.Fatalf("validation error %v does not name owner_team_id", ve.Fields)
	}
}

// TestUpdatingACustomFieldAlsoRequiresAnOwnerTeam: Validate (the shared path
// UpdateCustomField's own test suite exercises) refuses an empty owner the
// same way construction does -- an administrator correcting one of the
// eleven pre-existing orphans must set an owner in the same edit, not leave
// it unassigned forever because the requirement was only ever a creation-time
// check.
func TestUpdatingACustomFieldAlsoRequiresAnOwnerTeam(t *testing.T) {
	f, err := NewCustomField("id", CustomFieldEntityAsset, "cost_centre",
		"Cost Centre", CustomFieldText, "SAP cost centre", "user-1", "team-1", customFieldNow)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	f.OwnerTeamID = nil
	if err := f.Validate(); err == nil {
		t.Fatal("clearing the owner team on an edit must be refused")
	}
}

func TestACustomFieldCodeIsLowerCased(t *testing.T) {
	f, err := NewCustomField("id", CustomFieldEntityAsset, "Cost_Centre",
		"Cost Centre", CustomFieldText, "SAP cost centre", "user-1", "team-1", customFieldNow)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	if f.Code != "cost_centre" {
		t.Fatalf("got code %q, want cost_centre", f.Code)
	}
}

func TestACustomFieldRefusesACodeWithASpace(t *testing.T) {
	// code participates in a unique index and surfaces in HTML form field
	// names and CSV headers, where a space is a mess.
	_, err := NewCustomField("id", CustomFieldEntityAsset, "cost centre",
		"Cost Centre", CustomFieldText, "SAP cost centre", "user-1", "team-1", customFieldNow)
	if err == nil {
		t.Fatal("a code containing a space must be refused")
	}
}

func TestACustomFieldRefusesAnUnknownEntityType(t *testing.T) {
	_, err := NewCustomField("id", "network", "cost_centre",
		"Cost Centre", CustomFieldText, "SAP cost centre", "user-1", "team-1", customFieldNow)
	if err == nil {
		t.Fatal("an unknown entity type must be refused")
	}
}

func TestACustomFieldRefusesAnUnknownKind(t *testing.T) {
	_, err := NewCustomField("id", CustomFieldEntityAsset, "cost_centre",
		"Cost Centre", "colour", "SAP cost centre", "user-1", "team-1", customFieldNow)
	if err == nil {
		t.Fatal("an unknown kind must be refused")
	}
}

func TestACustomFieldStampsItsCreatedAtFromTheClockParameter(t *testing.T) {
	f, err := NewCustomField("id", CustomFieldEntityAsset, "cost_centre",
		"Cost Centre", CustomFieldText, "SAP cost centre", "user-1", "team-1", customFieldNow)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	if f.CreatedAt != FormatTime(customFieldNow) {
		t.Fatalf("got created_at %q, want %q", f.CreatedAt, FormatTime(customFieldNow))
	}
}

func TestANewCustomFieldIsNotRetired(t *testing.T) {
	f, err := NewCustomField("id", CustomFieldEntityAsset, "cost_centre",
		"Cost Centre", CustomFieldText, "SAP cost centre", "user-1", "team-1", customFieldNow)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	if f.IsRetired() {
		t.Fatal("a freshly created field must not report itself retired")
	}
}

func TestARetiredCustomFieldReportsItself(t *testing.T) {
	f, err := NewCustomField("id", CustomFieldEntityAsset, "cost_centre",
		"Cost Centre", CustomFieldText, "SAP cost centre", "user-1", "team-1", customFieldNow)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	retiredAt := FormatTime(customFieldNow)
	f.RetiredAt = &retiredAt
	if !f.IsRetired() {
		t.Fatal("a field with retired_at set must report itself retired")
	}
}

func TestCanonicalCustomValue(t *testing.T) {
	cases := []struct {
		name, kind, raw string
		options         []string
		want            string
		wantErr         bool
	}{
		{"text is trimmed", CustomFieldText, "  ABC-1234 ", nil, "ABC-1234", false},
		{"empty text is refused", CustomFieldText, "   ", nil, "", true},
		{"a whole number", CustomFieldNumber, "42", nil, "42", false},
		{"a decimal", CustomFieldNumber, "42.50", nil, "42.50", false},
		{"a negative", CustomFieldNumber, "-7", nil, "-7", false},
		// FINAL REVIEW B1, ROUND 2: the WHATWG "valid floating-point number"
		// grammar this codebase's number widget relies on allows the
		// integer part to be ABSENT when a fraction part is present -- "one
		// or both", not "both" -- so ".5" is genuinely valid and a real
		// browser round-trips it unchanged. The first correction of B1 got
		// this backwards and refused it too; isDecimalNumber's comment has
		// the full grammar. A leading '+' and a bare trailing '.' (nothing,
		// or nothing further valid, after the point) are still refused --
		// neither round-trips through the widget -- and an exponent form
		// like "1e3" is accepted, because the grammar allows it too.
		{"a leading decimal point is accepted (WHATWG allows an absent integer part)",
			CustomFieldNumber, ".5", nil, ".5", false},
		{"an exponent form is accepted", CustomFieldNumber, "1e3", nil, "1e3", false},
		{"a positive with an explicit sign is refused", CustomFieldNumber, "+42", nil, "", true},
		{"a trailing decimal point is refused", CustomFieldNumber, "5.", nil, "", true},
		{"a bare decimal point is refused", CustomFieldNumber, ".", nil, "", true},
		{"a grouped number is refused", CustomFieldNumber, "1,234", nil, "", true},
		{"words are not a number", CustomFieldNumber, "many", nil, "", true},
		{"underscore grouping is refused", CustomFieldNumber, "1_234", nil, "", true},
		{"Infinity is refused", CustomFieldNumber, "Infinity", nil, "", true},
		{"inf is refused", CustomFieldNumber, "inf", nil, "", true},
		{"NaN is refused", CustomFieldNumber, "NaN", nil, "", true},
		{"a hex float literal is refused", CustomFieldNumber, "0x1p4", nil, "", true},
		{"an ISO date", CustomFieldDate, "2027-03-01", nil, "2027-03-01", false},
		{"a non-date is refused", CustomFieldDate, "march next year", nil, "", true},
		{"an impossible date is refused", CustomFieldDate, "2027-02-30", nil, "", true},
		{"a non-leap-year 29th of February is refused", CustomFieldDate, "2027-02-29", nil, "", true},
		// FINAL REVIEW, ROUND 2: time.Parse accepts year "0000" (Go's
		// calendar has no lower bound), but HTML's valid-date-string grammar
		// requires a year greater than zero, so <input type="date"
		// value="0000-01-01"> renders EMPTY -- B1's exact shape, confirmed
		// live against the real handler.
		{"year 0000 is refused", CustomFieldDate, "0000-01-01", nil, "", true},
		{"true normalises", CustomFieldBoolean, "TRUE", nil, "true", false},
		{"yes is not a boolean", CustomFieldBoolean, "yes", nil, "", true},
		{"a live option", CustomFieldSelect, "it-42", []string{"it-42", "it-99"}, "it-42", false},
		{"an unlisted option is refused", CustomFieldSelect, "it-01", []string{"it-42"}, "", true},
		{"select with no options is refused", CustomFieldSelect, "it-42", nil, "", true},
		{"text at the length limit is accepted", CustomFieldText, strings.Repeat("a", MaxCustomTextLength), nil, strings.Repeat("a", MaxCustomTextLength), false},
		{"text over the length limit is refused", CustomFieldText, strings.Repeat("a", MaxCustomTextLength+1), nil, "", true},
		{"text with an embedded control character is refused", CustomFieldText, "ABC-1234\x00", nil, "", true},
		{"text with a newline is refused", CustomFieldText, "ABC-1234\nmore", nil, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := CanonicalCustomValue(c.kind, c.raw, c.options)
			if c.wantErr {
				if err == nil {
					t.Fatalf("%q was accepted for kind %s; want refused", c.raw, c.kind)
				}
				return
			}
			if err != nil {
				t.Fatalf("%q was refused for kind %s: %v", c.raw, c.kind, err)
			}
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestAnUnknownKindIsRefused(t *testing.T) {
	if _, err := CanonicalCustomValue("colour", "blue", nil); err == nil {
		t.Fatal("an unknown kind must be refused, not stored as text")
	}
}
