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
	"sort"
	"strconv"
	"strings"
	"testing"
)

// refusalFlashExceptions are the functions allowed to answer a refusal with a
// flash and a redirect instead of re-rendering the form. Each needs a reason
// that survives somebody reading it cold, because the default is the rule in
// CLAUDE.md: "Validation failure returns 422 with the form partial re-rendered."
//
// A stale entry fails as loudly as a missing one -- naming a function that no
// longer flashes is a claim about the code that has stopped being true, and a
// census carrying those is worse than no census at all.
var refusalFlashExceptions = map[string]string{
	"refuseAssetEdit": "the ONE case with no form to return to. An IP address " +
		"can exist with no interface, so there is no asset page to re-render; " +
		"the handler says so in place. Every other branch of this same function " +
		"re-renders through renderAssetDetail with refusalStatus.",

	"HealthOverrideClear": "there is nothing to preserve. Clearing an override " +
		"is a button with no fields, so a re-render would hand back an empty " +
		"form the operator never filled in. The message belongs on the page " +
		"they are looking at, which is where the flash puts it.",

	"HealthOverrideAmend": "the amend form lives inside the health banner on " +
		"the entity's own page, and entityURL dispatches across asset, service " +
		"and circuit -- so re-rendering means three page assemblies reached " +
		"through a runtime type switch. NAMED DEBT, not a principled exception: " +
		"the operator does lose the reason they typed. Same fan-out as " +
		"afterCostWrite below, and worth doing in one piece with it.",

	"afterCostWrite": "one helper serving twelve call sites across /assets, " +
		"/services, /projects and /circuits, and /projects has no " +
		"render*Detail assembly at all. Converting it means building one and " +
		"threading a re-render callback through every caller. NAMED DEBT, not a " +
		"principled exception -- the comment above it argues the trade was " +
		"deliberate, and that argument was true when renderAssetDetail did not " +
		"exist. It does now, and network.go uses it for exactly this.",
}

// TestARefusalIsRenderedNotFlashed is the rule CLAUDE.md states and this
// package spent months half-keeping: a validation failure returns 422 with the
// form re-rendered, so the operator gets their typed input back and the status
// says what happened.
//
// Flash-and-redirect fails both halves. The redirect discards the form, so an
// operator who mistyped one field retypes all of them, and the response is a
// 303 -- a status that means "done, look over there" for something that was
// refused. Eight handlers did this, and the comments on two of them argued it
// was a deliberate trade: re-rendering "would grow a second full page assembly
// it would then have to keep in step". That was true when it was written. It
// stopped being true when renderAssetDetail and renderPower arrived, and
// network.go had already been using renderAssetDetail for exactly this purpose
// while costs.go two files away said it did not exist.
//
// The property checked: a function that CLASSIFIES a refusal (calls
// refusalMessages or validationErrors) must not FLASH one. Classifying is what
// marks the function as being on the refusal path; flashing is what this rule
// forbids there.
func TestARefusalIsRenderedNotFlashed(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("listing handler files: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no handler files found; this test is asserting nothing")
	}

	var offenders []string
	seen := map[string]bool{}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			classifies, flashes := false, false
			var flashLine token.Position
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch f := call.Fun.(type) {
				case *ast.Ident:
					if f.Name == "refusalMessages" || f.Name == "validationErrors" {
						classifies = true
					}
				case *ast.SelectorExpr:
					if f.Sel.Name == "setFlash" && isErrorFlash(call) {
						flashes = true
						flashLine = fset.Position(call.Pos())
					}
				}
				return true
			})
			if !classifies || !flashes {
				continue
			}
			seen[fn.Name.Name] = true
			if _, excused := refusalFlashExceptions[fn.Name.Name]; excused {
				continue
			}
			offenders = append(offenders, fn.Name.Name+" ("+
				filepath.Base(flashLine.Filename)+":"+strconv.Itoa(flashLine.Line)+")")
		}
	}

	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("these handlers flash a refusal instead of re-rendering it:\n\t%s\n\n"+
			"A refused form must come back as 422 with what the operator typed still "+
			"in it. Re-render through the page's own assembly with refusalStatus(err) "+
			"and rejected(r, id, messages, fields...), the way renderAssetDetail and "+
			"renderPower are already called elsewhere. If a handler genuinely has no "+
			"form to return to, add it to refusalFlashExceptions with the reason.",
			strings.Join(offenders, "\n\t"))
	}

	// A stale exception is a lie about the code, so it fails too.
	for name := range refusalFlashExceptions {
		if !seen[name] {
			t.Errorf("refusalFlashExceptions names %q, but no such function both "+
				"classifies and flashes a refusal any more. Delete the entry: an "+
				"exception nobody needs still reads as permission.", name)
		}
	}
}

// isErrorFlash reports whether a setFlash call is the "error" kind. A success
// flash after a successful write is not what this rule is about.
func isErrorFlash(call *ast.CallExpr) bool {
	for _, arg := range call.Args {
		if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if lit.Value == `"error"` {
				return true
			}
		}
	}
	return false
}
