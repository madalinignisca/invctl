// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package seed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/madalinignisca/invctl/internal/auth"
	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// StageE2EProjectOwner creates the ONE account WP-G1 Task 17's browser suite
// needs and that nothing else in this codebase can create: a real,
// loggable-in project owner assigned to a real project. See
// internal/store/user_projects.go's AssignProject -- it is a store method
// with no HTTP route in front of it (WP-G1 has not built a project-membership
// UI yet), so every Go test that needs a project owner calls it directly
// against *store.SQLStore. A browser cannot do that; it can only reach the
// app over HTTP, so this fixture has to exist as a real row before the
// browser signs in, and there is no route by which the browser could create
// it itself.
//
// OFF BY DEFAULT, EVERYWHERE, ON PURPOSE -- and this is the reason the whole
// function exists rather than living inline in Load(). E2EProjectOwnerUsername
// and E2EProjectOwnerPassword are fixed, published constants (this doc
// comment, docs/E2E.md, tests/e2e's own spec files all name them), which is
// fine for a throwaway account on a disposable local instance and would be a
// live write-capable credential if it ever landed on a shared or public
// deployment: WP-G1 Task 13 (still unlanded) flips CanWrite(project owner) to
// true, at which point this account stops being inert. cmd/invctl only calls
// this when cfg.SeedE2EProjectOwner is true, which nothing in the Makefile's
// `make dev` / `make demo` defaults or docs/DEMO.md's deployment sets --
// see config.Config.SeedE2EProjectOwner's own comment. Whoever operates a
// long-lived deployment (including the public demo) must never set
// INV_SEED_E2E_PROJECT_OWNER=true on it.
//
// Idempotent, the same shape as StageCustomFields: an estate that already
// carries the fixture account is left alone, so a repeated dev-mode start
// does not collide on app_user's UNIQUE username.
//
// A SEPARATE PHASE FROM Load, called only after ensureAdmin, for the
// identical reason StageCustomFields is: app_user does not exist as a
// concept Load can safely mint one of (see that function's own doc comment)
// -- and adminUsername is that same seeded administrator's username, needed
// here for the identical reason StageCustomFields needs it: domain.
// SystemPermit refuses every app_user write unconditionally
// (internal/domain/role.go's systemPermit.Covers, and see
// internal/store/role_management_test.go's package comment for why),  so
// creating THIS account has to run under domain.AdministratorPermit wrapped
// around a REAL admin account already in the database -- attributing the row
// to "system" would misrepresent it, the same distinction
// system_permit_test.go's administratorPermitCallerFiles draws between
// StageCustomFields and the seeder's own ordinary writes. Assigning the
// project, by contrast, touches user_project, which systemPermit.Covers does
// NOT exclude -- so that write runs under the package's own seed.Permit
// instead, kept to the minimum privilege each individual write actually
// needs rather than reusing the wider one for both.
func StageE2EProjectOwner(ctx context.Context, s *store.SQLStore, adminUsername, password string) error {
	// NO PASSWORD, NO ACCOUNT. The password is not a constant in this
	// package any more: setting INV_SEED_E2E_PROJECT_OWNER alone must not be
	// able to produce a login-capable account, because the credential would
	// then be one anybody could read out of this repository. See
	// config.Config.SeedE2EProjectOwnerPassword for the auth review that
	// established the exposure is READ access today, not only the write
	// access WP-G1 Task 13 adds.
	if password == "" {
		return errors.New("INV_SEED_E2E_PROJECT_OWNER is set but " +
			"INV_E2E_PROJECT_OWNER_PASSWORD is empty: refusing to seed a " +
			"login-capable fixture account without an operator-chosen password")
	}

	existing, err := s.GetUserByUsername(ctx, E2EProjectOwnerUsername)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return fmt.Errorf("checking for the existing E2E project-owner fixture: %w", err)
	}

	admin, err := s.GetUserByUsername(ctx, adminUsername)
	if err != nil {
		return fmt.Errorf("looking up %s to attribute the E2E project-owner fixture: %w", adminUsername, err)
	}

	project, err := s.GetProjectByCode(ctx, e2eProjectOwnerProjectCode)
	if err != nil {
		return fmt.Errorf("resolving project %q for the E2E project-owner fixture: %w",
			e2eProjectOwnerProjectCode, err)
	}

	// LOUD, ALWAYS, AND ON BOTH PATHS. Staging used to succeed silently, and
	// the flag is a one-way ratchet: unsetting INV_SEED_E2E_PROJECT_OWNER
	// later does not remove an account this already created. An operator who
	// turned it on once and forgot has no other signal that a published-
	// username, write-capable-after-Task-13 account is live on their estate.
	slog.Warn("staging the E2E project-owner fixture account",
		"username", E2EProjectOwnerUsername,
		"project", e2eProjectOwnerProjectCode,
		"note", "never set INV_SEED_E2E_PROJECT_OWNER on a shared or long-lived deployment")

	// The account may already exist while its project assignment does not:
	// CreateUser and AssignProject are two transactions, so a crash between
	// them leaves the fixture half-staged. Returning early on the username
	// alone would make that state permanent and the browser suite would fail
	// far from its cause, so the assignment is (re-)made either way --
	// AssignProject is idempotent for a pair it already holds.
	permit := domain.AdministratorPermit(domain.UserActor(admin))
	if existing == nil {
		hash, err := auth.HashPassword(password)
		if err != nil {
			return fmt.Errorf("hashing the E2E project-owner fixture's password: %w", err)
		}

		u, err := domain.NewAppUser(store.NewID(), E2EProjectOwnerUsername, domain.UserSourceLocal, s.Now())
		if err != nil {
			return fmt.Errorf("building the E2E project-owner fixture account: %w", err)
		}
		u.Role = domain.RoleProjectOwner
		u.PasswordHash = &hash

		if err := s.CreateUser(ctx, permit, u); err != nil {
			return fmt.Errorf("creating the E2E project-owner fixture account: %w", err)
		}
		existing = u
	}

	// AdministratorPermit here too, not the package's own seed.Permit. Both
	// halves of one grant were previously attributed to different actors --
	// the account to the seeded administrator, the project assignment to
	// "system" -- and internal/domain/role.go is explicit that only an
	// Administrator grants scope. One grant, one actor in the change log.
	if err := s.AssignProject(ctx, permit, existing.ID, project.ID); err != nil {
		return fmt.Errorf("assigning the E2E project-owner fixture to project %q: %w",
			e2eProjectOwnerProjectCode, err)
	}
	return nil
}

const (
	// E2EProjectOwnerUsername is published and fixed; the PASSWORD
	// deliberately is not, and no longer exists as a constant anywhere in
	// this repository. A username identifies an account, which is only
	// useful to somebody who already has the estate in front of them; a
	// password opens one. See config.Config.SeedE2EProjectOwnerPassword.
	E2EProjectOwnerUsername = "e2e-project-owner"

	// e2eProjectOwnerProjectCode names the project (internal/seed/seed_projects.go)
	// this fixture is assigned to. "platform" owns hv-01/hv-02/hv-03 in the
	// BASE fixture, unconditionally built by Load() regardless of
	// CompanyEstate -- so this fixture needs nothing beyond INV_SEED=true.
	e2eProjectOwnerProjectCode = "platform"
)
