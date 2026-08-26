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
	"sort"
	"strings"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Applying tags to entities, piece 2 of WP-G4a (docs/tags-design.md §3, §4,
// §4a). Piece 1 (tags.go) built the tag itself and its registry; nothing
// there knows an entity exists. This file is the other half: what an asset,
// service or project carries, and the audit obligation that comes with it.
//
// TAGS ARE NOT INHERITED (design.md §4b). asset_closure exists and makes
// "tag the datacentre, everything inside inherits it" askable; the answer is
// no, and nothing here walks the closure. A tag row names exactly the entity
// somebody applied it to.
//
// THE FOLD IS THE HAZARD, so it gets the comment. entity_tag is a child
// table the same shape as asset_environment and custom_field_value: applying
// or removing a tag leaves every column of the asset/service/project
// untouched, so without folding the set into that entity's audited value the
// change would produce no diff and therefore no change_log entry at all --
// the failure this codebase has been bitten by four separate times. The set
// folds as SORTED STABLE IDS, never codes (design.md §4's amendment): a tag's
// code stays editable forever (see tag.go/UpdateTag), and folding the code
// would mean a rename rewrites this fold for every entity carrying the tag,
// logging a spurious "tags" diff on the next unrelated save of each --
// exactly the hazard docs/custom-fields-design.md §7 documents for field
// codes. Resolve ids to codes for DISPLAY only (see EntityTagsFor).

// EntityTagsFor returns every tag one entity carries, live and retired
// together, ordered by code. A retired tag keeps displaying on the entities
// that already carry it (docs/tags-design.md §2, mirroring the identical
// rule for a retired custom-field option) -- this is that rule made literal:
// nothing here filters on tag.retired_at.
func (s *SQLStore) EntityTagsFor(ctx context.Context, entityType, entityID string) ([]domain.Tag, error) {
	var tags []domain.Tag
	err := s.read(ctx, &tags, `
		SELECT t.* FROM entity_tag et
		JOIN tag t ON t.id = et.tag_id
		WHERE et.entity_type = ? AND et.entity_id = ?
		ORDER BY t.code`, entityType, entityID)
	if err != nil {
		return nil, fmt.Errorf("listing tags of %s %s: %w", entityType, entityID, err)
	}
	return tags, nil
}

// entityTagIDs reads the raw tag ids one entity carries INSIDE t, unordered
// -- callers that need a stable order sort what they get back, and the fold
// below is the only caller that cares.
func entityTagIDs(ctx context.Context, t *tx, entityType, entityID string) ([]string, error) {
	var ids []string
	if err := t.selectAll(ctx, &ids,
		`SELECT tag_id FROM entity_tag WHERE entity_type = ? AND entity_id = ?`,
		entityType, entityID); err != nil {
		return nil, fmt.Errorf("reading tag ids of %s %s: %w", entityType, entityID, err)
	}
	return ids, nil
}

// foldEntityTags renders a tag-id set as "id,id", sorted so that applying the
// same set in a different order is never reported as a change --
// TestReorderingTagsIsNotAChange is the test docs/tags-design.md §4 names
// against exactly this line.
func foldEntityTags(ids []string) string {
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}

// entityTagsAudit renders an entity's CURRENT tag set as the string that
// folds into assetAudit / serviceAudit / projectAudit. Empty when it carries
// none, which is a real answer and diffs correctly against a first tag being
// applied.
func entityTagsAudit(ctx context.Context, t *tx, entityType, entityID string) (string, error) {
	ids, err := entityTagIDs(ctx, t, entityType, entityID)
	if err != nil {
		return "", err
	}
	return foldEntityTags(ids), nil
}

// SetEntityTags applies one submission of a tag set to an asset, service or
// project, and audits the result against that entity.
//
// IT REPLACES THE WHOLE SET, unlike SetCustomValues. A tag picker is a
// checkbox list of every live tag plus whatever retired ones this entity
// already carries (docs/tags-design.md §4a's "same shape as the
// custom-field editor beside it" refers to the WORKFLOW, not the wire
// format): the operator sees the complete set and submits the complete set,
// the same contract asset_environment's editor already uses. tagIDs is
// therefore the DESIRED set, not a delta.
//
// A newly applied tag id must name a tag that exists and is not retired --
// design.md §2 and §4a: a retired tag is not offered for new application.
// An id already carried by this entity is never re-validated against that
// rule, so a retired tag an entity already holds survives an unrelated
// resubmission of the same set -- the identical "an unchanged value is not a
// new value" reasoning setCustomValues gives for a retired select option.
func (s *SQLStore) SetEntityTags(ctx context.Context, actor domain.Actor, entityType, entityID string, expected int, tagIDs []string) error {
	return s.setEntityTags(ctx, actor, entityType, entityID, expected, tagIDs, "")
}

// setEntityTags is SetEntityTags plus a batch id, so that WP-G4a piece 3's
// bulk apply (bulk_tags.go) can thread one batch id across many entities'
// change_log rows without a second, near-duplicate mutation path. batchID is
// "" for every ordinary caller -- SetEntityTags -- and non-empty only from
// bulk_tags.go, exactly the way BulkAssignOwnership shares
// assignOneEntity/requireActiveTeam with ReassignTeamOwnership rather than
// inventing a second write path.
func (s *SQLStore) setEntityTags(ctx context.Context, actor domain.Actor, entityType, entityID string, expected int, tagIDs []string, batchID string) error {
	switch entityType {
	case domain.TagEntityAsset, domain.TagEntityService, domain.TagEntityProject:
	default:
		return fmt.Errorf("setting tags: %q is not a taggable entity type: %w", entityType, domain.ErrInvalid)
	}

	desired := map[string]bool{}
	for _, id := range tagIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			desired[id] = true
		}
	}

	return s.write(ctx, domain.AdministratorPermit(actor), func(t *tx) error {
		existingIDs, err := entityTagIDs(ctx, t, entityType, entityID)
		if err != nil {
			return err
		}
		before := foldEntityTags(existingIDs)
		existing := make(map[string]bool, len(existingIDs))
		for _, id := range existingIDs {
			existing[id] = true
		}

		var added, removed []string
		for id := range desired {
			if !existing[id] {
				added = append(added, id)
			}
		}
		for id := range existing {
			if !desired[id] {
				removed = append(removed, id)
			}
		}
		sort.Strings(added)
		sort.Strings(removed)

		if len(added) > 0 {
			var candidates []domain.Tag
			if err := t.selectAll(ctx, &candidates,
				`SELECT * FROM tag WHERE id IN (`+placeholders(len(added))+`)`,
				anySlice(added)...); err != nil {
				return fmt.Errorf("reading tags to apply to %s %s: %w", entityType, entityID, err)
			}
			byID := make(map[string]domain.Tag, len(candidates))
			for _, tg := range candidates {
				byID[tg.ID] = tg
			}
			for _, id := range added {
				tg, ok := byID[id]
				if !ok {
					// A stale form naming a tag retired-and-then-deleted -- not
					// reachable since tags are soft-delete-only, so in practice
					// this is a hand-built request naming an id that never
					// existed. 404, never an upsert: ON CONFLICT here would turn
					// a narrow write into an inventory-write vector, the same
					// ruling setCustomValues already applies to an unknown
					// field id.
					return fmt.Errorf("applying tag %s to %s %s: no such tag: %w",
						id, entityType, entityID, domain.ErrNotFound)
				}
				if tg.IsRetired() {
					return fmt.Errorf(
						"applying tag %s (%s) to %s %s: the tag is retired and takes no new application: %w",
						id, tg.Code, entityType, entityID, domain.ErrInvalid)
				}
			}
		}

		if len(removed) > 0 {
			args := append([]any{entityType, entityID}, anySlice(removed)...)
			if _, err := t.exec(ctx,
				`DELETE FROM entity_tag WHERE entity_type = ? AND entity_id = ? AND tag_id IN (`+
					placeholders(len(removed))+`)`, args...); err != nil {
				return translateWriteErr(err, "removing tags")
			}
		}
		for _, id := range added {
			if _, err := t.exec(ctx, `
				INSERT INTO entity_tag (tag_id, entity_type, entity_id, created_at, created_by)
				VALUES (?, ?, ?, ?, ?)`,
				id, entityType, entityID, t.at, actor.ID); err != nil {
				return translateWriteErr(err, "applying tag")
			}
		}

		afterIDs := make([]string, 0, len(desired))
		for id := range desired {
			afterIDs = append(afterIDs, id)
		}
		after := foldEntityTags(afterIDs)

		switch entityType {
		case domain.TagEntityAsset:
			return logAssetTags(ctx, t, entityID, expected, before, after, batchID)
		case domain.TagEntityService:
			return logServiceTags(ctx, t, entityID, expected, before, after, batchID)
		default:
			return logProjectTags(ctx, t, entityID, expected, before, after, batchID)
		}
	})
}

