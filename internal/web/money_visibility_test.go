// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"text/template/parse"
)

// WP-1.1 Task 4d exists because three earlier tasks each believed they had
// closed the last money surface and each was wrong: 54d71fa gated
// CostReport/SupplierReport, d18b1e0 gated the change log, and this task
// still found price_movement.html (five money calls, zero CanSeeCosts
// references) and asset_detail.html's Replacement panel wide open to an
// ungranted Observer. A hand-maintained list of "pages that show money" is
// exactly the shape that keeps failing here, so this file is two guards
// instead of one:
//
//   - TestNoMoneySurfaceLeaksToAnUngrantedObserver fetches every route that
//     can render an amount through the real router, as the real "viewer"
//     fixture account (an Observer with no can_see_costs grant), and asserts
//     no currency symbol appears anywhere in the body. Modelled on
//     TestEveryDetailPageRenders (detail_pages_render_test.go): a route
//     missing from the table is a route nothing here checks.
//   - TestEveryMoneyTemplateHasABehaviouralRoute is the static half.
//     moneyRenderingTemplates walks every page and partial template with
//     Go's own text/template/parse -- the same "parse the real source,
//     don't grep it" approach permit_source_test.go (internal/store) uses
//     for permit minters -- and finds every template that calls the `money`
//     helper. That set is asserted equal to moneyRouteCoverage's templates
//     below, so a SIXTH template calling `money` fails this test on sight,
//     before anyone has to notice that the behavioural test never visits it.
var moneyRouteCoverage = []struct {
	name string
	// path resolves the URL to fetch, given a harness already logged in as
	// admin (so it can look up or create the fixture row it needs).
	path func(t *testing.T, h *harness) string
	// templates are the money-calling template files (relative to
	// web/templates) this route is expected to exercise. Every entry here
	// must appear in moneyRenderingTemplates, and every one of those must be
	// covered by some route -- TestEveryMoneyTemplateHasABehaviouralRoute
	// checks both directions.
	templates []string
}{
	{
		name: "asset detail",
		path: func(t *testing.T, h *harness) string {
			id := h.refs.Assets["hv-01"]
			if id == "" {
				t.Fatal("fixture asset hv-01 not found")
			}
			return "/assets/" + id
		},
		templates: []string{"pages/asset_detail.html", "partials/price_movement.html", "partials/costs.html"},
	},
	{
		name: "service detail",
		path: func(t *testing.T, h *harness) string {
			id := h.refs.Services["vault"]
			if id == "" {
				t.Fatal("fixture service vault not found")
			}
			return "/services/" + id
		},
		templates: []string{"partials/costs.html"},
	},
	{
		name: "circuit detail",
		path: func(t *testing.T, h *harness) string {
			// The base fixture circuit carries no cost line (see
			// change_log_cost_redaction_test.go's identical comment), so one
			// is added here through the real handler before either viewer
			// checks the page.
			id := h.lookup(`SELECT id FROM circuit LIMIT 1`)
			if id == "" {
				t.Fatal("no circuit in the fixture")
			}
			resp := h.post("/circuits/"+id+"/costs", url.Values{
				"csrf_token": {h.csrfToken("/")},
				"kind":       {"operating"}, "period": {"monthly"}, "amount": {"1450"},
				"note": {"fixture line for the money-visibility census"},
			}, false)
			resp.Body.Close()
			if resp.StatusCode != 303 {
				t.Fatalf("adding the fixture circuit cost line returned %d, want 303", resp.StatusCode)
			}
			return "/circuits/" + id
		},
		templates: []string{"partials/price_movement.html", "partials/costs.html"},
	},
	{
		name: "cluster detail",
		path: func(t *testing.T, h *harness) string {
			id := h.lookup(`SELECT id FROM cluster LIMIT 1`)
			if id == "" {
				t.Fatal("no cluster in the fixture")
			}
			return "/clusters/" + id
		},
		templates: []string{"pages/cluster_detail.html"},
	},
	{
		name: "project overview",
		path: func(t *testing.T, h *harness) string {
			id := h.refs.Projects["platform"]
			if id == "" {
				t.Fatal("fixture project platform not found")
			}
			return "/projects/" + id
		},
		templates: []string{"partials/costs.html"},
	},
	{
		name:      "cost report",
		path:      func(t *testing.T, h *harness) string { return "/reports/cost" },
		templates: []string{"pages/cost_report.html"},
	},
	{
		name:      "supplier report",
		path:      func(t *testing.T, h *harness) string { return "/reports/suppliers" },
		templates: []string{"pages/supplier_report.html"},
	},
}

