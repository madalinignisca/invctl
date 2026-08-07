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
	"strconv"
	"strings"

	"github.com/madalinignisca/invctl/internal/domain"
)

// VLANRow is a VLAN with everything the list needs resolved: which group it
// numbers within, how many prefixes it addresses and how many ports are in it.
type VLANRow struct {
	domain.VLAN
	GroupName       string `db:"group_name"`
	ScopeAssetName  string `db:"scope_asset_name"`
	EnvironmentCode string `db:"environment_code"`
	// PrefixCount and PortCount are what turn a record into a broadcast domain
	// on screen. A VLAN with neither is declared and unused, which is a finding
	// rather than a gap in this query.
	PrefixCount int `db:"prefix_count"`
	PortCount   int `db:"port_count"`
}

// ListVLANs returns every live VLAN, grouped numbering first.
func (s *SQLStore) ListVLANs(ctx context.Context) ([]VLANRow, error) {
	var rows []VLANRow
	err := s.read(ctx, &rows, `
		SELECT v.*,
		       COALESCE(g.name, '') AS group_name,
		       COALESCE(a.name, '') AS scope_asset_name,
		       COALESCE(e.code, '') AS environment_code,
		       (SELECT COUNT(*) FROM prefix p WHERE p.vlan_ref_id = v.id) AS prefix_count,
		       (SELECT COUNT(*) FROM interface_vlan iv WHERE iv.vlan_id = v.id) AS port_count
		FROM vlan v
		LEFT JOIN vlan_group g ON g.id = v.group_id
		LEFT JOIN asset a ON a.id = g.scope_asset_id
		LEFT JOIN environment e ON e.id = v.environment_id
		WHERE v.lifecycle <> 'retired'
		ORDER BY COALESCE(g.name, ''), v.vid`)
	if err != nil {
		return nil, fmt.Errorf("listing vlans: %w", err)
	}
	return rows, nil
}

// GetVLAN loads one broadcast domain.
func (s *SQLStore) GetVLAN(ctx context.Context, id string) (*domain.VLAN, error) {
	var v domain.VLAN
	if err := s.readOne(ctx, &v, `SELECT * FROM vlan WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting vlan %s: %w", id, err)
	}
	return &v, nil
}

// CreateVLAN declares a broadcast domain.
func (s *SQLStore) CreateVLAN(ctx context.Context, actor domain.Actor, v *domain.VLAN) error {
	if err := v.Validate(); err != nil {
		return err
	}
	v.RowVersion = 1
	at := domain.FormatTime(s.now())
	v.CreatedAt, v.UpdatedAt = &at, &at
	return s.write(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO vlan (id, vid, name, group_id, role, environment_id,
			                  description, lifecycle, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			v.ID, v.VID, v.Name, v.GroupID, v.Role, v.EnvironmentID,
			v.Description, v.Lifecycle, v.CreatedAt, v.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating vlan")
		}
		if err := t.logCreate(ctx, "vlan", v.ID, v); err != nil {
			return err
		}
		return s.indexEntity(ctx, t, searchDoc{
			EntityType: "vlan", EntityID: v.ID,
			Title:    fmt.Sprintf("VLAN %d — %s", v.VID, v.Name),
			Subtitle: derefString(v.Role),
			Body:     fmt.Sprintf("%d %s", v.VID, v.Name),
		})
	})
}

// UpdateVLAN corrects a broadcast domain.
func (s *SQLStore) UpdateVLAN(ctx context.Context, actor domain.Actor, v *domain.VLAN) error {
	if err := v.Validate(); err != nil {
		return err
	}
	before, err := s.GetVLAN(ctx, v.ID)
	if err != nil {
		return err
	}
	at := domain.FormatTime(s.now())
	v.UpdatedAt = &at

	return s.write(ctx, actor, func(t *tx) error {
		res, err := t.exec(ctx, `
			UPDATE vlan SET vid = ?, name = ?, group_id = ?, role = ?,
			                environment_id = ?, description = ?,
			                updated_at = ?, row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			v.VID, v.Name, v.GroupID, v.Role, v.EnvironmentID, v.Description,
			at, v.ID, v.RowVersion)
		if err != nil {
			return translateWriteErr(err, "updating vlan")
		}
		if err := requireVersion(res, "vlan", v.ID, &v.RowVersion); err != nil {
			return err
		}
		if err := t.logUpdate(ctx, "vlan", v.ID, before, v); err != nil {
			return err
		}
		return s.indexEntity(ctx, t, searchDoc{
			EntityType: "vlan", EntityID: v.ID,
			Title:    fmt.Sprintf("VLAN %d — %s", v.VID, v.Name),
			Subtitle: derefString(v.Role),
			Body:     fmt.Sprintf("%d %s", v.VID, v.Name),
		})
	})
}

