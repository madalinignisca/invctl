// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The structural half of the portability rule, written the way
// boundary_source_test.go is written: against the rule, not against the
// implementation.
//
// `make test` already runs the whole suite on SQLite and PostgreSQL, and for a
// query some test exercises that is the stronger check -- it runs the thing.
// But it only fires for covered queries, and coverage is exactly what a new
// domain does not have on the day its store lands. A `$1` in a rarely-taken
// branch, or an `ILIKE` in a filter nobody has written a case for yet, is green
// on both engines until somebody's production deployment is the first to run
// it. The portability constraint is one of the two things CLAUDE.md calls
// miserable to retrofit; the other one already has a source-level guard.
//
// So this reads the source and refuses the CONSTRUCT, whether or not any test
// reaches the statement containing it.
//
// It is a lexical check and it does not pretend otherwise: it knows the shapes
// on the forbidden list, not SQL. Something genuinely dialect-specific that
// none of these patterns name will pass. That is the same bargain
// parseSQLWrites makes next door, and the answer is the same -- when a new way
// to be unportable turns up, it goes on the list.

// dialectSpecific is every construct that does not run unmodified on both
// engines, with the reason it is refused. The reasons matter: a failure here
// arrives in front of somebody adding a domain, who needs to know what to write
// instead, not merely that they were wrong.
//
// Keyed by a regular expression so a word is matched as a word. `serial` must
// not fire on `serialise`, and `inet` must not fire on a column called
// `inet_family` that somebody invents later -- but `SERIAL` as a type and
// `INET` as a type must both fail on sight.
var dialectSpecific = []struct {
	pattern *regexp.Regexp
	name    string
	why     string
}{
	{regexp.MustCompile(`\$\d`), "a $n placeholder",
		"placeholders are always `?` and are rewritten by sqlx.Rebind before execution; a $1 runs on PostgreSQL and fails on SQLite"},

	// SERIAL in TYPE position, which is the awkward one. `serial` is also a
	// perfectly good column name -- asset.serial and certificate.serial hold
	// the manufacturer's serial number, and a pattern matching the bare word
	// fires on every query that reads one. So this matches a name followed by
	// the type followed by something a type is followed by: `id SERIAL PRIMARY
	// KEY` fails, `serial TEXT` and `COALESCE(serial, '')` do not.
	{regexp.MustCompile(`\b\w+\s+(big|small)?serial\s*(,|\)|primary|unique|not\b|check|default|$)`), "a SERIAL column",
		"IDs are UUIDv7 as TEXT, generated in Go -- never by the database, so they are known before the INSERT and sort by creation time"},

	{regexp.MustCompile(`::`), "a :: cast",
		"PostgreSQL cast syntax; write CAST(x AS type), which both engines accept"},

	{regexp.MustCompile(`\bilike\b`), "ILIKE",
		"PostgreSQL only; use LOWER(col) LIKE LOWER(?), which is what search.go already does"},

	{regexp.MustCompile(`\bjsonb\b|->>|->|@>|#>`), "a JSON operator or type",
		"attrs columns are opaque TEXT, unmarshalled in Go. If you need to filter on it, it is not an attribute -- promote it to a real column"},

	{regexp.MustCompile(`\barray\s*\[|=\s*any\s*\(`), "a native array",
		"arrays are PostgreSQL only; use a child table, which is also what makes the value auditable"},

	{regexp.MustCompile(`\bgenerate_series\b`), "generate_series()",
		"PostgreSQL only; generate the series in Go, where it can be tested without a database"},

	{regexp.MustCompile(`\bnum_nonnulls\b`), "num_nonnulls()",
		"PostgreSQL only; write the CHECK as an explicit boolean expression"},

	{regexp.MustCompile(`\bdistinct\s+on\b`), "DISTINCT ON",
		"PostgreSQL only; use a window function or resolve the pick in Go"},

	{regexp.MustCompile(`\bnow\s*\(|\bcurrent_(timestamp|date|time)\b`), "a database clock",
		"timestamps are RFC3339 UTC TEXT generated in Go and passed as parameters, so they sort lexicographically and do not depend on which machine ran the statement"},

	{regexp.MustCompile(`\b(inet|cidr|macaddr|macaddr8|ltree)\b`), "a PostgreSQL network or tree type",
		"addresses use the four-column pattern (addr_text, addr_family, addr_start, addr_end as BLOB), normalised in Go -- see HANDOVER.md 4.1"},

	{regexp.MustCompile(`\bexclude\s+using\b`), "an exclusion constraint",
		"PostgreSQL only; enforce it in the domain constructor, where the DB CHECK is the second line of defence"},

	{regexp.MustCompile(`\blanguage\s+(plpgsql|sql)\b`), "a stored procedure",
		"business logic lives in Go, where it is testable without an engine"},

	// CLAUDE.md forbids RETURNING on MULTI-ROW statements, and whether a
	// statement returns one row or many is not decidable from its text. This
	// codebase contains no RETURNING at all, so the guard holds the line it can
	// actually hold: zero. If a single-row RETURNING is ever genuinely wanted,
	// loosening this is one deleted entry and a sentence saying why.
	{regexp.MustCompile(`\breturning\b`), "RETURNING",
		"forbidden on multi-row statements, and single-row-ness is not readable off the statement; there are none today, so the guard holds zero. Read the row back if you need it"},
}

