// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// WP-G1 Task 7's structural half, copying boundary_source_test.go's two
// techniques (that file's opening comment explains why a runtime-only check
// is not enough): the compile barrier (domain.Actor cannot satisfy
// domain.Permit), the width lock (Permit has exactly three named methods),
// and a minter budget in dynamicTargetBudget's style, plus the two
// classification checks the security review asked for: an unclassified
// entity type must fail loudly, and the classification must cover every
// entity type this codebase actually audits.

// ---------------------------------------------------------------------------
// The compile barrier and the width lock
// ---------------------------------------------------------------------------

// TestThePermitInterfaceCannotBeWidenedWithoutSayingSo pins Permit at exactly
// three methods, by name. A fourth method compiles fine and quietly grows
// what a Permit can be asked to do -- and since a Permit is immutable by
// design (see scopedPermit's doc comment in internal/domain/role.go), a
// MUTATING fourth method is exactly the change this test exists to catch
// before it reaches production, not merely to flag as unusual in review.
func TestThePermitInterfaceCannotBeWidenedWithoutSayingSo(t *testing.T) {
	iface := reflect.TypeOf((*domain.Permit)(nil)).Elem()
	if n := iface.NumMethod(); n != 3 {
		t.Fatalf("domain.Permit has %d methods, want 3 (Actor, Covers, isPermit). "+
			"Every method added here is another capability every existing "+
			"Permit implementation silently gains.", n)
	}
	for _, name := range []string{"Actor", "Covers", "isPermit"} {
		if _, ok := iface.MethodByName(name); !ok {
			t.Errorf("domain.Permit has no %s method", name)
		}
	}
}

// TestDomainActorDoesNotSatisfyPermit is the compile barrier itself, asserted
// as a fact about the types rather than assumed: domain.Actor must NOT
// satisfy domain.Permit, which is what turns store.write/writeSerializable/
// writeTx's parameter change into a compile error at every call site that
// still passes a bare Actor -- the exact analogue of *SQLStore deliberately
// not satisfying ObservedStore in TestObservedPathTouchesNoDeclaredTable.
func TestDomainActorDoesNotSatisfyPermit(t *testing.T) {
	iface := reflect.TypeOf((*domain.Permit)(nil)).Elem()
	if reflect.TypeOf(domain.Actor{}).Implements(iface) {
		t.Fatal("domain.Actor satisfies domain.Permit, so a handler holding only an " +
			"Actor could pass it anywhere a Permit is required and nothing would fail " +
			"to compile. isPermit exists on the interface specifically to make this " +
			"impossible; it is being satisfied some other way and that other way needs " +
			"finding, not silencing.")
	}
}

// ---------------------------------------------------------------------------
// The minter budget
// ---------------------------------------------------------------------------

// permitMinterDirs is where a function returning a Permit is allowed to live
// at all: internal/domain (the interface's own package, the only one that
// can implement it), internal/auth (Authorizer.Permit, the one gate that
// turns a signed-in user into a decision), and internal/store.
//
// internal/store WAS NOT HERE UNTIL WP-G4B WAVE C, and an auth review named
// that the single weakest assumption in the whole design: this package's own
// authorizeSavedViewOwner, authorizeJournalSubject and their siblings
// (storePermitMinters, below) each check a caller's existing Permit and mint
// a NARROWER one scoped to the one row being written -- domain.ScopedPermit
// called from inside internal/store, not internal/domain or internal/auth --
// and nothing before this scanned the one directory where a route registered
// through the `self` gate (routes.go) actually depends on that narrowing
// happening correctly. A store method that minted a permit from
// p.Actor() without first checking p.Covers(subjectType, subjectID) would be
// exactly the "self stops being write your own things and starts being
// write anything" failure routes.go's own comment on the self registrar
// warns about, and it would have compiled, run and passed every OTHER test
// in this suite, because those all assert on domain/auth alone.
var permitMinterDirs = []string{"internal/domain", "internal/auth", "internal/store"}

