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
	"reflect"

	"github.com/gabriel/invctl/internal/domain"
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
