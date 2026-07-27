package store

import (
	"encoding/json"
	"fmt"
	"reflect"
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
func diffJSON(before, after any) (string, bool, error) {
	changes := map[string]fieldChange{}

	bv := reflect.Indirect(reflect.ValueOf(before))
	av := reflect.Indirect(reflect.ValueOf(after))
	if bv.Type() != av.Type() {
		return "", false, fmt.Errorf("diffing: type mismatch %s vs %s", bv.Type(), av.Type())
	}

	t := bv.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		name := field.Tag.Get("db")
		if name == "" || name == "-" {
			continue
		}
		// updated_at moves on every write and would drown the real change.
		if name == "updated_at" {
			continue
		}
		oldVal := deref(bv.Field(i))
		newVal := deref(av.Field(i))
		if reflect.DeepEqual(oldVal, newVal) {
			continue
		}
		changes[name] = fieldChange{Old: oldVal, New: newVal}
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
	v := reflect.Indirect(reflect.ValueOf(entity))
	t := v.Type()
	fields := map[string]any{}
	for i := 0; i < t.NumField(); i++ {
		name := t.Field(i).Tag.Get("db")
		if name == "" || name == "-" {
			continue
		}
		fields[name] = deref(v.Field(i))
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
