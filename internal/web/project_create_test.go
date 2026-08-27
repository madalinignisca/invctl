// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/auth"
	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// Create-and-link, WP-G1 Task 14 (docs/rbac-design.md §4). The store-level
// permit tests live in internal/store/project_create_test.go, driven
// directly against the permit layer per that file's own comment; this file
// covers what only the real router and the real handlers can prove: the
// route table's shape, the handler source's refusal to read an id off the
// request, and the escalation attempt end to end through HTTP.

// mustWebProjectOwner creates a real, loggable-in project owner account and
// assigns it to projectID, so a test can exercise these routes exactly the
// way a browser would -- CSRF, session, the works -- rather than only
// through the permit layer.
func mustWebProjectOwner(t *testing.T, h *harness, username, password, projectID string) {
	t.Helper()
	ctx := context.Background()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hashing password: %v", err)
	}
	u, err := domain.NewAppUser(store.NewID(), username, domain.UserSourceLocal, h.store.Now())
	if err != nil {
		t.Fatalf("building user %s: %v", username, err)
	}
	u.Role = domain.RoleProjectOwner
	u.PasswordHash = &hash
	if err := h.store.CreateUser(ctx, domain.AdministratorPermit(domain.SystemActor), u); err != nil {
		t.Fatalf("creating project owner %s: %v", username, err)
	}
	if err := h.store.AssignProject(ctx, domain.AdministratorPermit(domain.SystemActor), u.ID, projectID); err != nil {
		t.Fatalf("assigning %s to project %s: %v", username, projectID, err)
	}
}

