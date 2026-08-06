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
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/gabriel/invctl/internal/domain"
)

// The structural half of docs/AUDIT.md rule 1, written against the rule rather
// than against the implementation.
//
// Rule 1's closing sentence is the reason this file exists: "If a boundary can
// only be described as 'must not call X', it is not implemented." The rule then
// names three mechanisms, and a test that checked only the runtime behaviour
// would confirm none of them -- an observed write that happens not to touch a
// declared table today is indistinguishable from one that cannot.
//
// So this reads the source. Go files are parsed with go/ast rather than
// grepped, because internal/store/observed.go necessarily NAMES the tables it
// is forbidden to write, in comments explaining why. A grep flags the
// explanation as the violation, which is how a structural test gets disabled by
// the second person who trips over it.

// ---------------------------------------------------------------------------
// The observed write path
// ---------------------------------------------------------------------------

// observedPathFile is the one file rule 1 permits observed writes in.
const observedPathFile = "internal/store/observed.go"

// declaredReadAllowlist is every declared table internal/store/observed.go may
// name at all, and rule 6 is what fixes the list at exactly these four.
//
// The rule requires two reads on this path and no more: "is this thing in the
// inventory at all" -- because an observation for an unknown entity is 404,
// never created -- and "is it inside this credential's environment scope". Two
// questions, four tables: asset and service_instance answer the first,
// asset_environment and environment answer the second. Anything else appearing
// here is a declared table the observed path has acquired an interest in, which
// is the drift rule 1 is written to catch while it is still one line.
var declaredReadAllowlist = map[string]string{
	"asset":             "rule 6's existence check: an observation for an unknown entity is 404, never created",
	"service_instance":  "rule 6's existence check for a placement",
	"asset_environment": "rule 6's scope check: which environments the entity sits in",
	"environment":       "rule 6's scope check: the environment's code",
}