// permitMinterBudget is how many Permit-returning functions each directory
// is expected to contain. Exact, not a maximum.
//
// THIS IS GOVERNANCE, NOT A SECURITY CONTROL, and that is worth saying
// out loud rather than leaving implicit. Somebody CAN add a minter and bump
// this number in the same commit, and the test will stay green -- nothing
// here can stop that, because a number that could stop it would just be a
// second thing to update in the same commit. What the budget buys is that
// the addition is VISIBLE: a reviewer scanning a diff sees a budget line
// change and knows to look at what grew the capability to mint an
// authorization decision. A new permit minter is a security-relevant
// change. It requires auth-reviewer and sign-off from the repository owner
// before it merges. If you are editing this number, that is the
// conversation you are meant to be having, not a number to satisfy CI with.
var permitMinterBudget = map[string]int{
	"internal/domain": 3, // AdministratorPermit, SystemPermit, ScopedPermit
	"internal/auth":   1, // Authorizer.Permit
	// internal/store's budget is len(storePermitMinters), asserted equal in
	// TestOnlyTheNamedFunctionsMintAPermit's own setup below rather than
	// hand-kept in step with it -- the whole reason storePermitMinters
	// carries a reason per entry is that ADDING one is meant to be a
	// conversation, and a literal here that could silently drift out of
	// step with that map would be a second, easier way to add one without
	// having it.
	"internal/store": len(storePermitMinters),
}

// storePermitMinters is the narrow-minter allowlist for internal/store, the
// gap the WP-G4b Wave C auth review named directly (see permitMinterDirs'
// own comment on why this directory was not scanned at all before).
//
// EVERY ENTRY HERE FOLLOWS THE SAME SHAPE, and that shape is the property
// being allowlisted, not just the name: check the CALLER'S OWN permit with
// p.Covers(subjectType, subjectID) -- proving they may already write the
// SUBJECT the new row hangs off -- and only then mint a fresh
// domain.ScopedPermit scoped to exactly the one row being created or
// touched, off p.Actor(), never off a fresh domain.SystemActor or
// domain.AdministratorPermit. A minter that skipped the Covers check, or
// that scoped the new permit wider than the one row, would still compile
// and pass this test's structural checks; that is why the reason recorded
// for each entry names WHICH subject is checked and why that subject is the
// right one, not merely that a check exists. Listed individually, same
// discipline as dynamicTargetAllowlist (boundary_source_test.go): a new
// entry is a diff somebody has to read, not a number silently bumped.
var storePermitMinters = map[string]string{
	// authorizeSavedViewOwner (savedviews.go): saved_view classifies
	// ScopeSubjectDerived (docs/AUDIT.md) -- its subject is the person who
	// owns it, not a project. Checks p.Actor().Kind == user and
	// p.Actor().ID == ownerID (the STORED owner on update/retire, never a
	// submitted field -- see UpdateSavedView/RetireSavedView's own
	// comments), then scopes to exactly this view id. No Administrator
	// exception, deliberately (see the function's own comment).
	"authorizeSavedViewOwner": "saved_view's subject is a person: checks p.Actor().ID " +
		"against the row's stored owner, then scopes to exactly that view id",

	// authorizeJournalSubject (journal.go): a journal note's subject is
	// whatever entity it is written on. Checks p.Covers(subjectType,
	// subjectID) against the note's own EntityType/EntityID, then scopes to
	// exactly this note id.
	"authorizeJournalSubject": "a journal note's subject is the entity it documents: " +
		"checks p.Covers(subjectType, subjectID), then scopes to exactly that note id",

	// authorizeInstanceSubjects (services.go): a service_instance placement
	// is two-ended -- checks p.Covers("service", serviceID) AND
	// p.Covers("asset", hostAssetID), so a project owner cannot place their
	// own service on somebody else's host or somebody else's service on
	// their own host (docs/rbac-design.md §4, "both endpoints of a
	// relationship must be in scope"), then scopes to exactly this
	// instance id.
	"authorizeInstanceSubjects": "a placement has two owners, the service and the host " +
		"asset: checks p.Covers on both before scoping to exactly that instance id",

	// authorizeInterfaceSubject (network.go): an interface carries no
	// project of its own -- its scope is entirely the owning asset's, one
	// hop away. Checks p.Covers("asset", assetID), then scopes to exactly
	// this interface id.
	"authorizeInterfaceSubject": "an interface's subject is the asset it belongs to: " +
		"checks p.Covers(\"asset\", assetID), then scopes to exactly that interface id",

	// authorizeAddressSubject (network.go): an address attached to an
	// interface inherits that interface's asset as its subject and checks
	// p.Covers("asset", ...) the same as authorizeInterfaceSubject. An
	// UNATTACHED address (interfaceID == nil) is the one entry here that
	// does not mint a narrower permit at all -- it returns the caller's own
	// permit unchanged, which is safe only because ip_address is
	// ScopeSubjectDerived and a project owner's ScopedPermit never lists
	// "ip_address" among its entities (auth.Authorizer.Permit only ever
	// populates asset/service/circuit), so Covers still refuses them lower
	// down at tx.log; an Administrator or System permit still covers it
	// unconditionally, which is the intended fail-closed behaviour, not an
	// escalation.
	"SQLStore.authorizeAddressSubject": "an attached address's subject is the interface's asset " +
		"(checks p.Covers(\"asset\", ...)); an unattached one passes the caller's own " +
		"permit through, refused downstream for a project owner because ip_address is " +
		"never in their scoped entities",
}

