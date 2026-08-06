// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Change log diffs are field-level rather than full-row snapshots (see
// docs/DECISIONS.md Q3). Field-level is smaller, and it answers "who changed
// the availability policy" directly instead of requiring a reader to compare
// two snapshots by eye. The cost is that reconstructing a row at a point in
// time means replaying from creation -- acceptable, because the create entry
// carries a full snapshot to replay from.

// fieldChange is one field's before and after.
type fieldChange struct {
	Old any `json:"old"`
	New any `json:"new"`
}

// diffJSON produces the field-level diff between two values of the same struct
// type, keyed by db tag. Fields that did not change are omitted.
//
// Returns ok=false when nothing changed, which lets a caller skip a pointless
// audit row for a no-op form submission.
// auditFields flattens a struct into db-tag -> value.
//
// It descends into embedded structs, which is not an optimisation: the audited
// shapes (assetAudit, dependencyAudit) embed the domain entity and add the
// child-table set beside it. A walk that only looks at top-level fields sees
// the embedded struct as one untagged field, skips it, and silently reduces the
// whole audit entry to the added column -- which is exactly what happened, and
// shipped, because the test only asserted the added column was present.
func auditFields(v reflect.Value, out map[string]auditField) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.Anonymous {
			// An anonymous POINTER embed is the shape that made the certificate
			// audit record nothing at all: it silently matched neither this
			// branch nor the db-tag one below, so every column of the embedded
			// struct vanished from change_log while an entry was still written.
			// The bug was invisible for a week and was found by mutating an
			// unrelated redaction rule.
			//
			// A panic rather than a silent deref, because this is a programming
			// error in a struct literal that only ever appears at compile time
			// in this package -- and the failure it replaces is the worst kind:
			// an audit trail that looks complete and is empty. Embed by value.
			if field.Type.Kind() == reflect.Ptr {
				panic(fmt.Sprintf(
					"audit struct %s embeds *%s by pointer: embed it by value, or "+
						"its columns are silently absent from every change_log entry",
					t.Name(), field.Type.Elem().Name()))
			}
			if field.Type.Kind() == reflect.Struct {
				auditFields(v.Field(i), out)
				continue
			}
		}
		name := field.Tag.Get("db")
		if name == "" || name == "-" {
			continue
		}
		// updated_at and row_version move on EVERY write and would drown the
		// real change. row_version in particular is not a fact about the
		// entity at all -- it is the concurrency token, and an audit entry
		// reading "row_version: 4 -> 5" tells a reader nothing they can use.
		if name == "updated_at" || name == "row_version" {
			continue
		}
		out[name] = auditField{value: deref(v.Field(i)), entity: t.Name()}
	}
}

// auditField carries the owning struct's name so redaction can be judged per
// entity -- display_name is a person on app_user and a service description on
// rt_windows.
type auditField struct {
	value  any
	entity string
}

// diffJSON produces the field-level diff between two values of the same struct
// type, keyed by db tag. Fields that did not change are omitted.
//
// Returns ok=false when nothing changed, which lets a caller skip a pointless
// audit row for a no-op form submission.
func diffJSON(before, after any) (string, bool, error) {
	bv := reflect.Indirect(reflect.ValueOf(before))
	av := reflect.Indirect(reflect.ValueOf(after))
	if bv.Type() != av.Type() {
		return "", false, fmt.Errorf("diffing: type mismatch %s vs %s", bv.Type(), av.Type())
	}

	beforeFields := map[string]auditField{}
	afterFields := map[string]auditField{}
	auditFields(bv, beforeFields)
	auditFields(av, afterFields)

	changes := map[string]fieldChange{}
	for name, after := range afterFields {
		before := beforeFields[name]
		if reflect.DeepEqual(before.value, after.value) {
			continue
		}
		if domain.IsRedacted(after.entity, name) {
			// Record that it changed, never what to.
			changes[name] = fieldChange{Old: domain.Redacted, New: domain.Redacted}
			continue
		}
		changes[name] = fieldChange{Old: before.value, New: after.value}
	}

	if len(changes) == 0 {
		return "", false, nil
	}
	encoded, err := json.Marshal(changes)
	if err != nil {
		return "", false, fmt.Errorf("encoding diff: %w", err)
	}
	return string(encoded), true, nil
}

