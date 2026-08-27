// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// WP-G1 Task 9, step 2: the LDAP first-login landmine.
//
// POST /login is unauthenticated and reaches UpsertLDAPUser -> CreateUser,
// which writes app_user, a table carrying role and can_see_costs. The write
// is legitimate and stays -- the account exists because the directory said
// so, and users.go already attributes it to a system actor named "ldap"
// rather than to the person signing in. The defence is COLUMN-LEVEL, not
// permit-level: NewAppUser (WP-G1 Task 2) takes no role parameter at all, so
// nothing an unauthenticated path can influence ever reaches the INSERT.
//
// A NOTE ON A DEVIATION FROM THE WP-G1 PLAN'S STEP 1 TABLE, recorded here
// because it is exactly the kind of thing a reviewer should not have to
// reconstruct from a diff: the plan lists the LDAP upsert's permit as
// domain.SystemPermit("ldap"). It cannot be -- systemPermit.Covers refuses
// EVERY app_user write unconditionally (see its doc comment in
// internal/domain/role.go: "a system actor granting itself a role or
// creating an account is the exact privilege-escalation shape WP-G1 exists
// to close"). Using SystemPermit for the LDAP account-creation write would
// make first login fail outright, every time. CreateUser keeps taking a
// domain.Actor and minting domain.AdministratorPermit(actor) internally, the
// same pre-Task-10 shim every other not-yet-converted method uses; the LDAP
// actor is attributed by ID ("ldap"), not by which Permit constructor ran.
// TestASystemPermitCannotCreateAnAccount below is what actually exercises
// the exclusion the plan meant to point at: proving that IF a caller ever
// tried SystemPermit for an app_user write, it would be refused. The
// counterpart for the UPDATE direction is TestASystemPermitCannotChangeAnExistingUsersRole.

// TestAnLDAPFirstLoginCreatesAnObserverAndNeverAnAdministrator is the
// column-level defence, proven end to end through the real path an
// unauthenticated bind actually takes.
//
// Mutation: make NewAppUser set RoleAdministrator (instead of leaving Role
// at its zero value / RoleObserver) -- this must fail.
func TestAnLDAPFirstLoginCreatesAnObserverAndNeverAnAdministrator(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			u, err := s.UpsertLDAPUser(ctx, "task9-ldap-first-login", "Task Nine", "")
			if err != nil {
				t.Fatalf("UpsertLDAPUser: %v", err)
			}
			if u.Role != domain.RoleObserver {
				t.Fatalf("LDAP first login created role %q, want %q -- an unauthenticated "+
					"path must never be able to produce an Administrator account",
					u.Role, domain.RoleObserver)
			}
			if u.Source != domain.UserSourceLDAP {
				t.Errorf("source = %q, want %q", u.Source, domain.UserSourceLDAP)
			}
		})
	}
}

// TestASystemPermitCannotChangeAnExistingUsersRole is the UPDATE-direction
// half of systemPermit's app_user exclusion, and the caller the exclusion's
// own doc comment names.
//
// Mutation: drop the system-permit check from SetUserRole (i.e. pass permit
// straight to writeSerializable without going through tx.log's Covers gate
// -- which SetUserRole already does structurally, so the mutation to try is
// removing systemPermit's "entityType != app_user" exclusion in role.go)
// -- this must fail.
func TestASystemPermitCannotChangeAnExistingUsersRole(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			subject := mustUserWithRole(t, s, ctx, "task9-subject", domain.RoleObserver)

			err := s.SetUserRole(ctx, domain.SystemPermit("test"), subject.ID, domain.RoleProjectOwner)
			if err == nil {
				t.Fatal("a system permit changed a user's role and was not refused")
			}
			if !errors.Is(err, domain.ErrForbidden) {
				t.Errorf("error = %v, want it to wrap domain.ErrForbidden", err)
			}

			after, err := s.GetUser(ctx, subject.ID)
			if err != nil {
				t.Fatalf("reloading subject: %v", err)
			}
			if after.Role != domain.RoleObserver {
				t.Errorf("role = %q after a refused write, want it unchanged at %q",
					after.Role, domain.RoleObserver)
			}
		})
	}
}

