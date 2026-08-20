// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import (
	"testing"
	"time"
)

var testClock = time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

func TestACustomFieldRefusesAnEmptyDescription(t *testing.T) {
	// The description is what a new hire reads instead of telephoning the
	// vendor. An administrator who cannot say why a field exists is the
	// origin of that call, and creation is the cheapest moment to ask.
	_, err := NewCustomField("id", CustomFieldEntityAsset, "cost_centre",
		"Cost Centre", CustomFieldText, "   ", "user-1", testClock)
	if err == nil {
		t.Fatal("a field with no description must be refused")
	}
}

func TestACustomFieldCodeIsLowerCased(t *testing.T) {
	f, err := NewCustomField("id", CustomFieldEntityAsset, "Cost_Centre",
		"Cost Centre", CustomFieldText, "SAP cost centre", "user-1", testClock)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	if f.Code != "cost_centre" {
		t.Fatalf("got code %q, want cost_centre", f.Code)
	}
}

func TestACustomFieldRefusesAnUnknownEntityType(t *testing.T) {
	_, err := NewCustomField("id", "network", "cost_centre",
		"Cost Centre", CustomFieldText, "SAP cost centre", "user-1", testClock)
	if err == nil {
		t.Fatal("an unknown entity type must be refused")
	}
}

func TestACustomFieldRefusesAnUnknownKind(t *testing.T) {
	_, err := NewCustomField("id", CustomFieldEntityAsset, "cost_centre",
		"Cost Centre", "colour", "SAP cost centre", "user-1", testClock)
	if err == nil {
		t.Fatal("an unknown kind must be refused")
	}
}

func TestACustomFieldStampsItsCreatedAtFromTheClockParameter(t *testing.T) {
	f, err := NewCustomField("id", CustomFieldEntityAsset, "cost_centre",
		"Cost Centre", CustomFieldText, "SAP cost centre", "user-1", testClock)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	if f.CreatedAt != FormatTime(testClock) {
		t.Fatalf("got created_at %q, want %q", f.CreatedAt, FormatTime(testClock))
	}
}

func TestANewCustomFieldIsNotRetired(t *testing.T) {
	f, err := NewCustomField("id", CustomFieldEntityAsset, "cost_centre",
		"Cost Centre", CustomFieldText, "SAP cost centre", "user-1", testClock)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	if f.IsRetired() {
		t.Fatal("a freshly created field must not report itself retired")
	}
}

func TestARetiredCustomFieldReportsItself(t *testing.T) {
	f, err := NewCustomField("id", CustomFieldEntityAsset, "cost_centre",
		"Cost Centre", CustomFieldText, "SAP cost centre", "user-1", testClock)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	retiredAt := FormatTime(testClock)
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
		{"a grouped number is refused", CustomFieldNumber, "1,234", nil, "", true},
		{"words are not a number", CustomFieldNumber, "many", nil, "", true},
		{"an ISO date", CustomFieldDate, "2027-03-01", nil, "2027-03-01", false},
		{"a non-date is refused", CustomFieldDate, "march next year", nil, "", true},
		{"an impossible date is refused", CustomFieldDate, "2027-02-30", nil, "", true},
		{"true normalises", CustomFieldBoolean, "TRUE", nil, "true", false},
		{"yes is not a boolean", CustomFieldBoolean, "yes", nil, "", true},
		{"a live option", CustomFieldSelect, "it-42", []string{"it-42", "it-99"}, "it-42", false},
		{"an unlisted option is refused", CustomFieldSelect, "it-01", []string{"it-42"}, "", true},
		{"select with no options is refused", CustomFieldSelect, "it-42", nil, "", true},
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