// currencySymbols mirrors render/funcs.go's own table (unexported there, so
// this is a deliberate, small duplication rather than an import across the
// package boundary). The harness always renders EUR, so "€" alone would
// prove the point; every symbol is checked anyway so a future currency
// change to the harness does not silently defang this test.
var currencySymbols = []string{"€", "$", "£", "CHF ", "lei "}

// TestNoMoneySurfaceLeaksToAnUngrantedObserver is the behavioural half of the
// guard: every route in moneyRouteCoverage, fetched as the real "viewer"
// fixture account (an Observer with no can_see_costs grant), through the
// real router, must carry no currency symbol anywhere in the body. Each case
// also proves it is not simply an inert page that never shows money to
// anybody: fetched again as "admin" (an Administrator, whose CanSeeCosts is
// always true), the same page must carry at least one currency symbol.
// Without that half, a route that had already been (correctly, or by
// accident) stripped of all money content would pass this test for a reason
// that has nothing to do with the gate under test.
func TestNoMoneySurfaceLeaksToAnUngrantedObserver(t *testing.T) {
	for _, tc := range moneyRouteCoverage {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.login("admin", "admin-password")
			path := tc.path(t, h)

			adminPage := body(t, h.get(path, false))
			if !containsAny(adminPage, currencySymbols) {
				t.Fatalf("GET %s showed no currency symbol to an Administrator; this route "+
					"is not proven to render money at all, so a passing viewer check below "+
					"would prove nothing", path)
			}

			h.logout()
			h.login("viewer", "viewer-password")
			resp := h.get(path, false)
			if resp.StatusCode != 200 {
				resp.Body.Close()
				t.Fatalf("GET %s as an ungranted Observer returned %d, want 200 with the "+
					"money withheld, not a hard refusal", path, resp.StatusCode)
			}
			viewerPage := body(t, resp)
			if sym, ok := firstMatch(viewerPage, currencySymbols); ok {
				t.Errorf("GET %s leaked a currency symbol (%q) to an ungranted Observer", path, sym)
			}
		})
	}
}

func containsAny(s string, subs []string) bool {
	_, ok := firstMatch(s, subs)
	return ok
}

func firstMatch(s string, subs []string) (string, bool) {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return sub, true
		}
	}
	return "", false
}