// TestObservedPathTouchesNoDeclaredTable is the boundary test docs/AUDIT.md
// names for rule 1.
//
// It parses internal/store/observed.go and fails on any write naming a table or
// a column off the observed allowlist, which is domain.ObservedTables. The
// column half is checked against the LIVE schema on both engines, so "a column
// off the allowlist" means a column that is not on the observed table being
// written -- a fact about the database, not about a list somebody maintains in
// a test.
//
// Three properties, matching the three mechanisms rule 1 names:
//
//  1. Every write statement in that file targets an observed table, and every
//     column it names belongs to that table.
//  2. The webhook's store type is ObservedStore -- two methods -- and *SQLStore
//     deliberately does not satisfy it, so reaching for a declared mutation
//     does not compile.
//  3. The file cannot reach the audit-log helpers, because the transaction type
//     it holds does not have them.
func TestObservedPathTouchesNoDeclaredTable(t *testing.T) {
	path := filepath.Join(repoRoot(t), observedPathFile)
	stmts := sqlStatementsIn(t, path)
	if len(stmts) == 0 {
		t.Fatalf("no SQL found in %s; this test would pass on an empty file", observedPathFile)
	}

	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			db := migrated(t, e)

			// Columns come from the live schema so this cannot drift from the
			// migration. A write naming a column that does not exist is caught
			// here rather than at 03:00 inside a webhook.
			observedSchema := map[string]map[string]bool{}
			for table := range domain.ObservedTables {
				observedSchema[table] = map[string]bool{}
				for _, col := range liveColumns(t, db, table) {
					observedSchema[table][col] = true
				}
			}

			for _, stmt := range stmts {
				for _, w := range parseSQLWrites(stmt.sql) {
					where := fmt.Sprintf("%s:%d", observedPathFile, stmt.line)

					if w.table == dynamicTable {
						t.Errorf("%s: a %s statement whose table is built at run time:\n\t%s\n"+
							"A write whose target cannot be read off the source is a write this "+
							"boundary cannot check, and rule 1 says a boundary that can only be "+
							"described in prose is not implemented.", where, w.verb, stmt.sql)
						continue
					}
					if !domain.ObservedTables[w.table] {
						t.Errorf("%s: %s writes to %q, which is not an observed table.\n"+
							"Observed writes live only in %s and may write only %s. A reported "+
							"`down` never sets lifecycle, never sets desired_state, never deletes "+
							"a placement -- only a person retires something (rule 2).",
							where, w.verb, w.table, observedPathFile, observedTableList())
						continue
					}
					for _, col := range w.columns {
						if !observedSchema[w.table][col] {
							t.Errorf("%s: %s names %s.%s, which is not a column of that table "+
								"in the live %s schema", where, w.verb, w.table, col, e.Name)
						}
					}
				}
			}
		})
	}

	// Reads are not writes, and rule 6 requires exactly two of them on this
	// path. Everything else declared must be absent entirely: a declared table
	// that appears in a SELECT today is the one that appears in an UPDATE after
	// the next refactor, and by then the diff looks small.
	t.Run("the only declared tables it names at all are rule 6's two checks", func(t *testing.T) {
		forbidden := nonObservedTableNames()
		for _, stmt := range stmts {
			for _, word := range sqlIdentifiers(stmt.sql) {
				if !forbidden[word] {
					continue
				}
				if _, allowed := declaredReadAllowlist[word]; allowed {
					continue
				}
				t.Errorf("%s:%d names the declared table %q:\n\t%s\n"+
					"Rule 6 allows this path two reads -- does the entity exist, and is it "+
					"in this credential's environment scope. Anything else is an interest "+
					"the observed path should not have.", observedPathFile, stmt.line, word, stmt.sql)
			}
		}
	})

	// Rule 3: "Observed writes call neither logUpdate nor logCreate ... and do
	// not call indexEntity." The mechanism rule 1 asks for is that they cannot,
	// not that they do not -- so assert the names are absent from the file
	// rather than that the change_log stayed empty, which is the runtime
	// assertion TestObservedWriteLeavesDeclaredColumnsUntouched already makes.
	t.Run("it cannot reach the audit trail or the search index", func(t *testing.T) {
		forbiddenCalls := map[string]string{
			"logCreate":         "an observed write is not a decision; rule 3 logs transitions to observed_transition",
			"logUpdate":         "diffJSON compares every db-tagged field except updated_at, so this logs every heartbeat via the moving timestamp",
			"log":               "change_log is for declared change only (rule 3)",
			"indexEntity":       "rule 3: reindexing per heartbeat rewrites the FTS table thousands of times a day against a single SQLite writer",
			"indexAsset":        "rule 13: search_index is built from declared columns only",
			"indexService":      "rule 13: search_index is built from declared columns only",
			"write":             "SQLStore.write carries a domain.Actor and the audit helpers with it",
			"writeSerializable": "SQLStore.write carries a domain.Actor and the audit helpers with it",
			"writeTx":           "SQLStore.write carries a domain.Actor and the audit helpers with it",
		}
		for name, pos := range methodCallsIn(t, path) {
			if why, bad := forbiddenCalls[name]; bad {
				t.Errorf("%s:%d calls .%s(). %s", observedPathFile, pos, name, why)
			}
		}
	})

	// Rule 1's second mechanism, stated as a fact about the types rather than
	// as a convention: "the webhook handler's store field is typed
	// store.ObservedStore ... and never *SQLStore, so overreach is a compile
	// error". That is only true if *SQLStore does not satisfy the interface --
	// otherwise every handler is one assignment away from the whole store.
	t.Run("the webhook's store type cannot reach a declared mutation", func(t *testing.T) {
		iface := reflect.TypeOf((*ObservedStore)(nil)).Elem()
		if n := iface.NumMethod(); n != 2 {
			t.Errorf("ObservedStore has %d methods, want 2 (RecordObservation, GetObservedState). "+
				"Every method added here is another declared mutation a monitoring "+
				"credential's request path can reach.", n)
		}
		for _, want := range []string{"RecordObservation", "GetObservedState"} {
			if _, ok := iface.MethodByName(want); !ok {
				t.Errorf("ObservedStore has no %s; rule 1 names both methods", want)
			}
		}
		if reflect.TypeOf(&SQLStore{}).Implements(iface) {
			t.Error("*SQLStore satisfies ObservedStore, so a webhook handler can be given the " +
				"whole store by a one-word change and nothing fails to compile. Rule 1 " +
				"requires overreach to be a compile error, and this is what makes it one.")
		}
	})
}