// permitMinterNames is the exact, named set TestOnlyTheNamedFunctionsMintAPermit
// checks against. A minter that DISAPPEARS from this set fails the test too --
// it usually moved somewhere not on this list, which is exactly the drift
// dynamicTargetBudget's own doc comment warns about for the same shape of
// check.
var permitMinterNames = map[string]bool{
	"AdministratorPermit": true,
	"SystemPermit":        true,
	"ScopedPermit":        true,
	"Authorizer.Permit":   true,
}

// permitMinter is one function or method whose result type is domain.Permit.
type permitMinter struct {
	name string // "AdministratorPermit" or "Authorizer.Permit" (Recv.Method)
	file string // relative to the repo root
}

// TestOnlyTheNamedFunctionsMintAPermit walks internal/domain and internal/auth
// and collects every function or method whose result type is Permit (bare, in
// domain's own files) or domain.Permit (qualified, elsewhere). The set found
// must be exactly permitMinterNames, and the per-directory count must match
// permitMinterBudget.
func TestOnlyTheNamedFunctionsMintAPermit(t *testing.T) {
	root := repoRoot(t)
	var found []permitMinter
	seen := map[string]bool{}

	for _, dir := range permitMinterDirs {
		full := filepath.Join(root, dir)
		entries, err := os.ReadDir(full)
		if err != nil {
			t.Fatalf("reading %s: %v", dir, err)
		}
		fset := token.NewFileSet()
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(full, name)
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parsing %s: %v", path, err)
			}
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Type.Results == nil {
					continue
				}
				results := fn.Type.Results.List
				// Returning a Permit FIRST is the whole test for whether
				// something mints one. Everything after that is shape, and
				// an unrecognised shape must never be a way out of this
				// scan: this test exists so that nothing outside the named
				// list can mint a Permit, so a signature it does not
				// recognise has to fail loudly rather than skip quietly.
				// A `continue` here would have let func f() (Permit, string)
				// mint permits with the lock still reporting green.
				if len(results) == 0 || !returnsPermit(results[0].Type, f.Name.Name) {
					continue
				}
				mname := fn.Name.Name
				if fn.Recv != nil && len(fn.Recv.List) == 1 {
					mname = recvTypeName(fn.Recv.List[0].Type) + "." + mname
				}
				rel := dir + "/" + name
				// One result (every domain.* minter) or two -- a Permit and
				// an error (auth.Authorizer.Permit, WP-G1 Task 12): a real
				// derivation of a project owner's scope asks the store, and
				// a store call that cannot fail is not a store call this
				// codebase trusts (see CLAUDE.md's "wrap errors" rule).
				// Any other shape is still recorded as a minter above --
				// it is only reported here, never skipped.
				if len(results) > 2 || (len(results) == 2 && !isErrorType(results[1].Type)) {
					t.Errorf("%s (%s) mints a Permit with an unrecognised signature; "+
						"expected (Permit) or (Permit, error)", mname, rel)
				}
				found = append(found, permitMinter{name: mname, file: rel})
				if !seen[mname] {
					seen[mname] = true
				} else {
					t.Errorf("%s is defined more than once as a Permit minter", mname)
				}
			}
		}
	}

	for _, m := range found {
		if !permitMinterNames[m.name] && storePermitMinters[m.name] == "" {
			t.Errorf("%s (%s) mints a domain.Permit but is not in permitMinterNames or "+
				"storePermitMinters. A new permit minter is a security-relevant change: "+
				"it needs auth-reviewer and the repository owner's sign-off, and this "+
				"test failing is what makes the addition impossible to miss in the diff.",
				m.name, m.file)
		}
	}
	for name := range permitMinterNames {
		if !seen[name] {
			t.Errorf("%s is expected to mint a domain.Permit but was not found. "+
				"It usually moved somewhere not covered by permitMinterDirs, which "+
				"is exactly the kind of disappearance dynamicTargetBudget's own "+
				"doc comment warns about for this shape of check.", name)
		}
	}
	for name := range storePermitMinters {
		if !seen[name] {
			t.Errorf("%s is expected to mint a domain.Permit (storePermitMinters) but "+
				"was not found. It usually moved somewhere not covered by "+
				"permitMinterDirs, which is exactly the kind of disappearance "+
				"dynamicTargetBudget's own doc comment warns about for this shape of "+
				"check.", name)
		}
	}

	byDir := map[string]int{}
	for _, m := range found {
		dir := filepath.Dir(m.file)
		byDir[dir]++
	}
	for dir, want := range permitMinterBudget {
		if got := byDir[dir]; got != want {
			t.Errorf("%s has %d Permit-returning functions, expected %d. "+
				"A NEW one and a bumped budget number in the same commit is exactly "+
				"the case this budget cannot prevent by itself -- see this map's own "+
				"doc comment for what it buys instead.", dir, got, want)
		}
	}
}