// TestAProjectOwnerCannotReachThePlainCreateRouteAtAll: POST /assets -> 403.
// Replaces "cannot create with no project at all" -- there is no longer a
// route by which a project owner can create an entity outside a scope,
// which is §4's principle expressed as routing rather than as a runtime
// check.
//
// THIS PASSES TODAY FOR A REASON THAT WILL CHANGE: CanWrite(RoleProjectOwner)
// still returns false (Task 13 has not landed), so middleware.RequireAdmin
// refuses this request before AssetCreate is ever reached, for ANY write
// route, not because of anything specific to /assets. Task 13's own step 3
// re-runs this claim once CanWrite is real for a project owner -- see that
// task's plan. What THIS test can prove today, and does, is the structural
// half: POST /assets is registered to AssetCreate, not to
// AssetCreateInProject, so there is no routing table under which "no
// project" could ever mean "create it anyway" for anyone who does reach it.
func TestAProjectOwnerCannotReachThePlainCreateRouteAtAll(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	frontend := makeProject(t, h, "t-frontend-plain", "Frontend")
	mustWebProjectOwner(t, h, "po-plain", "po-plain-password", frontend)
	h.logout()
	h.login("po-plain", "po-plain-password")

	resp := h.post("/assets", url.Values{
		"csrf_token": {h.csrfToken("/")},
		"kind":       {domain.KindServer},
		"name":       {"should-not-exist"},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("POST /assets as a project owner returned %d, want 403", resp.StatusCode)
	}

	// The structural claim: routes.go never registers POST /assets to the
	// create-in-project handler, and never reads a project out of a form
	// field for it.
	//
	// NOT routescan.WriteRoutes: that helper's own repoRoot resolves
	// "../../.." relative to internal/web/routescan's package directory, so
	// calling it from this package (one directory shallower) silently walks
	// to the wrong root -- found while writing this test, not assumed.
	// Reading routes.go directly, from THIS file's own repoRoot, avoids
	// depending on another package's relative-path assumption.
	//
	// Mutation (Step 5): route POST /assets to AssetCreateInProject with the
	// project read from a form field -- this must go red.
	handler := writeRouteHandler(t, "POST /assets")
	if handler != "AssetCreate" {
		t.Errorf("POST /assets -> %s, want AssetCreate", handler)
	}
}

// TestAProjectOwnerCannotLinkAnExistingAssetToTheirProject is the escalation
// in docs/rbac-design.md §4, written as a test: create db-prod unowned as an
// Administrator, then attempt to link it (POST /projects/{id}/assets, the
// plain link route, never .../assets/new) as a project owner assigned to
// frontend.
//
// THIS ALSO PASSES TODAY VIA RequireAdmin, NOT VIA Covers: see
// internal/store/project_create_test.go's
// TestScopedPermitCannotLinkIntoAProjectItDoesNotHold doc comment for the
// documented, carried-forward gap this task's report flags -- Covers's Task
// 12 carve-out cannot itself tell "link an existing entity" apart from
// "create and link" WITHIN a project the permit holds, so this route's
// safety currently rests entirely on CanWrite(RoleProjectOwner) staying
// false. Re-run and re-verify once Task 13 flips it.
func TestAProjectOwnerCannotLinkAnExistingAssetToTheirProject(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	frontend := makeProject(t, h, "t-frontend-link", "Frontend")
	dbProd := mustAssetWeb(t, h, domain.KindServer, "db-prod")
	before, err := h.store.GetAsset(context.Background(), dbProd)
	if err != nil {
		t.Fatalf("reading db-prod before: %v", err)
	}

	mustWebProjectOwner(t, h, "po-link", "po-link-password", frontend)
	h.logout()
	h.login("po-link", "po-link-password")

	resp := h.post("/projects/"+frontend+"/assets", url.Values{
		"csrf_token": {h.csrfToken("/projects/" + frontend)},
		"asset_id":   {dbProd}, "relation": {domain.ProjectOwns},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("linking an existing asset as a project owner returned %d, want 403", resp.StatusCode)
	}

	after, err := h.store.GetAsset(context.Background(), dbProd)
	if err != nil {
		t.Fatalf("reading db-prod after: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Error("db-prod changed despite the refused link")
	}
	links, err := h.store.ListProjectAssets(context.Background(), frontend)
	if err != nil {
		t.Fatalf("listing project assets: %v", err)
	}
	for _, l := range links {
		if l.AssetID == dbProd {
			t.Error("db-prod was linked to frontend despite the refusal")
		}
	}
}

// TestPassingAnExistingAssetIdToTheCreatePathIsRefusedAndSeizesNothing is
// WP-G1 Task 14 Step 3's behavioural escalation test.
//
// Run as an ADMINISTRATOR, not a project owner: the attack under test --
// "does the create-in-project handler treat a caller-supplied id as
// authoritative" -- has nothing to do with WHO is signed in, and
// CanWrite(RoleProjectOwner) still being false means a project owner cannot
// reach this route via HTTP at all today (see the two tests above). An
// Administrator can reach it right now, through the real router, and the
// handler code under test (AssetCreateInProject) is exactly the same code a
// project owner will reach once Task 13 lands -- store.NewID() is called
// unconditionally, with no branch on who the caller is.
func TestPassingAnExistingAssetIdToTheCreatePathIsRefusedAndSeizesNothing(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	frontend := makeProject(t, h, "t-frontend-seize", "Frontend")
	dbProd := mustAssetWeb(t, h, domain.KindServer, "db-prod")
	before, err := h.store.GetAsset(context.Background(), dbProd)
	if err != nil {
		t.Fatalf("reading db-prod before: %v", err)
	}

	resp := h.post("/projects/"+frontend+"/assets/new", url.Values{
		"csrf_token": {h.csrfToken("/projects/" + frontend)},
		// THE ATTACK: naming an existing, out-of-scope asset's id on the
		// create form.
		"id":   {dbProd},
		"kind": {domain.KindServer},
		"name": {"seized"},
	}, false)
	defer resp.Body.Close()

	after, err := h.store.GetAsset(context.Background(), dbProd)
	if err != nil {
		t.Fatalf("reading db-prod after: %v", err)
	}
	// Field-for-field, including row_version -- not merely "still exists".
	if !reflect.DeepEqual(before, after) {
		t.Errorf("db-prod changed: before = %+v, after = %+v", before, after)
	}
	links, err := h.store.ListProjectAssets(context.Background(), frontend)
	if err != nil {
		t.Fatalf("listing project assets: %v", err)
	}
	for _, l := range links {
		if l.AssetID == dbProd {
			t.Fatal("db-prod was linked to frontend by the create-in-project route")
		}
	}

	// SUCCESS IS THE REQUIRED OUTCOME, and a failure here is a FAILURE of
	// this test. The brief allowed either "the request failed" or "it
	// created a new asset with a different id", but for THIS implementation
	// only the second can happen: the handler calls store.NewID() and never
	// reads an id from the request, so the submitted id is inert and the
	// create must simply succeed with a fresh one.
	//
	// Accepting the failure branch is what made this test unable to fail.
	// With it, the Step 5 mutation "handler takes the id from the form
	// instead of store.NewID()" left this test GREEN: the INSERT hit
	// db-prod's primary key, the request 500'd, and the failure branch
	// called that acceptable. The seizure was refused by the schema rather
	// than by the code this test exists to guard -- which is precisely the
	// inference the brief said not to make, since a primary key is a fact
	// about today's schema and the guarantee has to be about behaviour.
	//
	// So: the request MUST succeed, and it must have minted a different id.
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d -- the submitted id must be ignored, "+
			"not refused; a refusal here means something read it",
			resp.StatusCode, http.StatusSeeOther)
	}
	loc := resp.Header.Get("Location")
	newID := strings.TrimPrefix(loc, "/assets/")
	if newID == "" || newID == loc {
		t.Fatalf("redirect %q does not name a new asset", loc)
	}
	if newID == dbProd {
		t.Fatal("the created asset kept db-prod's id -- the submitted id was not ignored")
	}
	created, err := h.store.GetAsset(context.Background(), newID)
	if err != nil {
		t.Fatalf("the redirect named an asset that does not exist: %v", err)
	}
	if created.Name != "seized" {
		t.Errorf("the new asset's name = %q, want %q", created.Name, "seized")
	}
}

// TestASubmittedDuplicateServiceCodeIsRefusedByTheUniqueIndex is "the code
// variant" Step 3 names: a duplicate service code on the create-in-project
// form is refused by the existing unique index, exercising the SAME
// id-is-ignored guarantee from the other side -- a caller cannot collide
// with an existing row's NATURAL key any more than with its surrogate one.
func TestASubmittedDuplicateServiceCodeIsRefusedByTheUniqueIndex(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	frontend := makeProject(t, h, "t-frontend-code", "Frontend")

	existing, err := domain.NewService(store.NewID(), domain.ServiceSpec{
		Code: "dup-svc", Name: "dup-svc", Kind: domain.SvcAPI,
		EnvironmentID: h.refs.Environments["prod"], Availability: domain.AvailStandalone, Tier: 2,
	}, h.store.Now())
	if err != nil {
		t.Fatalf("building existing service: %v", err)
	}
	if err := h.store.CreateService(context.Background(), domain.AdministratorPermit(domain.SystemActor), existing); err != nil {
		t.Fatalf("creating existing service: %v", err)
	}

	resp := h.post("/projects/"+frontend+"/services/new", url.Values{
		"csrf_token": {h.csrfToken("/projects/" + frontend)},
		"code":       {"dup-svc"}, "name": {"seized-service"}, "kind": {domain.SvcAPI},
		"environment_id": {h.refs.Environments["prod"]}, "availability": {domain.AvailStandalone},
		"tier": {"2"},
	}, false)
	defer resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Fatalf("duplicate code returned %d, want a refusal", resp.StatusCode)
	}

	links, err := h.store.ListProjectServices(context.Background(), frontend)
	if err != nil {
		t.Fatalf("listing project services: %v", err)
	}
	for _, l := range links {
		if l.ServiceID == existing.ID {
			t.Error("the existing service got linked to frontend by the refused submission")
		}
	}
}

