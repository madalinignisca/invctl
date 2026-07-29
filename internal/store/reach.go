package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/gabriel/invctl/internal/domain"
)

// ---------- net groups ----------

// NetGroupRow is a forwarder group with the counts the /network page shows.
type NetGroupRow struct {
	domain.NetGroup
	MemberCount int `db:"member_count"`
	UplinkCount int `db:"uplink_count"`
	AnchorCount int `db:"anchor_count"`
}

// Three correlated subqueries per row -- known and accepted for now. The
// estate this targets is tens to low hundreds of groups (docs's own estimate
// for the judgement-heavy rows), so this is not a real cost today; it is the
// kind of thing that stops being fine at a size this tool is not yet aiming
// at. Left as a flag for whoever revisits this at scale, not fixed here.
const netGroupSelect = `
	SELECT g.*,
	       (SELECT COUNT(*) FROM net_group_member m WHERE m.group_id = g.id AND m.lifecycle = 'active') AS member_count,
	       (SELECT COUNT(*) FROM net_uplink u WHERE u.group_id = g.id AND u.lifecycle = 'active') AS uplink_count,
	       (SELECT COUNT(*) FROM net_anchor a WHERE a.group_id = g.id AND a.lifecycle = 'active') AS anchor_count
	FROM net_group g`

// CreateNetGroup inserts a forwarder group. Uniqueness of code is a real
// UNIQUE constraint, so no check-then-act race exists here.
func (s *SQLStore) CreateNetGroup(ctx context.Context, actor domain.Actor, g *domain.NetGroup) error {
	if err := g.Validate(); err != nil {
		return err
	}
	return s.write(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO net_group (id, code, name, kind, role, availability, min_healthy,
			                       failover_mode, environment_id, lifecycle, source, confidence,
			                       first_seen, last_seen, verified_by, verified_at, attrs,
			                       created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			g.ID, g.Code, g.Name, g.Kind, g.Role, g.Availability, g.MinHealthy,
			g.FailoverMode, g.EnvironmentID, g.Lifecycle, g.Source, g.Confidence,
			g.FirstSeen, g.LastSeen, g.VerifiedBy, g.VerifiedAt, g.Attrs,
			g.CreatedAt, g.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating net group")
		}
		return t.logCreate(ctx, "net_group", g.ID, g)
	})
}

