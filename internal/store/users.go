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
	"fmt"

	"github.com/madalinignisca/invctl/internal/domain"
)

// GetUserByUsername loads an account. Returns domain.ErrNotFound if there is
// no such user -- callers must not distinguish that from a bad password in
// anything they show the client.
func (s *SQLStore) GetUserByUsername(ctx context.Context, username string) (*domain.AppUser, error) {
	var u domain.AppUser
	if err := s.readOne(ctx, &u, `SELECT * FROM app_user WHERE username = ?`, lower(username)); err != nil {
		return nil, fmt.Errorf("getting user %s: %w", username, err)
	}
	return &u, nil
}

// UsernamesMatching returns the subset of names that exist as accounts.
//
// It answers docs/AUDIT.md rule 5's startup check: "Startup fails if an agent
// name collides with an app_user.username." The collision matters because
// change_log.actor is free TEXT shared between operator ids and namespaced
// credential ids -- the monitor: prefix already keeps the two spaces disjoint,
// but a credential and a person sharing a name makes every log line, every
// security event and every conversation about them ambiguous, and ambiguity in
// an audit trail is the thing the trail exists to remove.
//
// A read, not a write, and deliberately not in observed.go: this runs once at
// startup, from cmd/invctl, and no request path can reach it.
func (s *SQLStore) UsernamesMatching(ctx context.Context, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	lowered := make([]string, 0, len(names))
	for _, n := range names {
		lowered = append(lowered, lower(n))
	}
	var found []string
	err := s.read(ctx, &found,
		`SELECT username FROM app_user WHERE username IN (`+placeholders(len(lowered))+`) ORDER BY username`,
		anySlice(lowered)...)
	if err != nil {
		return nil, fmt.Errorf("checking usernames: %w", err)
	}
	return found, nil
}

// CountUsers reports how many accounts exist, used to decide whether the
// seeded admin needs creating on first run.
func (s *SQLStore) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := s.readOne(ctx, &n, `SELECT COUNT(*) FROM app_user`); err != nil {
		return 0, fmt.Errorf("counting users: %w", err)
	}
	return n, nil
}

// CreateUser inserts an account.
//
// The change log records the account, never the hash: an audit trail is read
// by more people than the user table is.
//
// WHY AN ADMINISTRATOR PERMIT ON A PATH REACHABLE WITHOUT AUTHENTICATION.
// POST /login is public and reaches here through UpsertLDAPUser: the account
// exists because the directory said so, and refusing the write would mean no
// LDAP user could ever sign in. It cannot use SystemPermit, because that one
// refuses app_user outright (domain.systemPermit.Covers) precisely so a system
// actor can never grant a role.
//
// So the permit here is wider than the operation, and the defence is at the
// COLUMN rather than the path -- which is the honest place for it:
//   - NewAppUser takes no role parameter, so nothing an unauthenticated caller
//     supplies can influence the role. It is always RoleObserver.
//   - This is an INSERT and never an upsert, so it cannot take over an
//     existing account.
//   - TestNoWriteOutsideTheRoleManagementFileNamesTheRoleColumn (an AST walk,
//     internal/store/role_management_test.go) forbids any file but
//     users_admin.go from naming app_user.role or can_see_costs in a write.
//     That is what stops somebody later adding "UPDATE app_user SET role" here
//     and finding it compiles.
//
// If you are widening what this function writes, that test is the one you will
// have to argue with, and it is the conversation you should be having.
func (s *SQLStore) CreateUser(ctx context.Context, p domain.Permit, u *domain.AppUser) error {
	return s.write(ctx, p, func(t *tx) error {
		// role and can_see_costs are named explicitly, not left to the
		// column defaults migration 00058 set. NewAppUser already populates
		// Role with domain.RoleObserver and CanSeeCosts with its safe
		// zero value, and until WP-G1 Task 3 they agreed with the schema
		// default by coincidence rather than by construction: the INSERT
		// named neither column, so the Go value was silently discarded and
		// the row took whatever the migration said. Both happened to be
		// 'observer'/FALSE, so nothing broke -- but a later change to
		// NewAppUser's default would have had no effect, and nothing would
		// have said why.
		_, err := t.exec(ctx, `
			INSERT INTO app_user (id, username, display_name, email, source,
			                      password_hash, is_active, last_login_at, created_at,
			                      role, can_see_costs)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			u.ID, u.Username, u.DisplayName, u.Email, u.Source,
			u.PasswordHash, u.IsActive, u.LastLoginAt, u.CreatedAt,
			u.Role, u.CanSeeCosts)
		if err != nil {
			return translateWriteErr(err, "creating user")
		}
		redacted := *u
		redacted.PasswordHash = nil
		return t.logCreate(ctx, "app_user", u.ID, &redacted)
	})
}

// UpsertLDAPUser records an account after a successful LDAP bind.
//
// LDAP users never get a password hash: the credential belongs to the
// directory and we have no business storing anything derived from it.
func (s *SQLStore) UpsertLDAPUser(ctx context.Context, username, displayName, email string) (*domain.AppUser, error) {
	existing, err := s.GetUserByUsername(ctx, username)
	switch {
	case err == nil:
		// Two directories of truth can produce the same username. Handing an
		// LDAP principal the local account row would merge the identities
		// outright: the directory user would inherit the local account's id,
		// and every change_log row it wrote would be attributed to the local
		// operator. Refuse instead -- an admin has to resolve the collision.
		if existing.Source != domain.UserSourceLDAP {
			return nil, fmt.Errorf(
				"recording ldap user %s: a %s account already owns that username: %w",
				username, existing.Source, domain.ErrConflict)
		}
		return existing, nil

	case errors.Is(err, domain.ErrNotFound):
		// Expected: first sign-in for this directory user.

	default:
		// A transient failure must not be mistaken for "no such user", or the
		// insert below turns a database blip into a confusing conflict.
		return nil, fmt.Errorf("looking up ldap user %s: %w", username, err)
	}
	u, err := domain.NewAppUser(NewID(), username, domain.UserSourceLDAP, s.now())
	if err != nil {
		return nil, err
	}
	if displayName != "" {
		u.DisplayName = &displayName
	}
	if email != "" {
		u.Email = &email
	}
	// The directory is the actor here, not the person logging in: the account
	// row exists because LDAP said so.
	if err := s.CreateUser(ctx, domain.AdministratorPermit(domain.Actor{ID: "ldap", Name: "ldap", Kind: "system"}), u); err != nil {
		return nil, err
	}
	return u, nil
}

// TouchLogin records a successful sign-in. Deliberately not audited: a
// change_log row per login would bury the changes that matter.
func (s *SQLStore) TouchLogin(ctx context.Context, userID string) error {
	at := domain.FormatTime(s.now())
	_, err := s.db.Writer.ExecContext(ctx,
		s.db.Rebind(`UPDATE app_user SET last_login_at = ? WHERE id = ?`), at, userID)
	if err != nil {
		return fmt.Errorf("recording login for %s: %w", userID, err)
	}
	return nil
}
