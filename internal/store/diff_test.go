// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

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
// structural instead of remembered. costEntityTypes (this file) and the
// costTable values (costs.go) name the same surfaces, but nothing before this
// test made that true by construction -- costEntityTypes is a hand-typed map,
// and it had already gone stale once: WP-1.1's authorization review found only
// "asset_cost" was ever proven redacted, and deleting "service_cost",
// "project_cost" or "circuit_cost" from the map left both test suites green.
//
// THE FIRST VERSION OF THIS TEST STILL ITERATED A HAND-TYPED SLICE --
// []costTable{costOnAsset, costOnService, costOnProject, costOnCircuit} --
// which guards deletion (remove a name from the slice, the test still passes
// because nothing is missing) but not ADDITION: a fifth costTable declared in
// costs.go with no costEntityTypes entry compiles, is never mentioned in this
// slice, and this test stays green while its diff leaks an amount to every
// viewer. That is the exact case the test's own comment claimed to cover.
//
// costTableEntitiesFromSource fixes that by reading costs.go with go/ast --
// the same approach permit_source_test.go (this package) already uses to
// census permit minters -- and collecting every `entity:` field out of every
// costTable{...} composite literal actually declared in the file, however
// many there are. A sixth cost surface is found the moment it is written,
// not the moment somebody remembers to update a slice beside this test.
func TestEveryCostTableEntityIsInCostEntityTypes(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "store")
	// Item 4 (2026-09-02 group-a-1-1 round): this used to read only
	// costs.go, which contradicted its own doc comment ("a fifth cost
	// surface is found the moment it is written") -- a costTable{...}
	// declared in estate_costs.go, reprice.go, or any new file was simply
	// never looked at. permit_source_test.go's TestOnlyTheNamedFunctionsMint-
	// APermit already walks a whole directory rather than a single named
	// file for exactly this reason; this scan now does the same.
	entities, err := costTableEntitiesFromDir(dir)
	if err != nil {
		t.Fatalf("reading costTable declarations from %s: %v", dir, err)
	}
	if len(entities) == 0 {
		t.Fatalf("found no costTable{...} composite literals in %s -- the scan itself is "+
			"broken, since costs.go plainly declares some", dir)
	}
	for _, entity := range entities {
		if !IsCostEntityType(entity) {
			t.Errorf("costTable with entity %q (declared in costs.go) has no "+
				"costEntityTypes entry -- a change_log diff for it would show its "+
				"amount_minor to any viewer, grant or no grant", entity)
		}
	}
}

// TestAllCostTablesIsEveryDeclaredCostTable is item 4's other half: the AST
// scan above proves every DECLARED costTable{...} is redaction-safe, but
// supplier_movement.go's pricedOwners (and, before it, estate_costs.go's
// EstateCosts) do not walk declarations -- they range over allCostTables, a
// runtime []costTable literal in costs.go. A fifth costTable could be
// declared, redaction-safe per the test above, and STILL never appear in the
// supplier report if nobody remembered to add it to allCostTables too. This
// asserts the entity set the AST scan finds equals the entity set
// allCostTables actually carries, so that omission fails here rather than
// silently under-counting a report nothing here would otherwise catch.
func TestAllCostTablesIsEveryDeclaredCostTable(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "store")
	declared, err := costTableEntitiesFromDir(dir)
	if err != nil {
		t.Fatalf("reading costTable declarations from %s: %v", dir, err)
	}

	declaredSet := map[string]bool{}
	for _, e := range declared {
		declaredSet[e] = true
	}
	runtimeSet := map[string]bool{}
	for _, t := range allCostTables {
		runtimeSet[t.entity] = true
	}

	for e := range declaredSet {
		if !runtimeSet[e] {
			t.Errorf("costTable with entity %q is declared but missing from allCostTables -- "+
				"supplier_movement.go's pricedOwners would never visit it, so its lines are "+
				"silently absent from the supplier report", e)
		}
	}
	for e := range runtimeSet {
		if !declaredSet[e] {
			t.Errorf("allCostTables carries entity %q that the declaration scan did not find "+
				"-- either the scan is broken or allCostTables carries a stale entry", e)
		}
	}
}

// costTableEntitiesFromDir walks every non-test .go file directly in dir
// (mirroring permit_source_test.go's TestOnlyTheNamedFunctionsMintAPermit --
// one directory level, not a recursive tree, since internal/store has no
// subpackages that could hide a costTable) and returns the `entity` field's
// string value out of every costTable{...} composite literal any of them
// declares, regardless of how many there are, which file, or what variable
// each is assigned to.
//
// A literal missing an `entity:` key, or one whose value is not a plain
// string constant, is a scan failure (an error), never a silent skip: a
// costTable this scan cannot read is a costTable TestEveryCostTableEntityIs-
// InCostEntityTypes cannot check, which is exactly the gap this rewrite
// exists to close.
func costTableEntitiesFromDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var all []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		found, err := costTableEntitiesFromSource(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		all = append(all, found...)
	}
	return all, nil
}

// costTableEntitiesFromSource parses path and returns the `entity` field's
// string value out of every costTable{...} composite literal it declares,
// regardless of how many there are or what variable each is assigned to.
//
// A literal missing an `entity:` key, or one whose value is not a plain
// string constant, is a scan failure (an error), never a silent skip: a
// costTable this scan cannot read is a costTable TestEveryCostTableEntityIs-
// InCostEntityTypes cannot check, which is exactly the gap this rewrite
// exists to close.
func costTableEntitiesFromSource(path string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}

	var entities []string
	var walkErr error
	ast.Inspect(f, func(n ast.Node) bool {
		if walkErr != nil {
			return false
		}
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		ident, ok := lit.Type.(*ast.Ident)
		if !ok || ident.Name != "costTable" {
			return true
		}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || key.Name != "entity" {
				continue
			}
			basic, ok := kv.Value.(*ast.BasicLit)
			if !ok || basic.Kind != token.STRING {
				walkErr = fmt.Errorf("a costTable{...} literal at %s has a non-literal "+
					"`entity` field, which this scan cannot read statically",
					fset.Position(lit.Pos()))
				return false
			}
			value, err := strconv.Unquote(basic.Value)
			if err != nil {
				walkErr = fmt.Errorf("unquoting entity field at %s: %w",
					fset.Position(basic.Pos()), err)
				return false
			}
			entities = append(entities, value)
		}
		return true
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return entities, nil
}