// TestNoQueryIsDialectSpecific reads every SQL statement out of the Go source
// and refuses the constructs that do not run on both engines.
//
// Whole tree, not just this package. A query assembled in a handler is as
// unportable as one assembled here, and the layering rule that keeps SQL in
// internal/store is a different rule with a different test.
func TestNoQueryIsDialectSpecific(t *testing.T) {
	root := repoRoot(t)
	scanned := 0

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)

		// sqlStatementsIn follows `+` concatenation and resolves package-level
		// string constants, so spelling the offending fragment in a const in
		// another file does not evade this -- the same evasion the observed
		// boundary had to close.
		for _, stmt := range sqlStatementsIn(t, path) {
			scanned++
			for _, bad := range dialectSpecific {
				if !bad.pattern.MatchString(scannableSQL(stmt.sql)) {
					continue
				}
				t.Errorf("%s:%d uses %s:\n\t%s\n%s",
					rel, stmt.line, bad.name, strings.TrimSpace(stmt.sql), bad.why)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}

	// A guard that scanned nothing passes for the wrong reason. This has caught
	// a broken walk before, in the file next door.
	if scanned == 0 {
		t.Fatal("no SQL statements found anywhere in the tree; this test would pass on an empty repository")
	}
	t.Logf("checked %d SQL statements in Go source", scanned)
}

// TestTheSharedSchemaRunsOnBothEngines applies the same list to
// migrations/shared, which is the half of the schema both engines execute
// verbatim.
//
// migrations/sqlite and migrations/postgres are deliberately exempt: they exist
// precisely so the two engines can be told different things about session
// storage and the search index. The rule is not "no dialect-specific SQL
// anywhere", it is "the dialect split is confined to the places that declare
// themselves to be one".
func TestTheSharedSchemaRunsOnBothEngines(t *testing.T) {
	dir := filepath.Join(repoRoot(t), "internal", "store", "migrations", "shared")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}

	files := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		files++
		// Comments are stripped first, and this is not a detail. These
		// migrations EXPLAIN the portability rule, which means they name the
		// forbidden constructs in prose -- "TEXT, not SERIAL" is a comment doing
		// its job, and a scanner that flagged it would be switched off by the
		// second person who tripped over it.
		body := stripSQLComments(readFile(t, filepath.Join(dir, entry.Name())))

		for _, bad := range dialectSpecific {
			if loc := bad.pattern.FindStringIndex(strings.ToLower(body)); loc != nil {
				t.Errorf("migrations/shared/%s uses %s near %q:\n%s",
					entry.Name(), bad.name, excerpt(body, loc[0]), bad.why)
			}
		}
	}
	if files == 0 {
		t.Fatal("no shared migrations found; this test would pass on an empty directory")
	}
	t.Logf("checked %d shared migrations", files)
}

// TestEveryDialectMigrationHasBothHalves asserts the two dialect directories
// carry the same version numbers.
//
// goose applies whatever it finds, so a missing PostgreSQL half is not an
// error -- it is a table that quietly does not exist on one engine. The suite
// catches that today only because something queries the table; the point of
// checking the numbers is that it fails at the migration, naming the file,
// rather than at whichever query happened to touch it first.
//
// Names are NOT required to match. 00001 is sqlite_specific.sql and
// postgres_specific.sql on purpose, because that pair is the one place the two
// schemas genuinely differ in kind rather than in syntax.
func TestEveryDialectMigrationHasBothHalves(t *testing.T) {
	root := filepath.Join(repoRoot(t), "internal", "store", "migrations")
	version := regexp.MustCompile(`^(\d+)_`)

	versions := map[string]map[string]string{} // dialect -> version -> filename
	for _, dialect := range []string{"sqlite", "postgres"} {
		versions[dialect] = map[string]string{}
		entries, err := os.ReadDir(filepath.Join(root, dialect))
		if err != nil {
			t.Fatalf("reading %s migrations: %v", dialect, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
				continue
			}
			m := version.FindStringSubmatch(entry.Name())
			if m == nil {
				t.Errorf("%s/%s does not start with a goose version number", dialect, entry.Name())
				continue
			}
			if other, dup := versions[dialect][m[1]]; dup {
				t.Errorf("%s has two migrations numbered %s: %s and %s. goose applies one and "+
					"records the version; the other is skipped forever.", dialect, m[1], other, entry.Name())
			}
			versions[dialect][m[1]] = entry.Name()
		}
	}

	if len(versions["sqlite"]) == 0 {
		t.Fatal("no dialect migrations found; this test would pass on an empty directory")
	}
	for _, n := range sortedVersions(versions["sqlite"], versions["postgres"]) {
		sqlite, onSQLite := versions["sqlite"][n]
		postgres, onPostgres := versions["postgres"][n]
		switch {
		case !onPostgres:
			t.Errorf("migration %s exists for SQLite (%s) but not for PostgreSQL. "+
				"A missing half is not a failed migration -- it is a table that silently "+
				"does not exist on one engine.", n, sqlite)
		case !onSQLite:
			t.Errorf("migration %s exists for PostgreSQL (%s) but not for SQLite.", n, postgres)
		}
	}
}

// scannableSQL lowercases a statement and drops its SQL comments, so a query
// that explains itself is judged on what it executes.
func scannableSQL(stmt string) string {
	return strings.ToLower(stripSQLComments(stmt))
}

// sqlBlockComment is used by stripSQLComments, which lives in prune_test.go
// because the retention guard needed it first.
var sqlBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)

// excerpt returns a short window around an offset, so a failure points at the
// line rather than printing a whole migration.
func excerpt(body string, at int) string {
	start := max(at-60, 0)
	end := min(at+60, len(body))
	return strings.Join(strings.Fields(body[start:end]), " ")
}

func sortedVersions(sets ...map[string]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, set := range sets {
		for n := range set {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out
}
