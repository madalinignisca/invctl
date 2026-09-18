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
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// TestUpsertOIDCUserMatchesOnSubjectNotUsername pins down spec D2: matching
// is on the immutable `sub`, so a rename in Keycloak resolves to the same
// account instead of orphaning whatever roles or projects it already holds.
func TestUpsertOIDCUserMatchesOnSubjectNotUsername(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			first, err := s.UpsertOIDCUser(ctx, "kc-sub-1", "alice", "Alice A", "alice@example.com")
			if err != nil {
				t.Fatalf("first sign-in: %v", err)
			}
			// Same person, renamed in Keycloak. Must be the SAME account.
			again, err := s.UpsertOIDCUser(ctx, "kc-sub-1", "alice.smith", "Alice Smith", "alice@example.com")
			if err != nil {
				t.Fatalf("second sign-in: %v", err)
			}
			if again.ID != first.ID {
				t.Errorf("a rename in Keycloak created a second account (%s then %s). "+
					"Matching is on the immutable sub precisely so a rename does not "+
					"orphan somebody's roles (spec D2).", first.ID, again.ID)
			}
			if again.Username != "alice.smith" {
				t.Errorf("username = %q, want the current Keycloak name", again.Username)
			}
		})
	}
}

// TestUpsertOIDCUserRefusesAUsernameHeldByAnotherAccount is the whole of spec
// D4: without this, anybody who can cause a Keycloak account named `admin` to
// exist inherits this system's admin account.
func TestUpsertOIDCUserRefusesAUsernameHeldByAnotherAccount(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			// A local account already called admin.
			local, err := domain.NewAppUser(NewID(), "admin", domain.UserSourceLocal, s.Now())
			if err != nil {
				t.Fatalf("building local admin: %v", err)
			}
			hash := "hash"
			local.PasswordHash = &hash
			if err := s.CreateUser(ctx, testPermit, local); err != nil {
				t.Fatalf("seeding the local admin: %v", err)
			}

			_, err = s.UpsertOIDCUser(ctx, "kc-sub-attacker", "admin", "Not The Admin", "x@example.com")
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("UpsertOIDCUser with a colliding username = %v, want ErrConflict.\n"+
					"    Without this, anybody who can cause a Keycloak account named "+
					"'admin' to exist inherits this system's admin account (spec D2/D4).", err)
			}
			if !strings.Contains(err.Error(), "admin") {
				t.Errorf("the refusal does not name the conflicting username: %v", err)
			}
		})
	}
}

// TestUpsertOIDCUserCreatesAnObserverWithNoProjects is spec D1: Keycloak
// answers WHO, never WHAT -- a new account gets nothing until an
// Administrator grants it, and never carries a password hash since its
// credential belongs to the identity provider.
func TestUpsertOIDCUserCreatesAnObserverWithNoProjects(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			u, err := s.UpsertOIDCUser(ctx, "kc-sub-2", "bob", "Bob B", "bob@example.com")
			if err != nil {
				t.Fatalf("first sign-in: %v", err)
			}
			if u.Role != domain.RoleObserver {
				t.Errorf("role = %q, want observer. Keycloak answers WHO, never WHAT "+
					"(spec D1) -- a new account gets nothing until an Administrator "+
					"grants it.", u.Role)
			}
			if u.PasswordHash != nil {
				t.Error("an OIDC account has a password hash; credentials never touch us")
			}
		})
	}
}

// TestUpsertOIDCUserNeverLogsTheSubject pins down spec §4's "subject never
// enters change_log": `sub` is an external identity, and the actor column
// already carries the opaque app_user.id -- recording a second identifier for
// the same person in a trail kept forever is exactly what redaction exists to
// stop.
func TestUpsertOIDCUserNeverLogsTheSubject(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			u, err := s.UpsertOIDCUser(ctx, "kc-sub-secret", "carol", "Carol C", "carol@example.com")
			if err != nil {
				t.Fatalf("first sign-in: %v", err)
			}
			var diffs []string
			if err := s.DB().Reader.SelectContext(ctx, &diffs,
				s.DB().Reader.Rebind(`SELECT diff FROM change_log WHERE entity_type = 'app_user' AND entity_id = ?`),
				u.ID); err != nil {
				t.Fatalf("reading change_log: %v", err)
			}
			for _, d := range diffs {
				if strings.Contains(d, "kc-sub-secret") {
					t.Errorf("change_log entry for %s contains the raw subject: %s", u.ID, d)
				}
			}
		})
	}
}
