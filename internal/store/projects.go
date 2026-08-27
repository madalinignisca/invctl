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
//   - There was no projectAudit fold for owns/uses links. The set-replacement
//     rule in CLAUDE.md -- the one assetAudit and dependencyAudit exist for --
//     is about sets REPLACED WHOLESALE inside a parent's transaction, where a
//     diff on the parent struct would show nothing. Those links are added and
//     retired one at a time and each writes its own change_log row, so
//     folding them into the project's audit value would double-count every
//     change.
//
//   - Tags ARE exactly that shape, though (WP-G4a piece 2,
//     docs/tags-design.md §4): entity_tag is replaced wholesale by one
//     submission against the project's own picker, so projectAudit exists
//     now, folding only Tags -- the owns/uses reasoning above is unaffected
//     and those links still log their own rows one at a time.

// projectAudit is the audited shape of a project: the row plus the tags it
// carries, folded the way assetAudit and serviceAudit fold theirs. See
// internal/store/entitytags.go.
type projectAudit struct {
	domain.Project
	Tags string `db:"tags"`
}

func auditedProject(p *domain.Project, tags string) *projectAudit {
	return &projectAudit{Project: *p, Tags: tags}
}

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
	// Unowned narrows to team_id IS NULL -- see AssetFilter.Unowned's comment;
	// same reuse, same reasoning, for WP-G7 piece 3's bulk-assignment screen.
	Unowned bool
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
	if f.Unowned {
		where = append(where, `p.team_id IS NULL`)
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

// ProjectCircuitRow is the same for a linked circuit, carrying the provider so
// the page can say who to ring without a second lookup.
type ProjectCircuitRow struct {
	ProjectID string  `db:"project_id"`
	CircuitID string  `db:"circuit_id"`
	Relation  string  `db:"relation"`
	Note      *string `db:"note"`
	CID       string  `db:"cid"`
	Provider  string  `db:"provider"`
	Lifecycle string  `db:"lifecycle"`
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

// ProjectsForCircuits is the same for circuits.
func (s *SQLStore) ProjectsForCircuits(ctx context.Context, circuitIDs []string) (map[string][]ProjectLinkRow, error) {
	return s.projectsFor(ctx, "project_circuit", "circuit_id", circuitIDs)
}

// projectsFor is shared by the three above. The table and column names are
// literals from the three call sites, never from a request.
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
func (s *SQLStore) CreateProject(ctx context.Context, permit domain.Permit, p *domain.Project) error {
	// The row the INSERT just wrote is version 1 (the column default).
	// Without this a caller that creates and then updates the SAME struct
	// compares 0 against 1 and gets a conflict against itself.
	p.RowVersion = 1
	return s.write(ctx, permit, func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO project (id, code, name, description, team_id,
			                     priced_for_vcpu, priced_for_memory_mb,
			                     lifecycle, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ID, p.Code, p.Name, p.Description, p.TeamID,
			p.PricedForVCPU, p.PricedForMemoryMB,
			p.Lifecycle, p.CreatedAt, p.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating project")
		}
		// No tag can exist yet -- p.ID was generated for this statement --
		// so the empty fold is the true one, the same reasoning as
		// insertAsset and CreateService.
		if err := t.logCreate(ctx, "project", p.ID, auditedProject(p, "")); err != nil {
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
func (s *SQLStore) UpdateProject(ctx context.Context, permit domain.Permit, p *domain.Project) error {
	return s.write(ctx, permit, func(t *tx) error {
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
			                   priced_for_vcpu = ?, priced_for_memory_mb = ?,
			                   lifecycle = ?, updated_at = ?, row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			p.Code, p.Name, p.Description, p.TeamID,
			p.PricedForVCPU, p.PricedForMemoryMB, p.Lifecycle, p.UpdatedAt,
			p.ID, p.RowVersion)
		if err != nil {
			return translateWriteErr(err, "updating project")
		}
		if err := requireVersion(res, "project", p.ID, &p.RowVersion); err != nil {
			return err
		}
		// This method does not touch tags, so the same fold goes on both
		// sides and cancels; read rather than assumed empty so the entry
		// describes the whole project.
		tags, err := entityTagsAudit(ctx, t, domain.TagEntityProject, p.ID)
		if err != nil {
			return err
		}
		if err := t.logUpdate(ctx, "project", p.ID, auditedProject(&before, tags), auditedProject(p, tags)); err != nil {
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
func (s *SQLStore) RetireProject(ctx context.Context, p domain.Permit, id string) error {
	return s.write(ctx, p, func(t *tx) error {
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
			`{"lifecycle":["active","retired"],"reason":["","project retired"]}`, ""); err != nil {
			return err
		}
	}
	return nil
}

// LinkProjectAsset links an asset to a project, or updates an existing link.
func (s *SQLStore) LinkProjectAsset(ctx context.Context, p domain.Permit, l *domain.ProjectAssetLink) error {
	return s.writeSerializable(ctx, p, func(t *tx) error {
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
func (s *SQLStore) LinkProjectService(ctx context.Context, p domain.Permit, l *domain.ProjectServiceLink) error {
	return s.writeSerializable(ctx, p, func(t *tx) error {
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
func (s *SQLStore) RetireProjectAsset(ctx context.Context, p domain.Permit, projectID, assetID string) error {
	return s.retireLink(ctx, p, "project_asset", "asset_id", projectID, assetID)
}

// ListProjectCircuits returns a project's linked circuits, owned first.
func (s *SQLStore) ListProjectCircuits(ctx context.Context, projectID string) ([]ProjectCircuitRow, error) {
	var rows []ProjectCircuitRow
	err := s.read(ctx, &rows, `
		SELECT pc.project_id, pc.circuit_id, pc.relation, pc.note,
		       c.cid, pr.name AS provider, c.lifecycle
		FROM project_circuit pc
		JOIN circuit c ON c.id = pc.circuit_id
		JOIN provider pr ON pr.id = c.provider_id
		WHERE pc.project_id = ? AND pc.lifecycle = ? AND c.lifecycle <> ?
		ORDER BY pc.relation, c.cid, pc.circuit_id`,
		projectID, domain.LifecycleActive, domain.LifecycleRetired)
	if err != nil {
		return nil, fmt.Errorf("listing circuits of project %s: %w", projectID, err)
	}
	return rows, nil
}

// LinkProjectCircuit links a circuit to a project, or updates an existing link.
func (s *SQLStore) LinkProjectCircuit(ctx context.Context, p domain.Permit, l *domain.ProjectCircuitLink) error {
	return s.writeSerializable(ctx, p, func(t *tx) error {
		if l.Relation == domain.ProjectOwns {
			if err := t.checkOwnerFree(ctx, "project_circuit", "circuit_id", l.CircuitID, l.ProjectID); err != nil {
				return err
			}
		}
		var before domain.ProjectCircuitLink
		hadRow := true
		if err := t.get(ctx, &before,
			`SELECT * FROM project_circuit WHERE project_id = ? AND circuit_id = ?`,
			l.ProjectID, l.CircuitID); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("checking existing project link: %w", err)
			}
			hadRow = false
		}
		_, err := t.exec(ctx, `
			INSERT INTO project_circuit (project_id, circuit_id, relation, note, lifecycle, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (project_id, circuit_id) DO UPDATE SET
				relation = excluded.relation, note = excluded.note,
				lifecycle = excluded.lifecycle, updated_at = excluded.updated_at`,
			l.ProjectID, l.CircuitID, l.Relation, l.Note, l.Lifecycle, l.CreatedAt, l.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "linking circuit to project")
		}
		if hadRow {
			return t.logUpdate(ctx, "project_circuit", l.ProjectID+"/"+l.CircuitID, &before, l)
		}
		return t.logCreate(ctx, "project_circuit", l.ProjectID+"/"+l.CircuitID, l)
	})
}

// RetireProjectCircuit releases one circuit link.
func (s *SQLStore) RetireProjectCircuit(ctx context.Context, p domain.Permit, projectID, circuitID string) error {
	return s.retireLink(ctx, p, "project_circuit", "circuit_id", projectID, circuitID)
}

// RetireProjectService releases one service link.
func (s *SQLStore) RetireProjectService(ctx context.Context, p domain.Permit, projectID, serviceID string) error {
	return s.retireLink(ctx, p, "project_service", "service_id", projectID, serviceID)
}

func (s *SQLStore) retireLink(ctx context.Context, p domain.Permit, table, column, projectID, entityID string) error {
	return s.write(ctx, p, func(t *tx) error {
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
			`{"lifecycle":["active","retired"]}`, "")
	})
}
