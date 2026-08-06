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
	"sort"
	"strings"
	"testing"
)

// Column shapes, compared across engines.
//
// TestConstraintNamesMatchAcrossEngines compares a flat SET of CHECK-constraint
// names, which is less than its name promises: it cannot see which table a
// constraint sits on, cannot see the expression, and cannot see nullability or
// defaults at all. A database review found two divergences it was structurally
// incapable of catching, both in migration 00012:
//
//   - `amount_minor INTEGER` is int4 on PostgreSQL (a EUR 21.4M ceiling that
//     returns an unrecognised driver error, so it reaches HTTP as a 500) and
//     64-bit on SQLite.
//   - `id TEXT PRIMARY KEY` is NULLABLE on SQLite, and SQLite treats NULLs as
//     distinct in a unique index, so several rows can hold one. Such a row is
//     invisible to every statement keyed on `WHERE id = ?` and still counts
//     towards every total.
//
// This file is the missing half. It needs both engines; with only SQLite
// available it can compare nothing, and says so rather than passing.

type columnShape struct {
	notNull bool
	// declaredType is normalised to lower case. Comparing it catches a column
	// declared INTEGER in one file and BIGINT in the other, which is the shape
	// of the first divergence above.
	declaredType string
}

func liveColumnShapes(t *testing.T, db *DB, table string) map[string]columnShape {
	t.Helper()
	out := map[string]columnShape{}

	switch db.Driver {
	case DriverSQLite:
		var rows []struct {
			Name    string  `db:"name"`
			Type    string  `db:"type"`
			NotNull int     `db:"notnull"`
			PK      int     `db:"pk"`
			Dflt    *string `db:"dflt_value"`
		}
		if err := db.Reader.Select(&rows,
			`SELECT name, type, "notnull", pk, dflt_value FROM pragma_table_info(?)`, table); err != nil {
			t.Fatalf("reading the shape of %s: %v", table, err)
		}
		for _, r := range rows {
			// pragma reports pk>0 for a PRIMARY KEY column, and SQLite treats
			// such a column as NOT NULL only if it was spelled that way. That
			// asymmetry is the bug this test exists for, so the raw notnull
			// flag is what is compared -- never pk as a proxy for it.
			out[r.Name] = columnShape{
				notNull:      r.NotNull == 1,
				declaredType: strings.ToLower(strings.TrimSpace(r.Type)),
			}
		}
	case DriverPostgres:
		var rows []struct {
			Name     string `db:"column_name"`
			Nullable string `db:"is_nullable"`
			Type     string `db:"data_type"`
		}
		if err := db.Reader.Select(&rows, `
			SELECT column_name, is_nullable, data_type
			FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = $1`, table); err != nil {
			t.Fatalf("reading the shape of %s: %v", table, err)
		}
		for _, r := range rows {
			out[r.Name] = columnShape{
				notNull:      r.Nullable == "NO",
				declaredType: pgTypeAlias(r.Type),
			}
		}
	}
	return out
}

// pgTypeAlias maps what information_schema reports back to what the migration
// wrote, so the two engines are compared on the same vocabulary.
func pgTypeAlias(t string) string {
	switch strings.ToLower(t) {
	case "character varying", "text":
		return "text"
	case "integer":
		return "integer"
	case "bigint":
		return "bigint"
	case "boolean":
		return "boolean"
	case "bytea":
		return "blob"
	case "double precision", "real":
		return "real"
	default:
		return strings.ToLower(t)
	}
}

func sqliteTypeAlias(t string) string {
	switch t {
	case "bytea":
		return "blob"
	case "":
		return "text" // SQLite allows a bare column name; affinity is BLOB/TEXT
	default:
		return t
	}
}

