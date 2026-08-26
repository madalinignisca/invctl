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
	"database/sql"
	"errors"
	"fmt"

	"github.com/madalinignisca/invctl/internal/domain"
)

// These take domain.Actor today (WP-G1 Task 3) and are rewritten to
// domain.Permit once object-level scope lands (WP-G1 Task 10). Deliberate:
// the audited write and the last-Administrator guard must be reviewable
// without the scope-check diff sitting on top of them.

// ListUsers returns every account for the roster screen, ordered so the list
// is stable across renders.
func (s *SQLStore) ListUsers(ctx context.Context) ([]domain.AppUser, error) {
	var users []domain.AppUser
	if err := s.read(ctx, &users, `SELECT * FROM app_user ORDER BY username`); err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	return users, nil
}

// GetUser loads one account by id.
func (s *SQLStore) GetUser(ctx context.Context, id string) (*domain.AppUser, error) {
	var u domain.AppUser
	if err := s.readOne(ctx, &u, `SELECT * FROM app_user WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting user %s: %w", id, err)
	}
	return &u, nil
}

// CountActiveAdministrators reports how many accounts can act as
// Administrator right now. Spec §8's guard re-runs this INSIDE the write
// transaction (see refuseIfLastActiveAdministrator below) before every
// demote, deactivate and scrub; this exported form is the plain read a
// roster screen uses to warn "this is the last one" before the user even
// submits.
func (s *SQLStore) CountActiveAdministrators(ctx context.Context) (int, error) {
	n, err := s.countOne(ctx,
		`SELECT COUNT(*) FROM app_user WHERE role = ? AND is_active = TRUE`, domain.RoleAdministrator)
	if err != nil {
		return 0, fmt.Errorf("counting active administrators: %w", err)
	}
	return int(n), nil
}

// getUser loads a row inside the write transaction. Never s.read/s.readOne:
// the reader pool is a separate connection and cannot see this transaction's
// view of the table, which matters for exactly one caller here --
// refuseIfLastActiveAdministrator needs the count as SEEN BY THIS
// TRANSACTION, not by a connection that started before it.
func (t *tx) getUser(ctx context.Context, id string) (*domain.AppUser, error) {
	var u domain.AppUser
	if err := t.get(ctx, &u, `SELECT * FROM app_user WHERE id = ?`, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("loading user %s: %w", id, err)
	}
	return &u, nil
}

// testAfterAdministratorCount is a test-only rendezvous hook, always nil in
// production. See its one call site in refuseIfLastActiveAdministrator.
var testAfterAdministratorCount func()

// refuseIfLastActiveAdministrator is spec §8: demoting, deactivating or
// scrubbing the final active Administrator would leave nobody able to
// administer invctl, so it is refused rather than recovered from afterwards.
//
// This is check-then-act against a COUNT, which is exactly the shape
// writeSerializable's doc comment describes: at PostgreSQL's default
// read-committed isolation, two concurrent demotions of two DIFFERENT
// administrators each see "two active administrators" and both commit,
// leaving zero. Every caller of this method reaches it through
// writeSerializable so the count is either taken at a level that cannot
// observe the other transaction's write, or the second commit is aborted and
// retried until it sees the first one land. SQLite never shows the race --
// one writer connection -- so it is not a substitute for exercising this on
// PostgreSQL; TestTwoSimultaneousDemotionsCannotRemoveTheLastAdministrator
// does that, and is a legitimate PostgreSQL-only skip because it declares its
// precondition (INV_TEST_POSTGRES_DSN) rather than skipping because the
// thing under test looked missing.
func (t *tx) refuseIfLastActiveAdministrator(ctx context.Context, verb string) error {
	n, err := t.countOne(ctx,
		`SELECT COUNT(*) FROM app_user WHERE role = ? AND is_active = TRUE`, domain.RoleAdministrator)
	if err != nil {
		return fmt.Errorf("counting active administrators: %w", err)
	}
	// Test-only rendezvous point, nil in production. A two-goroutine barrier
	// synchronised only on START cannot reliably force two real transactions
	// to both pass this count before either writes -- the window is a single
	// round trip, and it was observed NOT to race in five straight runs
	// against the buggy `write` version. This hook lets the test hold both
	// goroutines here until both have read the count, which is what actually
	// reproduces read-committed's failure mode and what makes writeSerializable's
	// retry-on-conflict machinery the thing standing between here and a lost
	// guard, rather than luck.
	if testAfterAdministratorCount != nil {
		testAfterAdministratorCount()
	}
	if n <= 1 {
		return fmt.Errorf(
			"%s the last active administrator would leave nobody able to administer invctl: %w",
			verb, domain.ErrForbidden)
	}
	return nil
}

// SetUserRole grants or revokes a role. Setting the role a user already has
// is a no-op: no write, no change_log row, and -- because of that -- no
// transaction even opens, matching the "retiring twice is a no-op" pattern
// the rest of this package already uses (see RetireCluster).
func (s *SQLStore) SetUserRole(ctx context.Context, actor domain.Actor, id, role string) error {
	current, err := s.GetUser(ctx, id)
	if err != nil {
		return err
	}
	if current.Role == role {
		return nil
	}
	candidate := *current
	candidate.Role = role
	if err := candidate.ValidateRole(); err != nil {
		return err
	}

	return s.writeSerializable(ctx, actor, func(t *tx) error {
		before, err := t.getUser(ctx, id)
		if err != nil {
			return err
		}
		if before.Role == role {
			// Somebody else made the same change while we were retrying.
			return nil
		}
		// Demoting AWAY from administrator is the only direction this guard
		// cares about -- granting the role, or moving between the two
		// non-administrator roles, never reduces the active-administrator
		// count.
		if before.Role == domain.RoleAdministrator && before.IsActive {
			if err := t.refuseIfLastActiveAdministrator(ctx, "demoting"); err != nil {
				return err
			}
		}
		if _, err := t.exec(ctx, `UPDATE app_user SET role = ? WHERE id = ?`, role, id); err != nil {
			return translateWriteErr(err, "setting role for user "+id)
		}
		after := *before
		after.Role = role
		return t.logUpdate(ctx, "app_user", id, before, &after)
	})
}

// SetUserCostVisibility grants or revokes the can_see_costs flag. Not a
// check-then-act write -- nothing else in the schema depends on the OLD
// value of this column the way the last-administrator count does -- so a
// plain write is enough; see writeSerializable's doc comment for the
// distinction.
func (s *SQLStore) SetUserCostVisibility(ctx context.Context, actor domain.Actor, id string, canSee bool) error {
	current, err := s.GetUser(ctx, id)
	if err != nil {
		return err
	}
	if current.CanSeeCosts == canSee {
		return nil
	}
	return s.write(ctx, actor, func(t *tx) error {
		before, err := t.getUser(ctx, id)
		if err != nil {
			return err
		}
		if before.CanSeeCosts == canSee {
			return nil
		}
		if _, err := t.exec(ctx, `UPDATE app_user SET can_see_costs = ? WHERE id = ?`, canSee, id); err != nil {
			return translateWriteErr(err, "setting cost visibility for user "+id)
		}
		after := *before
		after.CanSeeCosts = canSee
		return t.logUpdate(ctx, "app_user", id, before, &after)
	})
}

// SetUserActive activates or deactivates an account. Deactivating the last
// active Administrator is spec §8's second guarded verb.
func (s *SQLStore) SetUserActive(ctx context.Context, actor domain.Actor, id string, active bool) error {
	current, err := s.GetUser(ctx, id)
	if err != nil {
		return err
	}
	if current.IsActive == active {
		return nil
	}
	return s.writeSerializable(ctx, actor, func(t *tx) error {
		before, err := t.getUser(ctx, id)
		if err != nil {
			return err
		}
		if before.IsActive == active {
			return nil
		}
		if !active && before.Role == domain.RoleAdministrator && before.IsActive {
			if err := t.refuseIfLastActiveAdministrator(ctx, "deactivating"); err != nil {
				return err
			}
		}
		if _, err := t.exec(ctx, `UPDATE app_user SET is_active = ? WHERE id = ?`, active, id); err != nil {
			return translateWriteErr(err, "setting active state for user "+id)
		}
		after := *before
		after.IsActive = active
		return t.logUpdate(ctx, "app_user", id, before, &after)
	})
}

// ScrubUser is the GDPR erasure operation this codebase has described in nine
// comments and never implemented (CLAUDE.md, "Declared vs observed"). It
// clears display name, email and password hash while keeping the row and its
// id: change_log.actor keeps referencing a real row, the audit trail keeps
// its integrity, and every defensive "LEFT JOIN app_user" already written for
// this case finally has something real to degrade from.
//
// Scrub is spec §8's third guarded verb, and unconditionally so -- unlike
// SetUserRole/SetUserActive, which only guard the specific change that
// removes administrator capability, scrubbing an active Administrator always
// ends their ability to act as one (their password is gone), regardless of
// what role or is_active still say. Demote, deactivate and scrub all reach
// the same state -- no active administrator -- and a guard on two of the
// three verbs is a guard on none.
func (s *SQLStore) ScrubUser(ctx context.Context, actor domain.Actor, id string) error {
	return s.writeSerializable(ctx, actor, func(t *tx) error {
		before, err := t.getUser(ctx, id)
		if err != nil {
			return err
		}
		if before.Role == domain.RoleAdministrator && before.IsActive {
			if err := t.refuseIfLastActiveAdministrator(ctx, "scrubbing"); err != nil {
				return err
			}
		}
		if _, err := t.exec(ctx,
			`UPDATE app_user SET display_name = NULL, email = NULL, password_hash = NULL WHERE id = ?`,
			id); err != nil {
			return translateWriteErr(err, "scrubbing user "+id)
		}
		after := *before
		after.DisplayName = nil
		after.Email = nil
		after.PasswordHash = nil
		return t.logUpdate(ctx, "app_user", id, before, &after)
	})
}