// snapshotJSON renders a full row for a create entry, under "new" so that a
// create and an update entry can be read by the same code.
func snapshotJSON(entity any) (string, error) {
	collected := map[string]auditField{}
	auditFields(reflect.Indirect(reflect.ValueOf(entity)), collected)

	fields := make(map[string]any, len(collected))
	for name, f := range collected {
		if domain.IsRedacted(f.entity, name) {
			fields[name] = domain.Redacted
			continue
		}
		fields[name] = f.value
	}
	encoded, err := json.Marshal(map[string]any{"new": fields})
	if err != nil {
		return "", fmt.Errorf("encoding snapshot: %w", err)
	}
	return string(encoded), nil
}

// deref unwraps a pointer field so that a nil pointer serialises as JSON null
// rather than as an address, and so that two equal values behind different
// pointers compare equal.
func deref(v reflect.Value) any {
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return nil
		}
		return v.Elem().Interface()
	}
	if v.Kind() == reflect.Slice && v.IsNil() {
		return nil
	}
	return v.Interface()
}

// ---------- reading a diff back ----------

// FieldChange is one field's movement, rendered for a person.
type FieldChange struct {
	Field string
	Old   string
	New   string
	// Added is true for a create, where there is no old value to show and
	// printing an empty left-hand side would read as "it was blank".
	Added bool
}

// ChangeDetail is a stored diff turned into something a human can scan.
type ChangeDetail struct {
	// Fields is one row per field, sorted so two entries for the same entity
	// line up when read one after the other.
	Fields []FieldChange
	// Snapshot is true for a create entry, which records every field rather
	// than a movement. A screen collapses it: "created, 14 fields", with the
	// detail behind a disclosure.
	Snapshot bool
	// Raw is the stored text, kept so a diff this cannot parse is still
	// readable rather than silently blank. An audit screen that hides what it
	// failed to understand is worse than one that shows JSON.
	Raw string
	// Parsed is false when the text was not in either known shape.
	Parsed bool
}

// ParseDiff turns a stored change_log diff into display rows.
//
// BESIDE THE WRITER ON PURPOSE. The two shapes below are produced by diffJSON
// and snapshotJSON a few lines up; a parser living in the web layer would be
// one refactor away from disagreeing with them silently, and the thing that
// disagrees is the audit trail. TestADiffRoundTrips pins them together.
//
//	update:  {"name":{"old":"a","new":"b"}}
//	create:  {"new":{"name":"a","tier":2}}
func ParseDiff(raw string) ChangeDetail {
	out := ChangeDetail{Raw: raw}
	if strings.TrimSpace(raw) == "" {
		out.Parsed = true
		return out
	}

	var generic map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &generic); err != nil {
		return out
	}

	// A create carries exactly one key, "new", holding the whole row.
	if snapshot, ok := generic["new"]; ok && len(generic) == 1 {
		var fields map[string]any
		if err := json.Unmarshal(snapshot, &fields); err != nil {
			return out
		}
		out.Snapshot, out.Parsed = true, true
		for _, name := range sortedKeys(fields) {
			out.Fields = append(out.Fields, FieldChange{
				Field: name, New: renderValue(fields[name]), Added: true,
			})
		}
		return out
	}

	for _, name := range sortedKeys(generic) {
		var move struct {
			Old any `json:"old"`
			New any `json:"new"`
		}
		if err := json.Unmarshal(generic[name], &move); err != nil {
			// One unreadable field must not blank the whole entry.
			return out
		}
		out.Fields = append(out.Fields, FieldChange{
			Field: name, Old: renderValue(move.Old), New: renderValue(move.New),
		})
	}
	out.Parsed = len(out.Fields) > 0
	return out
}

// renderValue prints one JSON value the way an operator reads it.
//
// A missing value is an em dash rather than an empty cell, because "" and "not
// set" are different facts and a blank cell reads as neither.
func renderValue(v any) string {
	switch value := v.(type) {
	case nil:
		return "—"
	case string:
		if value == "" {
			return `""`
		}
		return value
	case bool:
		if value {
			return "true"
		}
		return "false"
	case float64:
		// Whole numbers print without the .0 that JSON decoding introduces.
		if value == math.Trunc(value) && math.Abs(value) < 1e15 {
			return strconv.FormatInt(int64(value), 10)
		}
		return strconv.FormatFloat(value, 'f', -1, 64)
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return fmt.Sprintf("%v", value)
		}
		return string(encoded)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
