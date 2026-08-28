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
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// TestReparentingIntoAnOutOfScopeParentIsRefused pins the SECOND end of a
// two-ended write.
//
// A reparent moves one asset and logs one change_log row, for the asset being
// moved -- so tx.log authorizes that end and nothing authorizes the
// destination. Before this was fixed, a project owner holding vm-a could post
// parent_id=<any asset in the estate> and it succeeded: the closure INSERT
// writes rows whose ancestor_id values are the new parent's ancestors, so an
// out-of-scope hypervisor's impact answer grows a descendant nobody
// authorized, and the audit trail names only asset/vm-a.
//
// docs/rbac-design.md §4: "a PO owning a VM does not thereby own its
// hypervisor, its rack or its site."
func TestReparentingIntoAnOutOfScopeParentIsRefused(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			vm := mustAsset(t, s, ctx, domain.KindServer, "vm-a", nil)
			esx := mustAsset(t, s, ctx, domain.KindServer, "esx-prod-01", nil)

			// Covers the asset being MOVED, not the destination.
			permit := domain.ScopedPermit(
				domain.Actor{ID: "po-9", Name: "po-9", Kind: domain.ActorKindUser},
				[]string{frontend},
				domain.ScopedEntities{"asset": {vm: true}})

			if err := s.ReparentAsset(ctx, permit, vm, &esx); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("ReparentAsset = %v, want domain.ErrForbidden", err)
			}

			// The closure table must be untouched: no ancestry linking the
			// out-of-scope parent to the moved asset.
			var n int
			if err := s.readOne(ctx, &n,
				`SELECT COUNT(*) FROM asset_closure WHERE ancestor_id = ? AND descendant_id = ?`,
				esx, vm); err != nil {
				t.Fatalf("counting closure rows: %v", err)
			}
			if n != 0 {
				t.Errorf("asset_closure gained %d row(s) linking the out-of-scope parent to the moved asset", n)
			}
		})
	}
}

// TestReparentingWithinScopeStillWorks is the other half: the check above must
// refuse the out-of-scope destination WITHOUT breaking the legitimate move,
// which is the whole point of the role.
func TestReparentingWithinScopeStillWorks(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			vm := mustAsset(t, s, ctx, domain.KindServer, "vm-b", nil)
			host := mustAsset(t, s, ctx, domain.KindServer, "host-in-scope", nil)

			permit := domain.ScopedPermit(
				domain.Actor{ID: "po-10", Name: "po-10", Kind: domain.ActorKindUser},
				[]string{frontend},
				domain.ScopedEntities{"asset": {vm: true, host: true}})

			if err := s.ReparentAsset(ctx, permit, vm, &host); err != nil {
				t.Fatalf("ReparentAsset within scope = %v, want nil", err)
			}
		})
	}
}
