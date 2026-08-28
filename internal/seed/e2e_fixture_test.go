// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package seed_test

import (
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

	if err := seed.StageE2EProjectOwner(f.ctx, f.store, "admin"); err != nil {
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
	authed, err := authenticator.Authenticate(f.ctx, seed.E2EProjectOwnerUsername, seed.E2EProjectOwnerPassword)
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

	if err := seed.StageE2EProjectOwner(f.ctx, f.store, "admin"); err != nil {
		t.Fatalf("staging (first run): %v", err)
	}
	before, err := f.store.GetUserByUsername(f.ctx, seed.E2EProjectOwnerUsername)
	if err != nil {
		t.Fatalf("looking up the fixture account after the first run: %v", err)
	}

	if err := seed.StageE2EProjectOwner(f.ctx, f.store, "admin"); err != nil {
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