// GetNetGroup loads one forwarder group.
func (s *SQLStore) GetNetGroup(ctx context.Context, id string) (*domain.NetGroup, error) {
	var g domain.NetGroup
	if err := s.readOne(ctx, &g, `SELECT * FROM net_group WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting net group %s: %w", id, err)
	}
	return &g, nil
}

// ListNetGroups returns every non-retired forwarder group, most upstream role
// first, with the counts the network page shows.
func (s *SQLStore) ListNetGroups(ctx context.Context) ([]NetGroupRow, error) {
	var rows []NetGroupRow
	err := s.read(ctx, &rows, netGroupSelect+`
		WHERE g.lifecycle <> ?
		ORDER BY CASE g.role
		           WHEN 'edge' THEN 0 WHEN 'core' THEN 1
		           WHEN 'distribution' THEN 2 WHEN 'access' THEN 3 ELSE 4 END,
		         g.code`, domain.LifecycleRetired)
	if err != nil {
		return nil, fmt.Errorf("listing net groups: %w", err)
	}
	return rows, nil
}

// RetireNetGroup withdraws a forwarder group and cascades: retiring the vertex
// without cascading would strand its members forever (the partial unique
// index gives a retired-group membership no route back to any live group),
// leave active anchors pointing at a group the M3 loader will exclude, and
// leave uplinks and attachments dangling. All four cascade in the same
// transaction as the group itself.
//
// writeSerializable, the same reasoning as RetireAsset: a concurrent
// CreateNetUplink/CreateNetAttachment/CreateNetAnchor could otherwise insert a
// fresh edge against this group between the retire and the cascade queries
// below, which are themselves a read followed by a write.
func (s *SQLStore) RetireNetGroup(ctx context.Context, actor domain.Actor, id string) error {
	before, err := s.GetNetGroup(ctx, id)
	if err != nil {
		return err
	}
	if before.Lifecycle == domain.LifecycleRetired {
		return nil
	}
	at := domain.FormatTime(s.now())
	return s.writeSerializable(ctx, actor, func(t *tx) error {
		if _, err := t.exec(ctx, `UPDATE net_group SET lifecycle = ?, updated_at = ? WHERE id = ?`,
			domain.LifecycleRetired, at, id); err != nil {
			return translateWriteErr(err, "retiring net group")
		}
		diff := fmt.Sprintf(`{"lifecycle":{"old":%q,"new":%q}}`, before.Lifecycle, domain.LifecycleRetired)
		if err := t.log(ctx, "net_group", id, domain.ActionRetire, diff); err != nil {
			return err
		}
		return retireNetGroupCascade(ctx, t, id, at)
	})
}

// retireNetGroupCascade retires every row that would otherwise dangle once
// its group is gone: members, uplinks in either direction, attachments and
// anchors.
func retireNetGroupCascade(ctx context.Context, t *tx, groupID, at string) error {
	var memberAssetIDs []string
	if err := t.tx.SelectContext(ctx, &memberAssetIDs, t.rebind(
		`SELECT asset_id FROM net_group_member WHERE group_id = ? AND lifecycle = ?`),
		groupID, domain.LifecycleActive); err != nil {
		return fmt.Errorf("finding group members to retire: %w", err)
	}
	for _, assetID := range memberAssetIDs {
		if _, err := t.exec(ctx,
			`UPDATE net_group_member SET lifecycle = ?, updated_at = ? WHERE group_id = ? AND asset_id = ?`,
			domain.LifecycleRetired, at, groupID, assetID); err != nil {
			return translateWriteErr(err, "retiring net group member")
		}
		diff := fmt.Sprintf(`{"lifecycle":{"old":%q,"new":%q}}`, domain.LifecycleActive, domain.LifecycleRetired)
		if err := t.log(ctx, "net_group_member", groupID+"/"+assetID, domain.ActionRetire, diff); err != nil {
			return err
		}
	}

	uplinkIDs, err := selectIDs(ctx, t,
		`SELECT id FROM net_uplink WHERE (group_id = ? OR upstream_group_id = ?) AND lifecycle = ?`,
		groupID, groupID, domain.LifecycleActive)
	if err != nil {
		return fmt.Errorf("finding uplinks to retire: %w", err)
	}
	if err := retireRows(ctx, t, "net_uplink", uplinkIDs, at); err != nil {
		return err
	}

	attachmentIDs, err := selectIDs(ctx, t,
		`SELECT id FROM net_attachment WHERE group_id = ? AND lifecycle = ?`, groupID, domain.LifecycleActive)
	if err != nil {
		return fmt.Errorf("finding attachments to retire: %w", err)
	}
	if err := retireRows(ctx, t, "net_attachment", attachmentIDs, at); err != nil {
		return err
	}

	anchorIDs, err := selectIDs(ctx, t,
		`SELECT id FROM net_anchor WHERE group_id = ? AND lifecycle = ?`, groupID, domain.LifecycleActive)
	if err != nil {
		return fmt.Errorf("finding anchors to retire: %w", err)
	}
	return retireRows(ctx, t, "net_anchor", anchorIDs, at)
}

// selectIDs runs a query expected to return a single `id`-shaped column
// inside the transaction, for the "find rows to cascade-retire" queries that
// need a plain SELECT rather than the aggregate shape s.read expects.
func selectIDs(ctx context.Context, t *tx, query string, args ...any) ([]string, error) {
	var ids []string
	err := t.tx.SelectContext(ctx, &ids, t.rebind(query), args...)
	return ids, err
}

// retireRows retires every row named by ids in table (which must have an
// `id` primary key and a lifecycle/updated_at column), logging one
// change_log entry per row. Shared by every cascade in this file.
func retireRows(ctx context.Context, t *tx, table string, ids []string, at string) error {
	for _, id := range ids {
		if _, err := t.exec(ctx, `UPDATE `+table+` SET lifecycle = ?, updated_at = ? WHERE id = ?`,
			domain.LifecycleRetired, at, id); err != nil {
			return translateWriteErr(err, "retiring "+table)
		}
		diff := fmt.Sprintf(`{"lifecycle":{"old":%q,"new":%q}}`, domain.LifecycleActive, domain.LifecycleRetired)
		if err := t.log(ctx, table, id, domain.ActionRetire, diff); err != nil {
			return err
		}
	}
	return nil
}

// ---------- net group members ----------

// NetGroupMemberRow is a membership row with the asset's name resolved.
type NetGroupMemberRow struct {
	domain.NetGroupMember
	AssetName string `db:"asset_name"`
}

// AddNetGroupMember places an asset into a forwarder group.
//
// "A device in two forwarding groups is a modelling error, not a topology" is
// enforced by a real partial UNIQUE index (idx_net_group_member_one), not by a
// check-then-act -- the constraint itself is atomic under any isolation level,
// so a plain write() is correct here, unlike CreateNetAttachment below.
//
// The insert is an upsert on (group_id, asset_id) -- the table's PRIMARY KEY --
// because that pair can pre-exist in a RETIRED state: an RMA'd switch's
// replacement re-joining the SAME group hits the PK, not the partial index,
// and a plain INSERT would report a permanent, false "already belongs to a
// group" with no way to recover. ON CONFLICT DO UPDATE reactivates the
// existing row instead. It does NOT catch a conflict on a DIFFERENT group's
// membership (that violates idx_net_group_member_one, a different index, so
// Postgres and SQLite both fall through to the ordinary constraint error),
// which is exactly the case that must still be refused.
func (s *SQLStore) AddNetGroupMember(ctx context.Context, actor domain.Actor, m *domain.NetGroupMember) error {
	if err := m.Validate(); err != nil {
		return err
	}
	return s.write(ctx, actor, func(t *tx) error {
		var before domain.NetGroupMember
		hadRow := true
		if err := t.get(ctx, &before,
			`SELECT * FROM net_group_member WHERE group_id = ? AND asset_id = ?`, m.GroupID, m.AssetID); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("checking existing net group membership: %w", err)
			}
			hadRow = false
		}

		_, err := t.exec(ctx, `
			INSERT INTO net_group_member (group_id, asset_id, role, lifecycle, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (group_id, asset_id) DO UPDATE SET
				role = excluded.role, lifecycle = excluded.lifecycle, updated_at = excluded.updated_at`,
			m.GroupID, m.AssetID, m.Role, m.Lifecycle, m.CreatedAt, m.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "adding net group member")
		}
		if hadRow {
			return t.logUpdate(ctx, "net_group_member", m.GroupID+"/"+m.AssetID, &before, m)
		}
		return t.logCreate(ctx, "net_group_member", m.GroupID+"/"+m.AssetID, m)
	})
}

// ListNetGroupMembers returns the active membership of one group.
func (s *SQLStore) ListNetGroupMembers(ctx context.Context, groupID string) ([]NetGroupMemberRow, error) {
	var rows []NetGroupMemberRow
	err := s.read(ctx, &rows, `
		SELECT m.*, a.name AS asset_name
		FROM net_group_member m
		JOIN asset a ON a.id = m.asset_id
		WHERE m.group_id = ? AND m.lifecycle = 'active'
		ORDER BY m.role, a.name`, groupID)
	if err != nil {
		return nil, fmt.Errorf("listing members of net group %s: %w", groupID, err)
	}
	return rows, nil
}

// RetireNetGroupMember removes an asset from a forwarder group without losing
// the record that it was once a member.
func (s *SQLStore) RetireNetGroupMember(ctx context.Context, actor domain.Actor, groupID, assetID string) error {
	at := domain.FormatTime(s.now())
	return s.write(ctx, actor, func(t *tx) error {
		res, err := t.exec(ctx, `
			UPDATE net_group_member SET lifecycle = ?, updated_at = ?
			WHERE group_id = ? AND asset_id = ? AND lifecycle = ?`,
			domain.LifecycleRetired, at, groupID, assetID, domain.LifecycleActive)
		if err != nil {
			return translateWriteErr(err, "retiring net group member")
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return nil // already retired, or never a member -- a no-op, not an error
		}
		diff := fmt.Sprintf(`{"lifecycle":{"old":%q,"new":%q}}`, domain.LifecycleActive, domain.LifecycleRetired)
		return t.log(ctx, "net_group_member", groupID+"/"+assetID, domain.ActionRetire, diff)
	})
}

// ---------- net uplinks ----------

// NetUplinkRow is an uplink with both group codes resolved.
type NetUplinkRow struct {
	domain.NetUplink
	GroupCode    string `db:"group_code"`
	UpstreamCode string `db:"upstream_code"`
}

const netUplinkSelect = `
	SELECT u.*, g.code AS group_code, ug.code AS upstream_code
	FROM net_uplink u
	JOIN net_group g ON g.id = u.group_id
	JOIN net_group ug ON ug.id = u.upstream_group_id`

// CreateNetUplink declares that a group uplinks to another.
//
// Several rows from one group are ALTERNATE paths (best-of) and are allowed;
// what is not allowed is a second ACTIVE edge to the SAME upstream group on
// the SAME plane -- a group pair genuinely can have one edge per plane (a
// data uplink and a separate mgmt uplink between the same two groups), so
// plane is part of the uniqueness check, not just the group pair. This check
// is now backed by a real partial UNIQUE index
// (idx_net_uplink_one, WHERE lifecycle = 'active'), so writeSerializable is
// defence in depth rather than the only thing preventing the race -- kept for
// the same reason CreateLink keeps its own check-then-act: a friendlier,
// field-attributed error before the round trip to the database, with the
// index as the backstop that holds regardless.
func (s *SQLStore) CreateNetUplink(ctx context.Context, actor domain.Actor, u *domain.NetUplink) error {
	if err := u.Validate(); err != nil {
		return err
	}
	return s.writeSerializable(ctx, actor, func(t *tx) error {
		var n int
		err := t.get(ctx, &n, `
			SELECT COUNT(*) FROM net_uplink
			WHERE lifecycle = 'active' AND group_id = ? AND upstream_group_id = ? AND plane = ?`,
			u.GroupID, u.UpstreamGroupID, u.Plane)
		if err != nil {
			return fmt.Errorf("checking existing uplinks: %w", err)
		}
		if n > 0 {
			return fmt.Errorf("uplinking %s to %s on plane %s: an active uplink between them already exists: %w",
				u.GroupID, u.UpstreamGroupID, u.Plane, domain.ErrConflict)
		}
		_, err = t.exec(ctx, `
			INSERT INTO net_uplink (id, group_id, upstream_group_id, plane, lifecycle, source,
			                        confidence, first_seen, last_seen, verified_by, verified_at,
			                        created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			u.ID, u.GroupID, u.UpstreamGroupID, u.Plane, u.Lifecycle, u.Source,
			u.Confidence, u.FirstSeen, u.LastSeen, u.VerifiedBy, u.VerifiedAt,
			u.CreatedAt, u.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating net uplink")
		}
		return t.logCreate(ctx, "net_uplink", u.ID, u)
	})
}

