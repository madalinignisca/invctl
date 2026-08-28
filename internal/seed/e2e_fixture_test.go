// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package seed_test

import (
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/auth"
	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/seed"
	"github.com/madalinignisca/invctl/internal/store"
)

// mustAdminUser creates a throwaway "admin" account the same way
// customfields_test.go's own fixtures do -- StageE2EProjectOwner needs a
// real admin row to attribute its CreateUser call to (see that function's
// doc comment), the identical reason StageCustomFields needs one. ONE
// AdministratorPermit call site, shared by every test in this file, so this
// file counts once against system_permit_test.go's
// administratorPermitCallerFiles budget regardless of how many tests call it.
func mustAdminUser(t *testing.T, f *fixture) {
	t.Helper()
	admin, err := domain.NewAppUser(store.NewID(), "admin", domain.UserSourceLocal, f.store.Now())
	if err != nil {
		t.Fatalf("building admin: %v", err)
	}
	if err := f.store.CreateUser(f.ctx, domain.AdministratorPermit(domain.SystemActor), admin); err != nil {
		t.Fatalf("creating admin: %v", err)
	}
}

// testFixturePassword is this test's own throwaway value. It is NOT a
// constant in internal/seed any more: seeding a login-capable account
// requires an operator-chosen password precisely so that no working
// credential is published in this repository. See
// config.Config.SeedE2EProjectOwnerPassword.
const testFixturePassword = "test-only-fixture-password" // #nosec G101 -- test-local, never shipped

// TestStageE2EProjectOwnerRefusesWithoutAPassword pins the mechanism that
// replaced a documented convention. Before this, INV_SEED_E2E_PROJECT_OWNER
// alone produced an account whose password anybody could read out of the
// source; now the flag on its own stages nothing at all.
func TestStageE2EProjectOwnerRefusesWithoutAPassword(t *testing.T) {
	f := newFixture(t)
	// mustAdminUser, so the EMPTY PASSWORD is the only thing left that can
	// make this fail. Without it the admin lookup fails first and the test
	// passes on an unrelated error -- which it did, until reinstating a
	// default password failed to turn it red and exposed that.
	mustAdminUser(t, f)

	err := seed.StageE2EProjectOwner(f.ctx, f.store, "admin", "")
	if err == nil {
		t.Fatal("StageE2EProjectOwner created an account with no password")
	}
	if !strings.Contains(err.Error(), "INV_E2E_PROJECT_OWNER_PASSWORD") {
		t.Fatalf("refused for the wrong reason: %v", err)
	}
	if _, err := f.store.GetUserByUsername(f.ctx, seed.E2EProjectOwnerUsername); err == nil {
		t.Error("the fixture account exists despite the refusal")
	}
}

// TestStageE2EProjectOwnerCreatesAWorkingAccountScopedToPlatform proves that
// the account tests/e2e's RBAC specs sign in as is real, not merely present:
// the published username/password (seed.E2EProjectOwnerUsername/Password)
// authenticate through the actual LocalAuthenticator a browser login goes
// through, the stored role is project_owner, and the account holds an
// active assignment to the "platform" project -- the one Load() has already
// linked hv-01/hv-02/hv-03 to, in the BASE fixture, so tests/e2e never needs
// INV_SEED_COMPANY for this.
func TestStageE2EProjectOwnerCreatesAWorkingAccountScopedToPlatform(t *testing.T) {
	f := newFixture(t)
	mustAdminUser(t, f)

	if err := seed.StageE2EProjectOwner(f.ctx, f.store, "admin", testFixturePassword); err != nil {
		t.Fatalf("staging the E2E project-owner fixture: %v", err)
	}

	u, err := f.store.GetUserByUsername(f.ctx, seed.E2EProjectOwnerUsername)
	if err != nil {
		t.Fatalf("looking up the fixture account: %v", err)
	}
	if u.Role != domain.RoleProjectOwner {
		t.Errorf("role = %q, want %q", u.Role, domain.RoleProjectOwner)
	}
	if !u.IsActive {
		t.Error("the fixture account is not active")
	}

	// The credentials actually work, through the real authenticator -- not
	// merely "a password_hash column is non-null". A hash that failed to
	// verify against the very password used to build it would leave this
	// fixture unable to sign in, which is the one thing a browser suite
	// cannot recover from.
	authenticator := auth.NewLocalAuthenticator(f.store)
	authed, err := authenticator.Authenticate(f.ctx, seed.E2EProjectOwnerUsername, testFixturePassword)
	if err != nil {
		t.Fatalf("authenticating as the fixture account: %v", err)
	}
	if authed.ID != u.ID {
		t.Error("authenticating the fixture account returned a different user")
	}
	if _, err := authenticator.Authenticate(f.ctx, seed.E2EProjectOwnerUsername, "definitely-wrong"); err == nil {
		t.Error("the fixture account authenticated with the wrong password")
	}

	platformID, ok := f.refs.Projects["platform"]
	if !ok {
		t.Fatal("the seed fixture declares no \"platform\" project")
	}
	projects, err := f.store.ProjectsForUser(f.ctx, u.ID)
	if err != nil {
		t.Fatalf("listing the fixture account's projects: %v", err)
	}
	found := false
	for _, id := range projects {
		if id == platformID {
			found = true
		}
	}
	if !found {
		t.Errorf("fixture account's projects = %v, want it to include platform (%s)", projects, platformID)
	}
}

// TestStageE2EProjectOwnerIsIdempotent guards the exact failure mode a
// second `make dev` against an already-seeded database would hit otherwise:
// app_user.username is UNIQUE, so a naive re-run would fail the whole
// startup on a collision rather than leaving the existing fixture alone --
// the same idempotency StageCustomFields already promises for its own
// fixture.
func TestStageE2EProjectOwnerIsIdempotent(t *testing.T) {
	f := newFixture(t)
	mustAdminUser(t, f)

	if err := seed.StageE2EProjectOwner(f.ctx, f.store, "admin", testFixturePassword); err != nil {
		t.Fatalf("staging (first run): %v", err)
	}
	before, err := f.store.GetUserByUsername(f.ctx, seed.E2EProjectOwnerUsername)
	if err != nil {
		t.Fatalf("looking up the fixture account after the first run: %v", err)
	}

	if err := seed.StageE2EProjectOwner(f.ctx, f.store, "admin", testFixturePassword); err != nil {
		t.Fatalf("staging (second run): %v", err)
	}
	after, err := f.store.GetUserByUsername(f.ctx, seed.E2EProjectOwnerUsername)
	if err != nil {
		t.Fatalf("looking up the fixture account after the second run: %v", err)
	}
	if before.ID != after.ID {
		t.Error("the second run minted a second account instead of leaving the first alone")
	}
}
