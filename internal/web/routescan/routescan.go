// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Package routescan reads internal/web/routes.go and internal/web/handlers
// with go/ast and answers one question, structurally rather than by review:
// which write-bucket routes reach handlers.actor(r) before they reach the
// store.
//
// It exists because WP-G1's Piece 2 plan makes a claim -- "the compiler
// enumerates the work once the three store write helpers take a
// domain.Permit instead of a domain.Actor" -- that is only true for the
// handlers that actually touch an actor. A handler that mutates state
// through some OTHER path (a shared helper's own value, an actor captured
// earlier and carried across a goroutine boundary, a hand-written
// domain.SystemActor) will not produce a type error when actor(r)'s return
// type changes, because it never called actor(r) in the first place. This
// package finds those handlers before Task 10's compiler-driven pass starts,
// so they can be converted by hand rather than missed.
//
// # The walk
//
// write("PATTERN", app.Handler) is a call expression against a closure
// defined inside Routes, not an entry in a package-level table -- unlike
// apiRoutes, routes.go has no writeRoutes slice to range over. WriteRoutes
// parses routes.go looking for that call shape directly, including the
// journal loop at routes.go:390-394, which ranges over
// handlers.JournalResources() and registers three patterns per resource. The
// real resource list is read by calling handlers.JournalResources() itself,
// not by re-parsing its map literal, so this cannot drift from what the
// route actually registers.
//
// Each handler is then resolved to its *ast.FuncDecl in internal/web/handlers
// and walked for a call to actor(, following every call within the package
// that the handler's body reaches -- one level of name-based resolution,
// deliberately: this repository's App methods all take the receiver name a,
// so a call written a.foo(...) or kind.run(...) is followed by matching the
// selector's name against every function and method declared in the package,
// with no attempt to resolve the receiver's static type. That is weaker than
// a real call graph and stronger than grepping the handler's own body only --
// which is the level of rigour this check needs: it must not miss
// bulkApplyTag, postEntityTags, editCost, postCustomFields and runImport, the
// five shared helpers the exploration named as the reason 140 direct calls
// cover 175 write routes.
//
// # Triage (WP-G1 Task 6, Step 4)
//
// Running this walk against the router as of this task, 181 write-bucket
// routes are registered in total:
//
//   - 173 routes reach actor(r) through the call graph above. The compiler
//     catches every one of these in Task 10: changing actor(r)'s return type
//     breaks the call site, and the fix is mechanical.
//   - 8 routes do not reach actor(r) and do not mutate. Six are the GETs the
//     brief names by hand -- /imports, /imports/{id}, /import/assets,
//     /import/device-types, /teams/{id}/retire and
//     /reports/ownership/candidates -- each rendering a page that exists
//     only to feed a POST beside it (see routes.go's comments on each). Two
//     more surfaced that the brief did not anticipate: GET /users (UserList
//     reads ListUsers and CountActiveAdministrators, nothing else, for the
//     same "view that exists only to feed the mutation forms on the same
//     page" reason as the other admin-only GETs) and POST /network/derive
//     (NetworkDerive computes a proposal and renders it; its own doc comment
//     in reach.go states directly that "this handler has no write path into
//     any of the topology tables at all" -- it is a POST because
//     DeriveNetworkProposal takes request parameters, not because it
//     writes). All eight are expected, and recorded as such in
//     routescan_test.go's renderOnlyWriteRoutes.
//   - 0 routes do not reach actor(r) and mutate. AssetImportRun and
//     DeviceTypeImportRun were the two the exploration flagged by name as
//     likely members of this category, because import_runner.go captures its
//     actor at submit time and runs the actual store write on
//     context.Background(), with no request in scope by the time the write
//     happens. Both are REFUTED as members of this category, not confirmed:
//     the handler itself -- AssetImportRun -> runImport -- calls actor(r)
//     directly (imports.go:204) to build the domain.Actor it carries into
//     the queued importWork, and the dry-run path (kind.run) calls actor(r)
//     again. The call the walker is asked to find is present in the
//     handler's own call graph; what changes across the goroutine boundary
//     is WHEN the resulting value is used, not WHETHER actor(r) was called.
//     Nothing else surfaced writing through domain.SystemActor directly
//     either -- the only reference to it in the package is inside actor(r)'s
//     own body, as the fallback for an unauthenticated request.
//
// The worked count above is not load-bearing on its own: what a reviewer
// checks is testdata/write_routes.txt, which lists every route this task
// found, and TestTheCommittedRouteInventoryMatchesTheRouter, which fails the
// moment the router and the file disagree.
package routescan

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/web/handlers"
)