// TestEveryMoneyTemplateHasABehaviouralRoute is the static half: it finds
// every template file that calls the `money` render helper and asserts that
// set equals the templates moneyRouteCoverage above declares its routes
// cover. Equality, not subset, in both directions:
//
//   - a template found calling `money` but missing from moneyRouteCoverage
//     means TestNoMoneySurfaceLeaksToAnUngrantedObserver never visits it --
//     the exact way price_movement.html sat undetected through three earlier
//     "the last surface is closed" commits;
//   - a template listed in moneyRouteCoverage that no longer calls `money`
//     means the table is stale and should be trimmed, so it keeps meaning
//     what it says.
func TestEveryMoneyTemplateHasABehaviouralRoute(t *testing.T) {
	root := repoRoot(t)
	templatesDir := filepath.Join(root, "web", "templates")

	found, err := moneyRenderingTemplates(templatesDir)
	if err != nil {
		t.Fatalf("scanning templates for money calls: %v", err)
	}

	covered := map[string]bool{}
	for _, tc := range moneyRouteCoverage {
		for _, tpl := range tc.templates {
			covered[tpl] = true
		}
	}

	var missing, stale []string
	for tpl := range found {
		if !covered[tpl] {
			missing = append(missing, tpl)
		}
	}
	for tpl := range covered {
		if !found[tpl] {
			stale = append(stale, tpl)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)

	if len(missing) > 0 {
		t.Errorf("template(s) call the money helper but are not covered by any route in "+
			"moneyRouteCoverage, so TestNoMoneySurfaceLeaksToAnUngrantedObserver never "+
			"renders them for an ungranted Observer: %v", missing)
	}
	if len(stale) > 0 {
		t.Errorf("moneyRouteCoverage names template(s) that no longer call the money "+
			"helper -- update the table so it names what it actually guards: %v", stale)
	}
}

// moneyRenderingTemplates walks web/templates/pages and web/templates/partials
// and returns the set of files (relative to dir, forward-slash separated)
// that contain at least one call to the `money` template function -- as
// either {{money .Field}} or {{.Field | money}}, both of which place an
// *parse.IdentifierNode named "money" as a CommandNode's first Arg.
//
// PARSED, NOT GREPPED. A grep for "{{money" would also have to dodge
// {{/* comments that mention money */}} and would miss {{.X | money}}; Go's
// own template parser already resolves both correctly and for free.
func moneyRenderingTemplates(dir string) (map[string]bool, error) {
	found := map[string]bool{}
	for _, sub := range []string{"pages", "partials"} {
		matches, err := filepath.Glob(filepath.Join(dir, sub, "*.html"))
		if err != nil {
			return nil, err
		}
		for _, path := range matches {
			trees, err := parseTemplateFile(path)
			if err != nil {
				return nil, err
			}
			for _, tree := range trees {
				if tree.Root != nil && nodeCallsFunc(tree.Root, "money") {
					found[sub+"/"+filepath.Base(path)] = true
					break
				}
			}
		}
	}
	return found, nil
}

var missingTemplateFuncRe = regexp.MustCompile(`function "([^"]+)" not defined`)

// parseTemplateFile parses one template file's every {{define}} block with
// text/template/parse, discovering the function names it must declare as it
// goes rather than hand-listing render/funcs.go's names here a second time
// (those are unexported, and a second hand-list is exactly the maintenance
// burden this test exists to remove). parse.Parse only needs a function NAME
// present in the funcs map to accept a call -- it never checks the value's
// type, since no template is executed here, only parsed -- so each retry adds
// the one name the previous attempt reported missing and tries again.
func parseTemplateFile(path string) (map[string]*parse.Tree, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	funcs := map[string]any{}
	// Bounded rather than an infinite loop: a real template in this repo
	// calls nowhere near a hundred distinct helpers, so hitting this bound
	// means something other than "found another missing func name" is
	// wrong, and that has to fail loudly rather than spin.
	for i := 0; i < 200; i++ {
		trees, err := parse.Parse(filepath.Base(path), string(data), "{{", "}}", funcs)
		if err == nil {
			return trees, nil
		}
		m := missingTemplateFuncRe.FindStringSubmatch(err.Error())
		if m == nil {
			return nil, err
		}
		funcs[m[1]] = struct{}{}
	}
	return nil, &templateScanError{path: path}
}

type templateScanError struct{ path string }

func (e *templateScanError) Error() string {
	return "parsing " + e.path + ": exceeded the retry bound looking for its function names"
}

// nodeCallsFunc reports whether name appears as a called function anywhere
// under n: as the first Arg of a CommandNode, which is where the parser
// places the identifier for both {{money .X}} and {{.X | money}}.
func nodeCallsFunc(n parse.Node, name string) bool {
	switch v := n.(type) {
	case *parse.ListNode:
		if v == nil {
			return false
		}
		for _, c := range v.Nodes {
			if nodeCallsFunc(c, name) {
				return true
			}
		}
	case *parse.ActionNode:
		return nodeCallsFunc(v.Pipe, name)
	case *parse.PipeNode:
		if v == nil {
			return false
		}
		for _, cmd := range v.Cmds {
			if nodeCallsFunc(cmd, name) {
				return true
			}
		}
	case *parse.CommandNode:
		for _, arg := range v.Args {
			if id, ok := arg.(*parse.IdentifierNode); ok && id.Ident == name {
				return true
			}
			if nodeCallsFunc(arg, name) {
				return true
			}
		}
	case *parse.IfNode:
		return branchCallsFunc(v.BranchNode, name)
	case *parse.RangeNode:
		return branchCallsFunc(v.BranchNode, name)
	case *parse.WithNode:
		return branchCallsFunc(v.BranchNode, name)
	case *parse.TemplateNode:
		return nodeCallsFunc(v.Pipe, name)
	}
	return false
}

func branchCallsFunc(b parse.BranchNode, name string) bool {
	return nodeCallsFunc(b.Pipe, name) || nodeCallsFunc(b.List, name) || nodeCallsFunc(b.ElseList, name)
}
