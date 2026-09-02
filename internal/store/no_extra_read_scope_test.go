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

// WP-G1 Task 15: "handlers that never load the object" -- AssetRetire,
// ClusterRetire and the four Cost* families never call GetAsset/GetCluster/
// GetCost themselves before writing; they hand the id straight from
// r.PathValue to the store method, and the store method's own tx.log call
// is where Covers is consulted, already inside the write transaction,
// already knowing the id.
//
// THE "NO EXTRA QUERY" HALF OF THE BRIEF'S OWN TEST NAME
// (TestRetiringAnOutOfScopeAssetIsRefusedWithoutTheHandlerLoadingIt) IS NOT
// PROVEN HERE. This codebase has no reader-pool query counter, and the
// brief is explicit that inventing a fragile one is worse than reporting
// the gap honestly. What CAN be said without one: RetireAsset itself DOES
// call s.GetAsset before opening its write transaction (to build the
// lifecycle diff and to short-circuit an already-retired asset) -- so "zero
// reads happen" is not literally true even for an Administrator, and this
// suite does not claim otherwise. What Covers buys is narrower than "no
// extra query": no SEPARATE membership lookup at authorization time, because
// the scope was already resolved once per request (auth.Authorizer.Permit,
// Task 12) and Covers itself is an in-memory map check. Only the 403 half is
// asserted below, honestly.
func TestRetiringAnOutOfScopeAssetIsRefusedWithoutTheHandlerLoadingIt(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			assetID := mustAsset(t, s, ctx, domain.KindServer, "no-extra-read-asset", nil)
			before, err := s.GetAsset(ctx, assetID)
			if err != nil {
				t.Fatalf("GetAsset: %v", err)
			}

			poID := mustEntityTagsScopeUser(t, s, ctx, "no-extra-read-po")
			permit := domain.ScopedPermit(
				domain.Actor{ID: poID, Name: "no-extra-read-po", Kind: domain.ActorKindUser}, nil, nil)

			if err := s.RetireAsset(ctx, permit, assetID); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("RetireAsset = %v, want domain.ErrForbidden", err)
			}
			after, err := s.GetAsset(ctx, assetID)
			if err != nil {
				t.Fatalf("GetAsset after refusal: %v", err)
			}
			if after.Lifecycle != before.Lifecycle {
				t.Errorf("lifecycle = %q, want unchanged %q", after.Lifecycle, before.Lifecycle)
			}
			if after.RowVersion != before.RowVersion {
				t.Errorf("row_version changed on a refused retire: before %d, after %d",
					before.RowVersion, after.RowVersion)
			}
		})
	}
}

func TestRetiringAnOutOfScopeClusterIsRefused(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			clusterID := mustCluster(t, s, ctx, "no-extra-read-cluster", domain.HANone, nil)

			poID := mustEntityTagsScopeUser(t, s, ctx, "no-extra-read-po-cluster")
			permit := domain.ScopedPermit(
				domain.Actor{ID: poID, Name: "no-extra-read-po-cluster", Kind: domain.ActorKindUser}, nil, nil)

			if err := s.RetireCluster(ctx, permit, clusterID); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("RetireCluster = %v, want domain.ErrForbidden", err)
			}
			c, err := s.GetCluster(ctx, clusterID)
			if err != nil {
				t.Fatalf("GetCluster: %v", err)
			}
			if c.Retired() {
				t.Error("the cluster was retired despite the refusal")
			}
		})
	}
}

// TestRetiringAnOutOfScopeCostLineIsRefused USED TO prove a permit covering
// the owning asset was not enough -- back when asset_cost was
// domain.ScopeTopology and unconditionally Administrator-only regardless of
// what the permit covered. WP-1.1 item 3 made that assertion false on
// purpose: asset_cost is domain.ScopeSubjectDerived now, and a permit that
// covers the owning asset IS meant to retire a cost line on it (see
// costs_scope_test.go's TestCostScopeOnTheirOwnAsset for that positive
// case). What this file's own family of tests is actually about --
// refused, and refused WITHOUT the handler having to load the object first
// -- still needs a genuinely out-of-scope permit to demonstrate, so this
// case now holds an unrelated asset instead of the cost line's own owner,
// the same shape TestRetiringAnOutOfScopeAssetIsRefusedWithoutTheHandlerLoadingIt
// and TestRetiringAnOutOfScopeClusterIsRefused already use above.
func TestRetiringAnOutOfScopeCostLineIsRefused(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			assetID := mustAsset(t, s, ctx, domain.KindServer, "no-extra-read-cost-asset", nil)
			otherAssetID := mustAsset(t, s, ctx, domain.KindServer, "no-extra-read-cost-other-asset", nil)
			cost, err := domain.NewCost(NewID(), domain.CostSpec{
				Kind: "acquisition", Period: domain.CostOnce, AmountMinor: 100_00,
			}, s.Now())
			if err != nil {
				t.Fatalf("building cost: %v", err)
			}
			if err := s.AddAssetCost(ctx, testPermit, assetID, cost); err != nil {
				t.Fatalf("adding cost: %v", err)
			}

			poID := mustEntityTagsScopeUser(t, s, ctx, "no-extra-read-po-cost")
			// Holds a DIFFERENT asset, not the cost line's own owner: proves
			// the refusal is a genuine out-of-scope subject, not merely
			// asset_cost being unconditionally Administrator-only (which it
			// no longer is).
			permit := domain.ScopedPermit(
				domain.Actor{ID: poID, Name: "no-extra-read-po-cost", Kind: domain.ActorKindUser},
				nil, domain.ScopedEntities{"asset": {otherAssetID: true}})

			if err := s.RetireAssetCost(ctx, permit, assetID, cost.ID); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("RetireAssetCost = %v, want domain.ErrForbidden", err)
			}
			row, err := s.getCost(ctx, costOnAsset, cost.ID)
			if err != nil {
				t.Fatalf("getCost: %v", err)
			}
			if row.Lifecycle != domain.LifecycleActive {
				t.Errorf("lifecycle = %q, want %q", row.Lifecycle, domain.LifecycleActive)
			}
		})
	}
}
