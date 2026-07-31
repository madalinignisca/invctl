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
