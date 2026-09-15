// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestOnlyRecordIdentityRotationWritesLastRotated keeps the spec's "One writer
// for last_rotated" true structurally rather than by review, in the shape
// TestTheOnlyFactDeletingStatementIsThePrune (prune_test.go) and
// permit_source_test.go already use for the two other rules in this package
// that are one edit away from being quietly broken.
//
// WHY IT NEEDS A TEST AT ALL. The rule is not obvious from any one call site:
// a create form that also stamps a date looks like a convenience, and it buries
// the rotation inside a create snapshot where no reader looking for a rotation
// will find it. Declaring a credential that already exists and recording when
// it was last rotated are TWO ACTS and two audit entries, both of which are
// true. That is the cost and it is the right one.
//
// IT SCANS FOR ANY MENTION, not only writes. Today no read query names the
// column either -- the list page's rotation-state filter and RotationFindings
// both fold in Go from domain.Identity.RotationStatus, exactly as
// CertificateFilter.Host matches in Go rather than in SQL -- so the strict form
// costs nothing and a future read query is a deliberate edit to the map below
// rather than a diff nobody reads.
//
// WALKS AND PARSES ONE FILE AT A TIME (goStringLiterals's own technique,
// prune_test.go) RATHER THAN go/parser.ParseDir: ParseDir is deprecated since
// Go 1.25 -- it does not consider build tags when associating files with
// packages -- and this package's non-test .go files carry none, so the walk
// costs nothing and keeps staticcheck clean.
var lastRotatedWriters = map[string]string{
	"RecordIdentityRotation": "the rotation action itself, and the only thing in " +
		"this codebase permitted to write identity.last_rotated. It stamps the " +
		"column, logs through logUpdate as an ordinary 'update' (VerifyDependency " +
		"is the precedent in every respect), and returns nil without writing " +
		"anything at all when the submitted date already matches the stored one.",
}

func TestOnlyRecordIdentityRotationWritesLastRotated(t *testing.T) {
	const column = "last_" + "rotated"

	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "store")
	fset := token.NewFileSet()

	seen := map[string]bool{}
	found := 0
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading internal/store: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" ||
			strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil || !strings.Contains(strings.ToLower(s), column) {
					return true
				}
				found++
				name := fn.Name.Name
				seen[name] = true
				if _, allowed := lastRotatedWriters[name]; !allowed {
					t.Errorf("%s (%s) names %s in a SQL literal.\n"+
						"last_rotated has exactly ONE writer, RecordIdentityRotation "+
						"(docs/identity-surface-design.md, \"One writer for "+
						"last_rotated\"). Not create, not correct. If this is a "+
						"legitimate READ, add it to lastRotatedWriters in this test "+
						"with the reason -- a fourth writer, or a fourth reader "+
						"nobody argued for, is what this scan exists to stop.",
						name, rel, column)
				}
				return true
			})
		}
	}

	// A positive control, for the reason every census in this repository has
	// one: a scan that matches nothing reports success.
	if found == 0 {
		t.Fatalf("no SQL literal in internal/store mentions %s at all. Either the "+
			"rotation action has been deleted or this walk has stopped matching "+
			"the package -- either way this test is checking nothing.", column)
	}
	// And the other direction: an entry that stops applying fails as loudly as
	// a writer that is not listed, so the map cannot rot into decoration.
	for name, why := range lastRotatedWriters {
		if !seen[name] {
			t.Errorf("%s is listed as a %s writer (%q) and names it nowhere. A census "+
				"describing code that has moved on is one nobody can rely on.",
				name, column, why)
		}
	}
}
