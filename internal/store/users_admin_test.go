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
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
)

// mustUserWithRole creates an account carrying a given role directly, so
// setup does not depend on the very write path (SetUserRole) these tests
// exercise. CreateUser (internal/store/users.go) now names role and
// can_see_costs explicitly in its INSERT, so setting the field on the struct
// before CreateUser is enough -- it no longer needs a follow-up write.
func mustUserWithRole(t *testing.T, s *SQLStore, ctx context.Context, username, role string) *domain.AppUser {
	t.Helper()
	u, err := domain.NewAppUser(NewID(), username, domain.UserSourceLocal, s.Now())
	if err != nil {
		t.Fatalf("building user %s: %v", username, err)
	}
	u.Role = role
	if err := s.CreateUser(ctx, domain.SystemActor, u); err != nil {
		t.Fatalf("creating user %s: %v", username, err)
	}
	return u
}

// TestGrantingARoleWritesOneChangeLogRowNamingTheActorAndTheSubject.
//
// Mutation: delete the t.logUpdate call from SetUserRole -- this must fail.
func TestGrantingARoleWritesOneChangeLogRowNamingTheActorAndTheSubject(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			admin := mustUserWithRole(t, s, ctx, "admin", domain.RoleAdministrator)
			subject := mustUserWithRole(t, s, ctx, "alice", domain.RoleObserver)

			before, err := s.countOne(ctx,
				`SELECT COUNT(*) FROM change_log WHERE entity_type = ? AND entity_id = ?`,
				"app_user", subject.ID)
			if err != nil {
				t.Fatalf("counting before: %v", err)
			}

			if err := s.SetUserRole(ctx, domain.UserActor(admin), subject.ID, domain.RoleProjectOwner); err != nil {
				t.Fatalf("granting role: %v", err)
			}

			after, err := s.countOne(ctx,
				`SELECT COUNT(*) FROM change_log WHERE entity_type = ? AND entity_id = ?`,
				"app_user", subject.ID)
			if err != nil {
				t.Fatalf("counting after: %v", err)
			}
			if after != before+1 {
				t.Fatalf("change_log rows for the subject went %d -> %d, want exactly one new row",
					before, after)
			}

			changes, err := s.ListChangesForEntity(ctx, "app_user", subject.ID, 10)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			row := changes[0]
			if row.EntityID != subject.ID {
				t.Errorf("entity_id = %q, want the subject's id %q", row.EntityID, subject.ID)
			}
			if row.Actor != admin.ID {
				t.Errorf("actor = %q, want the granting admin's opaque id %q", row.Actor, admin.ID)
			}
			if row.ActorKind != domain.ActorKindUser {
				t.Errorf("actor_kind = %q, want %q", row.ActorKind, domain.ActorKindUser)
			}

			var diff map[string]struct {
				Old any `json:"old"`
				New any `json:"new"`
			}
			if err := json.Unmarshal([]byte(row.Diff), &diff); err != nil {
				t.Fatalf("decoding diff %q: %v", row.Diff, err)
			}
			if diff["role"].Old != domain.RoleObserver || diff["role"].New != domain.RoleProjectOwner {
				t.Errorf("role diff = %+v, want observer -> project_owner", diff["role"])
			}
		})
	}
}

// TestARoleGrantNeverPutsAUsernameInTheAuditTrail.
//
// Mutation: pass domain.Actor{ID: user.Username} as the actor -- this must
// fail.
func TestARoleGrantNeverPutsAUsernameInTheAuditTrail(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			admin := mustUserWithRole(t, s, ctx, "admin", domain.RoleAdministrator)
			subject := mustUserWithRole(t, s, ctx, "alice", domain.RoleObserver)

			if err := s.SetUserRole(ctx, domain.UserActor(admin), subject.ID, domain.RoleProjectOwner); err != nil {
				t.Fatalf("granting role: %v", err)
			}

			changes, err := s.ListChangesForEntity(ctx, "app_user", subject.ID, 10)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			if changes[0].Actor != admin.ID {
				t.Errorf("change_log.actor = %q, want the opaque id %q", changes[0].Actor, admin.ID)
			}
			if changes[0].Actor == admin.Username {
				t.Error("change_log.actor stored the username")
			}
		})
	}
}

