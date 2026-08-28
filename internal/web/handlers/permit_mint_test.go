// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// widePermitMinters are the domain constructors that produce a permit
// covering things the signed-in caller may not be entitled to. A handler that
// calls one is asserting authority on the request's behalf instead of asking
// what the request actually has.
var widePermitMinters = map[string]bool{
	"AdministratorPermit": true,
	"SystemPermit":        true,
}

// TestNoHandlerMintsAPermitWiderThanItsCaller closes a class of bug, not an
// instance of one.
//
// WP-G1 Task 7 needed a domain.Permit at handler call sites before the
// request-scoped gate existed, so it minted domain.AdministratorPermit(actor(r))
// and justified it in a comment: every route in the file sits behind
// RequireWrite, so the caller is already an Administrator. That reasoning was
// true when written and expires at Task 13, which makes auth.CanWrite true
// for a project owner -- one line, in another package, nowhere near the
// handlers relying on it. Six sites had it, including UserSetRole, where a
// project owner admitted by RequireWrite while holding an administrator
// permit could have granted themselves the Administrator role outright.
//
// The rule: a handler takes the caller's own permit from a.permit(r) and
// never mints a wider one. Where a route really is administrator-only, the
// caller's permit already says so; where it is not, minting one is how the
// gate gets bypassed.
//
// Deliberately a source scan rather than a behavioural test: the failure it
// guards is a call site that LOOKS correct and is only wrong because of a
// change elsewhere, so there is no request that demonstrates it until the
// day it is exploitable.
func TestNoHandlerMintsAPermitWiderThanItsCaller(t *testing.T) {
	var offenders []string
	err := filepath.Walk(".", func(path string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			return perr
		}
		var fn string
		ast.Inspect(f, func(n ast.Node) bool {
			if d, ok := n.(*ast.FuncDecl); ok {
				fn = d.Name.Name
			}
			ce, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			se, ok := ce.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := se.X.(*ast.Ident)
			if !ok || pkg.Name != "domain" || !widePermitMinters[se.Sel.Name] {
				return true
			}
			offenders = append(offenders,
				path+" "+fn+" mints domain."+se.Sel.Name)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scanning handler sources: %v", err)
	}
	for _, o := range offenders {
		t.Errorf("%s -- use a.permit(r); a handler must not mint a permit "+
			"wider than its caller's (see this test's comment)", o)
	}
}
