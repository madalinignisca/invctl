// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Package domain: this file holds custom fields, the far end of the axis
// internal/help draws. That package's doc comment splits vocabularies in two:
// seven lookup tables an estate edits because they describe its own
// conventions, and a smaller set whose meaning the ENGINE defines and which
// therefore lives in Go. A custom field is entirely the estate's — `cost_centre`
// means whatever the administrator who created it says it means, and nothing
// in this codebase branches on its value. It is the estate-defined case taken
// as far as it goes: not just the label on a fixed slot, but the field itself.
package domain

import (
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Custom field kinds. This Go set is the first line of defence; the
// custom_field.kind CHECK constraint written in migration 00051 is the second
// and must list exactly these values.
const (
	CustomFieldText    = "text"
	CustomFieldNumber  = "number"
	CustomFieldDate    = "date"
	CustomFieldBoolean = "boolean"
	CustomFieldSelect  = "select"
)

// CustomFieldKinds are the permitted values of custom_field.kind.
var CustomFieldKinds = []string{
	CustomFieldText, CustomFieldNumber, CustomFieldDate, CustomFieldBoolean, CustomFieldSelect,
}

// Custom field entity types. Assets and services only — HANDOVER §1 lists
// widening this as additive later, not load-bearing now.
const (
	CustomFieldEntityAsset   = "asset"
	CustomFieldEntityService = "service"
)

// CustomFieldEntityTypes are the permitted values of custom_field.entity_type.
var CustomFieldEntityTypes = []string{CustomFieldEntityAsset, CustomFieldEntityService}

// MaxCustomTextLength bounds a `text` kind custom value. Generous for an
// identifier or a short note, far short of a paste bomb landing in a CSV
// export next to the injection surface WP-G5 already defuses.
const MaxCustomTextLength = 500

// CustomField is a definition: what an administrator says a field means, for
// which entity type, and why it exists. description is required — an
// administrator who cannot say why a field exists is the origin of the
// support call this feature is built against.
type CustomField struct {
	ID          string  `db:"id"`
	EntityType  string  `db:"entity_type"`
	Code        string  `db:"code"`
	Label       string  `db:"label"`
	Kind        string  `db:"kind"`
	Description string  `db:"description"`
	CreatedBy   string  `db:"created_by"`
	CreatedAt   string  `db:"created_at"`
	RetiredAt   *string `db:"retired_at"`
	RetiredBy   *string `db:"retired_by"`
	RowVersion  int     `db:"row_version"`
}

// CustomFieldOption is one selectable value of a `select` field.
type CustomFieldOption struct {
	ID        string  `db:"id"`
	FieldID   string  `db:"field_id"`
	Value     string  `db:"value"`
	Label     string  `db:"label"`
	Position  int     `db:"position"`
	RetiredAt *string `db:"retired_at"`
}

// CustomFieldValue is what one entity holds for one field. ValueText carries
// every kind: the canonical form CanonicalCustomValue returns.
type CustomFieldValue struct {
	ID         string `db:"id"`
	FieldID    string `db:"field_id"`
	EntityID   string `db:"entity_id"`
	ValueText  string `db:"value_text"`
	CreatedAt  string `db:"created_at"`
	UpdatedAt  string `db:"updated_at"`
	RowVersion int    `db:"row_version"`
}

// NewCustomField validates and constructs a field definition. now is the
// clock, last parameter, formatted here — the shape every constructor in this
// package follows, so the store never generates a timestamp.
func NewCustomField(id, entityType, code, label, kind, description, createdBy string, now time.Time) (*CustomField, error) {
	ve := &ValidationError{}
	checkEnum(ve, "entity_type", entityType, CustomFieldEntityTypes)
	code = strings.ToLower(checkVocabulary(ve, "code", code))
	label = checkRequired(ve, "label", label)
	checkEnum(ve, "kind", kind, CustomFieldKinds)
	description = checkRequired(ve, "description", description)
	createdBy = checkRequired(ve, "created_by", createdBy)
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	return &CustomField{
		ID:          id,
		EntityType:  entityType,
		Code:        code,
		Label:       label,
		Kind:        kind,
		Description: description,
		CreatedBy:   createdBy,
		CreatedAt:   FormatTime(now),
		RowVersion:  1,
	}, nil
}

// IsRetired reports whether the field has been retired. A retired field keeps
// every value it already holds — see docs/custom-fields-design.md §6 — and
// simply disappears from forms and from detail pages.
func (f *CustomField) IsRetired() bool {
	return f.RetiredAt != nil
}

// CanonicalCustomValue validates raw against kind and returns the form that
// gets stored in custom_field_value.value_text. options is the field's LIVE
// option values, used only when kind is select; a select field with no live
// options accepts nothing, because an option list nobody has filled in cannot
// yet make a value valid.
func CanonicalCustomValue(kind, raw string, options []string) (string, error) {
	switch kind {
	case CustomFieldText:
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			return "", NewValidation("value", "is required")
		}
		if len(trimmed) > MaxCustomTextLength {
			return "", NewValidation("value", "must be "+strconv.Itoa(MaxCustomTextLength)+" characters or fewer")
		}
		for _, r := range trimmed {
			if unicode.IsControl(r) {
				return "", NewValidation("value", "must not contain control characters")
			}
		}
		return trimmed, nil
	case CustomFieldNumber:
		trimmed := strings.TrimSpace(raw)
		// isDecimalNumber decides, not ParseFloat: ParseFloat accepts the
		// Go float-literal grammar, which is more permissive than "a
		// decimal number" -- it takes "1_234" (underscore grouping),
		// "Infinity", "inf", "NaN" and "0x1p4" (hex float), none of which
		// belong in value_text verbatim, in a CSV column or in an audit
		// diff. ParseFloat runs afterward only as a second check on a
		// string the character class has already approved.
		if !isDecimalNumber(trimmed) {
			return "", NewValidation("value", "must be a decimal number")
		}
		if _, err := strconv.ParseFloat(trimmed, 64); err != nil {
			return "", NewValidation("value", "must be a decimal number")
		}
		// The trimmed ORIGINAL text is returned, not the parsed float
		// reformatted: an operator who typed 42.50 meant two decimal
		// places, and 42.5 is a different string in an export and in an
		// audit diff.
		return trimmed, nil
	case CustomFieldDate:
		trimmed := strings.TrimSpace(raw)
		if _, err := time.Parse("2006-01-02", trimmed); err != nil {
			return "", NewValidation("value", "must be a real calendar date in YYYY-MM-DD form")
		}
		return trimmed, nil
	case CustomFieldBoolean:
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "true":
			return "true", nil
		case "false":
			return "false", nil
		default:
			return "", NewValidation("value", "must be true or false")
		}
	case CustomFieldSelect:
		trimmed := strings.TrimSpace(raw)
		for _, opt := range options {
			if opt == trimmed {
				return trimmed, nil
			}
		}
		return "", NewValidation("value", "must be one of this field's live options")
	default:
		return "", NewValidation("value", "is not a known custom field kind")
	}
}

// isDecimalNumber reports whether s is a decimal number and nothing else: an
// optional leading sign, digits, at most one '.', and at least one digit.
// Deliberately narrower than strconv.ParseFloat's grammar, which also
// accepts underscore-grouped literals, "Infinity", "inf", "NaN" and hex
// float literals -- none of which are "a decimal number" in the sense a
// custom field's number kind means.
func isDecimalNumber(s string) bool {
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	if i >= len(s) {
		return false
	}
	sawDigit := false
	sawDot := false
	for ; i < len(s); i++ {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			sawDigit = true
		case c == '.' && !sawDot:
			sawDot = true
		default:
			return false
		}
	}
	return sawDigit
}
