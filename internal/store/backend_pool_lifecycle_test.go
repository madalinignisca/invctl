// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// TestARetiredBackendPoolNameCanBeReused proves migration 00070's whole
// point: a withdrawn pool must stop reserving its (service_id, name), or
// "soft delete" quietly becomes "delete, except the name is gone forever" --
// the same defect migration 00003 fixed for identity(realm, name).
//
// Task 1 (this migration) ships before Task 3/4 (UpdateBackendPool,
// RetireBackendPool), so there is no store method to retire a pool yet. The
// retirement here goes straight through SQL, deliberately, the same way
// TestTheSiblingNameRuleIsInTheDatabaseNotOnlyInGo bypasses the store to
// prove a guarantee lives in the schema and not only in application code --
// the point of this test is the INDEX, not a future Go method.
func TestARetiredBackendPoolNameCanBeReused(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			svc := mustService(t, s, ctx, "lb-svc")

			first := &domain.BackendPool{ID: NewID(), ServiceID: svc, Name: "web-backends"}
			if err := s.CreateBackendPool(ctx, testPermit, first); err != nil {
				t.Fatalf("creating the first pool: %v", err)
			}

			t.Run("a second live pool with the same name is refused", func(t *testing.T) {
				dup := &domain.BackendPool{ID: NewID(), ServiceID: svc, Name: "web-backends"}
				if err := s.CreateBackendPool(ctx, testPermit, dup); err == nil {
					t.Error("web-backends was declared twice for the same service while both " +
						"were live -- the uniqueness constraint did not fire at all")
				}
			})

			// Retire the first pool directly through SQL -- see the doc comment
			// above for why there is no RetireBackendPool to call yet.
			retire := s.db.Rebind(`UPDATE backend_pool SET lifecycle = ? WHERE id = ?`)
			if _, err := s.db.Writer.ExecContext(ctx, retire, domain.LifecycleRetired, first.ID); err != nil {
				t.Fatalf("retiring the first pool by hand: %v", err)
			}

			t.Run("a new pool can now be declared under the withdrawn name", func(t *testing.T) {
				second := &domain.BackendPool{ID: NewID(), ServiceID: svc, Name: "web-backends"}
				if err := s.CreateBackendPool(ctx, testPermit, second); err != nil {
					t.Errorf("a retired pool's name could not be reused: %v -- the unique "+
						"index is still scoped to every row, not just the live ones", err)
				}
			})

			t.Run("the retired row itself is still there, not deleted", func(t *testing.T) {
				var lifecycle string
				if err := s.db.Reader.GetContext(ctx, &lifecycle,
					s.db.Rebind(`SELECT lifecycle FROM backend_pool WHERE id = ?`), first.ID); err != nil {
					t.Fatalf("reading the retired pool back: %v", err)
				}
				if lifecycle != domain.LifecycleRetired {
					t.Errorf("lifecycle = %q, want %q -- retirement must never be a delete",
						lifecycle, domain.LifecycleRetired)
				}
			})
		})
	}
}

// TestBackendPoolAndRouteCarryTheLifecycleColumns is a narrow structural
// check that migration 00070 actually landed the four columns on both
// tables, independent of the uniqueness behaviour above.
func TestBackendPoolAndRouteCarryTheLifecycleColumns(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			db := e.Open(t)
			for _, tbl := range []string{"backend_pool", "route"} {
				for _, col := range []string{"lifecycle", "row_version", "created_at", "updated_at"} {
					if !columnExists(t, db, tbl, col) {
						t.Errorf("%s.%s is missing", tbl, col)
					}
				}
			}
		})
	}
}