// RetireVLAN withdraws a broadcast domain.
//
// It refuses while prefixes or ports still name it. A retired VLAN that
// addresses live networks is a record saying "this does not exist" beside
// several saying "and here is what is on it" -- and soft delete means the
// contradiction is permanent rather than merely wrong.
func (s *SQLStore) RetireVLAN(ctx context.Context, actor domain.Actor, id string) error {
	before, err := s.GetVLAN(ctx, id)
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

	return s.writeSerializable(ctx, actor, func(t *tx) error {
		var users int
		if err := t.get(ctx, &users, `
			SELECT (SELECT COUNT(*) FROM prefix WHERE vlan_ref_id = ?)
			     + (SELECT COUNT(*) FROM interface_vlan WHERE vlan_id = ?)`, id, id); err != nil {
			return fmt.Errorf("counting what is on vlan %s: %w", id, err)
		}
		if users > 0 {
			return fmt.Errorf("%w: %d network(s) or port(s) are still on this VLAN",
				domain.ErrConflict, users)
		}
		res, err := t.exec(ctx, `
			UPDATE vlan SET lifecycle = 'retired', updated_at = ?,
			                row_version = row_version + 1
			WHERE id = ? AND row_version = ?`, at, id, before.RowVersion)
		if err != nil {
			return translateWriteErr(err, "retiring vlan")
		}
		if err := requireVersion(res, "vlan", id, &before.RowVersion); err != nil {
			return err
		}
		return t.logUpdate(ctx, "vlan", id, before, &after)
	})
}

// ---------- groups ----------

// VLANGroupRow is a numbering scope with its scope asset resolved.
type VLANGroupRow struct {
	domain.VLANGroup
	ScopeAssetName string `db:"scope_asset_name"`
	VLANCount      int    `db:"vlan_count"`
}

// ListVLANGroups returns every live numbering scope.
func (s *SQLStore) ListVLANGroups(ctx context.Context) ([]VLANGroupRow, error) {
	var rows []VLANGroupRow
	err := s.read(ctx, &rows, `
		SELECT g.*, COALESCE(a.name, '') AS scope_asset_name,
		       (SELECT COUNT(*) FROM vlan v
		         WHERE v.group_id = g.id AND v.lifecycle <> 'retired') AS vlan_count
		FROM vlan_group g
		LEFT JOIN asset a ON a.id = g.scope_asset_id
		WHERE g.lifecycle <> 'retired'
		ORDER BY g.name`)
	if err != nil {
		return nil, fmt.Errorf("listing vlan groups: %w", err)
	}
	return rows, nil
}

// CreateVLANGroup declares a numbering scope.
func (s *SQLStore) CreateVLANGroup(ctx context.Context, actor domain.Actor, g *domain.VLANGroup) error {
	if err := g.Validate(); err != nil {
		return err
	}
	g.RowVersion = 1
	at := domain.FormatTime(s.now())
	g.CreatedAt, g.UpdatedAt = &at, &at
	return s.write(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO vlan_group (id, name, scope_asset_id, description, lifecycle,
			                        created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			g.ID, g.Name, g.ScopeAssetID, g.Description, g.Lifecycle, g.CreatedAt, g.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating vlan group")
		}
		return t.logCreate(ctx, "vlan_group", g.ID, g)
	})
}

// ---------- port membership ----------

// VLANPort is one port in a VLAN, with the box it is in.
type VLANPort struct {
	InterfaceID   string `db:"interface_id"`
	InterfaceName string `db:"interface_name"`
	AssetID       string `db:"asset_id"`
	AssetName     string `db:"asset_name"`
	Mode          string `db:"mode"`
}

// ListVLANPorts returns every port in a VLAN.
//
// THIS IS THE EDGE. A VLAN with prefixes and no ports is a record; this is what
// makes it a broadcast domain, and it is a fact no cable trace can produce --
// two access ports in VLAN 30 can reach each other whether or not anybody drew
// a cable between them.
func (s *SQLStore) ListVLANPorts(ctx context.Context, vlanID string) ([]VLANPort, error) {
	var rows []VLANPort
	err := s.read(ctx, &rows, `
		SELECT iv.interface_id, i.name AS interface_name, iv.mode,
		       a.id AS asset_id, a.name AS asset_name
		FROM interface_vlan iv
		JOIN interface i ON i.id = iv.interface_id
		JOIN asset a ON a.id = i.asset_id
		WHERE iv.vlan_id = ?
		ORDER BY a.name, i.name`, vlanID)
	if err != nil {
		return nil, fmt.Errorf("listing ports on vlan %s: %w", vlanID, err)
	}
	return rows, nil
}

