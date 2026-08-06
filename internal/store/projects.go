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
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Projects: the business view, and the only place in this schema where a
// person asserts ownership rather than the estate implying it.
//
// Two things about the writes here are deliberate and easy to "fix" wrongly:
//
//   - The link writes use writeSerializable, not write. Each one asserts an
//     invariant it has just SELECTed -- "nobody else owns this" -- and at
//     PostgreSQL's default read-committed level two concurrent owners both see
//     an empty slot and both commit. The partial unique index still catches
//     it, but the operator would get a bare conflict instead of a sentence
//     naming the current owner, and which of the two racing writers loses
//     would be arbitrary. SQLite's single-writer pool hides this entirely,
//     which is exactly how two active cables once ended up on one port.
//
//   - There is no projectAudit fold. The set-replacement rule in CLAUDE.md --
//     the one assetAudit and dependencyAudit exist for -- is about sets
//     REPLACED WHOLESALE inside a parent's transaction, where a diff on the
//     parent struct would show nothing. These links are added and retired one
//     at a time and each writes its own change_log row, so folding them into
//     the project's audit value would double-count every change.

// ProjectRow is a project with the counts a list needs, so a page showing
// twenty projects still costs one query.
type ProjectRow struct {
	domain.Project
	OwnedAssets   int `db:"owned_assets"`
	UsedAssets    int `db:"used_assets"`
	OwnedServices int `db:"owned_services"`
	UsedServices  int `db:"used_services"`
	// Who looks after the project itself. Empty is a real answer.
	TeamCode string `db:"team_code"`
	TeamName string `db:"team_name"`
}

// ProjectFilter narrows a project list.
type ProjectFilter struct {
	TeamID string
	Query  string
	// IncludeRetired defaults false: a retired project is kept forever and is
	// noise in day-to-day lists, exactly like a retired asset.
	IncludeRetired bool
}

// ProjectAssetRow is one linked asset, with enough of the asset to render a
// row without a second query.
type ProjectAssetRow struct {
	ProjectID string  `db:"project_id"`
	AssetID   string  `db:"asset_id"`
	Relation  string  `db:"relation"`
	Note      *string `db:"note"`
	Name      string  `db:"name"`
	Kind      string  `db:"kind"`
	Lifecycle string  `db:"lifecycle"`
}

// ProjectServiceRow is the same for a linked service.
type ProjectServiceRow struct {
	ProjectID string  `db:"project_id"`
	ServiceID string  `db:"service_id"`
	Relation  string  `db:"relation"`
	Note      *string `db:"note"`
	Code      string  `db:"code"`
	Name      string  `db:"name"`
	Kind      string  `db:"kind"`
	Tier      int     `db:"tier"`
	Lifecycle string  `db:"lifecycle"`
}

// ProjectLinkRow says which project claims a thing and how. Used to put a
// pill on an asset or service page without that page knowing about projects.
type ProjectLinkRow struct {
	EntityID    string `db:"entity_id"`
	ProjectID   string `db:"project_id"`
	ProjectCode string `db:"code"`
	ProjectName string `db:"name"`
	Relation    string `db:"relation"`
}

const projectSelect = `
	SELECT p.*,
	       COALESCE(tm.code, '') AS team_code,
	       COALESCE(tm.name, '') AS team_name,
	       (SELECT COUNT(*) FROM project_asset pa
	         WHERE pa.project_id = p.id AND pa.relation = 'owns' AND pa.lifecycle = 'active') AS owned_assets,
	       (SELECT COUNT(*) FROM project_asset pa
	         WHERE pa.project_id = p.id AND pa.relation = 'uses' AND pa.lifecycle = 'active') AS used_assets,
	       (SELECT COUNT(*) FROM project_service ps
	         WHERE ps.project_id = p.id AND ps.relation = 'owns' AND ps.lifecycle = 'active') AS owned_services,
	       (SELECT COUNT(*) FROM project_service ps
	         WHERE ps.project_id = p.id AND ps.relation = 'uses' AND ps.lifecycle = 'active') AS used_services
	FROM project p
	LEFT JOIN team tm ON tm.id = p.team_id`