func TestColumnShapesMatchAcrossEngines(t *testing.T) {
	engines := Engines(t)
	if len(engines) < 2 {
		t.Skip("both engines are required to compare them")
	}

	shapes := map[string]map[string]map[string]columnShape{}
	var tables []string
	for _, e := range engines {
		db := e.Open(t)
		perTable := map[string]map[string]columnShape{}
		for _, table := range liveTables(t, db) {
			if _, exempt := exemptFromShapeComparison[table]; exempt {
				continue
			}
			perTable[table] = liveColumnShapes(t, db, table)
		}
		shapes[e.Name] = perTable
		if tables == nil {
			for name := range perTable {
				tables = append(tables, name)
			}
			sort.Strings(tables)
		}
	}

	for _, table := range tables {
		sqlite, pg := shapes["sqlite"][table], shapes["postgres"][table]
		if pg == nil {
			continue // SQLite-only shadow table; the exempt map covers the rest
		}
		// Symmetric. Iterating the SQLite side alone meant a column added to the
		// PostgreSQL half only was invisible here -- it was caught by an
		// unrelated boundary test, by accident. A database review pointed that
		// out.
		for column := range pg {
			if _, ok := sqlite[column]; !ok {
				t.Errorf("%s.%s exists on PostgreSQL and not on SQLite", table, column)
			}
		}
		for column, want := range sqlite {
			got, ok := pg[column]
			if !ok {
				t.Errorf("%s.%s exists on SQLite and not on PostgreSQL", table, column)
				continue
			}
			if want.notNull != got.notNull {
				t.Errorf("%s.%s is NOT NULL on sqlite=%v postgres=%v.\n"+
					"A PRIMARY KEY does NOT imply NOT NULL on SQLite, and SQLite treats "+
					"NULLs as distinct in a unique index -- so several rows can hold one, "+
					"and each is invisible to every statement keyed on that column while "+
					"still counting towards every total.",
					table, column, want.notNull, got.notNull)
			}
			if sqliteTypeAlias(want.declaredType) != got.declaredType {
				t.Errorf("%s.%s is declared %q on sqlite and %q on postgres.\n"+
					"INTEGER is int4 on PostgreSQL (max 2,147,483,647) and 64-bit on "+
					"SQLite; a value that fits one and not the other is saved on one "+
					"engine and a 500 on the other.",
					table, column, want.declaredType, got.declaredType)
			}
		}
	}
}

// exemptFromShapeComparison are tables that exist on one engine only, or whose
// shape is owned by a library rather than by this repository.
var exemptFromShapeComparison = map[string]struct{}{
	"goose_db_version":         {},
	"goose_db_version_dialect": {},
	"sessions":                 {},
	"search_index":             {}, // FTS5 virtual table on SQLite, a real one on PostgreSQL
	"search_index_config":      {},
	"search_index_content":     {},
	"search_index_data":        {},
	"search_index_docsize":     {},
	"search_index_idx":         {},
	"sqlite_sequence":          {},
}

// TestIndexesMatchAcrossEngines.
//
// The third guard. Its first version compared index NAMES, which caught an index
// disappearing from one engine — and missed the thing the very next migration
// did. `00016` reshapes three indexes from `(team_id)` to `(team_id, lifecycle)`
// and the names do not change, so reverting the shape on one half left the whole
// suite green. A database review proved it by doing exactly that.
//
// So the comparison is now the TUPLE: name, table, ordered column list, and
// whether it is partial. Primary keys and unique constraints are included too —
// they were excluded from both halves before, which meant a UNIQUE could vanish
// from one engine and only a functional test that happened to exist would catch
// it.
//
// Known gap, stated rather than pretended away: two constraints with the same
// name and different expressions still pass. Normalising PostgreSQL's
// pg_get_constraintdef against SQLite's raw CHECK text is real work and nothing
// has diverged that way yet.
// indexesByDesign exist on one engine only, with the reason. Listed
// individually so a second one is a deliberate edit and a conversation.
//
// Full-text search is the codebase's ONE sanctioned dialect split (HANDOVER
// §4.4): FTS5 on SQLite, pg_trgm on PostgreSQL. The SQLite side needs no
// separate index because search_index IS a virtual table; the PostgreSQL side
// needs these two. Everything else must match.
var indexesByDesign = map[string]string{
	"idx_search_title_trgm": "pg_trgm index for the PostgreSQL search path; SQLite uses FTS5",
	"idx_search_body_trgm":  "pg_trgm index for the PostgreSQL search path; SQLite uses FTS5",
}

