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

// FHRPGroupRow is a group with its virtual addresses and member count.
type FHRPGroupRow struct {
	domain.FHRPGroup
	MemberCount int `db:"member_count"`
	// VIPs is joined in Go, not by the query -- see ListFHRPGroups.
	VIPs string
}

// Redundancy is what this group's membership means for survivability.
func (r FHRPGroupRow) Redundancy() domain.FHRPRedundancy {
	return domain.Redundancy(r.MemberCount)
}

// RedundancyNote is the sentence for the state.
func (r FHRPGroupRow) RedundancyNote() string {
	return domain.RedundancyDescription(r.Redundancy())
}

// ListFHRPGroups returns every live group.
//
// TWO QUERIES AND A JOIN IN GO, not an aggregate. GROUP_CONCAT is SQLite,
// string_agg is PostgreSQL, and there is no portable spelling -- the first
// version of this used the former, passed every test, and would have failed on
// PostgreSQL the moment anybody opened the page. The portability guard now
// names both, because it did not catch that.
func (s *SQLStore) ListFHRPGroups(ctx context.Context) ([]FHRPGroupRow, error) {
	var rows []FHRPGroupRow
	err := s.read(ctx, &rows, `
		SELECT g.*,
		       (SELECT COUNT(*) FROM fhrp_member m WHERE m.group_id = g.id) AS member_count
		FROM fhrp_group g
		WHERE g.lifecycle <> 'retired'
		ORDER BY g.name`)
	if err != nil {
		return nil, fmt.Errorf("listing fhrp groups: %w", err)
	}
	if len(rows) == 0 {
		return rows, nil
	}

	var vips []struct {
		GroupID string `db:"fhrp_group_id"`
		Addr    string `db:"addr_text"`
	}
	err = s.read(ctx, &vips, `
		SELECT fhrp_group_id, addr_text FROM ip_address
		WHERE fhrp_group_id IS NOT NULL ORDER BY addr_text`)
	if err != nil {
		return nil, fmt.Errorf("listing virtual addresses: %w", err)
	}
	byGroup := map[string][]string{}
	for _, v := range vips {
		byGroup[v.GroupID] = append(byGroup[v.GroupID], v.Addr)
	}
	for i := range rows {
		rows[i].VIPs = strings.Join(byGroup[rows[i].ID], ", ")
	}
	return rows, nil
}

