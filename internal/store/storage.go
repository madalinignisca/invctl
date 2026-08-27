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
	"fmt"
	"strings"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Reading storage pools and what is held in them (migration 00046).
//
// A POOL IS AN ASSET, so there is no pool table to list -- a pool is an asset
// carrying a raw capacity, and the kind lookup supplies the replication ratio.
// That decision is argued in the migration; what it means here is that pools
// come back through the same soft-delete, containment and audit machinery as
// everything else, and none of it had to be written twice.

// ListStoragePools returns every pool that has declared a size, with its
// replication ratio resolved.
//
// A LEFT JOIN ON THE KIND, because a pool may name no kind and that is not an
// error -- it is read at 1:1, which reports what was recorded and no more. An
// INNER JOIN here would have silently dropped exactly the pools nobody had
// finished describing, which are the ones worth seeing.
func (s *SQLStore) ListStoragePools(ctx context.Context) ([]domain.StoragePool, error) {
	var rows []struct {
		AssetID      string  `db:"asset_id"`
		Name         string  `db:"name"`
		Kind         *string `db:"storage_kind"`
		KindLabel    *string `db:"kind_label"`
		RawGB        *int    `db:"raw_capacity_gb"`
		RawPerUsable *int    `db:"raw_per_usable"`
	}
	if err := s.read(ctx, &rows, `
		SELECT a.id AS asset_id, a.name, a.storage_kind, a.raw_capacity_gb,
		       k.label AS kind_label, k.raw_per_usable
		FROM asset a
		LEFT JOIN storage_kind k ON k.code = a.storage_kind
		WHERE a.lifecycle <> ?
		  AND (a.raw_capacity_gb IS NOT NULL OR a.storage_kind IS NOT NULL)
		ORDER BY a.name`, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("listing storage pools: %w", err)
	}

	out := make([]domain.StoragePool, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.NewStoragePool(
			r.AssetID, r.Name, orEmptyString(r.Kind), orEmptyString(r.KindLabel),
			r.RawGB, r.RawPerUsable))
	}
	return out, nil
}

// GetStoragePool returns one pool, or ErrNotFound if that asset is not one.
func (s *SQLStore) GetStoragePool(ctx context.Context, assetID string) (*domain.StoragePool, error) {
	pools, err := s.ListStoragePools(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range pools {
		if p.AssetID == assetID {
			return &p, nil
		}
	}
	return nil, fmt.Errorf("storage pool %s: %w", assetID, domain.ErrNotFound)
}

// StorageClaimsOn returns everything held in one pool.
//
// Retired workloads are excluded for the reason every other capacity query
// excludes them: a decommissioned machine holds nothing, and counting it would
// keep a pool looking full long after it was emptied.
func (s *SQLStore) StorageClaimsOn(ctx context.Context, poolID string) ([]domain.StorageClaim, error) {
	var rows []struct {
		AssetID     string  `db:"asset_id"`
		AssetName   string  `db:"asset_name"`
		PoolID      string  `db:"pool_id"`
		AllocatedGB int     `db:"allocated_gb"`
		Note        *string `db:"note"`
	}
	if err := s.read(ctx, &rows, `
		SELECT c.asset_id, c.pool_id, c.allocated_gb, c.note, a.name AS asset_name
		FROM asset_storage_claim c
		JOIN asset a ON a.id = c.asset_id
		WHERE c.pool_id = ? AND a.lifecycle <> ?
		ORDER BY a.name`, poolID, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("reading claims on pool %s: %w", poolID, err)
	}
	out := make([]domain.StorageClaim, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.StorageClaim{
			AssetID: r.AssetID, AssetName: r.AssetName, PoolID: r.PoolID,
			AllocatedGB: r.AllocatedGB, Note: r.Note,
		})
	}
	return out, nil
}

// StorageClaimsFor returns everything one workload holds, across pools.
func (s *SQLStore) StorageClaimsFor(ctx context.Context, assetID string) ([]domain.StorageClaim, error) {
	var rows []struct {
		AssetID     string  `db:"asset_id"`
		PoolID      string  `db:"pool_id"`
		PoolName    string  `db:"pool_name"`
		AllocatedGB int     `db:"allocated_gb"`
		Note        *string `db:"note"`
	}
	if err := s.read(ctx, &rows, `
		SELECT c.asset_id, c.pool_id, c.allocated_gb, c.note, p.name AS pool_name
		FROM asset_storage_claim c
		JOIN asset p ON p.id = c.pool_id
		WHERE c.asset_id = ? AND p.lifecycle <> ?
		ORDER BY p.name`, assetID, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("reading storage held by %s: %w", assetID, err)
	}
	out := make([]domain.StorageClaim, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.StorageClaim{
			AssetID: r.AssetID, PoolID: r.PoolID, AssetName: r.PoolName,
			AllocatedGB: r.AllocatedGB, Note: r.Note,
		})
	}
	return out, nil
}

