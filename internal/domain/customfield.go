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

// validateShape checks a CustomField's shape and enum membership: the rules
// that hold at every point in its life, construction or a later edit alike.
// It normalises Code, Label and Description in place. NewCustomField and
// Validate share it rather than keeping two copies of the same rules --
// immutability (entity_type frozen after creation, kind frozen once a value
// exists) is deliberately NOT here, because it needs the before row and a
// value count that only the store has; see internal/store/customfields.go.
func (f *CustomField) validateShape(ve *ValidationError) {
	f.Code = strings.ToLower(checkVocabulary(ve, "code", f.Code))
	f.Label = checkRequired(ve, "label", f.Label)
	checkEnum(ve, "entity_type", f.EntityType, CustomFieldEntityTypes)
	checkEnum(ve, "kind", f.Kind, CustomFieldKinds)
	f.Description = checkRequired(ve, "description", f.Description)
}

// Validate re-checks a CustomField after field updates, mirroring
// domain.Team.Validate(): a handler that mutated the struct directly --
// exactly what UpdateCustomField's own test suite does -- gets the same
// first line of defence a fresh NewCustomField call would, rather than
// leaving the database CHECK as the only backstop.
func (f *CustomField) Validate() error {
	ve := &ValidationError{}
	f.validateShape(ve)
	return ve.OrNil()
}

// NewCustomField validates and constructs a field definition. now is the
// clock, last parameter, formatted here — the shape every constructor in this
// package follows, so the store never generates a timestamp.
func NewCustomField(id, entityType, code, label, kind, description, createdBy string, now time.Time) (*CustomField, error) {
	f := &CustomField{
		ID:          id,
		EntityType:  entityType,
		Code:        code,
		Label:       label,
		Kind:        kind,
		Description: description,
		CreatedBy:   createdBy,
		CreatedAt:   FormatTime(now),
		RowVersion:  1,
	}
	ve := &ValidationError{}
	f.validateShape(ve)
	f.CreatedBy = checkRequired(ve, "created_by", f.CreatedBy)
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	return f, nil
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
		return customTextBounds("value", raw)
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
		parsed, err := time.Parse("2006-01-02", trimmed)
		if err != nil {
			return "", NewValidation("value", "must be a real calendar date in YYYY-MM-DD form")
		}
		// FINAL REVIEW, ROUND 2: time.Parse happily accepts year "0000" --
		// Go's calendar has no lower bound -- but HTML's own valid-date-
		// string grammar requires a year GREATER than zero, so
		// <input type="date" value="0000-01-01"> renders EMPTY. This is
		// B1's exact shape in the one kind the property test previously
		// covered with a value that could never fail: the store accepted a
		// value its own widget cannot draw back, so the next unrelated save
		// posted the blank the browser drew as an explicit clear. Confirmed
		// live: POST cf_<date field>=0000-01-01 stored it and the page came
		// back unable to render it.
		if parsed.Year() == 0 {
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

// customTextBounds enforces the bounds every piece of custom-field-adjacent
// free text obeys -- non-empty after trim, at most MaxCustomTextLength, no
// control characters -- shared by CanonicalCustomValue's `text` kind and by
// ValidateCustomFieldOptionText below.
//
// FINAL REVIEW AY: a `select` field's own option value and label used to
// obey neither bound. CanonicalCustomValue's select branch only ever checked
// membership -- "is trimmed equal to one of the live option values" -- so an
// unbounded, control-character-laden VALUE, once it existed as an option,
// sailed through that check the moment a value selected it. The bound
// belongs on the option itself, at the point an administrator types it, not
// on every value that later selects it.
func customTextBounds(field, raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", NewValidation(field, "is required")
	}
	if len(trimmed) > MaxCustomTextLength {
		return "", NewValidation(field, "must be "+strconv.Itoa(MaxCustomTextLength)+" characters or fewer")
	}
	for _, r := range trimmed {
		if unicode.IsControl(r) {
			return "", NewValidation(field, "must not contain control characters")
		}
	}
	return trimmed, nil
}

// ValidateCustomFieldOptionText bounds a select field's own option VALUE or
// LABEL to the same rules as a `text` custom value: MaxCustomTextLength and
// no control characters (FINAL REVIEW AY). field names which one this call
// is checking, "value" or "label", for the ValidationError it returns.
func ValidateCustomFieldOptionText(field, raw string) (string, error) {
	return customTextBounds(field, raw)
}

// isDecimalNumber reports whether s is a decimal number and nothing else,
// under EXACTLY the WHATWG "valid floating-point number" grammar an HTML
// <input type="number"> widget can represent: an optional leading '-'
// (never '+'), then ONE OR BOTH of (a series of digits) and ('.' followed
// by a series of digits), then an optional exponent ('e' or 'E', an
// optional sign, a series of digits). "One or both" is the clause that
// matters and is easy to get backwards: the integer part is OPTIONAL when a
// fraction part is present, so ".5" IS valid (matches "'.' + digits" alone)
// and so is "1e3" (exponent form) -- only a bare "5." (a '.' with nothing,
// or nothing valid, after it) and a leading '+' are refused. Deliberately
// narrower than strconv.ParseFloat's grammar, which also accepts
// underscore-grouped literals, "Infinity", "inf", "NaN" and hex float
// literals -- none of which are "a decimal number" in the sense a custom
// field's number kind means, and none of which this grammar admits either.
//
// FINAL REVIEW B1, AND THE CORRECTION THAT FOLLOWED IT: the first version of
// this function was looser than the widget that renders it -- it accepted
// "+5" and "5.", neither of which round-trips through an
// <input type="number">. The FIX for that overcorrected: it also rejected
// ".5", on the mistaken belief that the widget requires a digit before the
// decimal point. It does not -- the WHATWG grammar explicitly allows the
// integer part to be absent when digits follow the point, and a real
// browser renders and posts ".5" unchanged. That overcorrection is exactly
// what TestEveryStoredCustomValueSurvivesARoundTripThroughItsWidget's
// INDEPENDENT oracle exists to catch: had this function and the test's own
// idea of "what the widget draws back" shared one implementation, the test
// would have agreed with the bug instead of catching it. See
// sanitiseHTMLNumberWidgetValue in internal/web/customfield_roundtrip_test.go
// for that oracle, and the comment there on why it must never be merged
// with this function into one shared implementation.
func isDecimalNumber(s string) bool {
	i := 0
	if i < len(s) && s[i] == '-' {
		i++
	}
	intStart := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	hasInt := i > intStart

	hasFrac := false
	if i < len(s) && s[i] == '.' {
		fracStart := i + 1
		j := fracStart
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		hasFrac = j > fracStart
		if hasFrac {
			i = j
		}
		// A '.' with no digit after it is consumed by neither alternative
		// below -- i is deliberately left BEFORE the '.', so the final
		// i == len(s) check fails on a bare trailing '.' like "5.".
	}
	if !hasInt && !hasFrac {
		return false // need an integer part, a fraction part, or both
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
			return false // "e"/"E" with no exponent digits is not valid either
		}
		i = j
	}
	return i == len(s)
}