// Route is one route the write bucket in internal/web/routes.go registers.
type Route struct {
	Pattern string // "POST /assets/{id}/retire"
	Handler string // "AssetRetire"
	File    string // "internal/web/handlers/assets.go"
	// Gate is the registrar that declared this route: "write" (behind
	// RequireWrite/auth.CanWrite) or "writeAdminOnly" (behind
	// RequireAdministrator/auth.IsAdministrator).
	//
	// IT IS IN THE COMMITTED INVENTORY DELIBERATELY. WP-G1 Task 13 makes
	// CanWrite true for a project owner, so the two gates stop being
	// equivalent on that day: a route silently moved from writeAdminOnly to
	// write becomes reachable by a project owner, and without this field the
	// census produced byte-identical output before and after such a move.
	// Two privilege escalations in this work package came from a fact that
	// was recorded nowhere and invalidated by a one-line change elsewhere;
	// recording the gate makes that move a diff in a committed file.
	Gate         string
	ReachesActor bool
	// StoreCalls is every a.Store.<Method> call name reachable from the
	// handler through the same call graph ReachesActor walks. It is not part
	// of the committed inventory -- two handlers that both reach the same
	// store method through different call paths would make the file noisy
	// for no reviewing benefit -- but Tasks 10 and 16 can use it to find
	// every store call site a route's handler is responsible for converting.
	StoreCalls []string
}

// routesFile is where the write bucket lives, relative to the repository
// root.
const routesFile = "internal/web/routes.go"

// isRouteRegistrar reports whether name is one of routes.go's two write-bucket
// registrar closures: write (behind RequireWrite/CanWrite) and
// writeAdminOnly (behind RequireAdministrator -- WP-G1 Task 15, F2: the
// import surface, where no ScopedPermit can ever cover a freshly-minted row,
// so it stays reachable by a full Administrator only). Both register a route
// the same shape this package cares about -- pattern, handler, whether the
// handler's call graph reaches actor( -- so the walk treats them identically;
// which of the two gates a route sits behind is authz.CanWrite vs
// authz.IsAdministrator, an orthogonal question this package does not answer.
func isRouteRegistrar(name string) bool {
	return name == "write" || name == "writeAdminOnly"
}

// handlersDir is where every write-bucket handler and the shared helpers it
// calls are declared, relative to the repository root.
const handlersDir = "internal/web/handlers"

// WriteRoutes parses routes.go and internal/web/handlers and returns every
// route the write bucket registers -- write("PATTERN", app.Handler) call
// expressions, plus the journal loop's expansion -- each resolved to its
// handler and whether that handler's call graph reaches actor(.
//
// It takes *testing.T because it is meant to run inside a test: any parse
// failure is a bug in this package or a structural change to routes.go that
// this walker has not been taught about yet, and both are t.Fatal, not an
// error a caller is expected to handle.
func WriteRoutes(t *testing.T) []Route {
	t.Helper()

	root := repoRoot(t)
	fset := token.NewFileSet()

	routesAST, err := parser.ParseFile(fset, filepath.Join(root, routesFile), nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", routesFile, err)
	}

	funcs, files := loadHandlerFuncs(t, fset, filepath.Join(root, handlersDir))

	var routes []Route
	journalResources := handlers.JournalResources()

	visit := func(n ast.Node) bool {
		rangeStmt, ok := n.(*ast.RangeStmt)
		if ok && isJournalResourcesRange(rangeStmt) {
			routes = append(routes, expandJournalRoutes(rangeStmt, journalResources, funcs, files)...)
			// Handled above; do not also let the general write(...) scan
			// below see these calls with their loop variable unresolved.
			return false
		}

		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		fn, ok := call.Fun.(*ast.Ident)
		if !ok || !isRouteRegistrar(fn.Name) || len(call.Args) != 2 {
			return true
		}
		pattern, ok := literalString(call.Args[0])
		if !ok {
			// A pattern this walker cannot read statically would be a hole
			// in the inventory it cannot see, so it is loud rather than
			// silently skipped.
			t.Fatalf("write() call at %s has a non-literal pattern this walker cannot resolve",
				fset.Position(call.Pos()))
		}
		handlerName, ok := handlerSelectorName(call.Args[1])
		if !ok {
			t.Fatalf("write() call at %s registers a handler this walker cannot resolve",
				fset.Position(call.Pos()))
		}
		routes = append(routes, buildRoute(pattern, handlerName, fn.Name, funcs, files))
		return true
	}
	ast.Inspect(routesAST, visit)

	sort.Slice(routes, func(i, j int) bool { return routes[i].Pattern < routes[j].Pattern })
	return routes
}