func TestSettingTheSameRoleTwiceWritesNoSecondEntry(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			admin := mustUserWithRole(t, s, ctx, "admin", domain.RoleAdministrator)
			subject := mustUserWithRole(t, s, ctx, "alice", domain.RoleObserver)

			if err := s.SetUserRole(ctx, domain.UserActor(admin), subject.ID, domain.RoleProjectOwner); err != nil {
				t.Fatalf("first grant: %v", err)
			}
			before, err := s.countOne(ctx,
				`SELECT COUNT(*) FROM change_log WHERE entity_type = ? AND entity_id = ?`,
				"app_user", subject.ID)
			if err != nil {
				t.Fatalf("counting before: %v", err)
			}

			if err := s.SetUserRole(ctx, domain.UserActor(admin), subject.ID, domain.RoleProjectOwner); err != nil {
				t.Fatalf("second grant of the same role: %v", err)
			}

			after, err := s.countOne(ctx,
				`SELECT COUNT(*) FROM change_log WHERE entity_type = ? AND entity_id = ?`,
				"app_user", subject.ID)
			if err != nil {
				t.Fatalf("counting after: %v", err)
			}
			if after != before {
				t.Errorf("setting the same role twice wrote %d new entries, want 0", after-before)
			}
		})
	}
}

// TestARoleWriteNeverCarriesAPasswordHash covers both halves of the claim: a
// role change on an account that already has a password hash never mentions
// it (the hash did not change, so it is not even in the diff), and the one
// write that DOES touch password_hash on this codebase's app_user surface
// today -- ScrubUser -- redacts it structurally rather than by accident.
// CreateUser used to redact this column by hand; TestPasswordHashNeverReaches
// TheAuditTrail (store_test.go) already covers that path, so this test's job
// is the two paths this task adds.
func TestARoleWriteNeverCarriesAPasswordHash(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			admin := mustUserWithRole(t, s, ctx, "admin", domain.RoleAdministrator)

			const hash = "$argon2id$v=19$m=65536$UNIQUEHASHVALUE"
			subject, err := domain.NewAppUser(NewID(), "alice", domain.UserSourceLocal, s.Now())
			if err != nil {
				t.Fatalf("building user: %v", err)
			}
			subject.PasswordHash = strPtr(hash)
			if err := s.CreateUser(ctx, domain.SystemActor, subject); err != nil {
				t.Fatalf("creating user: %v", err)
			}

			if err := s.SetUserRole(ctx, domain.UserActor(admin), subject.ID, domain.RoleProjectOwner); err != nil {
				t.Fatalf("granting role: %v", err)
			}
			afterRole, err := s.ListChangesForEntity(ctx, "app_user", subject.ID, 10)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			for _, c := range afterRole {
				if strings.Contains(c.Diff, hash) {
					t.Fatalf("a role change carried the password hash: %s", c.Diff)
				}
			}

			if err := s.ScrubUser(ctx, domain.UserActor(admin), subject.ID); err != nil {
				t.Fatalf("scrubbing: %v", err)
			}
			afterScrub, err := s.ListChangesForEntity(ctx, "app_user", subject.ID, 10)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			latest := afterScrub[0]
			if strings.Contains(latest.Diff, hash) {
				t.Errorf("scrubbing wrote the password hash into the audit trail: %s", latest.Diff)
			}
			if !strings.Contains(latest.Diff, domain.Redacted) {
				t.Errorf("scrubbing a password hash left no redaction marker: %s", latest.Diff)
			}
		})
	}
}

// TestDeactivatingTheLastActiveAdministratorIsRefused: spec §8.
//
// Mutation: delete the guard from the deactivate path only, leaving it on
// demote -- this must fail.
func TestDeactivatingTheLastActiveAdministratorIsRefused(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			admin := mustUserWithRole(t, s, ctx, "admin", domain.RoleAdministrator)

			err := s.SetUserActive(ctx, domain.UserActor(admin), admin.ID, false)
			if err == nil {
				t.Fatal("deactivating the only administrator succeeded")
			}
			if !errors.Is(err, domain.ErrForbidden) {
				t.Errorf("error = %v, want ErrForbidden", err)
			}
			if !strings.Contains(err.Error(), "administ") {
				t.Errorf("error message does not say why: %v", err)
			}

			after, err := s.GetUser(ctx, admin.ID)
			if err != nil {
				t.Fatalf("getting user: %v", err)
			}
			if !after.IsActive {
				t.Error("the refused deactivation still took effect")
			}
		})
	}
}

