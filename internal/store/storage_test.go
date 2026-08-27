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

// pool turns an asset into a storage pool of the given size and kind.
func (f *projectFixture) pool(t *testing.T, name, kind string, rawGB int) string {
	t.Helper()
	id := mustAsset(t, f.s, f.ctx, domain.KindStorage, name, nil, f.env)
	f.assets[name] = id
	row, err := f.s.GetAsset(f.ctx, id)
	if err != nil {
		t.Fatalf("reading the pool: %v", err)
	}
	a := row.Asset
	a.StorageKind, a.RawCapacityGB = &kind, &rawGB
	if err := f.s.UpdateAsset(f.ctx, testPermit, &a, []string{f.env}); err != nil {
		t.Fatalf("sizing the pool: %v", err)
	}
	return id
}

// TestAPoolReportsUsableCapacityNotRaw.
//
// The reason raw is what gets recorded: an operator knows how many disks went
// in, and the replication factor is the part people get wrong in conversation.
// This proves the ratio survives the round trip through the lookup table rather
// than only through the domain arithmetic.
func TestAPoolReportsUsableCapacityNotRaw(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			id := f.pool(t, "ceph-block", "ceph_3x", 30720)

			p, err := f.s.GetStoragePool(f.ctx, id)
			if err != nil {
				t.Fatalf("reading the pool: %v", err)
			}
			if p.UsableGB() != 10240 {
				t.Errorf("usable is %d GB, want 10240 (30 TB raw at 3x)", p.UsableGB())
			}
			if !p.KindDeclared {
				t.Error("the declared storage kind did not reach the pool")
			}
			if p.KindLabel == "" {
				t.Error("the kind's label was not resolved, so no page can name it")
			}
		})
	}
}

// TestAPoolWithNoKindIsReadAtOneToOne.
//
// A LEFT JOIN, and the pessimistic direction is the opposite of the CPU
// ratio's. Assuming replication nobody declared would invent lost capacity,
// make the pool look smaller, and make every project's share of it look
// larger. Reporting exactly what was recorded cannot flatter or alarm anybody.
func TestAPoolWithNoKindIsReadAtOneToOne(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			id := mustAsset(t, f.s, f.ctx, domain.KindStorage, "unlabelled", nil, f.env)
			row, _ := f.s.GetAsset(f.ctx, id)
			a := row.Asset
			raw := 1000
			a.RawCapacityGB = &raw
			if err := f.s.UpdateAsset(f.ctx, testPermit, &a, []string{f.env}); err != nil {
				t.Fatalf("sizing: %v", err)
			}

			p, err := f.s.GetStoragePool(f.ctx, id)
			if err != nil {
				t.Fatalf("a pool with no declared kind was dropped from the list: %v", err)
			}
			if p.UsableGB() != 1000 {
				t.Errorf("usable is %d GB, want the 1000 that was recorded", p.UsableGB())
			}
			if p.KindDeclared {
				t.Error("a pool with no kind reports a declared ratio")
			}
		})
	}
}

// TestWhatAWorkloadHoldsIsAudited.
//
// CLAUDE.md's rule, and the one this codebase has broken three times: a set
// table replaced wholesale produces no diff on the parent struct and therefore
// no change_log entry at all. The claim is folded into the asset's audited
// value, so changing it is a change to the asset.
func TestWhatAWorkloadHoldsIsAudited(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			poolID := f.pool(t, "ceph-block", "ceph_3x", 30720)
			vm := f.assets["vm-app-1"]

			if err := f.s.SetStorageClaim(f.ctx, testPermit, vm, poolID, 200, nil); err != nil {
				t.Fatalf("recording a claim: %v", err)
			}
			entries, err := f.s.ListChangesForEntity(f.ctx, "asset", vm, 10)
			if err != nil {
				t.Fatalf("reading the audit trail: %v", err)
			}
			if len(entries) == 0 {
				t.Fatal("recording what a workload holds wrote no change_log entry")
			}
			if !strings.Contains(entries[0].Diff, "200 GB") {
				t.Errorf("the audit entry does not say what was claimed: %s", entries[0].Diff)
			}

			// Correcting it is an update, not a second row to reconcile.
			if err := f.s.SetStorageClaim(f.ctx, testPermit, vm, poolID, 350, nil); err != nil {
				t.Fatalf("correcting a claim: %v", err)
			}
			claims, err := f.s.StorageClaimsFor(f.ctx, vm)
			if err != nil {
				t.Fatalf("reading claims: %v", err)
			}
			if len(claims) != 1 || claims[0].AllocatedGB != 350 {
				t.Fatalf("after a correction the workload holds %+v, want one claim of 350", claims)
			}
			after, err := f.s.ListChangesForEntity(f.ctx, "asset", vm, 10)
			if err != nil {
				t.Fatalf("reading the audit trail: %v", err)
			}
			if len(after) <= len(entries) {
				t.Error("correcting a claim wrote no further change_log entry")
			}
		})
	}
}

// TestAPoolCannotHoldItselfAndARetiredOneHoldsNothing. Both refusals name the
// end that was wrong, which a foreign-key violation cannot.
func TestAPoolCannotHoldItselfAndARetiredOneHoldsNothing(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			poolID := f.pool(t, "ceph-block", "ceph_3x", 30720)
			vm := f.assets["vm-app-1"]

			if err := f.s.SetStorageClaim(f.ctx, testPermit, poolID, poolID, 10, nil); !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("a pool holding itself returned %v, want ErrInvalid", err)
			}
			if err := f.s.RetireAsset(f.ctx, testPermit, poolID); err != nil {
				t.Fatalf("retiring the pool: %v", err)
			}
			if err := f.s.SetStorageClaim(f.ctx, testPermit, vm, poolID, 10, nil); !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("a claim against a retired pool returned %v, want ErrInvalid", err)
			}
		})
	}
}

// TestAClaimOfZeroRemovesIt. Setting it to nothing is how a claim is withdrawn,
// and that is a correction of a declared figure rather than a deletion of a
// fact -- the row said how much a workload was given, and it is now given none.
func TestAClaimOfZeroRemovesIt(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			poolID := f.pool(t, "ceph-block", "ceph_3x", 30720)
			vm := f.assets["vm-app-1"]

			if err := f.s.SetStorageClaim(f.ctx, testPermit, vm, poolID, 200, nil); err != nil {
				t.Fatalf("recording: %v", err)
			}
			if err := f.s.SetStorageClaim(f.ctx, testPermit, vm, poolID, 0, nil); err != nil {
				t.Fatalf("withdrawing: %v", err)
			}
			o, err := f.s.StorageOccupancyFor(f.ctx, poolID)
			if err != nil {
				t.Fatalf("reading occupancy: %v", err)
			}
			if o.ClaimedGB != 0 || len(o.Claims) != 0 {
				t.Errorf("the pool still holds %d GB in %d claims", o.ClaimedGB, len(o.Claims))
			}
		})
	}
}
