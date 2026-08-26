// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

// Roles a person is given, replacing "a comma-separated list of usernames in
// an environment variable grants write to everything" (WP-G1, migration
// 00058). Nothing in this package consults a role for authorization yet --
// that is Authorizer.CanWrite/CanSeeCosts, WP-G1 task 4. This file exists
// only to give the column a typed Go vocabulary that migration 00058's CHECK
// constraint can be checked against, per
// TestTheGoConstantSetMatchesTheDatabaseCheck.
const (
	// RoleAdministrator has full write access.
	RoleAdministrator = "administrator"
	// RoleObserver is read-only. It is also the default -- see migration
	// 00058's comment and NewAppUser below for why that default is the
	// security decision in this whole piece of work.
	RoleObserver = "observer"
	// RoleProjectOwner manages their own estate but is not an administrator.
	RoleProjectOwner = "project_owner"
)

// Roles is the Go side of the app_user.role CHECK constraint added by
// migration 00058. Keep this in lockstep with that migration's CHECK clause
// -- TestTheGoConstantSetMatchesTheDatabaseCheck reads the clause off disk and
// fails if the two disagree in either direction.
var Roles = []string{RoleAdministrator, RoleObserver, RoleProjectOwner}

// ValidateRole checks Role against the constant set. The DB CHECK constraint
// is the second line of defence, not the first.
//
// Deliberately not folded into a broader AppUser.Validate: NewAppUser never
// takes a role (see its doc comment), so nothing constructs an AppUser with a
// caller-supplied role today, and adding validation nothing calls invites
// exactly the kind of build-ahead this task is told not to do. Task 4, which
// does let a role be set from outside the default, is what wires this in.
func (u *AppUser) ValidateRole() error {
	ve := &ValidationError{}
	checkEnum(ve, "role", u.Role, Roles)
	return ve.OrNil()
}