// TestDemotingTheLastActiveAdministratorIsRefused: the same guard, on the
// verb most likely to be forgotten.
//
// Mutation: delete the guard from the demote path only, leaving it on
// deactivate -- this must fail.
func TestDemotingTheLastActiveAdministratorIsRefused(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			admin := mustUserWithRole(t, s, ctx, "admin", domain.RoleAdministrator)

			err := s.SetUserRole(ctx, domain.UserActor(admin), admin.ID, domain.RoleObserver)
			if err == nil {
				t.Fatal("demoting the only administrator succeeded")
			}
			if !errors.Is(err, domain.ErrForbidden) {
				t.Errorf("error = %v, want ErrForbidden", err)
			}

			after, err := s.GetUser(ctx, admin.ID)
			if err != nil {
				t.Fatalf("getting user: %v", err)
			}
			if after.Role != domain.RoleAdministrator {
				t.Error("the refused demotion still took effect")
			}
		})
	}
}

func TestDemotingAnAdministratorIsAllowedWhileAnotherRemainsActive(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			admin1 := mustUserWithRole(t, s, ctx, "admin-1", domain.RoleAdministrator)
			admin2 := mustUserWithRole(t, s, ctx, "admin-2", domain.RoleAdministrator)

			if err := s.SetUserRole(ctx, domain.UserActor(admin2), admin1.ID, domain.RoleObserver); err != nil {
				t.Fatalf("demoting while another administrator remains active: %v", err)
			}

			after, err := s.GetUser(ctx, admin1.ID)
			if err != nil {
				t.Fatalf("getting user: %v", err)
			}
			if after.Role != domain.RoleObserver {
				t.Errorf("role = %s, want observer", after.Role)
			}

			n, err := s.CountActiveAdministrators(ctx)
			if err != nil {
				t.Fatalf("counting administrators: %v", err)
			}
			if n != 1 {
				t.Errorf("active administrators = %d, want 1 (admin-2)", n)
			}
		})
	}
}

// TestTheLastAdministratorGuardCountsOnlyActiveAdministrators: a second
// account holding the administrator role does not save the last ACTIVE one --
// an administrator who cannot sign in cannot administer anything.
func TestTheLastAdministratorGuardCountsOnlyActiveAdministrators(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			active := mustUserWithRole(t, s, ctx, "admin-active", domain.RoleAdministrator)

			inactive, err := domain.NewAppUser(NewID(), "admin-inactive", domain.UserSourceLocal, s.Now())
			if err != nil {
				t.Fatalf("building user: %v", err)
			}
			inactive.Role = domain.RoleAdministrator
			inactive.IsActive = false
			if err := s.CreateUser(ctx, domain.SystemActor, inactive); err != nil {
				t.Fatalf("creating inactive administrator: %v", err)
			}

			err = s.SetUserActive(ctx, domain.UserActor(active), active.ID, false)
			if err == nil {
				t.Fatal("deactivating the only ACTIVE administrator succeeded; " +
					"an inactive administrator row was miscounted as covering for it")
			}
			if !errors.Is(err, domain.ErrForbidden) {
				t.Errorf("error = %v, want ErrForbidden", err)
			}
		})
	}
}