// TestOnlyTheObservedPathWritesTheObservedTables is rule 1's first mechanism:
// "observed writes live only in internal/store/observed.go".
//
// The named boundary test above proves that file writes nothing declared. This
// proves the converse -- that nothing else writes observed -- and without it the
// separation holds in one direction only. A second writer somewhere else would
// bypass the transition rule, the monotonicity guard and the buffer, all of
// which live in that one file.
func TestOnlyTheObservedPathWritesTheObservedTables(t *testing.T) {
	// The prune is the sanctioned exception and rule 10 names it: the retention
	// job is the only DELETE FROM in this codebase that removes a fact, and it
	// reaches observed_transition and nothing else.
	allowed := map[string]map[string]bool{
		observedPathFile:          {"insert": true, "update": true, "delete": true},
		"internal/store/prune.go": {"delete": true},
	}

	root := repoRoot(t)
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

		for _, stmt := range sqlStatementsIn(t, path) {
			for _, w := range parseSQLWrites(stmt.sql) {
				if !domain.ObservedTables[w.table] {
					continue
				}
				if allowed[rel][w.verb] {
					continue
				}
				t.Errorf("%s:%d %ss %s. Observed writes live only in %s, which is what makes "+
					"the transition rule, the monotonicity guard and rule 4's buffer "+
					"unavoidable rather than merely usual.", rel, stmt.line, w.verb, w.table, observedPathFile)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}
}

// TestNoAssembledWriteReachesChangeLog closes a gap in TestChangeLogIsAppendOnly.
//
// That test inspects each string literal on its own, which is right for the
// shapes this codebase contains but blind to one it does not: `"DELETE " +
// "FROM " + "change_log ..."` is four literals, none of which contains the
// forbidden phrase, and it evades the check entirely. Verified by writing
// exactly that into internal/store/prune.go and watching the suite stay green
// while the audit trail was being deleted.
//
// It is not a hypothetical shape either. It is what a statement built around a
// table name looks like, and rule 10's whole argument is that a prune which can
// be pointed at a table will eventually be pointed at change_log. So the same
// prohibition is asserted again over statements assembled across `+`, which is
// what sqlStatementsIn already follows.
// dynamicTargetAllowlist is every file permitted to name its table at run time,
// with the reason. Listed individually so a second one is a deliberate edit and
// a conversation, rather than a diff nobody reads.
//
// KEYED BY FILE, BUDGETED BY COUNT. A per-file exemption means any FUTURE
// dynamic-table write added anywhere in one of these four files is silently
// exempt -- which is the exact drift this test exists to catch, inside the four
// files most likely to grow one. A security review pointed that out. Matching on
// the enclosing function would be tighter still, but the count is the cheap
// version of the same guarantee: add a fifth dynamic write to costs.go and this
// fails until somebody says so out loud.
var dynamicTargetAllowlist = map[string]string{
	// retireRows(ctx, t, table, ids, at) predates M6 and is the shared cascade
	// helper behind every net_* retirement. Every caller passes a literal, and
	// the statement it builds sets lifecycle and updated_at -- neither of which
	// exists on change_log, so pointing it there would fail rather than delete.
	// It stays on the list because "could not currently work" is a weaker
	// guarantee than "cannot be expressed", and rule 10 asks for the second.
	"internal/store/reach.go": "retireRows: the shared net_* cascade, called only with literal table names",

	// UpsertVocabularyTerm names one of the seven lookup tables. The name never
	// comes from a request: it is checked against vocabularyQueries, which is
	// the allow-list, before any statement is built -- and change_log is not in
	// it, so the table cannot be reached even by a caller that tried. The
	// statement also sets label/sort_order/description, none of which
	// change_log has.
	"internal/store/vocabulary.go": "UpsertVocabularyTerm: table checked against vocabularyQueries before use",

	// releaseLinks and retireLink name project_asset or project_service, both
	// literals at the four call sites; nothing derives the name from input.
	// They set lifecycle and updated_at, which change_log does not have, and
	// rule 10's concern is a statement that COULD be pointed at the audit
	// table -- these are two fixed strings chosen in Go, not a parameter.
	//
	// The alternative was writing each statement out twice, once per table.
	// That trades a guard for a copy-paste pair which will diverge the first
	// time somebody fixes one of them, which is the worse failure.
	"internal/store/projects.go": "releaseLinks/retireLink: called only with the two literal project link tables",

	// updateCost and retireCost name asset_cost, service_cost or project_cost.
	// The three are package-level VALUES (costOnAsset, costOnService,
	// costOnProject), not strings a caller supplies, and each public method
	// names its own -- the entity type never travels in from a request, which
	// is also why the routes are three URLs rather than /costs/{type}/{id}.
	// Both statements set lifecycle/amount_minor/valid_from, none of which
	// change_log has, so the audit table is unreachable in fact as well as by
	// construction.
	//
	// The alternative was three near-identical copies of each statement. They
	// would diverge the first time somebody fixed one of them, and a rollup
	// that totals three subtly different shapes is a worse failure than a
	// generated table name whose inputs are three constants.
	"internal/store/costs.go": "updateCost/retireCost: three package-level costTable values, never a caller's string",

	// deployCertificate/undeployCertificate name certificate_asset or
	// certificate_service. Both are literals at the four public call sites --
	// DeployCertificateToAsset and its three siblings each hardcode their own --
	// and no entity type travels in from a request, which is also why the routes
	// are separate URLs rather than /certificates/{id}/deploy/{type}. The
	// statements set lifecycle and note, neither of which change_log has.
	"internal/store/certificates.go": "deployCertificate/undeployCertificate: two literal link tables from four fixed call sites",
}

// dynamicTargetBudget is how many dynamic non-insert writes each allowlisted
// file is expected to contain. Exact, not a maximum: a write that DISAPPEARS is
// worth knowing about too, because it usually means the statement moved
// somewhere that is not on this list.
var dynamicTargetBudget = map[string]int{
	"internal/store/reach.go":      1, // retireRows
	"internal/store/vocabulary.go": 2, // UpsertVocabularyTerm: its INSERT and its UPDATE
	"internal/store/projects.go":   2, // releaseLinks, retireLink
	"internal/store/costs.go":      3, // addCost's INSERT, updateCost, retireCost
	// deployCertificate: the existence SELECT is not a write; its reactivation
	// UPDATE and its INSERT are, and undeployCertificate's UPDATE makes three.
	"internal/store/certificates.go": 3,
}

func TestNoAssembledWriteReachesChangeLog(t *testing.T) {
	root := repoRoot(t)
	dynamicSeen := map[string]int{}
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

		for _, stmt := range sqlStatementsIn(t, path) {
			for _, w := range parseSQLWrites(stmt.sql) {
				switch {
				case w.table == "change_log" && w.verb != "insert":
					t.Errorf("%s:%d %ss change_log. It is append-only: no UPDATE, no DELETE, "+
						"anywhere, ever. Correcting a wrong entry means writing a new one, and "+
						"the rule 10 prune targets observed_transition only.", rel, stmt.line, w.verb)

				// INSERT is checked too. The switch used to exempt it, which
				// defended DELETION of history and not FABRICATION of it: a
				// runtime-assembled INSERT aimed at change_log would forge audit
				// rows and the whole suite would stay green. A security review
				// pointed out that certificates.go already contains such an
				// INSERT -- safe today, invisible to the test either way.
				case w.table == dynamicTable:
					if _, ok := dynamicTargetAllowlist[rel]; ok {
						dynamicSeen[rel]++
						continue
					}
					t.Errorf("%s:%d builds a %s target at run time:\n\t%s\n"+
						"A statement that names its own table can eventually be pointed at "+
						"change_log, which is rule 10's argument for why the prune takes no "+
						"table name. If this one is safe, add it to dynamicTargetAllowlist "+
						"with the reason.", rel, stmt.line, w.verb, stmt.sql)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}

	// The allowlist exempts a FILE, so without this a future dynamic write
	// added anywhere in one of them inherits the exemption silently -- the
	// exact drift this test exists to catch.
	for file, want := range dynamicTargetBudget {
		if got := dynamicSeen[file]; got != want {
			t.Errorf("%s has %d dynamic write statements, expected %d (%s).\n"+
				"A NEW one inherits the file's allowlist entry without anybody "+
				"reading it; one that DISAPPEARED usually moved somewhere that is "+
				"not on the list. Either way, say so here on purpose.",
				file, got, want, dynamicTargetAllowlist[file])
		}
	}
}

// ---------------------------------------------------------------------------
// Reading SQL out of Go source
// ---------------------------------------------------------------------------

// dynamicTable marks a write whose target was assembled at run time. It is not
// a table name; it is the absence of one, and it fails the test on sight.
const dynamicTable = "\x00dynamic"

type sqlStatement struct {
	sql  string
	line int
}

// sqlStatementsIn returns every string in a Go file that looks like SQL,
// including strings assembled by concatenation.
//
// Concatenation is followed because `"DELETE FROM " + table` is the exact shape
// that would slip past a literal-only scan, and it is also the shape a prune
// that can be pointed anywhere would take. A non-literal operand becomes
// dynamicTable rather than being skipped, so the hole is loud instead of empty.
func sqlStatementsIn(t *testing.T, path string) []sqlStatement {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	// Constants from the WHOLE package, not just this file.
	//
	// Scoping this to the file under inspection left the boundary trivially
	// evadable: declare `const q = "UPDATE asset SET lifecycle = ..."` in
	// health.go, call w.exec(ctx, q, ...) from observed.go, and the argument is
	// a bare identifier this walker resolved to nothing. No statement, no
	// check, and both boundary tests pass while the observed path retires an
	// asset. Rule 1's whole claim is that the boundary is structural rather
	// than a convention, and a guard that a rename steps around is a
	// convention.
	consts := packageStringConstants(t, filepath.Dir(path))
	for name, value := range fileStringConstants(file) {
		consts[name] = value
	}

	var out []sqlStatement
	seen := map[token.Pos]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		var text string
		var pos token.Pos
		switch node := n.(type) {
		case *ast.BinaryExpr:
			if node.Op != token.ADD || seen[node.Pos()] {
				return true
			}
			text, pos = flattenStringConcat(node, consts)
			// Mark everything inside, so neither the literals nor the nested
			// `+` nodes of the same expression are reported a second time.
			ast.Inspect(node, func(inner ast.Node) bool {
				switch inner.(type) {
				case *ast.BasicLit, *ast.BinaryExpr:
					seen[inner.Pos()] = true
				}
				return true
			})
		case *ast.BasicLit:
			if node.Kind != token.STRING || seen[node.Pos()] {
				return true
			}
			unquoted, err := strconv.Unquote(node.Value)
			if err != nil {
				return true
			}
			text, pos = unquoted, node.Pos()
		case *ast.Ident:
			// A bare identifier standing in for a query string. This is the
			// evasion above: the SQL is real, it is simply spelled somewhere
			// else.
			if seen[node.Pos()] {
				return true
			}
			value, ok := consts[node.Name]
			if !ok {
				return true
			}
			text, pos = value, node.Pos()
		default:
			return true
		}
		if looksLikeSQL(text) {
			out = append(out, sqlStatement{sql: text, line: fset.Position(pos).Line})
		}
		return true
	})
	return out
}

// flattenStringConcat renders a `+` expression, substituting dynamicTable for
// anything whose value cannot be read off the source.
//
// File-level string constants ARE read off the source -- `prunableTable` is a
// const in prune.go and resolving it is how the retention statements stay
// checkable while still being assembled. What stays dynamic is a parameter, a
// function call or an identifier from another file: those are the shapes where
// the table this statement targets is genuinely not knowable here, which is the
// property the check is about.
func flattenStringConcat(expr ast.Expr, consts map[string]string) (string, token.Pos) {
	switch node := expr.(type) {
	case *ast.BinaryExpr:
		if node.Op != token.ADD {
			return " " + dynamicTable + " ", node.Pos()
		}
		left, pos := flattenStringConcat(node.X, consts)
		right, _ := flattenStringConcat(node.Y, consts)
		return left + right, pos
	case *ast.BasicLit:
		if node.Kind != token.STRING {
			return " " + dynamicTable + " ", node.Pos()
		}
		unquoted, err := strconv.Unquote(node.Value)
		if err != nil {
			return " " + dynamicTable + " ", node.Pos()
		}
		return unquoted, node.Pos()
	case *ast.Ident:
		if value, ok := consts[node.Name]; ok {
			return value, node.Pos()
		}
		return " " + dynamicTable + " ", node.Pos()
	default:
		return " " + dynamicTable + " ", expr.Pos()
	}
}

// fileStringConstants collects package-level `const name = "literal"` and
// `var name = "literal"` declarations, so a statement assembled around one is
// still readable.
// packageStringConstants collects every package-level string constant and var
// in a directory, across all non-test files, so a query defined in one file and
// used in another is still visible to the boundary check.
func packageStringConstants(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		for k, v := range fileStringConstants(file) {
			out[k] = v
		}
	}
	return out
}

