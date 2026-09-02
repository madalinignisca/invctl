// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import "testing"

// The parser and the writer must agree, and they only will if something makes
// them. They sit in one file for that reason; this is the test that pins it.
func TestADiffRoundTrips(t *testing.T) {
	type row struct {
		ID   string  `db:"id"`
		Name string  `db:"name"`
		Tier int     `db:"tier"`
		Note *string `db:"note"`
		On   bool    `db:"enabled"`
	}
	note := "before"
	before := &row{ID: "x", Name: "old", Tier: 2, Note: &note, On: false}
	after := &row{ID: "x", Name: "new", Tier: 3, Note: nil, On: true}

	raw, changed, err := diffJSON(before, after)
	if err != nil || !changed {
		t.Fatalf("diffJSON: changed=%v err=%v", changed, err)
	}

	got := ParseDiff(raw)
	if !got.Parsed || got.Snapshot {
		t.Fatalf("parsed=%v snapshot=%v, want a parsed update", got.Parsed, got.Snapshot)
	}
	want := map[string][2]string{
		"name":    {"old", "new"},
		"tier":    {"2", "3"},
		"note":    {"before", "—"},
		"enabled": {"false", "true"},
	}
	if len(got.Fields) != len(want) {
		t.Fatalf("got %d fields, want %d: %+v", len(got.Fields), len(want), got.Fields)
	}
	for _, f := range got.Fields {
		pair, ok := want[f.Field]
		if !ok {
			t.Errorf("unexpected field %q", f.Field)
			continue
		}
		if f.Old != pair[0] || f.New != pair[1] {
			t.Errorf("%s: got %q → %q, want %q → %q", f.Field, f.Old, f.New, pair[0], pair[1])
		}
	}
	// Sorted, so two entries for one entity line up when read in sequence.
	for i := 1; i < len(got.Fields); i++ {
		if got.Fields[i-1].Field > got.Fields[i].Field {
			t.Errorf("fields are not sorted: %s before %s", got.Fields[i-1].Field, got.Fields[i].Field)
		}
	}
}

func TestACreateSnapshotRoundTrips(t *testing.T) {
	type row struct {
		ID   string `db:"id"`
		Name string `db:"name"`
	}
	raw, err := snapshotJSON(&row{ID: "x", Name: "thing"})
	if err != nil {
		t.Fatalf("snapshotJSON: %v", err)
	}
	got := ParseDiff(raw)
	if !got.Parsed || !got.Snapshot {
		t.Fatalf("parsed=%v snapshot=%v, want a parsed snapshot", got.Parsed, got.Snapshot)
	}
	if len(got.Fields) != 2 {
		t.Fatalf("got %d fields, want 2: %+v", len(got.Fields), got.Fields)
	}
	for _, f := range got.Fields {
		if !f.Added || f.Old != "" {
			t.Errorf("%s: a create has no old value, got %q (added=%v)", f.Field, f.Old, f.Added)
		}
	}
}

// Text this cannot read is shown, not hidden. An audit screen that silently
// blanks what it failed to parse is worse than one that prints JSON.
func TestAnUnreadableDiffIsKeptVisible(t *testing.T) {
	for _, raw := range []string{"not json at all", `{"name":"unexpected shape"}`, `[1,2,3]`} {
		got := ParseDiff(raw)
		if got.Parsed {
			t.Errorf("%q: reported as parsed", raw)
		}
		if got.Raw != raw {
			t.Errorf("%q: raw text lost (%q)", raw, got.Raw)
		}
	}
}

// TestACustomFieldsRowCarriesAReaderNote is the legibility half of the GDPR
// change-counter fold: an operator reading "cost_centre@2" with no
// explanation reasonably concludes the log is corrupt, which is exactly the
// support call this whole work package exists to prevent. ParseDiff must
// attach an explanation to a custom_fields row, on both an update and a
// create, and to no other field.
func TestACustomFieldsRowCarriesAReaderNote(t *testing.T) {
	t.Run("update", func(t *testing.T) {
		got := ParseDiff(`{"custom_fields":{"old":"cost_centre@1","new":"cost_centre@2"},"serial":{"old":"a","new":"b"}}`)
		if !got.Parsed {
			t.Fatal("the diff must parse")
		}
		for _, f := range got.Fields {
			switch f.Field {
			case "custom_fields":
				if f.Note == "" {
					t.Error("a custom_fields row must carry an explanatory note")
				}
			default:
				if f.Note != "" {
					t.Errorf("%s: an ordinary field must carry no note, got %q", f.Field, f.Note)
				}
			}
		}
	})
	t.Run("create", func(t *testing.T) {
		got := ParseDiff(`{"new":{"custom_fields":"cost_centre@1","serial":"x"}}`)
		if !got.Parsed || !got.Snapshot {
			t.Fatal("the snapshot must parse")
		}
		for _, f := range got.Fields {
			switch f.Field {
			case "custom_fields":
				if f.Note == "" {
					t.Error("a custom_fields row must carry an explanatory note")
				}
			default:
				if f.Note != "" {
					t.Errorf("%s: an ordinary field must carry no note, got %q", f.Field, f.Note)
				}
			}
		}
	})
}

// TestEveryCostTableEntityIsInCostEntityTypes makes the redaction list
// structural instead of remembered. costEntityTypes (this file) and the four
// costTable values (costs.go) name the same four surfaces, but nothing before
// this test made that true by construction -- costEntityTypes is a hand-typed
// map, and it had already gone stale once: WP-1.1's authorization review found
// only "asset_cost" was ever proven redacted, and deleting "service_cost",
// "project_cost" or "circuit_cost" from the map left both test suites green.
// Iterating the real costTable values, rather than a second hand-typed list of
// entity names, is what closes that: a fifth cost surface added to costs.go
// without a matching costEntityTypes entry now fails here, on the one property
// that actually matters -- that its change_log diff gets redacted for a viewer
// with no can_see_costs grant -- rather than surviving both suites the way
// three of the four existing ones already did.
func TestEveryCostTableEntityIsInCostEntityTypes(t *testing.T) {
	for _, table := range []costTable{costOnAsset, costOnService, costOnProject, costOnCircuit} {
		if !IsCostEntityType(table.entity) {
			t.Errorf("costTable %q (SQL table %q) has no costEntityTypes entry -- "+
				"a change_log diff for it would show its amount_minor to any viewer, "+
				"grant or no grant", table.entity, table.name)
		}
	}
}
