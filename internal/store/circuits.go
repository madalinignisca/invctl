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

	"github.com/madalinignisca/invctl/internal/domain"
)

// Circuits, their providers and what they land on.

// CircuitRow is a circuit with its provider and terminations resolved.
type CircuitRow struct {
	domain.Circuit
	ProviderName string `db:"provider_name"`
	PortalURL    string `db:"portal_url"`
	Terminations int    `db:"terminations"`
}

// Landed reports whether both ends are recorded. A circuit terminating once is
// half a fact -- somebody knows where it arrives and not where it comes from --
// and the renewal conversation needs both.
func (c CircuitRow) Landed() bool { return c.Terminations >= 2 }

// ListCircuits returns every live circuit, soonest contract end first.
//
// Ordered by what expires, not by name: a list of circuits is opened because
// something is renewing, and the ones with no date recorded sort last rather
// than first -- an unknown date is not urgent, it is unrecorded.
func (s *SQLStore) ListCircuits(ctx context.Context) ([]CircuitRow, error) {
	var rows []CircuitRow
	err := s.read(ctx, &rows, `
		SELECT c.*, p.name AS provider_name, COALESCE(p.portal_url, '') AS portal_url,
		       (SELECT COUNT(*) FROM circuit_termination t
		         WHERE t.circuit_id = c.id AND t.lifecycle <> 'retired') AS terminations
		FROM circuit c
		JOIN provider p ON p.id = c.provider_id
		WHERE c.lifecycle <> 'retired'
		ORDER BY CASE WHEN c.contract_end IS NULL THEN 1 ELSE 0 END,
		         c.contract_end, p.name, c.cid`)
	if err != nil {
		return nil, fmt.Errorf("listing circuits: %w", err)
	}
	return rows, nil
}