// ListNetUplinks returns the active uplinks declared from a group.
func (s *SQLStore) ListNetUplinks(ctx context.Context, groupID string) ([]NetUplinkRow, error) {
	var rows []NetUplinkRow
	err := s.read(ctx, &rows, netUplinkSelect+`
		WHERE u.group_id = ? AND u.lifecycle = 'active'
		ORDER BY ug.code`, groupID)
	if err != nil {
		return nil, fmt.Errorf("listing uplinks of net group %s: %w", groupID, err)
	}
	return rows, nil
}

// GetNetUplink loads one uplink.
func (s *SQLStore) GetNetUplink(ctx context.Context, id string) (*domain.NetUplink, error) {
	var u domain.NetUplink
	if err := s.readOne(ctx, &u, `SELECT * FROM net_uplink WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting net uplink %s: %w", id, err)
	}
	return &u, nil
}

// RetireNetUplink withdraws an uplink edge.
func (s *SQLStore) RetireNetUplink(ctx context.Context, actor domain.Actor, id string) error {
	before, err := s.GetNetUplink(ctx, id)
	if err != nil {
		return err
	}
	if before.Lifecycle == domain.LifecycleRetired {
		return nil
	}
	at := domain.FormatTime(s.now())
	return s.write(ctx, actor, func(t *tx) error {
		if _, err := t.exec(ctx, `UPDATE net_uplink SET lifecycle = ?, updated_at = ? WHERE id = ?`,
			domain.LifecycleRetired, at, id); err != nil {
			return translateWriteErr(err, "retiring net uplink")
		}
		diff := fmt.Sprintf(`{"lifecycle":{"old":%q,"new":%q}}`, before.Lifecycle, domain.LifecycleRetired)
		return t.log(ctx, "net_uplink", id, domain.ActionRetire, diff)
	})
}

// ---------- net attachments ----------

// NetAttachmentRow is an attachment with the asset and group names resolved,
// plus the chassis members it is pinned to.
type NetAttachmentRow struct {
	domain.NetAttachment
	AssetName string   `db:"asset_name"`
	GroupCode string   `db:"group_code"`
	Members   []string `db:"-"`
}

const netAttachmentSelect = `
	SELECT na.*, a.name AS asset_name, g.code AS group_code
	FROM net_attachment na
	JOIN asset a ON a.id = na.asset_id
	JOIN net_group g ON g.id = na.group_id`

// CreateNetAttachment declares that a host attaches to a forwarder group on
// one plane, optionally pinned to specific chassis via members.
//
// "One active attachment per (asset, group, plane)" is now backed by a real
// partial UNIQUE index (idx_net_attach_one, WHERE lifecycle = 'active'), so
// writeSerializable is defence in depth rather than the only thing preventing
// the race -- kept for the friendlier, field-attributed error the app-level
// check below gives before the round trip, same reasoning as CreateNetUplink.
//
// Each pinned member is validated as an active net_group_member of the
// target group inside this same transaction: the migration deliberately does
// not enforce this with a composite FK (it would need group_id denormalised
// onto net_attachment_member), so Go is the only place this is ever checked.
// netAttachmentAudit folds the pinned chassis set into the audited value.
//
// The pins live in net_attachment_member, so writing them produced no diff on
// the parent struct and therefore no record at all -- the same failure as asset
// environments and dependency data classes, and the third time this shape has
// bitten. It matters more here than the count suggests: whether hv-03 is pinned
// to one chassis or two is the single fact that turns "lose sw-core-2" from a
// redundancy note into four services down, and without this the trail cannot
// answer who declared it single-homed, or when. There is no update path for
// members -- they can only be set at create -- so this entry is the only chance
// to record them.
//
// Names rather than ids would be friendlier, but resolving them needs a query
// per member inside the write transaction; the ids are at least present and
// diffable, which is the property the rule actually requires.
type netAttachmentAudit struct {
	domain.NetAttachment
	PinnedMembers string `db:"pinned_members"`
}

func auditedNetAttachment(na *domain.NetAttachment, members []domain.NetAttachmentMember) *netAttachmentAudit {
	pins := make([]string, 0, len(members))
	for _, m := range members {
		pin := m.AssetID
		if m.InterfaceID != nil && *m.InterfaceID != "" {
			pin += "@" + *m.InterfaceID
		}
		pins = append(pins, pin)
	}
	sort.Strings(pins)
	return &netAttachmentAudit{NetAttachment: *na, PinnedMembers: strings.Join(pins, ",")}
}

func (s *SQLStore) CreateNetAttachment(ctx context.Context, actor domain.Actor, na *domain.NetAttachment, members []domain.NetAttachmentMember) error {
	if err := na.Validate(); err != nil {
		return err
	}
	return s.writeSerializable(ctx, actor, func(t *tx) error {
		var assetKind string
		if err := t.get(ctx, &assetKind, `SELECT kind FROM asset WHERE id = ?`, na.AssetID); err != nil {
			return fmt.Errorf("looking up asset kind: %w", err)
		}
		// Read inside the transaction rather than from the tx: the vocabulary is
		// declared data that a concurrent writer is not racing us for, and the
		// alternative is a second requireVocabulary-shaped helper on tx for one
		// caller.
		behaviour, bErr := s.AssetKindBehaviour(ctx, assetKind)
		if bErr != nil {
			return bErr
		}
		if !behaviour.IsAttachable {
			ve := &domain.ValidationError{}
			ve.Add("asset_id", "a %s cannot have a network attachment", assetKind)
			return ve
		}

		var n int
		err := t.get(ctx, &n, `
			SELECT COUNT(*) FROM net_attachment
			WHERE lifecycle = 'active' AND asset_id = ? AND group_id = ? AND plane = ?`,
			na.AssetID, na.GroupID, na.Plane)
		if err != nil {
			return fmt.Errorf("checking existing attachments: %w", err)
		}
		if n > 0 {
			return fmt.Errorf("attaching %s to %s on plane %s: an active attachment already exists: %w",
				na.AssetID, na.GroupID, na.Plane, domain.ErrConflict)
		}

		for _, m := range members {
			var isMember int
			err := t.get(ctx, &isMember, `
				SELECT COUNT(*) FROM net_group_member
				WHERE group_id = ? AND asset_id = ? AND lifecycle = ?`,
				na.GroupID, m.AssetID, domain.LifecycleActive)
			if err != nil {
				return fmt.Errorf("checking attachment member %s: %w", m.AssetID, err)
			}
			if isMember == 0 {
				ve := &domain.ValidationError{}
				ve.Add("member_asset_id", "%s is not an active member of that group", m.AssetID)
				return ve
			}
		}

		_, err = t.exec(ctx, `
			INSERT INTO net_attachment (id, asset_id, group_id, plane, lifecycle, source, confidence,
			                            first_seen, last_seen, verified_by, verified_at,
			                            created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			na.ID, na.AssetID, na.GroupID, na.Plane, na.Lifecycle, na.Source, na.Confidence,
			na.FirstSeen, na.LastSeen, na.VerifiedBy, na.VerifiedAt, na.CreatedAt, na.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating net attachment")
		}
		for _, m := range members {
			if _, err := t.exec(ctx,
				`INSERT INTO net_attachment_member (attachment_id, asset_id, interface_id) VALUES (?, ?, ?)`,
				na.ID, m.AssetID, m.InterfaceID); err != nil {
				return translateWriteErr(err, "adding net attachment member")
			}
		}
		return t.logCreate(ctx, "net_attachment", na.ID, auditedNetAttachment(na, members))
	})
}

