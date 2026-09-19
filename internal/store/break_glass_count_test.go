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
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// TestCountActivePasswordAccountsCountsOnlyAccountsThatCouldActuallySignIn.
//
// This count is the whole basis of the startup warning about break-glass
// accounts, and a wrong answer here is worse than no warning at all: it would
// tell an operator they have a way back in on the morning they do not.
//
// The cases that matter are the ones where a row LOOKS like a way in and is
// not — an SSO account (no hash at all) and a deactivated local account. Both
// are refused by auth.LocalAuthenticator, so both must be refused here.
func TestCountActivePasswordAccountsCountsOnlyAccountsThatCouldActuallySignIn(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			admin := domain.AdministratorPermit(domain.SystemActor)

			if n := breakGlassCount(t, s, ctx); n != 0 {
				t.Fatalf("a fresh database reports %d password accounts, want 0", n)
			}

			// An SSO account: real, active, and no password at all.
			if _, err := s.UpsertOIDCUser(ctx, "kc-sub-bg", "erika", "Erika", "e@example.com"); err != nil {
				t.Fatalf("creating the oidc account: %v", err)
			}
			if n := breakGlassCount(t, s, ctx); n != 0 {
				t.Errorf("an OIDC account counted as a break-glass account (%d). It has no "+
					"hash, so switching local sign-in on during an outage would present a "+
					"form it cannot use -- the exact false comfort this count exists to "+
					"prevent", n)
			}

			// A real local account with a hash.
			local, err := domain.NewAppUser(NewID(), "breakglass", domain.UserSourceLocal, s.Now())
			if err != nil {
				t.Fatalf("building the local account: %v", err)
			}
			hash := "$argon2id$v=19$m=65536,t=1,p=2$YWJjZGVmZ2hpamtsbW5vcA$c2VtaWNvbnN0YW50ZHVtbXloYXNodmFsdWU"
			local.PasswordHash = &hash
			if err := s.CreateUser(ctx, admin, local); err != nil {
				t.Fatalf("creating the local account: %v", err)
			}
			if n := breakGlassCount(t, s, ctx); n != 1 {
				t.Errorf("got %d, want 1 after creating a local account with a hash", n)
			}

			// Deactivated: the hash is still there and it is still not a way in.
			if err := s.SetUserActive(ctx, admin, local.ID, false); err != nil {
				t.Fatalf("deactivating: %v", err)
			}
			if n := breakGlassCount(t, s, ctx); n != 0 {
				t.Errorf("got %d, want 0: a deactivated account cannot sign in, so it is not "+
					"a way back in either", n)
			}
		})
	}
}

func breakGlassCount(t *testing.T, s *SQLStore, ctx context.Context) int {
	t.Helper()
	n, err := s.CountActivePasswordAccounts(ctx)
	if err != nil {
		t.Fatalf("CountActivePasswordAccounts: %v", err)
	}
	return n
}
