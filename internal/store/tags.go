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

// Tags: the registry, piece 1 of WP-G4a (docs/tags-design.md). Applying a
// tag to an entity (`entity_tag`) is piece 2 and has no store methods here --
// nothing in this file knows an asset or a service exists.

// TagRow is a tag plus what the registry needs without a query per row: who
// defined and retired it, resolved to a display name the way change_log.actor
// and CustomFieldRow already are.
type TagRow struct {
	domain.Tag
	CreatedByName string  `db:"created_by_name"`
	RetiredByName *string `db:"retired_by_name"`
}

// tagSelect resolves both attribution columns the way customFieldSelect
// resolves created_by/retired_by: a LEFT JOIN so a scrubbed account still
// leaves the row readable, just without a name to show for it.
const tagSelect = `
	SELECT t.*,
	       COALESCE(cu.display_name, cu.username) AS created_by_name,
	       COALESCE(ru.display_name, ru.username) AS retired_by_name
	FROM tag t
	LEFT JOIN app_user cu ON cu.id = t.created_by
	LEFT JOIN app_user ru ON ru.id = t.retired_by`

// GetTag loads one tag.
func (s *SQLStore) GetTag(ctx context.Context, id string) (*TagRow, error) {
	var row TagRow
	if err := s.readOne(ctx, &row, tagSelect+` WHERE t.id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting tag %s: %w", id, err)
	}
	return &row, nil
}

// ListTags lists every tag, live only unless includeRetired is set. The
// registry is the only caller expected to ask for both.
func (s *SQLStore) ListTags(ctx context.Context, includeRetired bool) ([]TagRow, error) {
	query := tagSelect
	if !includeRetired {
		query += ` WHERE t.retired_at IS NULL`
	}
	query += ` ORDER BY t.code`

	var rows []TagRow
	if err := s.read(ctx, &rows, query); err != nil {
		return nil, fmt.Errorf("listing tags: %w", err)
	}
	return rows, nil
}

// CreateTag inserts a tag. Validation of the shape -- a non-empty code,
// label and description -- is the domain constructor's job (NewTag); the
// CHECK constraints in migration 00056 are the second line of defence, not
// the first.
func (s *SQLStore) CreateTag(ctx context.Context, actor domain.Actor, t *domain.Tag) error {
	t.RowVersion = 1
	return s.write(ctx, domain.AdministratorPermit(actor), func(tx *tx) error {
		_, err := tx.exec(ctx, `
			INSERT INTO tag (id, code, label, description, created_by, created_at, row_version)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			t.ID, t.Code, t.Label, t.Description, t.CreatedBy, t.CreatedAt, t.RowVersion)
		if err != nil {
			return translateWriteErr(err, "creating tag")
		}
		return tx.logCreate(ctx, "tag", t.ID, t)
	})
}

// UpdateTag persists tag changes -- code, label and description all stay
// editable forever, including a tag's code (docs/tags-design.md §4: a
// rename is a correction somebody makes deliberately, and piece 2's fold
// folds a tag's stable id rather than its code precisely so this rename
// never rewrites every entity carrying it).
func (s *SQLStore) UpdateTag(ctx context.Context, actor domain.Actor, t *domain.Tag) error {
	before, err := s.GetTag(ctx, t.ID)
	if err != nil {
		return err
	}
	if err := t.Validate(); err != nil {
		return err
	}
	// Attribution and retirement are not this method's business -- Retire
	// and Restore own those columns.
	t.CreatedAt = before.CreatedAt
	t.CreatedBy = before.CreatedBy
	t.RetiredAt = before.RetiredAt
	t.RetiredBy = before.RetiredBy

	return s.write(ctx, domain.AdministratorPermit(actor), func(tx *tx) error {
		res, err := tx.exec(ctx, `
			UPDATE tag SET code = ?, label = ?, description = ?, row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			t.Code, t.Label, t.Description, t.ID, t.RowVersion)
		if err != nil {
			return translateWriteErr(err, "updating tag")
		}
		if err := requireVersion(res, "tag", t.ID, &t.RowVersion); err != nil {
			return err
		}
		return tx.logUpdate(ctx, "tag", t.ID, &before.Tag, t)
	})
}

// RetireTag soft-deletes a tag. A tag already retired is left alone rather
// than refused, the way RetireCustomField treats a second retirement.
func (s *SQLStore) RetireTag(ctx context.Context, actor domain.Actor, id string) error {
	before, err := s.GetTag(ctx, id)
	if err != nil {
		return err
	}
	if before.RetiredAt != nil {
		return nil
	}
	at := domain.FormatTime(s.now())
	return s.write(ctx, domain.AdministratorPermit(actor), func(tx *tx) error {
		res, err := tx.exec(ctx, `
			UPDATE tag SET retired_at = ?, retired_by = ?, row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			at, actor.ID, id, before.RowVersion)
		if err != nil {
			return translateWriteErr(err, "retiring tag")
		}
		v := before.RowVersion
		if err := requireVersion(res, "tag", id, &v); err != nil {
			return err
		}
		diff := fmt.Sprintf(`{"retired_at":{"old":null,"new":%q},"retired_by":{"old":null,"new":%q}}`,
			at, actor.ID)
		return tx.log(ctx, "tag", id, domain.ActionRetire, diff, "")
	})
}

// RestoreTag clears retirement. Refused while a live tag holds the same
// code -- the partial unique index freed that code for reuse the moment
// this tag was retired, and restoring the original would produce two live
// tags with one code.
func (s *SQLStore) RestoreTag(ctx context.Context, actor domain.Actor, id string) error {
	before, err := s.GetTag(ctx, id)
	if err != nil {
		return err
	}
	if before.RetiredAt == nil {
		return nil
	}
	n, err := s.countOne(ctx, `
		SELECT COUNT(*) FROM tag WHERE code = ? AND retired_at IS NULL AND id <> ?`,
		before.Code, id)
	if err != nil {
		return fmt.Errorf("checking for a live tag holding code %s: %w", before.Code, err)
	}
	if n > 0 {
		return fmt.Errorf("restoring tag: a live tag already holds the code %q: %w",
			before.Code, domain.ErrConflict)
	}

	return s.write(ctx, domain.AdministratorPermit(actor), func(tx *tx) error {
		res, err := tx.exec(ctx, `
			UPDATE tag SET retired_at = NULL, retired_by = NULL, row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			id, before.RowVersion)
		if err != nil {
			return translateWriteErr(err, "restoring tag")
		}
		v := before.RowVersion
		if err := requireVersion(res, "tag", id, &v); err != nil {
			return err
		}
		retiredAt := ""
		if before.RetiredAt != nil {
			retiredAt = *before.RetiredAt
		}
		// "restore" is not in the change_log.action vocabulary
		// (00009_change_log_constraints.sql); logged as the update it is,
		// the same way RestoreCustomField logs its own restore.
		diff := fmt.Sprintf(`{"retired_at":{"old":%q,"new":null},"retired_by":{"old":%q,"new":null}}`,
			retiredAt, actorOrEmpty(before.RetiredBy))
		return tx.log(ctx, "tag", id, domain.ActionUpdate, diff, "")
	})
}
