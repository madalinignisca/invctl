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
	"strings"
	"testing"
)

// Prefix uniqueness is scoped to a VRF, and the global table is still a scope.
//
// THIS TEST EXISTS BECAUSE THE OBVIOUS MIGRATION IS WRONG. Replacing
// UNIQUE (cidr_text) with a composite UNIQUE (vrf_id, cidr_text) reads like it
// adds a constraint. It removes one: every row starts with vrf_id NULL, SQL
// treats NULLs as distinct on both engines, and the global table would silently
// accept 10.20.0.0/16 a hundred times. So the third case below is not a nicety
// -- it is the regression the two partial indexes exist to prevent, and it
// passes on a schema that has no VRF support at all, which is exactly why it
// needs to be asserted rather than assumed.
//
// Written against the schema rather than through the store, because the
// constraint is the thing under test and the store cannot yet write vrf_id.
// When the VRF surface lands this should gain a sibling that goes through it.
func TestAPrefixIsUniqueWithinItsVRFAndTheGlobalTableIsOne(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			mkVRF(t, s, ctx, "v-a", "tenant-a")
			mkVRF(t, s, ctx, "v-b", "tenant-b")

			// The global table: vrf_id NULL, which is what every row written
			// before this migration means.
			if err := mkPrefix(s, ctx, "g1", "10.20.0.0/16", nil); err != nil {
				t.Fatalf("the first global prefix was refused: %v", err)
			}

			t.Run("the same CIDR in two different VRFs is allowed", func(t *testing.T) {
				if err := mkPrefix(s, ctx, "a1", "10.0.0.0/8", strPtr("v-a")); err != nil {
					t.Fatalf("10.0.0.0/8 in tenant-a was refused: %v", err)
				}
				if err := mkPrefix(s, ctx, "b1", "10.0.0.0/8", strPtr("v-b")); err != nil {
					t.Fatalf("10.0.0.0/8 in tenant-b was refused, so overlapping "+
						"tenant space is still impossible: %v", err)
				}
			})

			t.Run("the same CIDR twice in one VRF is refused", func(t *testing.T) {
				err := mkPrefix(s, ctx, "a2", "10.0.0.0/8", strPtr("v-a"))
				if err == nil {
					t.Fatal("10.0.0.0/8 was created twice in tenant-a; a VRF that can " +
						"hold a prefix twice cannot answer what contains an address")
				}
				if !isUniqueViolation(err) {
					t.Errorf("error = %v, want a uniqueness violation", err)
				}
			})

			// The one that a composite index would have broken.
			t.Run("the same CIDR twice in the global table is refused", func(t *testing.T) {
				err := mkPrefix(s, ctx, "g2", "10.20.0.0/16", nil)
				if err == nil {
					t.Fatal("10.20.0.0/16 was created twice with no VRF. NULLs are " +
						"distinct in SQL, so a composite unique index enforces nothing " +
						"here -- this is the protection the partial index preserves")
				}
				if !isUniqueViolation(err) {
					t.Errorf("error = %v, want a uniqueness violation", err)
				}
			})
		})
	}
}

func mkVRF(t *testing.T, s *SQLStore, ctx context.Context, id, name string) {
	t.Helper()
	now := s.Now().UTC().Format("2006-01-02T15:04:05Z")
	q := s.db.Writer.Rebind(`INSERT INTO vrf (id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`)
	if _, err := s.db.Writer.ExecContext(ctx, q, id, name, now, now); err != nil {
		t.Fatalf("creating vrf %s: %v", name, err)
	}
}

// mkPrefix writes a prefix directly. The range columns are not meaningful here
// -- only the uniqueness of (vrf_id, cidr_text) is under test -- but they are
// NOT NULL, so they are filled with a value of the right width for the family.
func mkPrefix(s *SQLStore, ctx context.Context, id, cidr string, vrfID *string) error {
	start := make([]byte, 4)
	end := []byte{255, 255, 255, 255}
	q := s.db.Writer.Rebind(`INSERT INTO prefix
		(id, cidr_text, addr_family, addr_start, addr_end, vrf_id)
		VALUES (?, ?, 4, ?, ?, ?)`)
	_, err := s.db.Writer.ExecContext(ctx, q, id, cidr, start, end, vrfID)
	return err
}

// isUniqueViolation keeps the assertion engine-agnostic: SQLite says "UNIQUE
// constraint failed", PostgreSQL says "duplicate key value violates unique
// constraint". Matching on either would tie the test to one engine.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") || strings.Contains(msg, "duplicate key")
}
