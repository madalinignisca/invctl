// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package routescan

import (
	"flag"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/web/handlers"
)

const inventoryPath = "testdata/write_routes.txt"

// writeInventory regenerates testdata/write_routes.txt from the live router
// instead of comparing against it -- the standard Go golden-file update
// pattern. Never on by default: a route that silently starts writing its own
// answer sheet is a route this task's whole reason for existing has stopped
// checking.
var writeInventory = flag.Bool("write-inventory", false,
	"regenerate testdata/write_routes.txt instead of comparing against it")

// TestTheCommittedRouteInventoryMatchesTheRouter regenerates the route
// inventory from the live router and handlers, and fails the moment it
// disagrees with testdata/write_routes.txt.
//
// The file is the point of this task, not a byproduct of it: a route added
// or removed anywhere in the write bucket changes this file, which is what
// lets a reviewer tell an omitted authorization gate from a deliberate one
// -- see the package comment for the review this addresses.
func TestTheCommittedRouteInventoryMatchesTheRouter(t *testing.T) {
	routes := WriteRoutes(t)
	if len(routes) == 0 {
		t.Fatal("WriteRoutes found no routes at all; this test would pass on a router that registered nothing")
	}

	var got []string
	for _, r := range routes {
		got = append(got, r.Format())
	}
	gotText := strings.Join(got, "\n") + "\n"

	if *writeInventory {
		if err := os.WriteFile(inventoryPath, []byte(gotText), 0o600); err != nil {
			t.Fatalf("writing %s: %v", inventoryPath, err)
		}
		return
	}

	wantBytes, err := os.ReadFile(inventoryPath)
	if err != nil {
		t.Fatalf("reading %s: %v", inventoryPath, err)
	}
	if string(wantBytes) != gotText {
		t.Errorf("the committed route inventory does not match the router.\n\n"+
			"Regenerate it with:\n\tgo test ./internal/web/routescan/... "+
			"-run TestTheCommittedRouteInventoryMatchesTheRouter -write-inventory\n\n"+
			"and review the diff to confirm it is the route you meant to add or remove:\n\n%s", gotText)
	}
}

// TestNoRouteIsRegisteredMoreThanOnce guards a shape a mismatched pattern
// pair would produce silently: two write() calls for the same pattern, the
// second shadowing the first at the net/http.ServeMux level with no error
// from the router and no signal anywhere except this list growing a
// duplicate line.
func TestNoRouteIsRegisteredMoreThanOnce(t *testing.T) {
	routes := WriteRoutes(t)
	seen := map[string]bool{}
	for _, r := range routes {
		if seen[r.Pattern] {
			t.Errorf("%s is registered more than once", r.Pattern)
		}
		seen[r.Pattern] = true
	}
}

// sixNamedRenderOnlyGETs is exactly the six write-bucket GETs the brief
// names by hand: /imports, /imports/{id}, /import/assets,
// /import/device-types, /teams/{id}/retire and
// /reports/ownership/candidates. Kept separate from
// renderOnlyWriteRoutes so the brief's own list stays checkable on its own.
var sixNamedRenderOnlyGETs = map[string]bool{
	"GET /imports":                      true,
	"GET /imports/{id}":                 true,
	"GET /import/assets":                true,
	"GET /import/device-types":          true,
	"GET /teams/{id}/retire":            true,
	"GET /reports/ownership/candidates": true,
}

// renderOnlyWriteRoutes is every write-bucket route this task found that
// does not reach actor( and does not mutate -- the brief's six, plus two
// more the walk surfaced that the brief did not anticipate:
//
//   - GET /users (UserList) renders the same admin-only "view exists only to
//     feed the mutations beside it" page as the other six; UserList calls
//     only ListUsers and CountActiveAdministrators, both reads.
//   - POST /network/derive (NetworkDerive) computes a derivation proposal
//     and renders it for review; its own doc comment in reach.go says so
//     directly: "It never writes anything ... this handler has no write
//     path into any of the topology tables at all." It is a POST rather
//     than a GET because DeriveNetworkProposal takes request parameters,
//     not because it mutates.
var renderOnlyWriteRoutes = func() map[string]bool {
	out := map[string]bool{
		"GET /users":           true,
		"POST /network/derive": true,
	}
	for pattern := range sixNamedRenderOnlyGETs {
		out[pattern] = true
	}
	return out
}()