// ListNetAttachments returns the active attachments of one group, with each
// row's pinned chassis (if any) resolved.
func (s *SQLStore) ListNetAttachments(ctx context.Context, groupID string) ([]NetAttachmentRow, error) {
	var rows []NetAttachmentRow
	err := s.read(ctx, &rows, netAttachmentSelect+`
		WHERE na.group_id = ? AND na.lifecycle = 'active'
		ORDER BY a.name, na.plane`, groupID)
	if err != nil {
		return nil, fmt.Errorf("listing attachments of net group %s: %w", groupID, err)
	}
	return s.decorateAttachmentMembers(ctx, rows)
}

// ListNetAttachmentsForAsset returns an asset's own active attachments (not
// inherited ones), for the asset detail page.
func (s *SQLStore) ListNetAttachmentsForAsset(ctx context.Context, assetID string) ([]NetAttachmentRow, error) {
	var rows []NetAttachmentRow
	err := s.read(ctx, &rows, netAttachmentSelect+`
		WHERE na.asset_id = ? AND na.lifecycle = 'active'
		ORDER BY na.plane`, assetID)
	if err != nil {
		return nil, fmt.Errorf("listing attachments of asset %s: %w", assetID, err)
	}
	return s.decorateAttachmentMembers(ctx, rows)
}