// returnsPermit reports whether typeExpr names Permit: bare "Permit" only
// inside the domain package's own files (pkgName == "domain"), or the
// qualified "domain.Permit" selector everywhere else.
func returnsPermit(typeExpr ast.Expr, pkgName string) bool {
	switch e := typeExpr.(type) {
	case *ast.Ident:
		return pkgName == "domain" && e.Name == "Permit"
	case *ast.SelectorExpr:
		pkgIdent, ok := e.X.(*ast.Ident)
		return ok && pkgIdent.Name == "domain" && e.Sel.Name == "Permit"
	default:
		return false
	}
}

// isErrorType reports whether typeExpr is the bare identifier "error" --
// good enough here because the only two-result shape this scan accepts is
// (domain.Permit, error), and a function returning (domain.Permit,
// somethingElse) is not one already named in permitMinterNames, so it would
// fail the "not in permitMinterNames" check below regardless.
func isErrorType(typeExpr ast.Expr) bool {
	ident, ok := typeExpr.(*ast.Ident)
	return ok && ident.Name == "error"
}

// recvTypeName strips the pointer off a method receiver's type expression, so
// `*Authorizer` and `Authorizer` both read as "Authorizer".
func recvTypeName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return "?"
}

// ---------------------------------------------------------------------------
// The seam: write / writeSerializable / writeTx
// ---------------------------------------------------------------------------