// TestExpectedRenderOnlyGETsDoNotReachActor pins down the six write-bucket
// GETs the brief names by hand: they render a page that exists only to feed
// a mutation beside it, and none of them writes anything, so none is
// expected to reach actor(. If one of them starts reaching actor( that is
// worth knowing -- it means the page grew a write of its own -- and if one
// stops being in this set without a corresponding testdata change, that is
// exactly the drift TestTheCommittedRouteInventoryMatchesTheRouter exists to
// catch.
func TestExpectedRenderOnlyGETsDoNotReachActor(t *testing.T) {
	routes := WriteRoutes(t)
	found := map[string]bool{}
	for _, r := range routes {
		if !sixNamedRenderOnlyGETs[r.Pattern] {
			continue
		}
		found[r.Pattern] = true
		if r.ReachesActor {
			t.Errorf("%s (%s) reaches actor( but is one of the render-only write-bucket GETs; "+
				"either it grew a write and belongs off this list, or the walk is wrong", r.Pattern, r.Handler)
		}
	}
	for pattern := range sixNamedRenderOnlyGETs {
		if !found[pattern] {
			t.Errorf("%s is not registered at all; this test's list of render-only GETs is stale", pattern)
		}
	}
}

// TestNoMutatingRouteFailsToReachActorUnexpectedly is Task 6's Step 4 triage,
// written as a test rather than left as a comment. Every route that does not
// reach actor( must be accounted for: it is one of the eight render-only
// routes above, or it is named here as a handler Task 10 must convert by
// hand. An unnamed one failing this test is exactly the gap the whole task
// exists to close.
func TestNoMutatingRouteFailsToReachActorUnexpectedly(t *testing.T) {
	// As of this task, the walk finds none: see the package comment's
	// worked triage for why AssetImportRun and DeviceTypeImportRun -- the
	// two the exploration flagged by name -- are refuted rather than
	// confirmed. Naming the handler here (map[string]bool{"Foo": true})
	// is how Task 10 finds the ones the compiler will not.
	knownMutatingGap := map[string]bool{}

	routes := WriteRoutes(t)
	for _, r := range routes {
		if r.ReachesActor || renderOnlyWriteRoutes[r.Pattern] {
			continue
		}
		if knownMutatingGap[r.Handler] {
			continue
		}
		t.Errorf("%s (%s, %s) does not reach actor( and is not one of the render-only routes. "+
			"Either it is a newly discovered mutating gap Task 10 must convert by hand -- add it "+
			"to knownMutatingGap with the reason -- or the walk followed the wrong call graph.",
			r.Pattern, r.Handler, r.File)
	}
}

// TestWriteRoutesFindsTheSharedHelpers pins the five shared helpers the
// exploration named as the reason 140 direct actor( calls cover 175 routes:
// bulkApplyTag, postEntityTags, editCost, postCustomFields and runImport. A
// walker that only inspected a handler's own body, rather than following
// calls it makes within the package, would report every route fed by one of
// these as not reaching actor( -- which is the false-negative shape this
// task exists to rule out.
func TestWriteRoutesFindsTheSharedHelpers(t *testing.T) {
	sharedHelperRoutes := map[string]string{
		"POST /assets/tags/apply":          "bulkApplyTag",
		"POST /services/tags/apply":        "bulkApplyTag",
		"POST /assets/{id}/tags":           "postEntityTags",
		"POST /services/{id}/tags":         "postEntityTags",
		"POST /projects/{id}/tags":         "postEntityTags",
		"POST /assets/{id}/costs/{costID}": "editCost",
		"POST /assets/{id}/custom-fields":  "postCustomFields",
		"POST /import/assets":              "runImport",
	}
	routes := WriteRoutes(t)
	byPattern := map[string]Route{}
	for _, r := range routes {
		byPattern[r.Pattern] = r
	}
	for pattern, helper := range sharedHelperRoutes {
		r, ok := byPattern[pattern]
		if !ok {
			t.Fatalf("%s is not registered; this test's fixture is stale", pattern)
		}
		if !r.ReachesActor {
			t.Errorf("%s (%s) does not reach actor(, but its handler calls the shared helper %s, "+
				"which does -- the walk did not follow the call graph", pattern, r.Handler, helper)
		}
	}
}