// ListProjects returns projects in code order.
func (s *SQLStore) ListProjects(ctx context.Context, f ProjectFilter) ([]ProjectRow, error) {
	query := projectSelect
	var args []any
	var where []string
	if !f.IncludeRetired {
		where = append(where, `p.lifecycle <> ?`)
		args = append(args, domain.LifecycleRetired)
	}
	if f.TeamID != "" {
		where = append(where, `p.team_id = ?`)
		args = append(args, f.TeamID)
	}
	if f.Query != "" {
		// LOWER on both sides, like every other filter in this codebase.
		// SQLite's LIKE is case-insensitive for ASCII and PostgreSQL's is not,
		// so without this a search for "Orders" found the project on the demo
		// and nothing at all on PostgreSQL. This was the one filter that missed
		// it; found by a database review.
		where = append(where, `(LOWER(p.code) LIKE ? ESCAPE '' OR LOWER(p.name) LIKE ? ESCAPE '')`)
		like := "%" + escapeLike(lower(f.Query)) + "%"
		args = append(args, like, like)
	}
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	// Total order: code is unique, but saying so here keeps the ordering a
	// property of the query rather than of an index that could change.
	query += ` ORDER BY p.code, p.id`

	var rows []ProjectRow
	if err := s.read(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("listing projects: %w", err)
	}
	return rows, nil
}

// GetProject loads one project by id.
func (s *SQLStore) GetProject(ctx context.Context, id string) (*ProjectRow, error) {
	var row ProjectRow
	if err := s.readOne(ctx, &row, projectSelect+` WHERE p.id = ?`, id); err != nil {
		return nil, fmt.Errorf("loading project %s: %w", id, err)
	}
	return &row, nil
}

// GetProjectByCode loads one project by its human-facing code.
func (s *SQLStore) GetProjectByCode(ctx context.Context, code string) (*ProjectRow, error) {
	var row ProjectRow
	if err := s.readOne(ctx, &row, projectSelect+` WHERE p.code = ?`, code); err != nil {
		return nil, fmt.Errorf("loading project %s: %w", code, err)
	}
	return &row, nil
}

// ListProjectAssets returns a project's linked assets, owned first.
//
// Retired assets are excluded rather than shown struck through: the link is a
// declared fact and stays in the table, but a decommissioned box is not part
// of what a project consists of today. Retiring an asset deliberately does NOT
// retire the link -- that would let one team's decommissioning silently
// rewrite another team's declared record.
func (s *SQLStore) ListProjectAssets(ctx context.Context, projectID string) ([]ProjectAssetRow, error) {
	var rows []ProjectAssetRow
	err := s.read(ctx, &rows, `
		SELECT pa.project_id, pa.asset_id, pa.relation, pa.note,
		       a.name, a.kind, a.lifecycle
		FROM project_asset pa
		JOIN asset a ON a.id = pa.asset_id
		WHERE pa.project_id = ? AND pa.lifecycle = ? AND a.lifecycle <> ?
		ORDER BY pa.relation, a.name, pa.asset_id`,
		projectID, domain.LifecycleActive, domain.LifecycleRetired)
	if err != nil {
		return nil, fmt.Errorf("listing assets of project %s: %w", projectID, err)
	}
	return rows, nil
}

// ListProjectServices returns a project's linked services, owned first.
func (s *SQLStore) ListProjectServices(ctx context.Context, projectID string) ([]ProjectServiceRow, error) {
	var rows []ProjectServiceRow
	err := s.read(ctx, &rows, `
		SELECT ps.project_id, ps.service_id, ps.relation, ps.note,
		       sv.code, sv.name, sv.kind, sv.tier, sv.lifecycle
		FROM project_service ps
		JOIN service sv ON sv.id = ps.service_id
		WHERE ps.project_id = ? AND ps.lifecycle = ? AND sv.lifecycle <> ?
		ORDER BY ps.relation, sv.code, ps.service_id`,
		projectID, domain.LifecycleActive, domain.LifecycleRetired)
	if err != nil {
		return nil, fmt.Errorf("listing services of project %s: %w", projectID, err)
	}
	return rows, nil
}

// ProjectsForAssets maps asset id to the projects claiming it, for pills on
// pages that know nothing about projects.
func (s *SQLStore) ProjectsForAssets(ctx context.Context, assetIDs []string) (map[string][]ProjectLinkRow, error) {
	return s.projectsFor(ctx, "project_asset", "asset_id", assetIDs)
}

