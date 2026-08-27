// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package seed_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// WP-G1 Task 9, step 1 & 4: the seeder is a system credential, not a signed-in
// operator, and it must not be a hole by which the demo estate quietly proves
// a system credential can mint Administrator capability -- the exact shape
// docs/rbac-design.md and systemPermit's own doc comment
// (internal/domain/role.go) name as the thing WP-G1 closes.

// TestTheSeederRunsUnderASystemPermitAndNotAnAdministratorOne is a mutation
// target as well as a check: if seed.Permit is ever replaced with
// domain.AdministratorPermit(seed.Actor) at one of its three call sites, this
// goes red.
//
// Runtime half: the rows CreateProvider/CreateCircuit/CreateCircuitTermination
// write are attributed to the system actor, not to an Administrator-flavoured
// identity that happens to share the seeder's name.
func TestTheSeederRunsUnderASystemPermitAndNotAnAdministratorOne(t *testing.T) {
	f := newFixture(t)

	var actor, actorKind string
	err := f.store.DB().Reader.Get(&actor, f.store.DB().Reader.Rebind(
		`SELECT actor FROM change_log WHERE entity_type = 'provider' ORDER BY at LIMIT 1`))
	if err != nil {
		t.Fatalf("reading a provider's change_log row: %v", err)
	}
	if err := f.store.DB().Reader.Get(&actorKind, f.store.DB().Reader.Rebind(
		`SELECT actor_kind FROM change_log WHERE entity_type = 'provider' ORDER BY at LIMIT 1`)); err != nil {
		t.Fatalf("reading a provider's change_log row: %v", err)
	}
	if actor != "system" {
		t.Errorf("provider change_log.actor = %q, want %q -- domain.SystemPermit's Actor() "+
			"is fixed to domain.SystemActor regardless of the reason string passed to it",
			actor, "system")
	}
	if actorKind != "system" {
		t.Errorf("provider change_log.actor_kind = %q, want %q", actorKind, "system")
	}
}

// TestEverySystemPermitWriterProducesActorKindSystem is Step 1's positive
// check across both of today's system-permit writers: the seeder (via the
// fixture above) and the LDAP first-login upsert.
//
// The LDAP upsert does NOT call domain.SystemPermit at all -- see
// internal/store/role_management_test.go's package comment for why
// systemPermit.Covers's app_user exclusion makes that impossible, and why
// CreateUser keeps minting domain.AdministratorPermit(actor) internally
// instead. What this test actually checks for LDAP is the property Step 1
// cares about regardless of which Permit constructor ran: actor_kind is
// "system", because domain.Actor{Kind: ActorKindSystem} is what
// AdministratorPermit(actor).Actor() returns unchanged.
func TestEverySystemPermitWriterProducesActorKindSystem(t *testing.T) {
	f := newFixture(t)

	var actorKind string
	if err := f.store.DB().Reader.Get(&actorKind, f.store.DB().Reader.Rebind(
		`SELECT actor_kind FROM change_log WHERE entity_type = 'circuit' ORDER BY at LIMIT 1`)); err != nil {
		t.Fatalf("reading a circuit's change_log row: %v", err)
	}
	if actorKind != "system" {
		t.Errorf("the seeder's circuit change_log.actor_kind = %q, want %q", actorKind, "system")
	}

	u, err := f.store.UpsertLDAPUser(f.ctx, "task9-step1-ldap", "Task Nine", "")
	if err != nil {
		t.Fatalf("UpsertLDAPUser: %v", err)
	}
	var ldapActorKind string
	if err := f.store.DB().Reader.Get(&ldapActorKind, f.store.DB().Reader.Rebind(
		`SELECT actor_kind FROM change_log WHERE entity_type = 'app_user' AND entity_id = ?`), u.ID); err != nil {
		t.Fatalf("reading the LDAP user's change_log row: %v", err)
	}
	if ldapActorKind != "system" {
		t.Errorf("the LDAP upsert's app_user change_log.actor_kind = %q, want %q -- the "+
			"directory is the actor here, never the person signing in", ldapActorKind, "system")
	}
}

// ---------------------------------------------------------------------------
// TestOnlyTheNamedCallersMintASystemPermit -- lock the list.
// ---------------------------------------------------------------------------