// TestTheLastAdministratorGuardCoversAllThreeVerbs: demote, deactivate and
// scrub all reach the same state -- no active administrator -- so a guard on
// two of the three is a guard on none.
func TestTheLastAdministratorGuardCoversAllThreeVerbs(t *testing.T) {
	for _, e := range Engines(t) {
		for _, verb := range []string{"demote", "deactivate", "scrub"} {
			t.Run(e.Name+"/"+verb, func(t *testing.T) {
				s, ctx := newStore(t, e)
				admin := mustUserWithRole(t, s, ctx, "admin", domain.RoleAdministrator)

				var err error
				switch verb {
				case "demote":
					err = s.SetUserRole(ctx, domain.UserActor(admin), admin.ID, domain.RoleObserver)
				case "deactivate":
					err = s.SetUserActive(ctx, domain.UserActor(admin), admin.ID, false)
				case "scrub":
					err = s.ScrubUser(ctx, domain.UserActor(admin), admin.ID)
				}
				if err == nil {
					t.Fatalf("%s of the only administrator succeeded", verb)
				}
				if !errors.Is(err, domain.ErrForbidden) {
					t.Errorf("%s: error = %v, want ErrForbidden", verb, err)
				}

				n, cErr := s.CountActiveAdministrators(ctx)
				if cErr != nil {
					t.Fatalf("counting administrators: %v", cErr)
				}
				if n != 1 {
					t.Errorf("%s: active administrators = %d after a refused write, want 1", verb, n)
				}
			})
		}
	}
}

// TestTwoSimultaneousDemotionsCannotRemoveTheLastAdministrator is PostgreSQL
// only, on purpose: SQLite's writer pool holds one connection, so it cannot
// show the race writeSerializable exists to close. Skipping when
// INV_TEST_POSTGRES_DSN is unset is a declared precondition, not "the thing
// under test looked missing" -- see CLAUDE.md's testing policy.
//
// Mutation: change writeSerializable to write in SetUserRole. On PostgreSQL
// this must then fail -- both demotions observe two active administrators
// under read-committed and both commit, leaving zero. If it does not fail,
// the barrier below is not forcing the interleaving and this test is
// decoration.
func TestTwoSimultaneousDemotionsCannotRemoveTheLastAdministrator(t *testing.T) {
	if os.Getenv(postgresDSNEnv) == "" {
		t.Skipf("%s not set: this test exercises PostgreSQL's read-committed isolation "+
			"and has no SQLite equivalent", postgresDSNEnv)
	}
	s := New(openTestPostgres(t))
	ctx := context.Background()

	admin1 := mustUserWithRole(t, s, ctx, "admin-1", domain.RoleAdministrator)
	admin2 := mustUserWithRole(t, s, ctx, "admin-2", domain.RoleAdministrator)

	// A barrier synchronised only on START is not enough: the racing
	// transaction is a single round trip, and starting both goroutines
	// together was observed NOT to reproduce the read-committed failure in
	// five straight runs against the un-fixed `write` version -- the window
	// is real but too narrow to hit by scheduling luck alone. Rendezvousing
	// INSIDE refuseIfLastActiveAdministrator, after both have read the
	// count and before either has decided, forces the exact interleaving
	// this test is named for: both observe "two active administrators",
	// then both attempt to demote.
	//
	// writeSerializable retries on a serialization failure, and a correct
	// implementation's second attempt reaches this same hook again -- so the
	// rendezvous must open exactly once and then get out of the way, not
	// block a retry waiting for a "second arrival" that will never come a
	// second time. An atomic counter plus a channel closed on the second
	// arrival gives that: the first two calls block each other, everything
	// after returns immediately.
	var arrivals int32
	proceed := make(chan struct{})
	var closeOnce sync.Once
	testAfterAdministratorCount = func() {
		if atomic.AddInt32(&arrivals, 1) >= 2 {
			closeOnce.Do(func() { close(proceed) })
		}
		select {
		case <-proceed:
		case <-time.After(5 * time.Second):
			t.Error("rendezvous never reached a second arrival within 5s")
		}
	}
	t.Cleanup(func() { testAfterAdministratorCount = nil })

	results := make(chan error, 2)
	var wg sync.WaitGroup
	demote := func(id string) {
		defer wg.Done()
		results <- s.SetUserRole(ctx, testActor, id, domain.RoleObserver)
	}
	wg.Add(2)
	go demote(admin1.ID)
	go demote(admin2.ID)
	wg.Wait()
	close(results)

	succeeded := 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, domain.ErrForbidden):
			// The loser: correctly refused.
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if succeeded != 1 {
		t.Errorf("%d of 2 concurrent demotions succeeded, want exactly 1", succeeded)
	}

	n, err := s.CountActiveAdministrators(ctx)
	if err != nil {
		t.Fatalf("counting administrators: %v", err)
	}
	if n != 1 {
		t.Errorf("active administrators after the race = %d, want 1", n)
	}
}