// GetCircuit loads one circuit.
func (s *SQLStore) GetCircuit(ctx context.Context, id string) (*domain.Circuit, error) {
	var c domain.Circuit
	if err := s.readOne(ctx, &c, `SELECT * FROM circuit WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting circuit %s: %w", id, err)
	}
	return &c, nil
}

// CreateCircuit declares a contracted connection.
//
// Takes a domain.Permit -- WP-G1 Task 7's proof surface. Circuit is classified
// domain.ScopeProjectLinked (internal/domain/role.go), so a project owner's
// ScopedPermit can authorize this once Task 12/14 mint one from an actual
// request; every caller today (handlers, the seeder) still mints
// domain.AdministratorPermit or domain.SystemPermit, so nothing about who may
// create a circuit changes yet -- only that the authorization decision is now
// a real value threaded through, rather than an identity nothing checks.
func (s *SQLStore) CreateCircuit(ctx context.Context, permit domain.Permit, c *domain.Circuit) error {
	if err := c.Validate(); err != nil {
		return err
	}
	c.RowVersion = 1
	at := domain.FormatTime(s.now())
	c.CreatedAt, c.UpdatedAt = &at, &at
	return s.write(ctx, permit, func(t *tx) error {
		return s.insertCircuit(ctx, t, c)
	})
}

// insertCircuit writes one circuit row inside a transaction the CALLER owns.
//
// Split out of CreateCircuit, the same reason insertAsset (assets.go) is
// split out of CreateAsset: CreateCircuitInProject (WP-G1 Task 14) needs the
// identical entity INSERT inside a transaction that ALSO writes the
// project_circuit link row, and a second copy of this statement would be a
// second place for the two to drift.
func (s *SQLStore) insertCircuit(ctx context.Context, t *tx, c *domain.Circuit) error {
	_, err := t.exec(ctx, `
		INSERT INTO circuit (id, cid, provider_id, service_type, commit_mbps,
		                     install_date, contract_end, description, lifecycle,
		                     created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.CID, c.ProviderID, c.ServiceType, c.CommitMbps,
		c.InstallDate, c.ContractEnd, c.Description, c.Lifecycle,
		c.CreatedAt, c.UpdatedAt)
	if err != nil {
		return translateWriteErr(err, "creating circuit")
	}
	if err := t.logCreate(ctx, "circuit", c.ID, c); err != nil {
		return err
	}
	return s.indexEntity(ctx, t, searchDoc{
		EntityType: "circuit", EntityID: c.ID,
		Title: c.CID, Subtitle: derefString(c.ServiceType), Body: c.CID,
	})
}

// CreateCircuitInProject creates a NEW circuit and links it to projectID in
// the SAME transaction -- the WP-G1 Task 14 shape (docs/rbac-design.md §4)
// a project owner can reach because the project is a path parameter rather
// than a form field: the circuit is new by construction of the route, so
// there is nothing a caller could seize by naming an existing id on the
// form (see domain.scopedPermit.Covers's carve-out comment).
//
// The project_circuit INSERT is written out here rather than calling
// LinkProjectCircuit (internal/store/projects.go): that method's UPSERT
// (ON CONFLICT ... DO UPDATE) is the right shape for correcting an existing
// link, but wrong here, where the row is guaranteed not to exist yet -- a
// plain INSERT that simply fails on conflict is the more precise statement
// of "this must be new", and keeps this whole create path free of any
// upsert shape (Step 3, TestNoCreatePathIsUpsertShaped).
func (s *SQLStore) CreateCircuitInProject(ctx context.Context, p domain.Permit, projectID string, c *domain.Circuit) error {
	if err := c.Validate(); err != nil {
		return err
	}
	// THE SECURITY CHECK, on the CALLER'S OWN permit, before anything else
	// runs -- see CreateAssetInProject's identical comment (assets.go) and
	// domain.PermitHoldsProject's doc comment for the full argument.
	if !domain.PermitHoldsProject(p, projectID) {
		return fmt.Errorf("creating a circuit in project %s: %w", projectID, domain.ErrForbidden)
	}
	c.RowVersion = 1
	at := domain.FormatTime(s.now())
	c.CreatedAt, c.UpdatedAt = &at, &at
	// The transaction runs under a SECOND, narrower permit -- not p -- for
	// the same reason CreateAssetInProject's does: Covers cannot authorize
	// "circuit"/c.ID against a scope resolved before c.ID existed. Safe to
	// mint only because the check above already proved p holds projectID.
	txPermit := domain.ScopedPermit(p.Actor(), []string{projectID}, domain.ScopedEntities{
		"circuit": {c.ID: true},
	})
	return s.write(ctx, txPermit, func(t *tx) error {
		if err := s.insertCircuit(ctx, t, c); err != nil {
			return err
		}
		return s.insertProjectCircuitLink(ctx, t, projectID, c.ID)
	})
}

// insertProjectCircuitLink writes the `owns` link row for a circuit just
// created in this same transaction. Never ON CONFLICT: the circuit id was
// generated by s.insertCircuit a moment ago, so no project_circuit row for
// it can already exist, and a plain INSERT that fails loudly if that
// assumption is ever wrong is a stronger statement than an upsert that
// would paper over it.
func (s *SQLStore) insertProjectCircuitLink(ctx context.Context, t *tx, projectID, circuitID string) error {
	now := domain.FormatTime(s.now())
	link := &domain.ProjectCircuitLink{
		ProjectID: projectID, CircuitID: circuitID, Relation: domain.ProjectOwns,
		Lifecycle: domain.LifecycleActive, CreatedAt: now, UpdatedAt: now,
	}
	_, err := t.exec(ctx, `
		INSERT INTO project_circuit (project_id, circuit_id, relation, note, lifecycle, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		link.ProjectID, link.CircuitID, link.Relation, link.Note, link.Lifecycle, link.CreatedAt, link.UpdatedAt)
	if err != nil {
		return translateWriteErr(err, "linking new circuit to project")
	}
	return t.logCreate(ctx, "project_circuit", link.ProjectID+"/"+link.CircuitID, link)
}

// UpdateCircuit corrects a circuit.
func (s *SQLStore) UpdateCircuit(ctx context.Context, permit domain.Permit, c *domain.Circuit) error {
	if err := c.Validate(); err != nil {
		return err
	}
	before, err := s.GetCircuit(ctx, c.ID)
	if err != nil {
		return err
	}
	at := domain.FormatTime(s.now())
	c.UpdatedAt = &at
	return s.write(ctx, permit, func(t *tx) error {
		res, err := t.exec(ctx, `
			UPDATE circuit SET cid = ?, provider_id = ?, service_type = ?,
			                   commit_mbps = ?, install_date = ?, contract_end = ?,
			                   description = ?, updated_at = ?,
			                   row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			c.CID, c.ProviderID, c.ServiceType, c.CommitMbps, c.InstallDate,
			c.ContractEnd, c.Description, at, c.ID, c.RowVersion)
		if err != nil {
			return translateWriteErr(err, "updating circuit")
		}
		if err := requireVersion(res, "circuit", c.ID, &c.RowVersion); err != nil {
			return err
		}
		if err := t.logUpdate(ctx, "circuit", c.ID, before, c); err != nil {
			return err
		}
		return s.indexEntity(ctx, t, searchDoc{
			EntityType: "circuit", EntityID: c.ID,
			Title: c.CID, Subtitle: derefString(c.ServiceType), Body: c.CID,
		})
	})
}

