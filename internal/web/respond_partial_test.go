// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// render.Respond picks a named region for an HX-Request and the whole page
// otherwise, so the name it is handed must resolve in the template set that
// actually renders it. A name that does not resolve is not a fallback --
// ExecuteTemplate returns "no such template" and the handler answers 500.
//
// ELEVEN CALL SITES SHIPPED THAT WAY, a third of every Respond in the package.
// The swappable region of a page is conventionally declared inside the page
// file beside the markup that uses it -- {{define "cost_report"}} lives in
// pages/cost_report.html -- but Respond rendered through Renderer.Partial,
// whose set parse() builds from templates/partials/*.html and nothing else. No
// page-level define was ever in it.
//
// Nothing found them because nothing sends the header: the nav rail is plain
// hrefs with no hx-boost and the forms on the affected pages are plain
// method="post". The whole class was unreachable and silent, one attribute
// away from breaking eleven routes for somebody with no reason to suspect a
// handler.
//
// THE FIRST FIX ATTEMPT WAS WRONG AND THIS TEST IS WHY IT WAS CAUGHT. Reading
// only the partials directory, the obvious conclusion was that those names
// were mistakes and the handlers should pass "" to render the whole page --
// which would have quietly demoted eleven intended HTMX regions to full-page
// renders. The names were correct all along; the renderer was looking in the
// wrong place. So this checks the union the renderer now resolves, and it must
// keep matching pagePartial: page-level defines AND shared partials.
func TestEveryRespondNamesATemplateThatResolves(t *testing.T) {
	root := repoRoot(t)
	templates := filepath.Join(root, "web", "templates")

	shared, err := definedIn(filepath.Join(templates, "partials", "*.html"))
	if err != nil {
		t.Fatalf("collecting the shared partials: %v", err)
	}
	if len(shared) == 0 {
		t.Fatal("found no shared partials at all; this guard would pass vacuously, " +
			"which is worse than the bug it exists to catch")
	}

	calls, err := respondPartialArgs(filepath.Join(root, "internal", "web", "handlers"))
	if err != nil {
		t.Fatalf("scanning handlers for Respond calls: %v", err)
	}
	// A positive control. The scan walks the package with go/ast rather than
	// grepping, so a change to the call shape would find nothing and report
	// success -- the same vacuous pass the check above guards on the other side.
	if len(calls) < 20 {
		t.Fatalf("found only %d Respond calls with literal names; this package has many "+
			"more, so the scan has stopped matching the code and checks nothing", len(calls))
	}

	perPage := map[string]map[string]bool{}
	for _, c := range calls {
		if c.partial == "" {
			// A page with no swappable region. A real answer, not a gap.
			continue
		}
		if _, done := perPage[c.page]; !done {
			own, err := definedIn(filepath.Join(templates, "pages", c.page+".html"))
			if err != nil {
				t.Fatalf("%s:%d names page %q and its template cannot be read: %v",
					c.file, c.line, c.page, err)
			}
			perPage[c.page] = own
		}
		if perPage[c.page][c.partial] || shared[c.partial] {
			continue
		}
		t.Errorf("%s:%d passes page %q and region %q to Respond, and %q is defined neither "+
			"in pages/%s.html nor in any shared partial. An HX-Request to this route "+
			"answers 500, and nothing has to send one today for that to be true tomorrow "+
			"-- one hx-boost on the nav is enough.",
			c.file, c.line, c.page, c.partial, c.partial, c.page)
	}
}

// respondCall is one Respond call site with the two names it passes.
type respondCall struct {
	file    string
	line    int
	page    string
	partial string
}

