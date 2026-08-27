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

// Overlays and what terminates into them.

// L2VPNRow is an overlay with its attachment count.
type L2VPNRow struct {
	domain.L2VPN
	Terminations int `db:"terminations"`
}

// Reach is what this overlay's attachments mean for connectivity.
func (r L2VPNRow) Reach() domain.L2VPNReach { return domain.Reach(r.Terminations) }

// ReachNote is the sentence for the state.
func (r L2VPNRow) ReachNote() string { return domain.ReachDescription(r.Reach()) }

// ListL2VPNs returns every live overlay.
func (s *SQLStore) ListL2VPNs(ctx context.Context) ([]L2VPNRow, error) {
	var rows []L2VPNRow
	err := s.read(ctx, &rows, `
		SELECT v.*,
		       (SELECT COUNT(*) FROM l2vpn_termination t
		         WHERE t.l2vpn_id = v.id AND t.lifecycle <> 'retired') AS terminations
		FROM l2vpn v
		WHERE v.lifecycle <> 'retired'
		ORDER BY v.name`)
	if err != nil {
		return nil, fmt.Errorf("listing overlays: %w", err)
	}
	return rows, nil
}

// GetL2VPN loads one overlay.
func (s *SQLStore) GetL2VPN(ctx context.Context, id string) (*domain.L2VPN, error) {
	var v domain.L2VPN
	if err := s.readOne(ctx, &v, `SELECT * FROM l2vpn WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting overlay %s: %w", id, err)
	}
	return &v, nil
}

// CreateL2VPN declares an overlay.
func (s *SQLStore) CreateL2VPN(ctx context.Context, p domain.Permit, v *domain.L2VPN) error {
	if err := v.Validate(); err != nil {
		return err
	}
	v.RowVersion = 1
	at := domain.FormatTime(s.now())
	v.CreatedAt, v.UpdatedAt = &at, &at
	return s.write(ctx, p, func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO l2vpn (id, name, kind, identifier, description, lifecycle,
			                   created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			v.ID, v.Name, v.Kind, v.Identifier, v.Description, v.Lifecycle,
			v.CreatedAt, v.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating overlay")
		}
		if err := t.logCreate(ctx, "l2vpn", v.ID, v); err != nil {
			return err
		}
		return s.indexEntity(ctx, t, searchDoc{
			EntityType: "l2vpn", EntityID: v.ID,
			Title: v.Name, Subtitle: v.Kind, Body: v.Name + " " + v.Kind,
		})
	})
}

// RetireL2VPN withdraws an overlay, refusing while anything still terminates
// into it -- the same rule a VLAN with ports follows, for the same reason.
func (s *SQLStore) RetireL2VPN(ctx context.Context, p domain.Permit, id string) error {
	before, err := s.GetL2VPN(ctx, id)
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

	return s.writeSerializable(ctx, p, func(t *tx) error {
		var live int
		if err := t.get(ctx, &live, `
			SELECT COUNT(*) FROM l2vpn_termination
			WHERE l2vpn_id = ? AND lifecycle <> 'retired'`, id); err != nil {
			return fmt.Errorf("counting terminations of %s: %w", id, err)
		}
		if live > 0 {
			return fmt.Errorf("%w: %d attachment(s) still terminate into this overlay",
				domain.ErrConflict, live)
		}
		res, err := t.exec(ctx, `
			UPDATE l2vpn SET lifecycle = 'retired', updated_at = ?,
			                 row_version = row_version + 1
			WHERE id = ? AND row_version = ?`, at, id, before.RowVersion)
		if err != nil {
			return translateWriteErr(err, "retiring overlay")
		}
		if err := requireVersion(res, "l2vpn", id, &before.RowVersion); err != nil {
			return err
		}
		return t.logUpdate(ctx, "l2vpn", id, before, &after)
	})
}

// ---------- terminations ----------

// L2VPNTerminationRow is one attachment with whatever end it names resolved.
type L2VPNTerminationRow struct {
	domain.L2VPNTermination
	VLANName      string `db:"vlan_name"`
	VLANVID       *int   `db:"vlan_vid"`
	InterfaceName string `db:"interface_name"`
	AssetName     string `db:"asset_name"`
	AssetID       string `db:"asset_id"`
}

// ListL2VPNTerminations returns what is attached to an overlay.
func (s *SQLStore) ListL2VPNTerminations(ctx context.Context, l2vpnID string) ([]L2VPNTerminationRow, error) {
	var rows []L2VPNTerminationRow
	err := s.read(ctx, &rows, `
		SELECT t.*,
		       COALESCE(v.name, '') AS vlan_name, v.vid AS vlan_vid,
		       COALESCE(i.name, '') AS interface_name,
		       COALESCE(a.name, '') AS asset_name,
		       COALESCE(a.id, '') AS asset_id
		FROM l2vpn_termination t
		LEFT JOIN vlan v ON v.id = t.vlan_id
		LEFT JOIN interface i ON i.id = t.interface_id
		LEFT JOIN asset a ON a.id = i.asset_id
		WHERE t.l2vpn_id = ? AND t.lifecycle <> 'retired'
		ORDER BY v.vid, a.name, i.name`, l2vpnID)
	if err != nil {
		return nil, fmt.Errorf("listing terminations of %s: %w", l2vpnID, err)
	}
	return rows, nil
}

// CreateL2VPNTermination attaches a VLAN or a port to an overlay.
func (s *SQLStore) CreateL2VPNTermination(ctx context.Context, p domain.Permit,
	t *domain.L2VPNTermination) error {

	if err := t.Validate(); err != nil {
		return err
	}
	t.RowVersion = 1
	at := domain.FormatTime(s.now())
	t.CreatedAt, t.UpdatedAt = &at, &at
	return s.write(ctx, p, func(tx *tx) error {
		_, err := tx.exec(ctx, `
			INSERT INTO l2vpn_termination (id, l2vpn_id, vlan_id, interface_id,
			                               lifecycle, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			t.ID, t.L2VPNID, t.VLANID, t.InterfaceID, t.Lifecycle, t.CreatedAt, t.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "attaching to overlay")
		}
		return tx.logCreate(ctx, "l2vpn_termination", t.ID, t)
	})
}

// RetireL2VPNTermination detaches, softly like everything else.
func (s *SQLStore) RetireL2VPNTermination(ctx context.Context, p domain.Permit, id string) error {
	var before domain.L2VPNTermination
	if err := s.readOne(ctx, &before,
		`SELECT * FROM l2vpn_termination WHERE id = ?`, id); err != nil {
		return fmt.Errorf("getting termination %s: %w", id, err)
	}
	if before.Lifecycle == domain.LifecycleRetired {
		return nil
	}
	at := domain.FormatTime(s.now())
	after := before
	after.Lifecycle = domain.LifecycleRetired
	after.UpdatedAt = &at
	return s.write(ctx, p, func(t *tx) error {
		res, err := t.exec(ctx, `
			UPDATE l2vpn_termination SET lifecycle = 'retired', updated_at = ?,
			                             row_version = row_version + 1
			WHERE id = ? AND row_version = ?`, at, id, before.RowVersion)
		if err != nil {
			return translateWriteErr(err, "detaching from overlay")
		}
		if err := requireVersion(res, "l2vpn_termination", id, &before.RowVersion); err != nil {
			return err
		}
		return t.logUpdate(ctx, "l2vpn_termination", id, &before, &after)
	})
}
