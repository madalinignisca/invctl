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

var tagNow = time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)

func TestNewTagAcceptsAValidTag(t *testing.T) {
	tag, err := NewTag("id-1", "dr", "DR", "in scope for the annual DR exercise", "user-1", tagNow)
	if err != nil {
		t.Fatalf("a valid tag must be accepted: %v", err)
	}
	if tag.Code != "dr" || tag.Label != "DR" || tag.Description == "" {
		t.Fatalf("unexpected tag: %+v", tag)
	}
	if tag.RowVersion != 1 {
		t.Fatalf("got row_version %d, want 1", tag.RowVersion)
	}
	if tag.IsRetired() {
		t.Fatal("a freshly built tag must not be retired")
	}
}

// TestNewTagLowerCasesTheCode is the sprawl control docs/tags-design.md §2
// names explicitly: "dr", "DR" and "disaster-recovery" must not become three
// codes meaning one thing. Case-folding at construction, not only at the
// uniqueness index, means "DR" and "dr" are literally one string by the time
// either reaches the database.
func TestNewTagLowerCasesTheCode(t *testing.T) {
	tag, err := NewTag("id-1", "DR", "DR", "why this exists", "user-1", tagNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag.Code != "dr" {
		t.Fatalf("got code %q, want it lower-cased to \"dr\"", tag.Code)
	}
}

func TestNewTagRefusesAnEmptyOrMalformedCode(t *testing.T) {
	cases := []struct {
		name string
		code string
	}{
		{"empty", ""},
		{"blank", "   "},
		{"contains a space", "d r"},
		{"contains a control character", "d\tr"},
		{"too long", strings.Repeat("a", MaxVocabularyCodeLen+1)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewTag("id-1", c.code, "Label", "why this exists", "user-1", tagNow)
			if err == nil {
				t.Fatalf("code %q must be refused", c.code)
			}
			ve, ok := AsValidation(err)
			if !ok {
				t.Fatalf("got %v, want a ValidationError", err)
			}
			found := false
			for _, f := range ve.Fields {
				if f.Field == "code" {
					found = true
				}
			}
			if !found {
				t.Fatalf("validation error %v does not name code", ve.Fields)
			}
		})
	}
}

func TestNewTagRefusesAnEmptyLabel(t *testing.T) {
	_, err := NewTag("id-1", "dr", "   ", "why this exists", "user-1", tagNow)
	if err == nil {
		t.Fatal("a tag with no label must be refused")
	}
}

func TestNewTagRefusesAnEmptyDescription(t *testing.T) {
	// The description is what makes somebody say why a tag exists at the
	// cheapest moment -- the same reasoning as custom_field.description.
	_, err := NewTag("id-1", "dr", "DR", "   ", "user-1", tagNow)
	if err == nil {
		t.Fatal("a tag with no description must be refused")
	}
	ve, ok := AsValidation(err)
	if !ok {
		t.Fatalf("got %v, want a ValidationError naming description", err)
	}
	found := false
	for _, f := range ve.Fields {
		if f.Field == "description" {
			found = true
		}
	}
	if !found {
		t.Fatalf("validation error %v does not name description", ve.Fields)
	}
}

func TestNewTagRefusesAnEmptyCreatedBy(t *testing.T) {
	_, err := NewTag("id-1", "dr", "DR", "why this exists", "   ", tagNow)
	if err == nil {
		t.Fatal("a tag with no creator must be refused")
	}
}

// TestTagValidateMirrorsConstruction: a handler that mutates the struct
// directly (exactly what UpdateTag's own caller does) gets the same first
// line of defence a fresh NewTag call would.
func TestTagValidateMirrorsConstruction(t *testing.T) {
	tag, err := NewTag("id-1", "dr", "DR", "why this exists", "user-1", tagNow)
	if err != nil {
		t.Fatalf("building the fixture tag: %v", err)
	}
	tag.Description = ""
	if err := tag.Validate(); err == nil {
		t.Fatal("clearing the description must be refused on re-validation")
	}
}

// TestTagValidateLowerCasesARenamedCode: a rename typed in mixed case is
// still normalised on the shared validation path, not only at construction.
func TestTagValidateLowerCasesARenamedCode(t *testing.T) {
	tag, err := NewTag("id-1", "dr", "DR", "why this exists", "user-1", tagNow)
	if err != nil {
		t.Fatalf("building the fixture tag: %v", err)
	}
	tag.Code = "DR-SITE"
	if err := tag.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag.Code != "dr-site" {
		t.Fatalf("got code %q, want it lower-cased to \"dr-site\"", tag.Code)
	}
}