// TestWriteRoutesIncludesTheJournalLoop pins routes.go:390-394's loop over
// handlers.JournalResources(): three patterns per resource, registered from
// three call sites rather than thirty literal write() calls. A walker that
// only recognised literal-string write() calls would silently miss all
// thirty.
func TestWriteRoutesIncludesTheJournalLoop(t *testing.T) {
	resources := handlers.JournalResources
	_ = resources // documents where the real list comes from; see below
	routes := WriteRoutes(t)
	byPattern := map[string]bool{}
	for _, r := range routes {
		byPattern[r.Pattern] = true
	}
	want := []string{
		"POST /assets/{id}/journal",
		"POST /assets/{id}/journal/{noteID}",
		"POST /assets/{id}/journal/{noteID}/retire",
	}
	for _, pattern := range want {
		if !byPattern[pattern] {
			t.Errorf("%s is missing; the journal loop was not expanded", pattern)
		}
	}
}

// TestWriteRoutesCanFail proves the walker can fail, per the brief: deleting
// the actor(r) call from a handler and substituting domain.SystemActor must
// make the walk stop naming that handler as reaching actor(. Rather than
// mutate a real file on disk (which would leave the repository dirty on
// failure and race every other test in the package), this rewrites a
// throwaway copy of one real handler file into a temp handlers directory,
// with the actor(r) call deleted, and points a private variant of the walk
// at it.
//
// This is deliberately closer to the mutation the brief asks for than a
// second, independently-hand-written "fake package" would be: it starts from
// the actual assets.go, so what is proven to fail is the real walk against a
// realistic edit, not a walk against a fixture built to make the test pass.
func TestWriteRoutesCanFail(t *testing.T) {
	root := repoRoot(t)
	original, err := os.ReadFile(filepath.Join(root, handlersDir, "assets.go"))
	if err != nil {
		t.Fatalf("reading assets.go: %v", err)
	}

	// AssetRetire calls actor(r) once, directly, to build the domain.Actor
	// its store call carries. Deleting that call and writing
	// domain.SystemActor in its place is exactly the mutation the brief
	// describes: a real handler, edited to stop calling actor(r), and the
	// question is whether the walk still says it does.
	const from = "a.Store.RetireAsset(r.Context(), actor(r), id)"
	const to = "a.Store.RetireAsset(r.Context(), domain.SystemActor, id)"
	if !strings.Contains(string(original), from) {
		t.Fatalf("AssetRetire's store call no longer reads %q; update this test's fixture to match "+
			"the current call site before trusting its result", from)
	}
	mutated := strings.Replace(string(original), from, to, 1)

	tmp := t.TempDir()
	for _, name := range []string{"assets.go", "app.go", "imports.go", "import_runner.go", "bulk_tags.go", "entitytags.go", "costs.go", "customvalues.go", "journal.go"} {
		src, err := os.ReadFile(filepath.Join(root, handlersDir, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		content := string(src)
		if name == "assets.go" {
			content = mutated
		}
		if err := os.WriteFile(filepath.Join(tmp, name), []byte(content), 0o600); err != nil {
			t.Fatalf("writing %s into temp dir: %v", name, err)
		}
	}

	fset := token.NewFileSet()
	funcs, _ := loadHandlerFuncs(t, fset, tmp)
	decl, ok := funcs["AssetRetire"]
	if !ok {
		t.Fatal("AssetRetire not found in the temp copy; the fixture file list above is incomplete")
	}
	visited := map[string]bool{}
	store := map[string]bool{}
	if callGraphReachesActor(decl, funcs, visited, store) {
		t.Fatal("callGraphReachesActor still reports AssetRetire as reaching actor( after the " +
			"actor(r) call was deleted and replaced with domain.SystemActor -- the walk is not " +
			"following the call graph, and its green result on the real router means nothing")
	}
}
