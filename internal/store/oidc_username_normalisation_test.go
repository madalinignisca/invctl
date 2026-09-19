// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import "testing"

// TestOIDCRenameStoresTheUsernameTheWayCreateWouldHave.
//
// The create path spells a username through domain.NormalizeUsername (trim,
// then Unicode lower). The rename path used the store's own lower(), which is
// ASCII-only and does not trim -- so the SAME claim produced two different
// stored strings depending on whether it was somebody's first sign-in or
// their fifth.
//
// That matters because the UNIQUE index and every lookup are built from the
// create-path spelling. A row written the other way is a row that the
// collision check (spec D4) cannot see and that GetUserByUsername cannot
// find, which is how one person quietly becomes reachable under a name
// nobody else can claim and they cannot use.
func TestOIDCRenameStoresTheUsernameTheWayCreateWouldHave(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			created, err := s.UpsertOIDCUser(ctx, "kc-sub-norm", "alice", "Alice", "a@example.com")
			if err != nil {
				t.Fatalf("first sign-in: %v", err)
			}
			if created.Username != "alice" {
				t.Fatalf("create stored %q, want %q", created.Username, "alice")
			}

			// Keycloak now reports the same person with padding and capitals.
			renamed, err := s.UpsertOIDCUser(ctx, "kc-sub-norm", "  Alice.Smith  ", "Alice Smith", "a@example.com")
			if err != nil {
				t.Fatalf("rename sign-in: %v", err)
			}
			if renamed.ID != created.ID {
				t.Fatalf("rename created a second account: %s then %s", created.ID, renamed.ID)
			}
			if got, want := renamed.Username, "alice.smith"; got != want {
				t.Errorf("rename stored %q, want %q -- the rename path must spell a username "+
					"exactly as the create path would, or the UNIQUE index and every lookup "+
					"disagree about who this row is", got, want)
			}

			// And the stored spelling must be the one lookups actually use.
			found, err := s.GetUserByUsername(ctx, "alice.smith")
			if err != nil {
				t.Fatalf("looking up the renamed account by its normalised name: %v", err)
			}
			if found.ID != created.ID {
				t.Errorf("GetUserByUsername found %s, want the renamed account %s", found.ID, created.ID)
			}
		})
	}
}