// gatedCall pairs a registrar call with WHICH registrar made it, so the
// journal-route expansion below carries the gate through to Route.Gate the
// same way the plain walk does.
type gatedCall struct {
	call *ast.CallExpr
	gate string
}

// isJournalResourcesRange reports whether a range statement is
// `for _, res := range handlers.JournalResources() { ... }`.
func isJournalResourcesRange(r *ast.RangeStmt) bool {
	call, ok := r.X.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "handlers" && sel.Sel.Name == "JournalResources"
}

// expandJournalRoutes evaluates every write(...) call inside the journal
// loop's body once per real resource name, substituting the loop variable
// for the resource string the way the router does at run time.
func expandJournalRoutes(r *ast.RangeStmt, resources []string, funcs map[string]*ast.FuncDecl, files map[string]string) []Route {
	loopVar, ok := r.Value.(*ast.Ident)
	if !ok {
		return nil
	}

	var calls []gatedCall
	ast.Inspect(r.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if fn, ok := call.Fun.(*ast.Ident); ok && isRouteRegistrar(fn.Name) && len(call.Args) == 2 {
			calls = append(calls, gatedCall{call: call, gate: fn.Name})
		}
		return true
	})

	var routes []Route
	for _, res := range resources {
		for _, gc := range calls {
			pattern, ok := evalPattern(gc.call.Args[0], loopVar.Name, res)
			if !ok {
				continue
			}
			handlerName, ok := handlerSelectorName(gc.call.Args[1])
			if !ok {
				continue
			}
			routes = append(routes, buildRoute(pattern, handlerName, gc.gate, funcs, files))
		}
	}
	return routes
}

// evalPattern renders a route pattern expression built from string literals
// and the journal loop's range variable -- "POST /"+res+"/{id}/journal" --
// substituting varName for value.
func evalPattern(expr ast.Expr, varName, value string) (string, bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return literalString(e)
	case *ast.Ident:
		if e.Name == varName {
			return value, true
		}
		return "", false
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		left, ok := evalPattern(e.X, varName, value)
		if !ok {
			return "", false
		}
		right, ok := evalPattern(e.Y, varName, value)
		if !ok {
			return "", false
		}
		return left + right, true
	default:
		return "", false
	}
}

// handlerSelectorName reads the method name out of an `app.Handler` argument.
func handlerSelectorName(expr ast.Expr) (string, bool) {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	base, ok := sel.X.(*ast.Ident)
	if !ok || base.Name != "app" {
		return "", false
	}
	return sel.Sel.Name, true
}

