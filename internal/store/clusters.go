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
	"sort"
	"strings"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Clusters: hosts that can carry each other's guests.

// ClusterRow is a cluster with its member and guest counts resolved.
type ClusterRow struct {
	domain.Cluster
	Hosts  int `db:"hosts"`
	Guests int `db:"guests"`
}

// AtRisk reports a cluster whose HA cannot survive losing one host.
//
// The arithmetic the operator would otherwise do in their head, and the reason
// this column exists: a two-node cluster needing two hosts has HA configured
// and buys nothing, which looks identical to a healthy one on every other page.
func (c ClusterRow) AtRisk() bool {
	if c.HAPolicy != domain.HARestart {
		return false
	}
	return domain.CanRelocate(c.HAPolicy, c.Hosts-1, c.MinHosts) != domain.RelocateOK
}

// ListClusters returns every live cluster.
func (s *SQLStore) ListClusters(ctx context.Context) ([]ClusterRow, error) {
	var rows []ClusterRow
	err := s.read(ctx, &rows, `
		SELECT c.*,
		       (SELECT COUNT(*) FROM cluster_member m
		         JOIN asset a ON a.id = m.asset_id
		         WHERE m.cluster_id = c.id AND a.lifecycle <> 'retired') AS hosts,
		       (SELECT COUNT(*) FROM cluster_member m
		         JOIN asset_closure cl ON cl.ancestor_id = m.asset_id
		         JOIN asset g ON g.id = cl.descendant_id
		         WHERE m.cluster_id = c.id
		           AND cl.ancestor_id <> cl.descendant_id
		           AND g.lifecycle <> 'retired') AS guests
		FROM cluster c
		WHERE c.lifecycle <> 'retired'
		ORDER BY c.name`)
	if err != nil {
		return nil, fmt.Errorf("listing clusters: %w", err)
	}
	return rows, nil
}

// GetCluster loads one cluster.
func (s *SQLStore) GetCluster(ctx context.Context, id string) (*domain.Cluster, error) {
	var c domain.Cluster
	if err := s.readOne(ctx, &c, `SELECT * FROM cluster WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting cluster %s: %w", id, err)
	}
	return &c, nil
}

// CreateCluster declares a cluster.
func (s *SQLStore) CreateCluster(ctx context.Context, actor domain.Actor, c *domain.Cluster) error {
	if err := c.Validate(); err != nil {
		return err
	}
	c.RowVersion = 1
	at := domain.FormatTime(s.now())
	c.CreatedAt, c.UpdatedAt = &at, &at
	return s.write(ctx, domain.AdministratorPermit(actor), func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO cluster (id, name, kind, ha_policy, min_hosts, cpu_overcommit,
			                     cost_split_cpu, description, lifecycle, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ID, c.Name, c.Kind, c.HAPolicy, c.MinHosts, c.CPUOvercommit,
			c.CostSplitCPU, c.Description, c.Lifecycle, c.CreatedAt, c.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating cluster")
		}
		if err := t.logCreate(ctx, "cluster", c.ID, c); err != nil {
			return err
		}
		return s.indexEntity(ctx, t, searchDoc{
			EntityType: "cluster", EntityID: c.ID,
			Title: c.Name, Subtitle: c.Kind, Body: c.Name + " " + c.Kind,
		})
	})
}

// UpdateCluster corrects a cluster, including the policy the engine reads.
func (s *SQLStore) UpdateCluster(ctx context.Context, actor domain.Actor, c *domain.Cluster) error {
	if err := c.Validate(); err != nil {
		return err
	}
	before, err := s.GetCluster(ctx, c.ID)
	if err != nil {
		return err
	}
	at := domain.FormatTime(s.now())
	c.UpdatedAt = &at
	return s.write(ctx, domain.AdministratorPermit(actor), func(t *tx) error {
		res, err := t.exec(ctx, `
			UPDATE cluster SET name = ?, kind = ?, ha_policy = ?, min_hosts = ?,
			                   cpu_overcommit = ?, cost_split_cpu = ?,
			                   description = ?, updated_at = ?,
			                   row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			c.Name, c.Kind, c.HAPolicy, c.MinHosts, c.CPUOvercommit, c.CostSplitCPU,
			c.Description, at, c.ID, c.RowVersion)
		if err != nil {
			return translateWriteErr(err, "updating cluster")
		}
		if err := requireVersion(res, "cluster", c.ID, &c.RowVersion); err != nil {
			return err
		}
		if err := t.logUpdate(ctx, "cluster", c.ID, before, c); err != nil {
			return err
		}
		return s.indexEntity(ctx, t, searchDoc{
			EntityType: "cluster", EntityID: c.ID,
			Title: c.Name, Subtitle: c.Kind, Body: c.Name + " " + c.Kind,
		})
	})
}

// RetireCluster withdraws a cluster, refusing while hosts are still in it.
//
// A retired cluster with members is a row saying "these hosts cannot carry each
// other" beside several saying they are in it -- and because the ENGINE reads
// this, the contradiction would change what a simulation concludes rather than
// only what a page shows.
func (s *SQLStore) RetireCluster(ctx context.Context, actor domain.Actor, id string) error {
	before, err := s.GetCluster(ctx, id)
	if err != nil {
		return err
	}
	if before.Retired() {
		return nil
	}
	at := domain.FormatTime(s.now())
	after := *before
	after.Lifecycle = domain.LifecycleRetired
	after.UpdatedAt = &at

	return s.writeSerializable(ctx, domain.AdministratorPermit(actor), func(t *tx) error {
		var members int
		if err := t.get(ctx, &members,
			`SELECT COUNT(*) FROM cluster_member WHERE cluster_id = ?`, id); err != nil {
			return fmt.Errorf("counting members of %s: %w", id, err)
		}
		if members > 0 {
			return fmt.Errorf("%w: %d host(s) are still in this cluster",
				domain.ErrConflict, members)
		}
		res, err := t.exec(ctx, `
			UPDATE cluster SET lifecycle = 'retired', updated_at = ?,
			                   row_version = row_version + 1
			WHERE id = ? AND row_version = ?`, at, id, before.RowVersion)
		if err != nil {
			return translateWriteErr(err, "retiring cluster")
		}
		if err := requireVersion(res, "cluster", id, &before.RowVersion); err != nil {
			return err
		}
		return t.logUpdate(ctx, "cluster", id, before, &after)
	})
}