// StorageOccupancyFor is a pool and everything claimed against it.
func (s *SQLStore) StorageOccupancyFor(ctx context.Context, poolID string) (*domain.StorageOccupancy, error) {
	pool, err := s.GetStoragePool(ctx, poolID)
	if err != nil {
		return nil, err
	}
	claims, err := s.StorageClaimsOn(ctx, poolID)
	if err != nil {
		return nil, err
	}
	o := domain.StorageOccupancy{Pool: *pool, Claims: claims}
	for _, c := range claims {
		o.ClaimedGB += c.AllocatedGB
	}
	return &o, nil
}

// SetStorageClaim records what a workload holds in a pool, or removes the claim
// when the amount is zero.
//
// UPSERT, NOT INSERT, and the composite key is what makes it one: a workload
// states its claim on a given pool once, so correcting it is an UPDATE and the
// audit trail stays linear instead of accumulating rows nobody can order.
//
// AUDITED ON THE WORKLOAD, NOT THE POOL. The claim is a fact about the machine
// -- it is what that workload was given -- and a reader asking "why did this VM
// get more expensive" looks at the VM's history. Writing it against the pool
// would scatter one machine's story across every pool it has ever touched.
func (s *SQLStore) SetStorageClaim(ctx context.Context, p domain.Permit,
	assetID, poolID string, allocatedGB int, note *string) error {

	if assetID == poolID {
		return fmt.Errorf("a pool cannot hold itself: %w", domain.ErrInvalid)
	}
	if allocatedGB < 0 {
		return fmt.Errorf("a claim cannot be negative: %w", domain.ErrInvalid)
	}
	asset, err := s.GetAsset(ctx, assetID)
	if err != nil {
		return err
	}
	// A claim against a retired pool is a data-entry mistake, and checking it
	// here rather than leaving it to the foreign key is what makes the message
	// name the end that was wrong. The foreign key is still the second line of
	// defence, exactly as a CHECK is behind a domain constructor.
	pool, err := s.GetAsset(ctx, poolID)
	if err != nil {
		return fmt.Errorf("the pool: %w", err)
	}
	if pool.IsRetired() || asset.IsRetired() {
		return fmt.Errorf("a retired asset holds nothing: %w", domain.ErrInvalid)
	}

	now := domain.FormatTime(s.Now())
	return s.write(ctx, p, func(t *tx) error {
		before, err := storageAudit(ctx, t, &asset.Asset)
		if err != nil {
			return err
		}
		if _, err := t.exec(ctx,
			`DELETE FROM asset_storage_claim WHERE asset_id = ? AND pool_id = ?`,
			assetID, poolID); err != nil {
			return translateWriteErr(err, "clearing the previous claim")
		}
		if allocatedGB > 0 {
			if _, err := t.exec(ctx, `
				INSERT INTO asset_storage_claim
					(asset_id, pool_id, allocated_gb, note, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?)`,
				assetID, poolID, allocatedGB, note, now, now); err != nil {
				return translateWriteErr(err, "recording the claim")
			}
		}
		after, err := storageAudit(ctx, t, &asset.Asset)
		if err != nil {
			return err
		}
		return t.logUpdate(ctx, "asset", assetID, before, after)
	})
}

// assetStorageAudit is the audited shape when a claim changes: the workload
// plus everything it holds.
//
// THE SET IS FOLDED INTO THE PARENT'S AUDITED VALUE, which CLAUDE.md requires
// because this codebase has already lost the audit three times to exactly this
// shape -- a set table replaced wholesale produces no diff on the parent struct
// and therefore no change_log entry at all. The `db` tag is what makes the fold
// work: diffJSON compares db-tagged fields and ignores everything else.
type assetStorageAudit struct {
	domain.Asset
	Storage string `db:"storage"`
}

// storageAudit reads the value a claim change is diffed against.
//
// Read inside the transaction, not through s.read: the reader pool is a
// separate connection and cannot see this transaction's uncommitted writes, so
// the "after" snapshot would be identical to the "before" one and every claim
// change would log nothing.
func storageAudit(ctx context.Context, t *tx, a *domain.Asset) (*assetStorageAudit, error) {
	var rows []struct {
		PoolName    string  `db:"pool_name"`
		AllocatedGB int     `db:"allocated_gb"`
		Note        *string `db:"note"`
	}
	if err := t.selectAll(ctx, &rows, `
		SELECT c.allocated_gb, c.note, p.name AS pool_name
		FROM asset_storage_claim c
		JOIN asset p ON p.id = c.pool_id
		WHERE c.asset_id = ?
		ORDER BY p.name, c.pool_id`, a.ID); err != nil {
		return nil, fmt.Errorf("reading claims for the audit trail: %w", err)
	}
	held := make([]string, 0, len(rows))
	for _, r := range rows {
		entry := fmt.Sprintf("%s: %d GB", r.PoolName, r.AllocatedGB)
		if r.Note != nil && *r.Note != "" {
			entry += " (" + *r.Note + ")"
		}
		held = append(held, entry)
	}
	return &assetStorageAudit{Asset: *a, Storage: strings.Join(held, ", ")}, nil
}

// orEmptyString is the string half of the nil-to-zero readers this package
// already has for numbers.
func orEmptyString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