// RetireCircuit ceases a circuit. Soft, like everything: a ceased circuit that
// carried a site for four years is exactly what somebody reads the change log
// to find.
func (s *SQLStore) RetireCircuit(ctx context.Context, permit domain.Permit, id string) error {
	before, err := s.GetCircuit(ctx, id)
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
	return s.write(ctx, permit, func(t *tx) error {
		res, err := t.exec(ctx, `
			UPDATE circuit SET lifecycle = 'retired', updated_at = ?,
			                   row_version = row_version + 1
			WHERE id = ? AND row_version = ?`, at, id, before.RowVersion)
		if err != nil {
			return translateWriteErr(err, "retiring circuit")
		}
		if err := requireVersion(res, "circuit", id, &before.RowVersion); err != nil {
			return err
		}
		return t.logUpdate(ctx, "circuit", id, before, &after)
	})
}

// ---------- providers ----------

// ProviderRow is a carrier with how many live circuits it sells us.
type ProviderRow struct {
	domain.Provider
	CircuitCount int `db:"circuit_count"`
}

// ListProviders returns every live carrier.
func (s *SQLStore) ListProviders(ctx context.Context) ([]ProviderRow, error) {
	var rows []ProviderRow
	err := s.read(ctx, &rows, `
		SELECT p.*,
		       (SELECT COUNT(*) FROM circuit c
		         WHERE c.provider_id = p.id AND c.lifecycle <> 'retired') AS circuit_count
		FROM provider p
		WHERE p.lifecycle <> 'retired'
		ORDER BY p.name`)
	if err != nil {
		return nil, fmt.Errorf("listing providers: %w", err)
	}
	return rows, nil
}

// CreateProvider declares a carrier.
//
// provider is classified domain.ScopeEstateConfig, not project-linked -- a
// carrier is shared across the whole estate the way a team or a vocabulary
// term is, so a ScopedPermit can never Covers() one. This method still
// changes its parameter to domain.Permit, because it shares s.write with the
// five other transactions in this file and the whole point of the seam is
// that it has no per-method exception.
func (s *SQLStore) CreateProvider(ctx context.Context, permit domain.Permit, p *domain.Provider) error {
	if err := p.Validate(); err != nil {
		return err
	}
	p.RowVersion = 1
	at := domain.FormatTime(s.now())
	p.CreatedAt, p.UpdatedAt = &at, &at
	return s.write(ctx, permit, func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO provider (id, name, account_ref, portal_url, description,
			                      lifecycle, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ID, p.Name, p.AccountRef, p.PortalURL, p.Description, p.Lifecycle,
			p.CreatedAt, p.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating provider")
		}
		return t.logCreate(ctx, "provider", p.ID, p)
	})
}