func fileStringConstants(file *ast.File) map[string]string {
	out := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != len(value.Values) {
				continue
			}
			for i, name := range value.Names {
				lit, ok := value.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if unquoted, err := strconv.Unquote(lit.Value); err == nil {
					out[name.Name] = unquoted
				}
			}
		}
	}
	return out
}

func looksLikeSQL(text string) bool {
	lower := strings.ToLower(text)
	for _, verb := range []string{"select ", "insert ", "update ", "delete ", "replace ", "truncate "} {
		if strings.Contains(lower, verb) {
			return true
		}
	}
	return false
}

// sqlWrite is one write clause: what it does, to which table, naming which
// columns.
type sqlWrite struct {
	verb    string
	table   string
	columns []string
}

// parseSQLWrites extracts the write clauses from a statement.
//
// Deliberately a small hand-rolled scanner rather than a dependency: the shapes
// this codebase actually contains are INSERT ... ON CONFLICT DO UPDATE SET,
// UPDATE ... SET ... WHERE, and DELETE FROM ... WHERE, and a parser that
// understood more of SQL than that would be harder to trust, not easier.
func parseSQLWrites(stmt string) []sqlWrite {
	tokens := tokeniseSQL(stmt)
	var out []sqlWrite
	lastInsert := ""

	for i := 0; i < len(tokens); i++ {
		switch tokens[i] {
		case "insert", "replace":
			if i+2 >= len(tokens) || tokens[i+1] != "into" {
				continue
			}
			w := sqlWrite{verb: "insert", table: tokens[i+2]}
			w.columns = parenList(tokens, i+3)
			lastInsert = w.table
			out = append(out, w)
			i += 2

		case "delete", "truncate":
			j := i + 1
			if j < len(tokens) && (tokens[j] == "from" || tokens[j] == "table") {
				j++
			}
			if j >= len(tokens) {
				continue
			}
			out = append(out, sqlWrite{verb: "delete", table: tokens[j]})
			i = j

		case "update":
			// `ON CONFLICT ... DO UPDATE SET` is not a second table: it is the
			// conflict arm of the INSERT above it, and its columns belong to
			// that table.
			if i+1 < len(tokens) && tokens[i+1] == "set" {
				if lastInsert != "" {
					out = append(out, sqlWrite{
						verb: "update", table: lastInsert, columns: assignmentTargets(tokens, i+2),
					})
				}
				i++
				continue
			}
			if i+1 >= len(tokens) {
				continue
			}
			w := sqlWrite{verb: "update", table: tokens[i+1]}
			if i+2 < len(tokens) && tokens[i+2] == "set" {
				w.columns = assignmentTargets(tokens, i+3)
			}
			out = append(out, w)
			i++
		}
	}
	return out
}