// systemPermitCallerFiles is exactly where the identifier "SystemPermit"
// appears in internal/seed today.
//
// ONLY seed.go: it is the ONE place domain.SystemPermit is actually called --
// `var Permit = domain.SystemPermit("seed")` -- and every other write site
// (seed_engine.go, seed_drlink.go, seed_money.go) refers to that already-
// minted seed.Permit value, never to the constructor itself. Deliberately
// centralised this way: a second call site minting its own SystemPermit
// independently is exactly the kind of drift this list exists to catch
// before it is three call sites deep instead of one.
//
// THE LDAP UPSERT IS DELIBERATELY NOT ON THIS LIST, which is a deviation from
// the WP-G1 plan's Step 1 table (it names the LDAP upsert as a SystemPermit
// caller). It cannot be: systemPermit.Covers refuses every app_user write
// unconditionally (internal/domain/role.go), so calling it for CreateUser
// would make first login fail every time. See
// internal/store/role_management_test.go's package comment for the full
// reasoning and TestASystemPermitCannotCreateAnAccount for the proof.
//
// Same governance comment as permitMinterBudget in
// internal/store/permit_source_test.go: a new caller here is a
// security-relevant change. It requires auth-reviewer and the repository
// owner's sign-off before it merges. If you are editing this list or its
// budget, that is the conversation you are meant to be having, not a map to
// satisfy CI with.
var systemPermitCallerFiles = map[string]int{
	"internal/seed/seed.go": 1, // var Permit = domain.SystemPermit("seed")
}

// repoRoot resolves the repository root the same way
// internal/store/prune_test.go's does -- internal/seed sits at the same
// depth (internal/<pkg>), so "../.." is correct here too.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolving the repository root: %v", err)
	}
	return root
}