// ---------- membership ----------

// ClusterHostRow is one host in a cluster, with what it is carrying.
type ClusterHostRow struct {
	AssetID   string `db:"asset_id"`
	AssetName string `db:"asset_name"`
	Guests    int    `db:"guests"`
}

// ListClusterHosts returns the members of a cluster.
func (s *SQLStore) ListClusterHosts(ctx context.Context, clusterID string) ([]ClusterHostRow, error) {
	var rows []ClusterHostRow
	err := s.read(ctx, &rows, `
		SELECT m.asset_id, a.name AS asset_name,
		       (SELECT COUNT(*) FROM asset_closure cl
		         JOIN asset g ON g.id = cl.descendant_id
		         WHERE cl.ancestor_id = m.asset_id
		           AND cl.ancestor_id <> cl.descendant_id
		           AND g.lifecycle <> 'retired') AS guests
		FROM cluster_member m
		JOIN asset a ON a.id = m.asset_id
		WHERE m.cluster_id = ? AND a.lifecycle <> 'retired'
		ORDER BY a.name`, clusterID)
	if err != nil {
		return nil, fmt.Errorf("listing hosts of %s: %w", clusterID, err)
	}
	return rows, nil
}

// SetClusterMembers replaces a cluster's whole membership.
//
// The sixth set table in this schema, and the rule has not changed: replaced
// wholesale inside the parent's transaction and folded into the parent's
// audited value. This one moves guests, so a host leaving with no diff on the
// cluster is a change that alters what the engine concludes and that nobody can
// find afterwards.
func (s *SQLStore) SetClusterMembers(ctx context.Context, actor domain.Actor,
	clusterID string, members []domain.ClusterMember) error {

	cluster, err := s.GetCluster(ctx, clusterID)
	if err != nil {
		return err
	}
	current, err := s.ListClusterHosts(ctx, clusterID)
	if err != nil {
		return err
	}
	before := auditedCluster(cluster, current)
	at := domain.FormatTime(s.now())

	return s.write(ctx, domain.AdministratorPermit(actor), func(t *tx) error {
		if _, err := t.exec(ctx,
			`DELETE FROM cluster_member WHERE cluster_id = ?`, clusterID); err != nil {
			return translateWriteErr(err, "clearing cluster membership")
		}
		for _, m := range members {
			_, err := t.exec(ctx,
				`INSERT INTO cluster_member (cluster_id, asset_id) VALUES (?, ?)`,
				clusterID, m.AssetID)
			if err != nil {
				return translateWriteErr(err, "setting cluster membership")
			}
		}
		if _, err := t.exec(ctx, `
			UPDATE cluster SET updated_at = ?, row_version = row_version + 1
			WHERE id = ?`, at, clusterID); err != nil {
			return translateWriteErr(err, "touching cluster")
		}

		var rows []ClusterHostRow
		if err := t.selectAll(ctx, &rows, `
			SELECT m.asset_id, a.name AS asset_name, 0 AS guests
			FROM cluster_member m JOIN asset a ON a.id = m.asset_id
			WHERE m.cluster_id = ? ORDER BY a.name`, clusterID); err != nil {
			return fmt.Errorf("reading cluster membership: %w", err)
		}
		updated := *cluster
		updated.UpdatedAt = &at
		updated.RowVersion = cluster.RowVersion + 1
		return t.logUpdate(ctx, "cluster", clusterID, before, auditedCluster(&updated, rows))
	})
}

// clusterAudit is the audited shape: the cluster plus which hosts are in it.
//
// The `db` tag is what makes the fold work -- diffJSON compares db-tagged
// fields and ignores everything else, which is how the VLAN membership diff
// came out empty the first time it was written.
type clusterAudit struct {
	domain.Cluster
	Members string `db:"members"`
}

func auditedCluster(c *domain.Cluster, hosts []ClusterHostRow) *clusterAudit {
	names := make([]string, 0, len(hosts))
	for _, h := range hosts {
		names = append(names, h.AssetName)
	}
	sort.Strings(names)
	return &clusterAudit{Cluster: *c, Members: strings.Join(names, ",")}
}

// ClusterCandidates lists hosts that could join a cluster: hypervisors and
// servers not already in one, since a host belongs to at most one.
func (s *SQLStore) ClusterCandidates(ctx context.Context, clusterID string) ([]AssetRow, error) {
	var rows []AssetRow
	err := s.read(ctx, &rows, `
		SELECT a.* FROM asset a
		WHERE a.lifecycle <> 'retired'
		  AND a.kind IN ('hypervisor', 'server')
		  AND (a.id NOT IN (SELECT asset_id FROM cluster_member)
		       OR a.id IN (SELECT asset_id FROM cluster_member WHERE cluster_id = ?))
		ORDER BY a.name`, clusterID)
	if err != nil {
		return nil, fmt.Errorf("listing cluster candidates: %w", err)
	}
	return rows, nil
}