type indexShape struct {
	Name    string `db:"idx_name"`
	Table   string `db:"tbl_name"`
	Columns string `db:"cols"`
	Partial int    `db:"partial"`
}

func (i indexShape) String() string {
	return fmt.Sprintf("%s on %s(%s) partial=%d", i.Name, i.Table, i.Columns, i.Partial)
}

func liveIndexShapes(t *testing.T, db *DB) map[string]indexShape {
	t.Helper()
	var rows []indexShape

	switch db.Driver {
	case DriverSQLite:
		// origin 'c' is CREATE INDEX, 'pk' a primary key, 'u' a UNIQUE
		// constraint. All three, because all three can diverge.
		if err := db.Reader.Select(&rows, `
			SELECT il.name AS idx_name, m.name AS tbl_name,
			       (SELECT group_concat(ii.name, ',') FROM pragma_index_info(il.name) ii) AS cols,
			       il.partial
			FROM sqlite_master m
			JOIN pragma_index_list(m.name) il
			WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite_%'`); err != nil {
			t.Fatalf("reading sqlite index shapes: %v", err)
		}
	case DriverPostgres:
		if err := db.Reader.Select(&rows, `
			SELECT c.relname AS idx_name, t.relname AS tbl_name,
			       (SELECT string_agg(a.attname, ',' ORDER BY k.ord)
			          FROM unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
			          JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = k.attnum) AS cols,
			       (i.indpred IS NOT NULL)::int AS partial
			FROM pg_index i
			JOIN pg_class c ON c.oid = i.indexrelid
			JOIN pg_class t ON t.oid = i.indrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = current_schema()`); err != nil {
			t.Fatalf("reading postgres index shapes: %v", err)
		}
	}

	out := make(map[string]indexShape, len(rows))
	for _, r := range rows {
		// Tables owned by a library or private to FTS5, exempted by the same
		// list the column comparison uses: goose names its bookkeeping index
		// differently per engine, and the FTS5 shadow tables exist on SQLite
		// only. Neither is this repository's schema.
		if _, exempt := exemptFromShapeComparison[r.Table]; exempt {
			continue
		}
		// SQLite names its implicit constraint indexes sqlite_autoindex_*;
		// PostgreSQL names them after the constraint. Compared by SHAPE under a
		// table+columns key so the naming difference does not matter.
		key := r.Name
		if strings.HasPrefix(r.Name, "sqlite_autoindex_") || isConstraintIndexName(r.Name) {
			key = "constraint:" + r.Table + "(" + r.Columns + ")"
			r.Name = key
		}
		out[key] = r
	}
	return out
}

// isConstraintIndexName reports whether PostgreSQL generated this name for a
// primary key or unique constraint rather than a CREATE INDEX.
func isConstraintIndexName(name string) bool {
	return strings.HasSuffix(name, "_pkey") || strings.HasSuffix(name, "_key")
}

func TestIndexesMatchAcrossEngines(t *testing.T) {
	engines := Engines(t)
	if len(engines) < 2 {
		t.Skip("both engines are required to compare them")
	}

	shapes := map[string]map[string]indexShape{}
	for _, e := range engines {
		shapes[e.Name] = liveIndexShapes(t, e.Open(t))
	}

	report := func(a, b string) {
		var problems []string
		for key, want := range shapes[a] {
			if _, ok := indexesByDesign[want.Name]; ok {
				continue
			}
			got, present := shapes[b][key]
			if !present {
				problems = append(problems, fmt.Sprintf("%s exists on %s and not on %s",
					want, a, b))
				continue
			}
			if want.Columns != got.Columns || want.Partial != got.Partial {
				problems = append(problems, fmt.Sprintf("%s is %q on %s and %q on %s",
					key, want.String(), a, got.String(), b))
			}
		}
		sort.Strings(problems)
		for _, p := range problems {
			t.Errorf("%s\nOne engine gets a covering scan and the other does not, and "+
				"nothing says so until somebody profiles the slow deployment.", p)
		}
	}
	report("sqlite", "postgres")
	report("postgres", "sqlite")

	if len(shapes["sqlite"]) == 0 {
		t.Error("no indexes found at all; the comparison above was vacuous")
	}
}