// SetInterfaceVLANs replaces a port's whole membership set.
//
// REPLACED WHOLESALE, like asset_environment, and audited on the INTERFACE --
// the membership belongs to the port and has no life of its own. The parent's
// change_log entry has to record it, or a port moving from VLAN 10 to VLAN 20
// produces no diff at all: the failure CLAUDE.md names three times over as the
// one set replacement keeps causing. It was made a fourth time here, and the
// test caught it -- see interfaceVLANAudit for exactly how.
func (s *SQLStore) SetInterfaceVLANs(ctx context.Context, actor domain.Actor,
	interfaceID string, members []domain.InterfaceVLAN) error {

	if err := domain.ValidateVLANMembership(members); err != nil {
		return err
	}
	iface, err := s.GetInterface(ctx, interfaceID)
	if err != nil {
		return err
	}
	beforeMembers, err := s.listInterfaceVLANs(ctx, interfaceID)
	if err != nil {
		return err
	}
	before := auditedInterfaceVLANs(iface, beforeMembers)
	at := domain.FormatTime(s.now())

	return s.write(ctx, actor, func(t *tx) error {
		if _, err := t.exec(ctx,
			`DELETE FROM interface_vlan WHERE interface_id = ?`, interfaceID); err != nil {
			return translateWriteErr(err, "clearing vlan membership")
		}
		for _, m := range members {
			_, err := t.exec(ctx, `
				INSERT INTO interface_vlan (interface_id, vlan_id, mode) VALUES (?, ?, ?)`,
				interfaceID, m.VLANID, m.Mode)
			if err != nil {
				return translateWriteErr(err, "setting vlan membership")
			}
		}
		// The interface's own row carries the audit, so its timestamp moves with
		// the change it is recording.
		if _, err := t.exec(ctx, `
			UPDATE interface SET updated_at = ?, row_version = row_version + 1
			WHERE id = ?`, at, interfaceID); err != nil {
			return translateWriteErr(err, "touching interface")
		}

		var rows []struct {
			VID  int    `db:"vid"`
			Mode string `db:"mode"`
		}
		err := t.selectAll(ctx, &rows, `
			SELECT v.vid, iv.mode
			FROM interface_vlan iv JOIN vlan v ON v.id = iv.vlan_id
			WHERE iv.interface_id = ? ORDER BY v.vid`, interfaceID)
		if err != nil {
			return fmt.Errorf("reading vlan membership: %w", err)
		}
		nowMembers := make([]vlanMembership, 0, len(rows))
		for _, r := range rows {
			nowMembers = append(nowMembers, vlanMembership{VID: r.VID, Mode: r.Mode})
		}
		updated := *iface
		updated.UpdatedAt = &at
		updated.RowVersion = iface.RowVersion + 1
		after := auditedInterfaceVLANs(&updated, nowMembers)
		return t.logUpdate(ctx, "interface", interfaceID, before, after)
	})
}

// interfaceVLANAudit is the audited shape of a port: the row plus the VLANs it
// is in.
//
// THE `db` TAGS ARE THE WHOLE POINT, and leaving them off is how this went
// wrong the first time. diffJSON compares every db-TAGGED field and ignores the
// rest, so an audit struct whose membership fields carried only json tags
// diffed nothing but the interface id -- which never changes. The port moved
// from VLAN 10 to VLAN 20 and change_log recorded silence, which is the exact
// failure the set-table rule exists to prevent, made for a fourth time.
//
// VIDs rather than row ids, and a joined string rather than a slice, both for
// the reason assetAudit gives: an audit entry is read by people, and "untagged
// 30, tagged 40,99" is a sentence where three UUIDs are a lookup exercise.
type interfaceVLANAudit struct {
	domain.Interface
	UntaggedVLAN string `db:"untagged_vlan"`
	TaggedVLANs  string `db:"tagged_vlans"`
}

func auditedInterfaceVLANs(i *domain.Interface, members []vlanMembership) *interfaceVLANAudit {
	out := &interfaceVLANAudit{Interface: *i}
	tagged := make([]string, 0, len(members))
	for _, m := range members {
		if m.Mode == domain.VLANModeUntagged {
			out.UntaggedVLAN = strconv.Itoa(m.VID)
			continue
		}
		tagged = append(tagged, strconv.Itoa(m.VID))
	}
	sort.Strings(tagged)
	out.TaggedVLANs = strings.Join(tagged, ",")
	return out
}