func (s *SQLStore) decorateAttachmentMembers(ctx context.Context, rows []NetAttachmentRow) ([]NetAttachmentRow, error) {
	if len(rows) == 0 {
		return rows, nil
	}
	ids := make([]string, len(rows))
	index := make(map[string]int, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
		index[r.ID] = i
	}
	type memberName struct {
		AttachmentID string `db:"attachment_id"`
		AssetName    string `db:"asset_name"`
	}
	var members []memberName
	err := s.read(ctx, &members, `
		SELECT am.attachment_id, a.name AS asset_name
		FROM net_attachment_member am
		JOIN asset a ON a.id = am.asset_id
		WHERE am.attachment_id IN (`+placeholders(len(ids))+`)
		ORDER BY a.name`, anySlice(ids)...)
	if err != nil {
		return nil, fmt.Errorf("loading attachment members: %w", err)
	}
	for _, m := range members {
		if i, ok := index[m.AttachmentID]; ok {
			rows[i].Members = append(rows[i].Members, m.AssetName)
		}
	}
	return rows, nil
}

// GetNetAttachment loads one attachment.
func (s *SQLStore) GetNetAttachment(ctx context.Context, id string) (*domain.NetAttachment, error) {
	var na domain.NetAttachment
	if err := s.readOne(ctx, &na, `SELECT * FROM net_attachment WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting net attachment %s: %w", id, err)
	}
	return &na, nil
}

// RetireNetAttachment withdraws a host's attachment to a group.
func (s *SQLStore) RetireNetAttachment(ctx context.Context, actor domain.Actor, id string) error {
	before, err := s.GetNetAttachment(ctx, id)
	if err != nil {
		return err
	}
	if before.Lifecycle == domain.LifecycleRetired {
		return nil
	}
	at := domain.FormatTime(s.now())
	return s.write(ctx, actor, func(t *tx) error {
		if _, err := t.exec(ctx, `UPDATE net_attachment SET lifecycle = ?, updated_at = ? WHERE id = ?`,
			domain.LifecycleRetired, at, id); err != nil {
			return translateWriteErr(err, "retiring net attachment")
		}
		diff := fmt.Sprintf(`{"lifecycle":{"old":%q,"new":%q}}`, before.Lifecycle, domain.LifecycleRetired)
		return t.log(ctx, "net_attachment", id, domain.ActionRetire, diff)
	})
}

// ---------- net anchors ----------

// NetAnchorRow is an anchor with its group's code resolved.
type NetAnchorRow struct {
	domain.NetAnchor
	GroupCode string `db:"group_code"`
}

const netAnchorSelect = `
	SELECT na.*, g.code AS group_code
	FROM net_anchor na
	JOIN net_group g ON g.id = na.group_id`

// CreateNetAnchor declares where a reachability scope enters the estate.
// Uniqueness of code is a real UNIQUE constraint.
func (s *SQLStore) CreateNetAnchor(ctx context.Context, actor domain.Actor, na *domain.NetAnchor) error {
	if err := na.Validate(); err != nil {
		return err
	}
	return s.write(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO net_anchor (id, code, name, scope, group_id, environment_id, plane,
			                        lifecycle, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			na.ID, na.Code, na.Name, na.Scope, na.GroupID, na.EnvironmentID, na.Plane,
			na.Lifecycle, na.CreatedAt, na.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating net anchor")
		}
		return t.logCreate(ctx, "net_anchor", na.ID, na)
	})
}

// ListNetAnchors returns every active anchor.
func (s *SQLStore) ListNetAnchors(ctx context.Context) ([]NetAnchorRow, error) {
	var rows []NetAnchorRow
	err := s.read(ctx, &rows, netAnchorSelect+`
		WHERE na.lifecycle = 'active'
		ORDER BY na.scope, na.code`)
	if err != nil {
		return nil, fmt.Errorf("listing net anchors: %w", err)
	}
	return rows, nil
}

// GetNetAnchor loads one anchor.
func (s *SQLStore) GetNetAnchor(ctx context.Context, id string) (*domain.NetAnchor, error) {
	var na domain.NetAnchor
	if err := s.readOne(ctx, &na, `SELECT * FROM net_anchor WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting net anchor %s: %w", id, err)
	}
	return &na, nil
}