// TestEveryWriteTransactionHelperTakesAPermit is the AST half of the seam
// docs/rbac-design.md §6 and the WP-G1 plan name: write, writeSerializable
// and writeTx must each take a domain.Permit parameter and NONE may take a
// domain.Actor parameter any more. This is what makes the 148-site
// conversion (Task 10) a compile error at every remaining domain.Actor call
// site rather than a convention -- checked structurally so a future edit
// that quietly reintroduces an Actor parameter (an overload-shaped helper,
// say) fails here instead of shipping.
func TestEveryWriteTransactionHelperTakesAPermit(t *testing.T) {
	path := filepath.Join(repoRoot(t), "internal/store/store.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	want := map[string]bool{"write": true, "writeSerializable": true, "writeTx": true}
	found := map[string]bool{}

	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || !want[fn.Name.Name] {
			continue
		}
		found[fn.Name.Name] = true

		hasPermit, hasActor := false, false
		for _, param := range fn.Type.Params.List {
			sel, ok := param.Type.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			pkgIdent, ok := sel.X.(*ast.Ident)
			if !ok || pkgIdent.Name != "domain" {
				continue
			}
			switch sel.Sel.Name {
			case "Permit":
				hasPermit = true
			case "Actor":
				hasActor = true
			}
		}
		if !hasPermit {
			t.Errorf("%s has no domain.Permit parameter", fn.Name.Name)
		}
		if hasActor {
			t.Errorf("%s still has a domain.Actor parameter -- the seam requires "+
				"exactly a Permit, not a Permit alongside an Actor", fn.Name.Name)
		}
	}
	for name := range want {
		if !found[name] {
			t.Errorf("could not find func (s *SQLStore) %s in %s", name, path)
		}
	}
}

// ---------------------------------------------------------------------------
// The loud hole, and the positive audit matrix
// ---------------------------------------------------------------------------

// TestAnUnclassifiedEntityTypeFailsLoudlyRatherThanBeingAllowed is the
// dynamicTable lesson applied to authorization: an entity type nobody
// classified must be refused, and the refusal must NAME the type rather than
// disappearing into a bare "forbidden" -- the same reason dynamicTable is a
// sentinel string rather than a skip.
//
// It exercises the real guard, tx.log, rather than only ScopedPermit.Covers
// in isolation: constructing a bare *tx and calling log directly is safe
// here because the guard runs and returns before ever touching t.tx, which
// is nil in this test.
func TestAnUnclassifiedEntityTypeFailsLoudlyRatherThanBeingAllowed(t *testing.T) {
	permit := domain.ScopedPermit(
		domain.Actor{ID: "po-1", Name: "po-1", Kind: domain.ActorKindUser},
		nil,
		domain.ScopedEntities{"something_new": {"id-1": true}},
	)
	if permit.Covers("something_new", "id-1") {
		t.Fatal("Covers authorized an entity type nothing classifies -- an " +
			"unclassified type must be refused, not accepted because a caller " +
			"happened to list it in the entities set")
	}

	tr := &tx{permit: permit, actor: permit.Actor()}
	err := tr.log(context.Background(), "something_new", "id-1", domain.ActionCreate, "{}", "")
	if err == nil {
		t.Fatal("log accepted a write to an unclassified entity type")
	}
	if !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("error = %v, want it to wrap domain.ErrForbidden", err)
	}
	if !strings.Contains(err.Error(), "something_new") {
		t.Errorf("error %q does not name the unclassified entity type -- a refusal "+
			"that cannot be traced back to which entity type caused it is exactly "+
			"the silent hole this test exists to rule out", err.Error())
	}
}