// respondPartialArgs finds every `…Respond(w, r, status, page, partial, data)`
// in dir and returns both names where each is a string literal.
//
// PARSED, NOT GREPPED, for the reason money_visibility_test.go gives about
// templates: a regexp would have to dodge the words in comments and would miss
// a call split across lines, which gofmt produces routinely here.
//
// A computed name is skipped rather than failed. Nothing in this package
// computes one today, and a rule against it would be inventing a convention
// this test was not asked to enforce.
// GLOBBED AND PARSED PER FILE rather than parser.ParseDir, which staticcheck
// rejects as deprecated since Go 1.25 because it ignores build tags when
// grouping files into packages. That grouping is the only thing ParseDir added
// here and this scan never used it -- every Respond call is wanted wherever it
// sits. The suggested replacement, golang.org/x/tools/go/packages, would be a
// new dependency for a convenience this does not need.
func respondPartialArgs(dir string) ([]respondCall, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}

	fset := token.NewFileSet()
	var out []respondCall
	for _, path := range paths {
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		{
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Respond" {
					return true
				}
				// w, r, status, page, partial, data.
				const pageArg, partialArg = 3, 4
				if len(call.Args) <= partialArg {
					return true
				}
				page, ok := stringLit(call.Args[pageArg])
				if !ok {
					return true
				}
				partial, ok := stringLit(call.Args[partialArg])
				if !ok {
					return true
				}
				out = append(out, respondCall{
					file:    filepath.Base(path),
					line:    fset.Position(call.Pos()).Line,
					page:    page,
					partial: partial,
				})
				return true
			})
		}
	}
	return out, nil
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return v, true
}

// definedIn is every template name defined by the files matching glob.
func definedIn(glob string) (map[string]bool, error) {
	matches, err := filepath.Glob(glob)
	if err != nil {
		return nil, err
	}
	defined := map[string]bool{}
	for _, path := range matches {
		trees, err := parseTemplateFile(path)
		if err != nil {
			return nil, err
		}
		for name := range trees {
			defined[name] = true
		}
	}
	return defined, nil
}

// TestAnHTMXRequestGetsTheFragmentAndNotA500 is the behavioural half of the
// guard above, and the one that proves the fix rather than the intent.
//
// The static check catches a NAME that resolves nowhere. It cannot catch the
// defect that shipped, because every name was correct: the renderer looked for
// them in the wrong set. Restoring that bug leaves the static check green and
// leaves every other test in this package green too -- nothing else in the
// suite sends HX-Request to one of these routes, which is exactly why eleven
// broken handlers went unnoticed.
//
// So this asks the question directly, over a real router and a real request:
// does an HX-Request come back as a usable fragment, and not a 500?
//
// It checks a page-level region specifically. A shared partial would have
// worked before the fix and proves nothing about it.
func TestAnHTMXRequestGetsTheFragmentAndNotA500(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	for _, tc := range []struct{ path, region string }{
		// Each names a region defined inside its own page file rather than in
		// templates/partials, which is the shape that used to 500.
		{"/reports/cost", "cost_report"},
		{"/reports/ownership", "ownership_report"},
		{"/power", "power_panel_list"},
		{"/reports/power", "power_findings"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			resp := h.get(tc.path, true)
			if resp.StatusCode != http.StatusOK {
				resp.Body.Close()
				t.Fatalf("an HX-Request to %s answered %d, want 200. The region %q is defined "+
					"in its own page file, and Respond used to look only in the shared "+
					"partial set -- so this was a 500 that no test and no click could reach.",
					tc.path, resp.StatusCode, tc.region)
			}
			body := body(t, resp)
			if body == "" {
				t.Fatalf("an HX-Request to %s answered 200 with an empty body, which HTMX "+
					"would swap in as nothing at all", tc.path)
			}
			// A FRAGMENT, NOT THE WHOLE PAGE. Rendering the full page would also
			// be a 200 and would look fine here, so assert the thing that
			// distinguishes them: the layout shell must be absent.
			if strings.Contains(body, "<!doctype") || strings.Contains(body, "<html") {
				t.Errorf("an HX-Request to %s came back as a whole page rather than a "+
					"fragment; HTMX would swap a document into an element", tc.path)
			}
		})
	}
}