// TestASystemPermitCannotCreateAnAccount is the CREATE-direction companion:
// systemPermit.Covers refuses app_user unconditionally, which is what makes
// domain.SystemPermit unusable for CreateUser and is the reason the LDAP
// upsert does not use it (see this file's package comment above).
//
// Mutation: drop "entityType != app_user" from systemPermit.Covers in
// internal/domain/role.go (i.e. make it `return true` unconditionally) --
// this must fail, and TestASystemPermitCannotChangeAnExistingUsersRole
// above would also start passing incorrectly, doubling the signal.
func TestASystemPermitCannotCreateAnAccount(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			u, err := domain.NewAppUser(NewID(), "task9-system-create", domain.UserSourceLocal, s.Now())
			if err != nil {
				t.Fatalf("NewAppUser: %v", err)
			}

			err = s.CreateUser(ctx, domain.AdministratorPermit(domain.SystemActor), u)
			// CreateUser still takes a bare domain.Actor and mints
			// AdministratorPermit internally (see this file's comment), so this
			// call succeeds -- it is not the shape being guarded against here.
			// The guard is exercised directly at the tx.log chokepoint instead,
			// the same way TestAnUnclassifiedEntityTypeFailsLoudlyRatherThanBeingAllowed
			// (permit_source_test.go) exercises Covers without going through a
			// full store method.
			if err != nil {
				t.Fatalf("CreateUser under the pre-Task-10 shim: %v", err)
			}

			if domain.SystemPermit("test").Covers("app_user", u.ID) {
				t.Fatal("a system permit covers an app_user write -- a system actor " +
					"creating an account is the exact privilege-escalation shape WP-G1 " +
					"exists to close, and it must be refused unconditionally")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestNoWriteOutsideTheRoleManagementFileNamesTheRoleColumn -- the structural
// half of the LDAP landmine.
// ---------------------------------------------------------------------------

// roleColumnAllowlist is every non-test .go file permitted to write
// app_user.role or app_user.can_see_costs, with an exact count of how many
// such write statements it contains -- the dynamicTargetAllowlist /
// dynamicTargetBudget pattern from boundary_source_test.go, applied to a
// column instead of a table.
//
// users.go's ONE entry is CreateUser's INSERT, which names both columns
// explicitly (see that method's own doc comment: naming them beats relying
// on the migration's default agreeing with NewAppUser's by coincidence).
// That write is safe because NewAppUser never lets a caller supply a role
// (Task 2) -- not because of which file it lives in. users_admin.go's two
// entries are SetUserRole's UPDATE and SetUserCostVisibility's UPDATE, the
// only two places either column is EVER changed after creation.
//
// TouchLogin (users.go:139-147) is not on this list and does not need to be:
// it names only last_login_at.
var roleColumnAllowlist = map[string]string{
	"internal/store/users.go": "CreateUser's INSERT: safe because NewAppUser never accepts " +
		"a caller-supplied role (Task 2), not because of which file this is",
	"internal/store/users_admin.go": "SetUserRole and SetUserCostVisibility: the only two " +
		"places either column changes after creation",
}

var roleColumnBudget = map[string]int{
	"internal/store/users.go":       1, // CreateUser's INSERT
	"internal/store/users_admin.go": 2, // SetUserRole's UPDATE, SetUserCostVisibility's UPDATE
}

// TestNoWriteOutsideTheRoleManagementFileNamesTheRoleColumn reuses
// sqlStatementsIn and parseSQLWrites from boundary_source_test.go rather than
// writing a second scanner -- see that file's opening comment for why this
// reads the source instead of only exercising runtime behaviour.
//
// Mutation: add `UPDATE app_user SET role = ?` to internal/store/users.go --
// this must fail, because it bumps that file's count from 1 to 2 without
// bumping roleColumnBudget, which is deliberately an EXACT count and not a
// maximum (see dynamicTargetBudget's own doc comment for why a write that
// silently appears is exactly what an exact count catches that a ceiling
// would not).
func TestNoWriteOutsideTheRoleManagementFileNamesTheRoleColumn(t *testing.T) {
	root := repoRoot(t)
	seen := map[string]int{}

	for file := range roleColumnAllowlist {
		path := filepath.Join(root, file)
		for _, stmt := range sqlStatementsIn(t, path) {
			for _, w := range parseSQLWrites(stmt.sql) {
				if w.table != "app_user" {
					continue
				}
				for _, col := range w.columns {
					if col == "role" || col == "can_see_costs" {
						seen[file]++
						break
					}
				}
			}
		}
	}

	// Everywhere else: nothing at all may write these two columns. Walked
	// exactly the way TestOnlyTheObservedPathWritesTheObservedTables walks
	// the tree in boundary_source_test.go.
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if _, allowed := roleColumnAllowlist[rel]; allowed {
			// Already counted above.
			return nil
		}

		for _, stmt := range sqlStatementsIn(t, path) {
			for _, w := range parseSQLWrites(stmt.sql) {
				if w.table != "app_user" {
					continue
				}
				for _, col := range w.columns {
					if col == "role" || col == "can_see_costs" {
						t.Errorf("%s:%d writes app_user.%s. Only %s may write role or "+
							"can_see_costs -- everything else is an unauthenticated or "+
							"otherwise ungoverned path acquiring an interest in who may "+
							"administer invctl.\n\t%s", rel, stmt.line, col,
							roleManagementFiles(), stmt.sql)
					}
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walking the tree: %v", walkErr)
	}

	for file, want := range roleColumnBudget {
		if got := seen[file]; got != want {
			t.Errorf("%s has %d writes naming role/can_see_costs, want exactly %d (%s). "+
				"A NEW one inherits the file's allowlist entry without anybody reading "+
				"it; one that disappeared usually moved somewhere not on this list. "+
				"Either way, say so here on purpose.", file, got, want, roleColumnAllowlist[file])
		}
	}
}

func roleManagementFiles() string {
	return "internal/store/users.go and internal/store/users_admin.go"
}