// parenList reads a parenthesised, comma-separated identifier list starting at
// i, or nothing when i does not begin one.
func parenList(tokens []string, i int) []string {
	if i >= len(tokens) || tokens[i] != "(" {
		return nil
	}
	var out []string
	for j := i + 1; j < len(tokens); j++ {
		switch tokens[j] {
		case ")":
			return out
		case ",":
		default:
			out = append(out, tokens[j])
		}
	}
	return out
}

// assignmentTargets reads the left-hand sides of a SET clause: every identifier
// immediately followed by `=`, stopping at the clause that ends it.
func assignmentTargets(tokens []string, i int) []string {
	var out []string
	for j := i; j+1 < len(tokens); j++ {
		switch tokens[j] {
		case "where", "returning", "from":
			return out
		}
		if tokens[j+1] == "=" && isIdentifier(tokens[j]) && !strings.Contains(tokens[j], ".") {
			out = append(out, tokens[j])
		}
	}
	return out
}

// tokeniseSQL lower-cases a statement and splits it so that punctuation the
// scanner cares about is its own token.
func tokeniseSQL(stmt string) []string {
	replaced := strings.NewReplacer(
		"(", " ( ", ")", " ) ", ",", " , ", "=", " = ", ";", " ; ",
	).Replace(strings.ToLower(stmt))
	return strings.Fields(replaced)
}