// GetFHRPGroup loads one group.
func (s *SQLStore) GetFHRPGroup(ctx context.Context, id string) (*domain.FHRPGroup, error) {
	var g domain.FHRPGroup
	if err := s.readOne(ctx, &g, `SELECT * FROM fhrp_group WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting fhrp group %s: %w", id, err)
	}
	return &g, nil
}

// CreateFHRPGroup declares a group.
func (s *SQLStore) CreateFHRPGroup(ctx context.Context, p domain.Permit, g *domain.FHRPGroup) error {
	if err := g.Validate(); err != nil {
		return err
	}
	g.RowVersion = 1
	at := domain.FormatTime(s.now())
	g.CreatedAt, g.UpdatedAt = &at, &at
	return s.write(ctx, p, func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO fhrp_group (id, protocol, group_number, name, description,
			                        lifecycle, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			g.ID, g.Protocol, g.GroupNumber, g.Name, g.Description,
			g.Lifecycle, g.CreatedAt, g.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating fhrp group")
		}
		if err := t.logCreate(ctx, "fhrp_group", g.ID, g); err != nil {
			return err
		}
		return s.indexEntity(ctx, t, searchDoc{
			EntityType: "fhrp_group", EntityID: g.ID,
			Title:    g.Name,
			Subtitle: fmt.Sprintf("%s group %d", domain.FHRPProtocolLabel(g.Protocol), g.GroupNumber),
			Body:     fmt.Sprintf("%s %d %s", g.Protocol, g.GroupNumber, g.Name),
		})
	})
}

// RetireFHRPGroup withdraws a group, refusing while a VIP still names it.
//
// A retired group holding a live address is a row saying "this does not exist"
// beside one saying "and here is the gateway it answers for". Soft delete makes
// that contradiction permanent rather than merely wrong.
func (s *SQLStore) RetireFHRPGroup(ctx context.Context, p domain.Permit, id string) error {
	before, err := s.GetFHRPGroup(ctx, id)
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
		var vips int
		if err := t.get(ctx, &vips,
			`SELECT COUNT(*) FROM ip_address WHERE fhrp_group_id = ?`, id); err != nil {
			return fmt.Errorf("counting the addresses on group %s: %w", id, err)
		}
		if vips > 0 {
			return fmt.Errorf("%w: %d virtual address(es) still answer through this group",
				domain.ErrConflict, vips)
		}
		res, err := t.exec(ctx, `
			UPDATE fhrp_group SET lifecycle = 'retired', updated_at = ?,
			                      row_version = row_version + 1
			WHERE id = ? AND row_version = ?`, at, id, before.RowVersion)
		if err != nil {
			return translateWriteErr(err, "retiring fhrp group")
		}
		if err := requireVersion(res, "fhrp_group", id, &before.RowVersion); err != nil {
			return err
		}
		return t.logUpdate(ctx, "fhrp_group", id, before, &after)
	})
}

// ---------- membership ----------

// FHRPMemberRow is one router in a group, with the box it is.
type FHRPMemberRow struct {
	InterfaceID   string `db:"interface_id"`
	InterfaceName string `db:"interface_name"`
	AssetID       string `db:"asset_id"`
	AssetName     string `db:"asset_name"`
	Priority      *int   `db:"priority"`
}

// ListFHRPMembers returns the routers in a group, keenest first.
func (s *SQLStore) ListFHRPMembers(ctx context.Context, groupID string) ([]FHRPMemberRow, error) {
	var rows []FHRPMemberRow
	err := s.read(ctx, &rows, `
		SELECT m.interface_id, i.name AS interface_name, m.priority,
		       a.id AS asset_id, a.name AS asset_name
		FROM fhrp_member m
		JOIN interface i ON i.id = m.interface_id
		JOIN asset a ON a.id = i.asset_id
		WHERE m.group_id = ?
		ORDER BY m.priority DESC, a.name, i.name`, groupID)
	if err != nil {
		return nil, fmt.Errorf("listing members of group %s: %w", groupID, err)
	}
	return rows, nil
}

// SetFHRPMembers replaces a group's whole membership.
//
// The fifth set table in this schema, and the rule has not changed: replaced
// wholesale inside the parent's transaction, folded into the parent's audited
// value so the replacement cannot produce an empty diff. A router leaving a
// redundancy group is precisely the change somebody needs to find afterwards.
func (s *SQLStore) SetFHRPMembers(ctx context.Context, p domain.Permit,
	groupID string, members []domain.FHRPMember) error {

	if err := domain.ValidateFHRPMembers(members); err != nil {
		return err
	}
	group, err := s.GetFHRPGroup(ctx, groupID)
	if err != nil {
		return err
	}
	current, err := s.ListFHRPMembers(ctx, groupID)
	if err != nil {
		return err
	}
	before := auditedFHRPGroup(group, current)
	at := domain.FormatTime(s.now())

	return s.write(ctx, p, func(t *tx) error {
		if _, err := t.exec(ctx,
			`DELETE FROM fhrp_member WHERE group_id = ?`, groupID); err != nil {
			return translateWriteErr(err, "clearing fhrp membership")
		}
		for _, m := range members {
			_, err := t.exec(ctx, `
				INSERT INTO fhrp_member (group_id, interface_id, priority) VALUES (?, ?, ?)`,
				groupID, m.InterfaceID, m.Priority)
			if err != nil {
				return translateWriteErr(err, "setting fhrp membership")
			}
		}
		if _, err := t.exec(ctx, `
			UPDATE fhrp_group SET updated_at = ?, row_version = row_version + 1
			WHERE id = ?`, at, groupID); err != nil {
			return translateWriteErr(err, "touching fhrp group")
		}

		var rows []FHRPMemberRow
		err := t.selectAll(ctx, &rows, `
			SELECT m.interface_id, i.name AS interface_name, m.priority,
			       a.id AS asset_id, a.name AS asset_name
			FROM fhrp_member m
			JOIN interface i ON i.id = m.interface_id
			JOIN asset a ON a.id = i.asset_id
			WHERE m.group_id = ?
			ORDER BY a.name, i.name`, groupID)
		if err != nil {
			return fmt.Errorf("reading fhrp membership: %w", err)
		}
		updated := *group
		updated.UpdatedAt = &at
		updated.RowVersion = group.RowVersion + 1
		return t.logUpdate(ctx, "fhrp_group", groupID, before, auditedFHRPGroup(&updated, rows))
	})
}

// fhrpGroupAudit is the audited shape: the group plus who is in it.
//
// The `db` tag is what makes the fold work -- diffJSON compares db-tagged
// fields and ignores everything else, which is how the VLAN membership diff
// came out empty the first time. Names rather than ids, because an audit entry
// is read by people.
type fhrpGroupAudit struct {
	domain.FHRPGroup
	Members string `db:"members"`
}

func auditedFHRPGroup(g *domain.FHRPGroup, members []FHRPMemberRow) *fhrpGroupAudit {
	parts := make([]string, 0, len(members))
	for _, m := range members {
		label := m.AssetName + "/" + m.InterfaceName
		if m.Priority != nil {
			label += "@" + strconv.Itoa(*m.Priority)
		}
		parts = append(parts, label)
	}
	sort.Strings(parts)
	return &fhrpGroupAudit{FHRPGroup: *g, Members: strings.Join(parts, ",")}
}

// AssignVIP points a virtual address at a group.
//
// The address is an ordinary ip_address row, so it already lands in its prefix,
// counts towards utilisation and is excluded by the allocator. All this does is
// say which group answers for it -- and clear the interface, because an address
// answered for by a group is not held by one port.
func (s *SQLStore) AssignVIP(ctx context.Context, p domain.Permit, addressID, groupID string) error {
	before, err := s.GetIPAddress(ctx, addressID)
	if err != nil {
		return err
	}
	at := domain.FormatTime(s.now())
	after := *before
	after.FHRPGroupID = &groupID
	after.InterfaceID = nil
	after.UpdatedAt = &at

	return s.write(ctx, p, func(t *tx) error {
		res, err := t.exec(ctx, `
			UPDATE ip_address SET fhrp_group_id = ?, interface_id = NULL, updated_at = ?,
			                      row_version = row_version + 1
			WHERE id = ? AND row_version = ?`, groupID, at, addressID, before.RowVersion)
		if err != nil {
			return translateWriteErr(err, "assigning virtual address")
		}
		if err := requireVersion(res, "ip_address", addressID, &after.RowVersion); err != nil {
			return err
		}
		return t.logUpdate(ctx, "ip_address", addressID, before, &after)
	})
}
