// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"testing"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
)

func mustUser(t *testing.T, username, role string, active bool) *domain.AppUser {
	t.Helper()
	u, err := domain.NewAppUser("id-"+username, username, domain.UserSourceLocal, time.Now())
	if err != nil {
		t.Fatalf("building user %s: %v", username, err)
	}
	u.Role = role
	u.IsActive = active
	return u
}

// TestAnAdministratorsPermitCoversEverything mints via Authorizer.Permit --
// the one caller (once Task 12 wires a request through it) that turns "who is
// signed in" into an authorization decision -- and checks the mint agrees
// with isAdministrator's own two paths: the role column, and the
// INV_ADMIN_USERS break-glass override (auth.go's isAdministrator doc
// comment explains why the override must win independently of the column).
func TestAnAdministratorsPermitCoversEverything(t *testing.T) {
	tests := []struct {
		name  string
		user  *domain.AppUser
		admin []string
	}{
		{"role column", mustUser(t, "alice", domain.RoleAdministrator, true), nil},
		{"break-glass override", mustUser(t, "bob", domain.RoleObserver, true), []string{"bob"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := NewAuthorizer(tc.admin)
			p := a.Permit(tc.user)
			if !p.Covers("circuit", "any-id") {
				t.Error("Administrator's permit refused an arbitrary entity; Covers must " +
					"be unconditional for an Administrator or the mechanism denies " +
					"service to the one role the whole estate depends on")
			}
			if p.Actor().ID != tc.user.ID {
				t.Errorf("Actor().ID = %q, want %q", p.Actor().ID, tc.user.ID)
			}
		})
	}
}

// TestAnObserverOrProjectOwnersPermitCoversNothingYet pins today's
// fail-closed state: Task 13/14 have not landed a real derivation of a
// project owner's scope, so Authorizer.Permit hands back a ScopedPermit with
// an empty entity set for anyone who is not an Administrator, and that
// permit must refuse everything -- the exact same answer CanWrite already
// gives for a ProjectOwner (see CanWrite's own doc comment,
// TestAProjectOwnerCannotWriteAnythingUntilTheObjectGateIsLive in auth_test.go).
// A caller of this method before Task 13 lands must see the same closed
// door, not a permit that quietly authorizes something because nobody
// populated its scope yet.
func TestAnObserverOrProjectOwnersPermitCoversNothingYet(t *testing.T) {
	tests := []struct {
		name string
		user *domain.AppUser
	}{
		{"observer", mustUser(t, "carol", domain.RoleObserver, true)},
		{"project owner", mustUser(t, "dave", domain.RoleProjectOwner, true)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := NewAuthorizer(nil)
			p := a.Permit(tc.user)
			if p.Covers("circuit", "any-id") {
				t.Error("a non-Administrator's permit authorized a write with no scope " +
					"populated -- the fail-closed door CanWrite already keeps shut must " +
					"stay shut here too, until Task 13 lands the real derivation")
			}
			if p.Actor().ID != tc.user.ID {
				t.Errorf("Actor().ID = %q, want %q", p.Actor().ID, tc.user.ID)
			}
		})
	}
}

// TestADeactivatedAdministratorsPermitCoversNothing mirrors
// TestADeactivatedAdministratorMayNotWriteEvenWhenNamedInTheEnvironment in
// auth_test.go: a disabled account named in INV_ADMIN_USERS -- an
// ex-employee's name can sit there long after they left -- must not mint an
// Administrator's permit merely because isAdministrator's role/env check
// would say yes on its own. Permit checks IsActive itself rather than
// trusting a caller to have checked it first, for the same reason
// IsAdministrator does.
func TestADeactivatedAdministratorsPermitCoversNothing(t *testing.T) {
	user := mustUser(t, "eve", domain.RoleAdministrator, false)
	a := NewAuthorizer([]string{"eve"})
	p := a.Permit(user)
	if p.Covers("circuit", "any-id") {
		t.Error("a deactivated Administrator's permit authorized a write; " +
			"break-glass restores a role, not a disabled account")
	}
}