// RetireNetAnchor withdraws an anchor.
func (s *SQLStore) RetireNetAnchor(ctx context.Context, actor domain.Actor, id string) error {
	before, err := s.GetNetAnchor(ctx, id)
	if err != nil {
		return err
	}
	if before.Lifecycle == domain.LifecycleRetired {
		return nil
	}
	at := domain.FormatTime(s.now())
	return s.write(ctx, actor, func(t *tx) error {
		if _, err := t.exec(ctx, `UPDATE net_anchor SET lifecycle = ?, updated_at = ? WHERE id = ?`,
			domain.LifecycleRetired, at, id); err != nil {
			return translateWriteErr(err, "retiring net anchor")
		}
		diff := fmt.Sprintf(`{"lifecycle":{"old":%q,"new":%q}}`, before.Lifecycle, domain.LifecycleRetired)
		return t.log(ctx, "net_anchor", id, domain.ActionRetire, diff)
	})
}

// ---------- coverage ----------

// PlaneCoverage is the modelled/unmodelled split for one plane.
//
// "Modelled" means an active net_attachment on that plane exists somewhere in
// the asset's containment ancestry (itself or an ancestor) -- the closure
// query CLAUDE.md demands instead of a parent_id walk. Everything else is
// Unmodelled: absence of topology data, never evidence of disconnection
// (docs/reachability-design.md's default posture).
type PlaneCoverage struct {
	Modelled   int
	Unmodelled int
}

// NetworkCoverage is the /network page's headline number: how much of the
// estate has any declared path at all, split by plane.
//
// Splitting by plane matters because the seed fixture (and plenty of real
// estates early on) has management cabling declared and nothing on the data
// plane: a single combined "any plane" figure would report the estate as
// mostly modelled while the plane that actually drives service status has
// zero rows. TotalAttachable is scoped to domain.AttachableAssetKinds, not
// every asset: a rack, a PDU or a switch can never carry a net_attachment
// (see domain.IsAttachable), and counting them in the denominator would mean
// an operator chasing "N unmodelled" mostly finds asset kinds this feature
// was never going to cover.
type NetworkCoverage struct {
	TotalAttachable int
	Data            PlaneCoverage
	Mgmt            PlaneCoverage
	Storage         PlaneCoverage
}

// NetworkCoverage computes the per-plane modelled/unmodelled split over every
// live (non-retired), attachable asset.
//
// Total and all three modelled counts come from one statement, not four: two
// separate queries run at different points in time could see a concurrent
// insert land in between them, and a modelled count computed from a *later*
// snapshot than the total it is subtracted from can exceed that total --
// rendering a negative Unmodelled. A single statement is one snapshot on
// Postgres (a single query is consistent within itself under any isolation
// level) and trivially so on SQLite's one-connection writer/reader pair.
func (s *SQLStore) NetworkCoverage(ctx context.Context) (NetworkCoverage, error) {
	kinds := domain.AttachableAssetKinds
	query := `
		SELECT
			COUNT(*) AS total,
			COALESCE(SUM(CASE WHEN EXISTS (
				SELECT 1 FROM asset_closure c
				JOIN net_attachment na ON na.asset_id = c.ancestor_id
				  AND na.lifecycle = 'active' AND na.plane = 'data'
				WHERE c.descendant_id = a.id
			) THEN 1 ELSE 0 END), 0) AS modelled_data,
			COALESCE(SUM(CASE WHEN EXISTS (
				SELECT 1 FROM asset_closure c
				JOIN net_attachment na ON na.asset_id = c.ancestor_id
				  AND na.lifecycle = 'active' AND na.plane = 'mgmt'
				WHERE c.descendant_id = a.id
			) THEN 1 ELSE 0 END), 0) AS modelled_mgmt,
			COALESCE(SUM(CASE WHEN EXISTS (
				SELECT 1 FROM asset_closure c
				JOIN net_attachment na ON na.asset_id = c.ancestor_id
				  AND na.lifecycle = 'active' AND na.plane = 'storage'
				WHERE c.descendant_id = a.id
			) THEN 1 ELSE 0 END), 0) AS modelled_storage
		FROM asset a
		WHERE a.lifecycle <> ? AND a.kind IN (` + placeholders(len(kinds)) + `)`

	args := append([]any{domain.LifecycleRetired}, anySlice(kinds)...)
	var row struct {
		Total           int `db:"total"`
		ModelledData    int `db:"modelled_data"`
		ModelledMgmt    int `db:"modelled_mgmt"`
		ModelledStorage int `db:"modelled_storage"`
	}
	if err := s.db.Reader.GetContext(ctx, &row, s.db.Reader.Rebind(query), args...); err != nil {
		return NetworkCoverage{}, fmt.Errorf("computing network coverage: %w", err)
	}
	return NetworkCoverage{
		TotalAttachable: row.Total,
		Data:            PlaneCoverage{Modelled: row.ModelledData, Unmodelled: row.Total - row.ModelledData},
		Mgmt:            PlaneCoverage{Modelled: row.ModelledMgmt, Unmodelled: row.Total - row.ModelledMgmt},
		Storage:         PlaneCoverage{Modelled: row.ModelledStorage, Unmodelled: row.Total - row.ModelledStorage},
	}, nil
}

