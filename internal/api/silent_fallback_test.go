// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE SHAPE THIS TEST EXISTS FOR
//
// internal/web/handlers/silent_fallback_test.go documents five real defects
// that all shared one mechanism: a value arrives from a request, cannot be
// used, and is silently replaced by something indistinguishable from a
// legitimate answer. That test scans only its own package directory
// (os.ReadDir(".")), so it says nothing about internal/api — and internal/api
// is precisely the package that parses untrusted client input for this API
// (see ParsePageRequest). This is a sibling copy of that guard, scoped to
// this package, for the same reason the original exists: a discarded parse
// error here would let a malformed ?after= or ?limit= be silently replaced by
// a default, which for a client paginating a feed means re-ingesting page one
// forever instead of ever seeing an error.
//
// It is deliberately narrow, exactly as the original is. It cannot catch
// every silent fallback — only the one written as a thrown-away parse error.
func TestNoParseErrorIsDiscarded(t *testing.T) {
	// parsers whose error carries the "the operator typed something unusable"
	// signal. A discarded error from any of these is the shape.
	parsers := map[string]bool{
		"Atoi": true, "ParseInt": true, "ParseUint": true, "ParseFloat": true,
		"ParseBool": true, "Parse": true, "ParseTime": true, "ParseAddr": true,
		"ParsePrefix": true, "ParseMAC": true, "Unmarshal": true,
	}

	// allowed names a site where throwing the error away is correct, and why.
	// An entry here is a claim somebody has to defend in review; an empty map
	// is the goal.
	allowed := map[string]string{}

	// ParseFile over a directory listing rather than ParseDir, which is
	// deprecated, and rather than x/tools/go/packages, which would be a new
	// dependency for a test — invariant 7 in docs/ROADMAP.md.
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}

	var checked int
	{
		for _, entry := range entries {
			path := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parsing %s: %v", path, err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				assign, ok := n.(*ast.AssignStmt)
				if !ok || len(assign.Rhs) != 1 {
					return true
				}
				call, ok := assign.Rhs[0].(*ast.CallExpr)
				if !ok {
					return true
				}
				name := calleeName(call)
				if !parsers[name] {
					return true
				}
				checked++
				for i, lhs := range assign.Lhs {
					ident, ok := lhs.(*ast.Ident)
					if !ok || ident.Name != "_" {
						continue
					}
					// The error is conventionally last; a discarded first
					// return is a value nobody wanted, which is fine.
					if i != len(assign.Lhs)-1 {
						continue
					}
					pos := fset.Position(assign.Pos())
					where := fmt.Sprintf("%s:%d", filepath.Base(path), pos.Line)
					if reason, ok := allowed[where]; ok {
						t.Logf("allowed: %s — %s", where, reason)
						continue
					}
					t.Errorf("%s discards the error from %s.\n"+
						"A value that will not parse must be REFUSED, not replaced by a "+
						"zero the operator cannot tell from a real one. Return whether it "+
						"was usable and let the caller decide — see ParsePageRequest. If "+
						"throwing it away is genuinely right here, add it to `allowed` with "+
						"the reason.", where, name)
				}
				return true
			})
		}
	}
	if checked == 0 {
		t.Fatal("no parse calls were examined; this test is asserting nothing")
	}
	t.Logf("%d parse calls examined", checked)
}

func calleeName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fn.Sel.Name
	case *ast.Ident:
		return fn.Name
	}
	return ""
}