// TestOnlyTheNamedCallersMintASystemPermit walks internal/seed for source
// text mentioning "SystemPermit" and asserts the exact, budgeted set of files
// found is systemPermitCallerFiles.
//
// A source-text scan rather than an AST call-graph, unlike
// permit_source_test.go's TestOnlyTheNamedFunctionsMintAPermit: that test
// looks for FUNCTIONS whose result type is Permit, which SystemPermit's
// call sites are not -- they are ordinary call expressions and a var
// declaration. Grepping identifier occurrences of "SystemPermit" catches a
// call, a var initializer, and a comment alike, which is broader than
// strictly necessary but errs the safe direction for a governance check: a
// comment mentioning it in a NEW file still means a human should look.
//
// Mutation: call domain.SystemPermit from a third file -- this must fail.
func TestOnlyTheNamedCallersMintASystemPermit(t *testing.T) {
	root := repoRoot(t)
	seedDir := filepath.Join(root, "internal", "seed")

	entries, err := os.ReadDir(seedDir)
	if err != nil {
		t.Fatalf("reading internal/seed: %v", err)
	}

	found := map[string]int{}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		path := filepath.Join(seedDir, name)
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		// Parsed rather than grepped so a mention inside a string literal (a
		// log message, say) does not inflate the count -- only identifier
		// occurrences count.
		file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		n := 0
		ast.Inspect(file, func(node ast.Node) bool {
			if ident, ok := node.(*ast.Ident); ok && ident.Name == "SystemPermit" {
				n++
			}
			return true
		})
		if n > 0 {
			found["internal/seed/"+name] = n
		}
	}

	for file, n := range found {
		if _, allowed := systemPermitCallerFiles[file]; !allowed {
			t.Errorf("%s mentions SystemPermit %d time(s) but is not in "+
				"systemPermitCallerFiles. A new SystemPermit caller is a "+
				"security-relevant change: it needs auth-reviewer and the "+
				"repository owner's sign-off, and this test failing is what "+
				"makes the addition impossible to miss in the diff.", file, n)
		}
	}
	for file, want := range systemPermitCallerFiles {
		if got := found[file]; got != want {
			t.Errorf("%s mentions SystemPermit %d time(s), want exactly %d. A NEW "+
				"mention inherits this file's allowlist entry without anybody "+
				"reading it; one that disappeared usually moved somewhere not on "+
				"this list. Either way, say so here on purpose.", file, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// TestOnlyTheNamedCallersMintAnAdministratorPermitInSeed -- lock the list.
// ---------------------------------------------------------------------------

// administratorPermitCallerFiles is exactly where the identifier
// "AdministratorPermit" appears in internal/seed today.
//
// WP-G1 TASK 10 REGRESSION, FOUND AND FIXED BY THIS TEST'S FIRST VERSION: the
// mechanical actor->permit conversion applied Task 7's stand-in pattern --
// `domain.AdministratorPermit(actor)` -- to the seed package's 88 write call
// sites instead of threading the package's own `seed.Permit`
// (domain.SystemPermit("seed"), built by Task 9 for exactly this purpose).
// TestTheSeederRunsUnderASystemPermitAndNotAnAdministratorOne did not catch
// it: it reads one change_log row, for entity_type = 'provider', and the
// provider path happened to still use seed.Permit. The other 88 sites did
// not, and nothing was reading them. That is this test's whole reason to
// exist -- an assertion that reads one row can prove a property holds
// somewhere; it cannot prove a property holds everywhere, and "everywhere"
// is what "the seed package is not a hole for administrator capability"
// actually requires.
//
// The two production files below are NOT that regression -- they mint
// AdministratorPermit around a real domain.UserActor(admin), not around the
// seed package's own Actor, to attribute demo data (custom field
// definitions, a health override) to an actual admin account already in the
// database, the way StageCustomFields' own comments describe. That is a
// deliberate, different design choice from "the seeder holds administrator
// capability" -- it is "a named administrator is doing this, and the demo
// data says so" -- and domain.SystemPermit cannot stand in for it: Covers
// only ever returns SystemActor from Actor(), which would misattribute
// these rows to "system" instead of to the admin they are meant to
// describe. Widening this test to forbid AdministratorPermit outright would
// have to break one of these two files to pass, which is exactly the
// "fixing the test instead of the bug" failure mode this whole exercise is
// about not repeating.
//
// The four test files are the mechanical testActor/seed.Actor -> permit
// fixture updates Task 10 made across the store test suite, wrapping a bare
// Actor value in AdministratorPermit at a handful of call sites so a test
// helper keeps compiling against a signature that now takes a Permit. Test
// fixtures are not the property this test protects; production code is.
//
// Same governance comment as systemPermitCallerFiles above: a new
// production caller here is a security-relevant change. It requires
// auth-reviewer and the repository owner's sign-off before it merges. If
// you are editing this list or its budget, that is the conversation you are
// meant to be having, not a map to satisfy CI with.
var administratorPermitCallerFiles = map[string]int{
	"internal/seed/seed_customfields.go":      7, // domain.AdministratorPermit(actor), actor a real admin user
	"internal/seed/seed_customfields_demo.go": 8, // same, in the demo's own custom-field findings
	"internal/seed/seed_observed.go":          1, // CreateHealthOverride, attributed to a real admin user

	// Test fixtures -- see the doc comment above for why these are budgeted
	// separately from the two production files.
	"internal/seed/customfields_demo_test.go": 1,
	"internal/seed/customfields_test.go":      2,
	"internal/seed/fit_test.go":               1,
	"internal/seed/topup_test.go":             3,
}

// TestOnlyTheNamedCallersMintAnAdministratorPermitInSeed walks internal/seed
// for source text mentioning "AdministratorPermit" and asserts the exact,
// budgeted set of files found is administratorPermitCallerFiles.
//
// AST rather than grep, same reasoning as
// TestOnlyTheNamedCallersMintASystemPermit: only identifier occurrences
// count, not a mention inside a string literal or comment.
//
// Mutation: put one domain.AdministratorPermit(Actor) call back in a
// production seed file not already on this list (or raise an existing
// production entry's count without raising its budget) -- this must fail.
func TestOnlyTheNamedCallersMintAnAdministratorPermitInSeed(t *testing.T) {
	root := repoRoot(t)
	seedDir := filepath.Join(root, "internal", "seed")

	entries, err := os.ReadDir(seedDir)
	if err != nil {
		t.Fatalf("reading internal/seed: %v", err)
	}

	found := map[string]int{}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		path := filepath.Join(seedDir, name)
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		n := 0
		ast.Inspect(file, func(node ast.Node) bool {
			if ident, ok := node.(*ast.Ident); ok && ident.Name == "AdministratorPermit" {
				n++
			}
			return true
		})
		if n > 0 {
			found["internal/seed/"+name] = n
		}
	}

	for file, n := range found {
		if _, allowed := administratorPermitCallerFiles[file]; !allowed {
			t.Errorf("%s mentions AdministratorPermit %d time(s) but is not in "+
				"administratorPermitCallerFiles. The seed package writing under "+
				"an administrator-flavoured permit instead of seed.Permit is the "+
				"exact regression this test exists to catch -- see this test's "+
				"doc comment before adding an entry.", file, n)
		}
	}
	for file, want := range administratorPermitCallerFiles {
		if got := found[file]; got != want {
			t.Errorf("%s mentions AdministratorPermit %d time(s), want exactly %d. A NEW "+
				"mention inherits this file's allowlist entry without anybody "+
				"reading it; one that disappeared usually moved somewhere not on "+
				"this list. Either way, say so here on purpose.", file, got, want)
		}
	}
}
