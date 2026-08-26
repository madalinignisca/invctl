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
	})
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