// ProjectsForServices is the same for services.
func (s *SQLStore) ProjectsForServices(ctx context.Context, serviceIDs []string) (map[string][]ProjectLinkRow, error) {
	return s.projectsFor(ctx, "project_service", "service_id", serviceIDs)
}

// projectsFor is shared by the two above. The table and column names are
// literals from the two call sites, never from a request.
func (s *SQLStore) projectsFor(ctx context.Context, table, column string, ids []string) (map[string][]ProjectLinkRow, error) {
	if len(ids) == 0 {
		return map[string][]ProjectLinkRow{}, nil
	}
	out := make(map[string][]ProjectLinkRow, len(ids))
	for _, chunk := range chunkIDs(ids) {
		var rows []ProjectLinkRow
		err := s.read(ctx, &rows, `
			SELECT l.`+column+` AS entity_id, l.project_id, l.relation, p.code, p.name
			FROM `+table+` l
			JOIN project p ON p.id = l.project_id
			WHERE l.`+column+` IN (`+placeholders(len(chunk))+`)
			  AND l.lifecycle = ? AND p.lifecycle <> ?
			ORDER BY l.relation, p.code`,
			append(anySlice(chunk), domain.LifecycleActive, domain.LifecycleRetired)...)
		if err != nil {
			return nil, fmt.Errorf("loading project links: %w", err)
		}
		for _, r := range rows {
			out[r.EntityID] = append(out[r.EntityID], r)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Writes. Every one records a change_log row in the same transaction.

// CreateProject stores a new project.
func (s *SQLStore) CreateProject(ctx context.Context, actor domain.Actor, p *domain.Project) error {
	// The row the INSERT just wrote is version 1 (the column default).
	// Without this a caller that creates and then updates the SAME struct
	// compares 0 against 1 and gets a conflict against itself.
	p.RowVersion = 1
	return s.write(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO project (id, code, name, description, team_id, lifecycle, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ID, p.Code, p.Name, p.Description, p.TeamID, p.Lifecycle, p.CreatedAt, p.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating project")
		}
		if err := t.logCreate(ctx, "project", p.ID, p); err != nil {
			return err
		}
		return s.indexEntity(ctx, t, searchDoc{
			EntityType: "project", EntityID: p.ID,
			// Body is empty, NOT the team id. A mechanical rename of owner_team
			// to team_id put a raw UUID here, which broke search twice over:
			// the team name stopped being findable through its project, and
			// because UUIDv7 is time-sortable every project written in the same
			// millisecond shared a leading token, so FTS5 matched them all on a
			// fragment and skewed bm25 for every other document. indexAsset and
			// indexService dropped the team from their bodies in the same
			// commit; this one substituted it. Found by a database review.
			//
			// The team is its own searchable entity via indexTeam.
			Title: p.Name, Subtitle: p.Code, Body: "",
		})
	})
}

// UpdateProject stores an edit.
func (s *SQLStore) UpdateProject(ctx context.Context, actor domain.Actor, p *domain.Project) error {
	return s.write(ctx, actor, func(t *tx) error {
		var before domain.Project
		if err := t.get(ctx, &before, `SELECT * FROM project WHERE id = ?`, p.ID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("project %s: %w", p.ID, domain.ErrNotFound)
			}
			return fmt.Errorf("loading project for update: %w", err)
		}
		p.CreatedAt = before.CreatedAt
		res, err := t.exec(ctx, `
			UPDATE project SET code = ?, name = ?, description = ?, team_id = ?,
			                   lifecycle = ?, updated_at = ?, row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			p.Code, p.Name, p.Description, p.TeamID, p.Lifecycle, p.UpdatedAt,
			p.ID, p.RowVersion)
		if err != nil {
			return translateWriteErr(err, "updating project")
		}
		if err := requireVersion(res, "project", p.ID, &p.RowVersion); err != nil {
			return err
		}
		if err := t.logUpdate(ctx, "project", p.ID, &before, p); err != nil {
			return err
		}
		return s.indexEntity(ctx, t, searchDoc{
			EntityType: "project", EntityID: p.ID,
			// Body is empty, NOT the team id. A mechanical rename of owner_team
			// to team_id put a raw UUID here, which broke search twice over:
			// the team name stopped being findable through its project, and
			// because UUIDv7 is time-sortable every project written in the same
			// millisecond shared a leading token, so FTS5 matched them all on a
			// fragment and skewed bm25 for every other document. indexAsset and
			// indexService dropped the team from their bodies in the same
			// commit; this one substituted it. Found by a database review.
			//
			// The team is its own searchable entity via indexTeam.
			Title: p.Name, Subtitle: p.Code, Body: "",
		})
	})
}

// RetireProject soft-deletes a project AND releases its links.
//
// The cascade is the point. A retired project that still holds `owns` links
// keeps the owner slot forever, so nobody else can own those assets and no
// screen explains why -- the partial unique index does not care that the
// project is retired, only that the link is active. RetireAsset does the same
// thing for group memberships and attachments, and for the same reason.
func (s *SQLStore) RetireProject(ctx context.Context, actor domain.Actor, id string) error {
	return s.write(ctx, actor, func(t *tx) error {
		var before domain.Project
		if err := t.get(ctx, &before, `SELECT * FROM project WHERE id = ?`, id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("project %s: %w", id, domain.ErrNotFound)
			}
			return fmt.Errorf("loading project for retirement: %w", err)
		}
		if before.IsRetired() {
			return nil
		}

		now := domain.FormatTime(s.Now())
		if _, err := t.exec(ctx,
			`UPDATE project SET lifecycle = ?, updated_at = ?,
			                    row_version = row_version + 1 WHERE id = ?`,
			domain.LifecycleRetired, now, id); err != nil {
			return translateWriteErr(err, "retiring project")
		}
		after := before
		after.Lifecycle = domain.LifecycleRetired
		after.UpdatedAt = now
		if err := t.logUpdate(ctx, "project", id, &before, &after); err != nil {
			return err
		}

		// One audit row per released link, so "why can I own this now" has an
		// answer in the log rather than only in this function.
		if err := s.releaseLinks(ctx, t, "project_asset", "asset_id", id, now); err != nil {
			return err
		}
		return s.releaseLinks(ctx, t, "project_service", "service_id", id, now)
	})
}

func (s *SQLStore) releaseLinks(ctx context.Context, t *tx, table, column, projectID, now string) error {
	var ids []string
	if err := t.tx.SelectContext(ctx, &ids, t.rebind(
		`SELECT `+column+` FROM `+table+` WHERE project_id = ? AND lifecycle = ? ORDER BY `+column),
		projectID, domain.LifecycleActive); err != nil {
		return fmt.Errorf("listing links to release: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	if _, err := t.exec(ctx,
		`UPDATE `+table+` SET lifecycle = ?, updated_at = ? WHERE project_id = ? AND lifecycle = ?`,
		domain.LifecycleRetired, now, projectID, domain.LifecycleActive); err != nil {
		return translateWriteErr(err, "releasing project links")
	}
	for _, id := range ids {
		if err := t.log(ctx, table, projectID+"/"+id, domain.ActionRetire,
			`{"lifecycle":["active","retired"],"reason":["","project retired"]}`); err != nil {
			return err
		}
	}
	return nil
}

// LinkProjectAsset links an asset to a project, or updates an existing link.
func (s *SQLStore) LinkProjectAsset(ctx context.Context, actor domain.Actor, l *domain.ProjectAssetLink) error {
	return s.writeSerializable(ctx, actor, func(t *tx) error {
		if l.Relation == domain.ProjectOwns {
			if err := t.checkOwnerFree(ctx, "project_asset", "asset_id", l.AssetID, l.ProjectID); err != nil {
				return err
			}
		}
		var before domain.ProjectAssetLink
		hadRow := true
		if err := t.get(ctx, &before,
			`SELECT * FROM project_asset WHERE project_id = ? AND asset_id = ?`,
			l.ProjectID, l.AssetID); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("checking existing project link: %w", err)
			}
			hadRow = false
		}
		_, err := t.exec(ctx, `
			INSERT INTO project_asset (project_id, asset_id, relation, note, lifecycle, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (project_id, asset_id) DO UPDATE SET
				relation = excluded.relation, note = excluded.note,
				lifecycle = excluded.lifecycle, updated_at = excluded.updated_at`,
			l.ProjectID, l.AssetID, l.Relation, l.Note, l.Lifecycle, l.CreatedAt, l.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "linking asset to project")
		}
		if hadRow {
			return t.logUpdate(ctx, "project_asset", l.ProjectID+"/"+l.AssetID, &before, l)
		}
		return t.logCreate(ctx, "project_asset", l.ProjectID+"/"+l.AssetID, l)
	})
}

// LinkProjectService links a service to a project, or updates an existing link.
func (s *SQLStore) LinkProjectService(ctx context.Context, actor domain.Actor, l *domain.ProjectServiceLink) error {
	return s.writeSerializable(ctx, actor, func(t *tx) error {
		if l.Relation == domain.ProjectOwns {
			if err := t.checkOwnerFree(ctx, "project_service", "service_id", l.ServiceID, l.ProjectID); err != nil {
				return err
			}
		}
		var before domain.ProjectServiceLink
		hadRow := true
		if err := t.get(ctx, &before,
			`SELECT * FROM project_service WHERE project_id = ? AND service_id = ?`,
			l.ProjectID, l.ServiceID); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("checking existing project link: %w", err)
			}
			hadRow = false
		}
		_, err := t.exec(ctx, `
			INSERT INTO project_service (project_id, service_id, relation, note, lifecycle, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (project_id, service_id) DO UPDATE SET
				relation = excluded.relation, note = excluded.note,
				lifecycle = excluded.lifecycle, updated_at = excluded.updated_at`,
			l.ProjectID, l.ServiceID, l.Relation, l.Note, l.Lifecycle, l.CreatedAt, l.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "linking service to project")
		}
		if hadRow {
			return t.logUpdate(ctx, "project_service", l.ProjectID+"/"+l.ServiceID, &before, l)
		}
		return t.logCreate(ctx, "project_service", l.ProjectID+"/"+l.ServiceID, l)
	})
}

// checkOwnerFree refuses a second owner with a sentence naming the first.
//
// The partial unique index is the authority and still runs; this exists purely
// so the operator reads "already owned by platform" instead of a bare
// conflict. SQLite reports a unique violation without naming a column, so
// without this the page would say "that conflicts with something that already
// exists" and leave them to guess. Same argument as requireVocabulary.
func (t *tx) checkOwnerFree(ctx context.Context, table, column, entityID, projectID string) error {
	var owner struct {
		Code string `db:"code"`
		Name string `db:"name"`
	}
	err := t.get(ctx, &owner, `
		SELECT p.code, p.name FROM `+table+` l
		JOIN project p ON p.id = l.project_id
		WHERE l.`+column+` = ? AND l.relation = ? AND l.lifecycle = ? AND l.project_id <> ?`,
		entityID, domain.ProjectOwns, domain.LifecycleActive, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking for an existing owner: %w", err)
	}
	return fmt.Errorf("already owned by %s (%s); a thing has at most one owning project, "+
		"so retire that link first or link this project with `uses` instead: %w",
		owner.Name, owner.Code, domain.ErrConflict)
}

// RetireProjectAsset releases one asset link.
func (s *SQLStore) RetireProjectAsset(ctx context.Context, actor domain.Actor, projectID, assetID string) error {
	return s.retireLink(ctx, actor, "project_asset", "asset_id", projectID, assetID)
}

// RetireProjectService releases one service link.
func (s *SQLStore) RetireProjectService(ctx context.Context, actor domain.Actor, projectID, serviceID string) error {
	return s.retireLink(ctx, actor, "project_service", "service_id", projectID, serviceID)
}

func (s *SQLStore) retireLink(ctx context.Context, actor domain.Actor, table, column, projectID, entityID string) error {
	return s.write(ctx, actor, func(t *tx) error {
		var lifecycle string
		err := t.get(ctx, &lifecycle,
			`SELECT lifecycle FROM `+table+` WHERE project_id = ? AND `+column+` = ?`,
			projectID, entityID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("link %s/%s: %w", projectID, entityID, domain.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("loading link for retirement: %w", err)
		}
		if lifecycle == domain.LifecycleRetired {
			return nil
		}
		now := domain.FormatTime(s.Now())
		if _, err := t.exec(ctx,
			`UPDATE `+table+` SET lifecycle = ?, updated_at = ? WHERE project_id = ? AND `+column+` = ?`,
			domain.LifecycleRetired, now, projectID, entityID); err != nil {
			return translateWriteErr(err, "retiring project link")
		}
		return t.log(ctx, table, projectID+"/"+entityID, domain.ActionRetire,
			`{"lifecycle":["active","retired"]}`)
	})
}