// auditedEntityTypes is every entity_type value internal/store writes to
// change_log today.
//
// THIS LIST HAS ALREADY DRIFTED ONCE, which is worth knowing before trusting
// it. WP-G1 Task 11 added AssignProject/ReleaseProject writing
// entity_type "user_project" (internal/store/user_projects.go) and neither
// this list nor entityScope gained it; an auth review found the gap. The
// note at the end of this comment -- "add it here AND to entityScope in the
// same commit" -- is the right instruction and it was simply not followed,
// because nothing failed when it was not. A hand-maintained list is
// exhaustive only until the next writer lands, so treat a passing census as
// evidence about the types IN the list, never as proof the list is complete.
//
// COMPUTED BY READING THE SOURCE, NOT GUESSED, and kept as a literal list
// here (rather than as a live AST walk in this test) for the same reason
// dynamicTargetAllowlist is a hand-maintained map with per-entry comments
// instead of a generic dataflow solver: resolving costTable.entity and
// vocabulary.go's table parameter back to their finite set of literal values
// needs a handful of one-off call-graph traces that are cheaper to do once,
// by hand, and record, than to keep re-deriving generically on every test
// run. Enumerated by parsing every call to
// tx.log/logCreate/logUpdate/logUpdateBatch across internal/store's non-test
// files and resolving each entityType argument -- literal, package-level
// string constant, or (for the handful of dynamic call sites) the finite set
// of values its enclosing function is actually called with. The WP-G1 plan's
// own route survey (docs/superpowers/plans/2026-08-26-rbac.md) estimates
// "~54"; the true count, arrived at this way, is 71 -- the plan's figure was
// a route-count estimate, not a count of this list, and the two were never
// meant to match exactly. If this test ever fails because a new entityType
// literal shipped uncoverered, add it here AND to domain.role.go's
// entityScope in the same commit -- the whole point of this test is that
// those two edits cannot drift apart silently.
var auditedEntityTypes = []string{
	"aggregate", "app_user", "asn", "asset", "asset_cost", "asset_kind",
	"backend_member", "backend_pool", "certificate", "circuit", "circuit_cost",
	"circuit_termination", "cluster", "container_engine", "cost_kind",
	"custom_field", "data_class", "dependency", "device_type", "endpoint",
	"environment", "environment_role", "fhrp_group", "health_override",
	"identity", "inflation_rate", "interface", "interface_form_factor",
	"ip_address", "ip_address_role", "ip_range", "journal_entry", "l2vpn",
	"l2vpn_termination", "link", "manufacturer", "net_anchor", "net_attachment",
	"net_group", "net_group_member", "net_uplink", "observed_transition",
	"port_pass_through", "power_feed", "power_input", "power_panel",
	"power_source", "prefix", "project", "project_asset", "project_circuit",
	"project_cost", "project_service", "provider", "responsibility_role",
	"rir", "route", "rt_container", "rt_k8s", "rt_systemd", "rt_windows",
	"saved_view", "service", "service_cost", "service_instance", "service_kind",
	"storage_kind", "tag", "team", "unmatched_observation", "user_project",
	"vlan", "vlan_group",
}

// TestTheScopeClassificationCoversEveryAuditedEntityType is the positive
// audit matrix for authorization: TestNoAssembledWriteReachesChangeLog and
// its neighbours prove nothing FORGES an entry or reaches change_log by an
// unchecked path, but none of them prove that every entity type this
// codebase actually logs has an authorization class at all. A type with no
// entry falls through ScopeClassOf to the zero ScopeClass, which
// scopedPermit.Covers treats as "not project-linked" and refuses -- safe by
// construction -- but an Administrator whose entity type silently stopped
// being an entity type recognised by ClassifiedTables() at all would be a
// real gap this test is written to catch, and the earlier one -- an
// unclassified type accepted -- is exactly what the previous test exercises
// directly.
func TestTheScopeClassificationCoversEveryAuditedEntityType(t *testing.T) {
	tables := map[string]bool{}
	for _, tbl := range domain.ClassifiedTables() {
		tables[tbl] = true
	}

	validClass := map[domain.ScopeClass]bool{
		domain.ScopeProjectLinked:  true,
		domain.ScopeSubjectDerived: true,
		domain.ScopeEstateConfig:   true,
		domain.ScopeTopology:       true,
	}

	seen := map[string]bool{}
	for _, et := range auditedEntityTypes {
		if seen[et] {
			t.Errorf("%q appears twice in auditedEntityTypes", et)
		}
		seen[et] = true

		class := domain.ScopeClassOf(et)
		if !validClass[class] {
			t.Errorf("%q has no scope classification in internal/domain/role.go's "+
				"entityScope -- every entity_type this codebase writes to change_log "+
				"must fall into exactly one of ScopeProjectLinked, ScopeSubjectDerived, "+
				"ScopeEstateConfig or ScopeTopology", et)
		}
		if !tables[et] {
			t.Errorf("%q is audited but does not appear in domain.ClassifiedTables(); "+
				"the two lists should never name a different set of tables", et)
		}
	}

	// The reverse direction, cheaply: ClassifiedTables() also contains
	// tables that are never their own change_log entity_type (set tables
	// folded into a parent's audit entry, per CLAUDE.md's "fold the set
	// into the audited value" rule) -- so this is deliberately NOT a
	// symmetric diff. That asymmetry is a separate, already-covered
	// property (CLAUDE.md's own audit rules), not this test's job.
	sort.Strings(auditedEntityTypes)
}