func literalString(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

// buildRoute resolves one pattern/handler pair into a Route, walking the
// handler's call graph for actor( and for a.Store.* calls.
func buildRoute(pattern, handlerName, gate string, funcs map[string]*ast.FuncDecl, files map[string]string) Route {
	decl := funcs[handlerName]
	visitedActor := map[string]bool{}
	visitedStore := map[string]bool{}
	storeCalls := map[string]bool{}
	reaches := callGraphReachesActor(decl, funcs, visitedActor, storeCalls)

	names := make([]string, 0, len(storeCalls))
	for name := range storeCalls {
		names = append(names, name)
	}
	sort.Strings(names)
	_ = visitedStore // kept for symmetry / future use; storeCalls collected inline above

	return Route{
		Pattern:      pattern,
		Handler:      handlerName,
		File:         files[handlerName],
		Gate:         gate,
		ReachesActor: reaches,
		StoreCalls:   names,
	}
}

// attributionSources are the helpers a handler can call to learn WHO is
// making the request, derived from the server's own session state and never
// from anything the request supplied.
//
// "actor" was the only one when Task 6 wrote this walker. WP-G1 moved write
// attribution onto the permit: a.permit(r) and a.entityPermit(r) both go
// through App.resolvePermit, which asks auth.Authorizer.Permit for a permit
// built from the signed-in user, and domain.Permit carries Actor(). A
// handler reaching any of these has derived its attribution server-side,
// which is the property this walk exists to measure.
//
// Keeping only "actor" here would have made this walker measure a name
// rather than a property: handlers that correctly took their attribution
// from the permit would have been reported as reaching nothing, and the
// pressure would have been to keep a vestigial actor(r) call alive purely to
// satisfy a test. That is a test dictating code shape, which is how a check
// quietly stops meaning anything.
// entityPermit and resolvePermit are deliberately ABSENT. Base calls
// entityPermit on every page render so CanWriteEntity can answer without a
// query, which means including it -- or the shared resolvePermit it delegates
// to -- would mark all 184 routes as reaching attribution, including the six
// render-only GETs, and a check that everything passes measures nothing.
// permit(r) is the write path specifically; entityPermit is the read-side UI
// helper.
var attributionSources = map[string]bool{
	"actor":  true,
	"permit": true,
}

// callGraphReachesActor walks decl's body and every function it calls within
// the package (one level of name-based resolution -- see the package
// comment), looking for a bare call to actor(. Along the way it also records
// every a.Store.<Method>(...) call it passes through, into storeCalls.
//
// visited prevents infinite recursion on a call cycle; it is a plain
// map[string]bool shared across the whole DFS rather than one map per call,
// because the property being checked -- "can this handler's call graph reach
// actor("- is a property of the graph, not of any one path through it, and a
// function visited on one branch does not need re-walking from another.
func callGraphReachesActor(decl *ast.FuncDecl, funcs map[string]*ast.FuncDecl, visited map[string]bool, storeCalls map[string]bool) bool {
	if decl == nil || decl.Body == nil {
		return false
	}
	if visited[decl.Name.Name] {
		return false
	}
	visited[decl.Name.Name] = true

	found := false
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			if attributionSources[fn.Name] {
				found = true
				return false
			}
			if next, ok := funcs[fn.Name]; ok {
				if callGraphReachesActor(next, funcs, visited, storeCalls) {
					found = true
					return false
				}
			}
		case *ast.SelectorExpr:
			if isStoreCall(fn) {
				storeCalls[fn.Sel.Name] = true
			}
			name := fn.Sel.Name
			if attributionSources[name] {
				found = true
				return false
			}
			if next, ok := funcs[name]; ok {
				if callGraphReachesActor(next, funcs, visited, storeCalls) {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

// isStoreCall reports whether a selector expression is a.Store.<Method>(...).
func isStoreCall(sel *ast.SelectorExpr) bool {
	inner, ok := sel.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	base, ok := inner.X.(*ast.Ident)
	return ok && base.Name == "a" && inner.Sel.Name == "Store"
}

// loadHandlerFuncs parses every non-test .go file in dir and returns every
// function and method it declares, keyed by name, plus the file each was
// declared in (relative to the repository root, forward-slashed).
//
// Keyed by name alone, deliberately: every App method in this package takes
// the receiver name a (verified once, by hand, rather than assumed), so a
// call written a.foo(...) is resolved the same way a bare foo(...) would be.
// The tradeoff is a same-named method on an unrelated type being followed by
// mistake; the alternative is a call graph walker that has to type-check the
// receiver, which is a much larger tool for a check whose job is to flag
// candidates for a human to look at, not to be sound.
func loadHandlerFuncs(t *testing.T, fset *token.FileSet, dir string) (map[string]*ast.FuncDecl, map[string]string) {
	t.Helper()
	funcs := map[string]*ast.FuncDecl{}
	files := map[string]string{}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	root := repoRoot(t)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("relativising %s: %v", path, err)
		}
		rel = filepath.ToSlash(rel)

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			funcs[fn.Name.Name] = fn
			files[fn.Name.Name] = rel
		}
	}
	return funcs, files
}

// repoRoot resolves the repository root by walking up from the test binary's
// working directory until it finds go.mod.
//
// WP-G1 Task 15: an earlier version resolved "../../..." against the test's
// own working directory, which `go test` sets to the PACKAGE UNDER TEST's
// directory -- correct only for a test that lives in
// internal/web/routescan itself, and silently wrong by one level for any
// caller one directory shallower (internal/web/project_create_test.go hit
// this and worked around it locally with its own copy of this same walk,
// rather than trusting this one -- see that file's repoRoot doc comment).
// Walking up to go.mod instead of counting directories makes this function
// correct for every caller regardless of which package invokes it, so that
// local copy can be retired.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolving the working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test's working directory")
		}
		dir = parent
	}
}

// Format renders a Route the way the committed inventory spells it: pattern,
// handler, ReachesActor, pipe-separated so the file reads as a table without
// needing fixed-width columns that would churn on every long handler name.
func (r Route) Format() string {
	return fmt.Sprintf("%s | %s | %s | %t", r.Pattern, r.Handler, r.Gate, r.ReachesActor)
}