// logAssetTags writes the asset's audit entry for a tag-set replacement. The
// asset row, its environments and its custom values are read inside the
// transaction and used on BOTH sides of the diff, so they cancel and the
// entry names the one thing that actually moved -- the same shape as
// logAssetCustomValues in customvalues.go, mirrored rather than shared
// because each reads a different set fresh and folds a different one from
// its own before/after.
func logAssetTags(ctx context.Context, t *tx, assetID string, expected int, before, after, batchID string) error {
	var a domain.Asset
	if err := t.get(ctx, &a, `SELECT * FROM asset WHERE id = ?`, assetID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("setting tags: asset %s: %w", assetID, domain.ErrNotFound)
		}
		return fmt.Errorf("reading asset %s for its audit entry: %w", assetID, err)
	}
	var codes []string
	if err := t.selectAll(ctx, &codes, `
		SELECT e.code FROM asset_environment ae
		JOIN environment e ON e.id = ae.environment_id
		WHERE ae.asset_id = ?`, assetID); err != nil {
		return fmt.Errorf("reading environments of asset %s for its audit entry: %w", assetID, err)
	}
	custom, err := customFieldsAudit(ctx, t, domain.CustomFieldEntityAsset, assetID)
	if err != nil {
		return err
	}
	res, err := t.exec(ctx,
		`UPDATE asset SET row_version = row_version + 1, updated_at = ?
		 WHERE id = ? AND row_version = ?`, t.at, assetID, expected)
	if err := bumpedParentVersion(res, err, "asset", assetID, expected); err != nil {
		return err
	}
	return t.logUpdateBatch(ctx, "asset", assetID,
		auditedAsset(&a, codes, custom, before), auditedAsset(&a, codes, custom, after), batchID)
}

