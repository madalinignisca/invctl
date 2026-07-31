package store

import (
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
// The third guard, and a database review proved it was needed by DELETING an
// index from one dialect half and watching the entire suite stay green. That is
// the same class of invisible divergence the other two were written for — one
// engine gets a covering scan and the other a sequential one, and nothing says
// so until somebody profiles the slow deployment.
//
// Names are compared, not definitions. Every index in this schema is named by
// convention and named identically in both halves, so a set comparison catches
// an index that is missing, renamed or added on one side. Comparing the column
// lists would be stronger; it would also mean normalising two very different
// catalogue shapes, and the failure mode this exists for is a whole index going
// missing rather than one drifting a column.
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

func TestIndexesMatchAcrossEngines(t *testing.T) {
	engines := Engines(t)
	if len(engines) < 2 {
		t.Skip("both engines are required to compare them")
	}

	perEngine := map[string]map[string]bool{}
	for _, e := range engines {
		db := e.Open(t)
		found := map[string]bool{}

		var names []string
		switch db.Driver {
		case DriverSQLite:
			// Explicitly created indexes only: sqlite_autoindex_* are the
			// engine's own for UNIQUE and PRIMARY KEY, which PostgreSQL names
			// differently and which the column-shape test already covers.
			if err := db.Reader.Select(&names, `
				SELECT name FROM sqlite_master
				WHERE type = 'index' AND sql IS NOT NULL AND name NOT LIKE 'sqlite_%'`); err != nil {
				t.Fatalf("listing sqlite indexes: %v", err)
			}
		case DriverPostgres:
			// Likewise: constraint-backed indexes carry the constraint's name
			// and are compared by TestConstraintNamesMatchAcrossEngines.
			if err := db.Reader.Select(&names, `
				SELECT i.indexname FROM pg_indexes i
				JOIN pg_class c ON c.relname = i.indexname
				LEFT JOIN pg_constraint con ON con.conindid = c.oid
				WHERE i.schemaname = current_schema() AND con.oid IS NULL`); err != nil {
				t.Fatalf("listing postgres indexes: %v", err)
			}
		}
		for _, n := range names {
			found[n] = true
		}
		perEngine[e.Name] = found
	}

	only := func(a, b string) []string {
		var out []string
		for name := range perEngine[a] {
			if !perEngine[b][name] {
				if _, ok := indexesByDesign[name]; ok {
					continue
				}
				out = append(out, name)
			}
		}
		sort.Strings(out)
		return out
	}
	if extra := only("sqlite", "postgres"); len(extra) > 0 {
		t.Errorf("indexes on SQLite and not on PostgreSQL:\n  %s\n"+
			"One engine gets a covering scan and the other a sequential one, and "+
			"nothing says so until somebody profiles the slow deployment.",
			strings.Join(extra, "\n  "))
	}
	if extra := only("postgres", "sqlite"); len(extra) > 0 {
		t.Errorf("indexes on PostgreSQL and not on SQLite:\n  %s",
			strings.Join(extra, "\n  "))
	}
	if len(perEngine["sqlite"]) == 0 {
		t.Error("no indexes found at all; the comparison above was vacuous")
	}
}