func isIdentifier(tok string) bool {
	if tok == "" {
		return false
	}
	for _, r := range tok {
		if r != '_' && r != '.' && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// sqlIdentifiers returns the distinct bare identifiers in a statement, so a
// table name is matched as a word: `asset` must not match `asset_health`.
func sqlIdentifiers(stmt string) []string {
	seen := map[string]bool{}
	var out []string
	for _, tok := range tokeniseSQL(stmt) {
		for _, part := range strings.Split(tok, ".") {
			if part == "" || seen[part] || !isIdentifier(part) {
				continue
			}
			seen[part] = true
			out = append(out, part)
		}
	}
	return out
}

// nonObservedTableNames is every table in the classification that is not an
// observed one, plus the two derived tables the census exempts but which still
// hold data.
func nonObservedTableNames() map[string]bool {
	out := map[string]bool{
		"search_index": true,
		"sessions":     true,
	}
	for _, table := range domain.ClassifiedTables() {
		if !domain.ObservedTables[table] {
			out[table] = true
		}
	}
	return out
}

func observedTableList() string {
	out := make([]string, 0, len(domain.ObservedTables))
	for table := range domain.ObservedTables {
		out = append(out, table)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// methodCallsIn returns every method name called in a file, mapped to the line
// of its first appearance.
func methodCallsIn(t *testing.T, path string) map[string]int {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	out := map[string]int{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if _, seen := out[sel.Sel.Name]; !seen {
			out[sel.Sel.Name] = fset.Position(sel.Sel.Pos()).Line
		}
		return true
	})
	return out
}