// ---------- terminations ----------

// CircuitTerminationRow is one end with whatever it lands on resolved.
type CircuitTerminationRow struct {
	domain.CircuitTermination
	AssetName     string `db:"asset_name"`
	InterfaceName string `db:"interface_name"`
	PortAssetID   string `db:"port_asset_id"`
	PortAssetName string `db:"port_asset_name"`
}

// ListCircuitTerminations returns both ends of a circuit.
func (s *SQLStore) ListCircuitTerminations(ctx context.Context, circuitID string) ([]CircuitTerminationRow, error) {
	var rows []CircuitTerminationRow
	err := s.read(ctx, &rows, `
		SELECT t.*,
		       COALESCE(a.name, '') AS asset_name,
		       COALESCE(i.name, '') AS interface_name,
		       COALESCE(pa.id, '') AS port_asset_id,
		       COALESCE(pa.name, '') AS port_asset_name
		FROM circuit_termination t
		LEFT JOIN asset a ON a.id = t.asset_id
		LEFT JOIN interface i ON i.id = t.interface_id
		LEFT JOIN asset pa ON pa.id = i.asset_id
		WHERE t.circuit_id = ? AND t.lifecycle <> 'retired'
		ORDER BY t.side`, circuitID)
	if err != nil {
		return nil, fmt.Errorf("listing terminations of %s: %w", circuitID, err)
	}
	return rows, nil
}

// CreateCircuitTermination lands one end of a circuit.
func (s *SQLStore) CreateCircuitTermination(ctx context.Context, permit domain.Permit,
	t *domain.CircuitTermination) error {

	if err := t.Validate(); err != nil {
		return err
	}
	t.RowVersion = 1
	at := domain.FormatTime(s.now())
	t.CreatedAt, t.UpdatedAt = &at, &at
	return s.write(ctx, permit, func(tx *tx) error {
		_, err := tx.exec(ctx, `
			INSERT INTO circuit_termination (id, circuit_id, side, asset_id, interface_id,
			                                 lifecycle, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID, t.CircuitID, t.Side, t.AssetID, t.InterfaceID, t.Lifecycle,
			t.CreatedAt, t.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "landing circuit end")
		}
		return tx.logCreate(ctx, "circuit_termination", t.ID, t)
	})
}

// RetireCircuitTermination lifts one end.
func (s *SQLStore) RetireCircuitTermination(ctx context.Context, permit domain.Permit, id string) error {
	var before domain.CircuitTermination
	if err := s.readOne(ctx, &before,
		`SELECT * FROM circuit_termination WHERE id = ?`, id); err != nil {
		return fmt.Errorf("getting termination %s: %w", id, err)
	}
	if before.Lifecycle == domain.LifecycleRetired {
		return nil
	}
	at := domain.FormatTime(s.now())
	after := before
	after.Lifecycle = domain.LifecycleRetired
	after.UpdatedAt = &at
	return s.write(ctx, permit, func(t *tx) error {
		res, err := t.exec(ctx, `
			UPDATE circuit_termination SET lifecycle = 'retired', updated_at = ?,
			                               row_version = row_version + 1
			WHERE id = ? AND row_version = ?`, at, id, before.RowVersion)
		if err != nil {
			return translateWriteErr(err, "lifting circuit end")
		}
		if err := requireVersion(res, "circuit_termination", id, &before.RowVersion); err != nil {
			return err
		}
		return t.logUpdate(ctx, "circuit_termination", id, &before, &after)
	})
}