// logServiceTags is the service half of the same thing.
func logServiceTags(ctx context.Context, t *tx, serviceID string, expected int, before, after, batchID string) error {
	var svc domain.Service
	if err := t.get(ctx, &svc, `SELECT * FROM service WHERE id = ?`, serviceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("setting tags: service %s: %w", serviceID, domain.ErrNotFound)
		}
		return fmt.Errorf("reading service %s for its audit entry: %w", serviceID, err)
	}
	custom, err := customFieldsAudit(ctx, t, domain.CustomFieldEntityService, serviceID)
	if err != nil {
		return err
	}
	res, err := t.exec(ctx,
		`UPDATE service SET row_version = row_version + 1, updated_at = ?
		 WHERE id = ? AND row_version = ?`, t.at, serviceID, expected)
	if err := bumpedParentVersion(res, err, "service", serviceID, expected); err != nil {
		return err
	}
	return t.logUpdateBatch(ctx, "service", serviceID,
		auditedService(&svc, custom, before), auditedService(&svc, custom, after), batchID)
}

// logProjectTags is the project half. Projects hold no custom field values
// (domain.CustomFieldEntityTypes does not include "project"), so there is no
// second fold to read fresh here -- Tags is the only thing projectAudit
// carries beyond the row itself.
func logProjectTags(ctx context.Context, t *tx, projectID string, expected int, before, after, batchID string) error {
	var p domain.Project
	if err := t.get(ctx, &p, `SELECT * FROM project WHERE id = ?`, projectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("setting tags: project %s: %w", projectID, domain.ErrNotFound)
		}
		return fmt.Errorf("reading project %s for its audit entry: %w", projectID, err)
	}
	res, err := t.exec(ctx,
		`UPDATE project SET row_version = row_version + 1, updated_at = ?
		 WHERE id = ? AND row_version = ?`, t.at, projectID, expected)
	if err := bumpedParentVersion(res, err, "project", projectID, expected); err != nil {
		return err
	}
	return t.logUpdateBatch(ctx, "project", projectID,
		auditedProject(&p, before), auditedProject(&p, after), batchID)
}

// EntityTagOrphan is one entity_tag row whose entity_id names nothing that
// exists -- the risk docs/tags-design.md §3 accepts by going polymorphic
// with no foreign key, and compensates for with this check rather than
// pretending it cannot happen. Not reachable through this package's own
// writes (SetEntityTags 404s before writing when an entity does not exist,
// and no entity is ever hard-deleted), so a non-empty result here points at
// a hand-written fix, a bad import, or a migration defect -- exactly what
// design.md §3 names.
type EntityTagOrphan struct {
	TagID      string `db:"tag_id"`
	EntityType string `db:"entity_type"`
	EntityID   string `db:"entity_id"`
}

// TagIntegrityViolations finds every entity_tag row pointing at nothing.
// Cheap (one UNION ALL of three anti-joins, each against a primary key) and
// meant to be run occasionally rather than on every request -- the missing
// foreign key this compensates for would have cost nothing at write time
// either.
func (s *SQLStore) TagIntegrityViolations(ctx context.Context) ([]EntityTagOrphan, error) {
	var rows []EntityTagOrphan
	err := s.read(ctx, &rows, `
		SELECT et.tag_id, et.entity_type, et.entity_id
		FROM entity_tag et
		WHERE (et.entity_type = 'asset'
		       AND NOT EXISTS (SELECT 1 FROM asset a WHERE a.id = et.entity_id))
		   OR (et.entity_type = 'service'
		       AND NOT EXISTS (SELECT 1 FROM service s WHERE s.id = et.entity_id))
		   OR (et.entity_type = 'project'
		       AND NOT EXISTS (SELECT 1 FROM project p WHERE p.id = et.entity_id))
		ORDER BY et.entity_type, et.entity_id, et.tag_id`)
	if err != nil {
		return nil, fmt.Errorf("checking entity_tag integrity: %w", err)
	}
	return rows, nil
}