// ---------- derivation (propose-only, M2) ----------
//
// POST /network/derive turns `link` rows into proposed attachments and
// uplinks for a human to review. It never writes anything: `link` is evidence,
// not authority (docs/reachability-design.md), so there is no write path here
// to accidentally take, and no risk of clobbering a declared row.

// DerivedAttachment is one proposed host-to-group attachment.
type DerivedAttachment struct {
	AssetID     string
	AssetName   string
	GroupID     string
	GroupCode   string
	Plane       string
	InterfaceID string
}

// DerivedUplink is one proposed group-to-group uplink.
type DerivedUplink struct {
	GroupID         string
	GroupCode       string
	UpstreamGroupID string
	UpstreamCode    string
	Plane           string
}

// DerivationProposal is the result of running derivation once.
type DerivationProposal struct {
	Attachments []DerivedAttachment
	Uplinks     []DerivedUplink
	// SkippedAmbiguous counts forwarder-to-forwarder cables between two groups
	// of equal role rank, where direction cannot be derived from role alone --
	// e.g. a core-to-core peer link. Reported, not guessed.
	SkippedAmbiguous int
}

// DeriveNetworkProposal reads active, enabled-port links and proposes
// attachments and uplinks for review. It orients a forwarder-to-forwarder
// cable by net_group.role rank (edge > core > distribution > access) and sets
// an attachment's plane from interface.is_mgmt on the host side of the cable.
// A proposal matching a row that already exists (of any source) is skipped,
// so re-running derivation is idempotent and a declared row is never
// shadowed, let alone overwritten.
//
// Unbounded for now: this loads every active link in one query with no
// paging and no cap. Known and accepted at the estate size this milestone
// targets (thousands of rows, not millions); flagged rather than fixed here
// for whoever revisits this once the cable plant is large enough to matter.
func (s *SQLStore) DeriveNetworkProposal(ctx context.Context) (*DerivationProposal, error) {
	type linkEnd struct {
		AInterfaceID string `db:"a_interface_id"`
		BInterfaceID string `db:"b_interface_id"`
		AAssetID     string `db:"a_asset_id"`
		BAssetID     string `db:"b_asset_id"`
		AAssetName   string `db:"a_asset_name"`
		BAssetName   string `db:"b_asset_name"`
		AEnabled     bool   `db:"a_enabled"`
		BEnabled     bool   `db:"b_enabled"`
		AIsMgmt      bool   `db:"a_is_mgmt"`
		BIsMgmt      bool   `db:"b_is_mgmt"`
	}
	var links []linkEnd
	err := s.read(ctx, &links, `
		SELECT ai.id AS a_interface_id, bi.id AS b_interface_id,
		       ai.asset_id AS a_asset_id, bi.asset_id AS b_asset_id,
		       aa.name AS a_asset_name, ba.name AS b_asset_name,
		       ai.enabled AS a_enabled, bi.enabled AS b_enabled,
		       ai.is_mgmt AS a_is_mgmt, bi.is_mgmt AS b_is_mgmt
		FROM link l
		JOIN interface ai ON ai.id = l.a_interface_id
		JOIN interface bi ON bi.id = l.b_interface_id
		JOIN asset aa ON aa.id = ai.asset_id
		JOIN asset ba ON ba.id = bi.asset_id
		WHERE l.lifecycle = ?`, domain.LifecycleActive)
	if err != nil {
		return nil, fmt.Errorf("loading links for derivation: %w", err)
	}

	type memberInfo struct {
		AssetID   string `db:"asset_id"`
		GroupID   string `db:"group_id"`
		GroupCode string `db:"code"`
		Role      string `db:"role"`
	}
	var members []memberInfo
	err = s.read(ctx, &members, `
		SELECT m.asset_id, m.group_id, g.code, g.role
		FROM net_group_member m
		JOIN net_group g ON g.id = m.group_id
		WHERE m.lifecycle = ? AND g.lifecycle <> ?`, domain.LifecycleActive, domain.LifecycleRetired)
	if err != nil {
		return nil, fmt.Errorf("loading net group members for derivation: %w", err)
	}
	byAsset := make(map[string]memberInfo, len(members))
	for _, m := range members {
		byAsset[m.AssetID] = m
	}

	type attachKey struct{ AssetID, GroupID, Plane string }
	existingAttach := map[attachKey]bool{}
	var attachRows []struct {
		AssetID string `db:"asset_id"`
		GroupID string `db:"group_id"`
		Plane   string `db:"plane"`
	}
	if err := s.read(ctx, &attachRows,
		`SELECT asset_id, group_id, plane FROM net_attachment WHERE lifecycle = ?`, domain.LifecycleActive); err != nil {
		return nil, fmt.Errorf("loading existing attachments for derivation: %w", err)
	}
	for _, r := range attachRows {
		existingAttach[attachKey{r.AssetID, r.GroupID, r.Plane}] = true
	}

	type uplinkKey struct{ GroupID, UpstreamGroupID, Plane string }
	existingUplink := map[uplinkKey]bool{}
	var uplinkRows []struct {
		GroupID         string `db:"group_id"`
		UpstreamGroupID string `db:"upstream_group_id"`
		Plane           string `db:"plane"`
	}
	if err := s.read(ctx, &uplinkRows,
		`SELECT group_id, upstream_group_id, plane FROM net_uplink WHERE lifecycle = ?`, domain.LifecycleActive); err != nil {
		return nil, fmt.Errorf("loading existing uplinks for derivation: %w", err)
	}
	for _, r := range uplinkRows {
		existingUplink[uplinkKey{r.GroupID, r.UpstreamGroupID, r.Plane}] = true
	}

	proposal := &DerivationProposal{}
	seenAttach := map[attachKey]bool{}
	seenUplink := map[uplinkKey]bool{}

	addAttachment := func(fwd memberInfo, hostAssetID, hostAssetName string, hostIsMgmt bool, hostInterfaceID string) {
		plane := domain.PlaneData
		if hostIsMgmt {
			plane = domain.PlaneMgmt
		}
		key := attachKey{hostAssetID, fwd.GroupID, plane}
		if existingAttach[key] || seenAttach[key] {
			return
		}
		seenAttach[key] = true
		proposal.Attachments = append(proposal.Attachments, DerivedAttachment{
			AssetID: hostAssetID, AssetName: hostAssetName,
			GroupID: fwd.GroupID, GroupCode: fwd.GroupCode, Plane: plane,
			InterfaceID: hostInterfaceID,
		})
	}

	for _, l := range links {
		if !l.AEnabled || !l.BEnabled {
			continue // skip disabled ports
		}
		if l.AAssetID == l.BAssetID {
			continue // a cable between two ports of the same asset proposes nothing
		}
		aMember, aOK := byAsset[l.AAssetID]
		bMember, bOK := byAsset[l.BAssetID]

		switch {
		case aOK && bOK:
			if aMember.GroupID == bMember.GroupID {
				continue // an intra-group peer link, not a group-to-group uplink
			}
			rankA, rankB := domain.NetRoleRank(aMember.Role), domain.NetRoleRank(bMember.Role)
			var downstream, upstream memberInfo
			switch {
			case rankA > rankB:
				downstream, upstream = aMember, bMember
			case rankB > rankA:
				downstream, upstream = bMember, aMember
			default:
				// Equal rank: direction cannot be derived from role alone.
				proposal.SkippedAmbiguous++
				continue
			}
			// Same rule as an attachment's plane, just checked on both ends:
			// there is no single "host side" for a forwarder-to-forwarder
			// cable, so either interface flagged is_mgmt marks the whole link
			// as management-plane. Getting this wrong is exactly the sw-oob-1
			// trap the design calls out -- an out-of-band cable proposed as a
			// data-plane uplink would join the management switch into the
			// data-plane graph.
			plane := domain.PlaneData
			if l.AIsMgmt || l.BIsMgmt {
				plane = domain.PlaneMgmt
			}
			key := uplinkKey{downstream.GroupID, upstream.GroupID, plane}
			if existingUplink[key] || seenUplink[key] {
				continue
			}
			seenUplink[key] = true
			proposal.Uplinks = append(proposal.Uplinks, DerivedUplink{
				GroupID: downstream.GroupID, GroupCode: downstream.GroupCode,
				UpstreamGroupID: upstream.GroupID, UpstreamCode: upstream.GroupCode,
				Plane: plane,
			})
		case aOK && !bOK:
			addAttachment(aMember, l.BAssetID, l.BAssetName, l.BIsMgmt, l.BInterfaceID)
		case bOK && !aOK:
			addAttachment(bMember, l.AAssetID, l.AAssetName, l.AIsMgmt, l.AInterfaceID)
		default:
			// Neither end is a declared forwarder; nothing to propose.
		}
	}

	sort.Slice(proposal.Attachments, func(i, j int) bool {
		if proposal.Attachments[i].AssetName != proposal.Attachments[j].AssetName {
			return proposal.Attachments[i].AssetName < proposal.Attachments[j].AssetName
		}
		return proposal.Attachments[i].GroupCode < proposal.Attachments[j].GroupCode
	})
	sort.Slice(proposal.Uplinks, func(i, j int) bool {
		if proposal.Uplinks[i].GroupCode != proposal.Uplinks[j].GroupCode {
			return proposal.Uplinks[i].GroupCode < proposal.Uplinks[j].GroupCode
		}
		return proposal.Uplinks[i].UpstreamCode < proposal.Uplinks[j].UpstreamCode
	})
	return proposal, nil
}