type vlanMembership struct {
	VID  int
	Mode string
}

func (s *SQLStore) listInterfaceVLANs(ctx context.Context, interfaceID string) ([]vlanMembership, error) {
	var rows []struct {
		VID  int    `db:"vid"`
		Mode string `db:"mode"`
	}
	err := s.read(ctx, &rows, `
		SELECT v.vid, iv.mode
		FROM interface_vlan iv JOIN vlan v ON v.id = iv.vlan_id
		WHERE iv.interface_id = ? ORDER BY v.vid`, interfaceID)
	if err != nil {
		return nil, fmt.Errorf("reading vlan membership: %w", err)
	}
	out := make([]vlanMembership, 0, len(rows))
	for _, r := range rows {
		out = append(out, vlanMembership{VID: r.VID, Mode: r.Mode})
	}
	return out, nil
}

// The backfill that turned prefix.vlan_id into VLAN rows lived here and is gone
// with the column (migration 00036). It was correct and it was temporary:
// expand, backfill, contract. Removing it is the third step, and leaving code
// that reads a column no schema has would be worse than the duplication was.

// ListPortOptions returns every port, with its box, for a "put this in the
// VLAN" picker.
//
// Unlike ListAvailableInterfaces this does NOT exclude patched ports: a cable
// and a VLAN are different facts about a port, and a switch port is normally
// both patched and in a VLAN. Excluding them would offer only the ports nobody
// has plugged anything into.
func (s *SQLStore) ListPortOptions(ctx context.Context) ([]InterfaceOption, error) {
	var opts []InterfaceOption
	err := s.read(ctx, &opts, `
		SELECT i.*, a.name AS asset_name
		FROM interface i
		JOIN asset a ON a.id = i.asset_id
		WHERE a.lifecycle <> 'retired'
		ORDER BY a.name, i.name`)
	if err != nil {
		return nil, fmt.Errorf("listing ports: %w", err)
	}
	return opts, nil
}

// AddPortToVLAN puts one port in one VLAN, keeping its other memberships.
//
// The set table is replaced wholesale, so adding one member means reading the
// port's current set and writing it back with the new one -- which is why this
// goes through SetInterfaceVLANs rather than inserting directly. Doing the
// INSERT here would skip the validation AND the audit fold, and the audit fold
// is the thing this codebase has now got wrong four times.
func (s *SQLStore) AddPortToVLAN(ctx context.Context, actor domain.Actor,
	vlanID, interfaceID, mode string) error {

	current, err := s.currentMembership(ctx, interfaceID)
	if err != nil {
		return err
	}
	for i, m := range current {
		if m.VLANID == vlanID {
			current[i].Mode = mode // already in it: this is a change of mode
			return s.SetInterfaceVLANs(ctx, actor, interfaceID, current)
		}
	}
	// An untagged VLAN replaces whatever untagged VLAN was there, because a
	// port has only one and the alternative is refusing with an error the
	// operator would fix by doing exactly this.
	if mode == domain.VLANModeUntagged {
		kept := current[:0]
		for _, m := range current {
			if m.Mode != domain.VLANModeUntagged {
				kept = append(kept, m)
			}
		}
		current = kept
	}
	current = append(current, domain.InterfaceVLAN{
		InterfaceID: interfaceID, VLANID: vlanID, Mode: mode,
	})
	return s.SetInterfaceVLANs(ctx, actor, interfaceID, current)
}

// RemovePortFromVLAN takes one port out of one VLAN, keeping the rest.
func (s *SQLStore) RemovePortFromVLAN(ctx context.Context, actor domain.Actor,
	vlanID, interfaceID string) error {

	current, err := s.currentMembership(ctx, interfaceID)
	if err != nil {
		return err
	}
	kept := make([]domain.InterfaceVLAN, 0, len(current))
	for _, m := range current {
		if m.VLANID != vlanID {
			kept = append(kept, m)
		}
	}
	if len(kept) == len(current) {
		return nil // not in it; nothing to record
	}
	return s.SetInterfaceVLANs(ctx, actor, interfaceID, kept)
}

func (s *SQLStore) currentMembership(ctx context.Context, interfaceID string) ([]domain.InterfaceVLAN, error) {
	var rows []domain.InterfaceVLAN
	err := s.read(ctx, &rows,
		`SELECT interface_id, vlan_id, mode FROM interface_vlan WHERE interface_id = ?`,
		interfaceID)
	if err != nil {
		return nil, fmt.Errorf("reading vlan membership: %w", err)
	}
	return rows, nil
}