// TestNoCreateHandlerReadsAnIdFromTheRequest is WP-G1 Task 14 Step 3's
// structural half for the handlers: AssetCreateInProject, ServiceCreateInProject
// and CircuitCreateInProject must never name "id" in a formValue or
// PathValue call -- store.NewID() is the id, and nothing else.
func TestNoCreateHandlerReadsAnIdFromTheRequest(t *testing.T) {
	targets := map[string]string{
		"internal/web/handlers/assets.go":   "AssetCreateInProject",
		"internal/web/handlers/services.go": "ServiceCreateInProject",
		"internal/web/handlers/circuits.go": "CircuitCreateInProject",
	}

	root := repoRoot(t)
	for rel, funcName := range targets {
		path := filepath.Join(root, rel)
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", rel, err)
		}
		var fn *ast.FuncDecl
		for _, decl := range file.Decls {
			if f, ok := decl.(*ast.FuncDecl); ok && f.Name.Name == funcName {
				fn = f
				break
			}
		}
		if fn == nil {
			t.Fatalf("%s not found in %s", funcName, rel)
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var isFormValue, isPathValue bool
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				isFormValue = fun.Name == "formValue" || fun.Name == "optionalString" || fun.Name == "submittedString"
			case *ast.SelectorExpr:
				isPathValue = fun.Sel.Name == "PathValue"
			}
			if !isFormValue && !isPathValue {
				return true
			}
			for _, arg := range call.Args {
				lit, ok := arg.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if lit.Value == `"id"` {
					t.Errorf("%s (%s) names \"id\" in a %s call at %s -- the id must come only "+
						"from store.NewID()", funcName, rel, describe(isFormValue), fset.Position(call.Pos()))
				}
			}
			return true
		})
	}
}

func describe(isFormValue bool) string {
	if isFormValue {
		return "formValue/optionalString/submittedString"
	}
	return "PathValue"
}

// writeRouteHandler reads routes.go and returns the handler name registered
// against exactly one write("PATTERN", app.Handler) call -- a narrower,
// self-contained version of routescan.WriteRoutes's walk (see the comment at
// its one call site above for why this file does not reuse that helper
// directly).
func writeRouteHandler(t *testing.T, pattern string) string {
	t.Helper()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "web", "routes.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing routes.go: %v", err)
	}
	var found string
	var count int
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		fn, ok := call.Fun.(*ast.Ident)
		if !ok || fn.Name != "write" || len(call.Args) != 2 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		unquoted := strings.Trim(lit.Value, `"`)
		if unquoted != pattern {
			return true
		}
		sel, ok := call.Args[1].(*ast.SelectorExpr)
		if !ok {
			t.Fatalf("write(%q, ...) at %s does not register a simple app.Handler value",
				pattern, fset.Position(call.Pos()))
		}
		found = sel.Sel.Name
		count++
		return true
	})
	if count == 0 {
		t.Fatalf("%q is not registered in routes.go at all", pattern)
	}
	if count > 1 {
		t.Fatalf("%q is registered %d times in routes.go, want exactly once", pattern, count)
	}
	return found
}

// repoRoot is a package-local copy of the store package's helper of the same
// name -- walking up from this test file's own working directory to find
// go.mod, so this package does not need to import an internal test-only
// symbol across package boundaries.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolving working directory: %v", err)
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
