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

	"github.com/madalinignisca/invctl/internal/domain"
)

// user_project (WP-G1 piece 3, task 11) says which projects a person is
// assigned to. Nothing consults it yet -- Authorizer.Permit is Task 12, the
// gate flip is Task 13. See migration 00059 for why this table gets its own
// id and row_version rather than the composite-key, single-row-toggled shape
// project_asset and its neighbours use: an assignment's history (granted,
// released, later re-granted) is worth keeping as distinct rows.

// userProjectRow is the audited shape of one assignment. db-tagged so
// snapshotJSON/diffJSON can walk it the way every other audited write does.
type userProjectRow struct {
	ID         string `db:"id"`
	UserID     string `db:"user_id"`
	ProjectID  string `db:"project_id"`
	Lifecycle  string `db:"lifecycle"`
	CreatedAt  string `db:"created_at"`
	UpdatedAt  string `db:"updated_at"`
	RowVersion int    `db:"row_version"`
}

// AssignProject grants userID scope over projectID.
//
// Idempotent: assigning somebody who already holds an active assignment to
// the same project writes nothing further, the same "no-op writes no
// change_log row" rule logUpdate already applies elsewhere in this package --
// there is simply no second row to insert. Re-assigning after a release
// inserts a NEW row rather than reactivating the retired one, which is what
// the partial unique index on (user_id, project_id) WHERE lifecycle =
// 'active' exists to allow; see migration 00059.
//
// Not writeSerializable: the partial unique index itself is what prevents two
// concurrent grants from producing two active rows for the same pair -- the
// SELECT here is a courtesy that avoids a pointless duplicate-row error in
// the ordinary case, not an invariant this transaction must observe
// atomically the way checkOwnerFree's cross-column check must.
func (s *SQLStore) AssignProject(ctx context.Context, p domain.Permit, userID, projectID string) error {
	return s.write(ctx, p, func(t *tx) error {
		var existing string
		err := t.get(ctx, &existing,
			`SELECT id FROM user_project WHERE user_id = ? AND project_id = ? AND lifecycle = ?`,
			userID, projectID, domain.LifecycleActive)
		if err == nil {
			// Already assigned -- nothing to do, nothing to log.
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("checking existing assignment: %w", err)
		}

		now := domain.FormatTime(s.Now())
		row := userProjectRow{
			ID:         NewID(),
			UserID:     userID,
			ProjectID:  projectID,
			Lifecycle:  domain.LifecycleActive,
			CreatedAt:  now,
			UpdatedAt:  now,
			RowVersion: 1,
		}
		_, err = t.exec(ctx, `
			INSERT INTO user_project (id, user_id, project_id, lifecycle, created_at, updated_at, row_version)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			row.ID, row.UserID, row.ProjectID, row.Lifecycle, row.CreatedAt, row.UpdatedAt, row.RowVersion)
		if err != nil {
			return translateWriteErr(err, "assigning project")
		}
		return t.logCreate(ctx, "user_project", userID+"/"+projectID, &row)
	})
}

// ReleaseProject retires the active assignment, if there is one. It never
// deletes the row -- soft delete, as everywhere in this schema. Releasing an
// assignment that is not active (already released, or never made) is a
// no-op: nothing to retire, nothing to log.
func (s *SQLStore) ReleaseProject(ctx context.Context, p domain.Permit, userID, projectID string) error {
	return s.write(ctx, p, func(t *tx) error {
		var id string
		err := t.get(ctx, &id,
			`SELECT id FROM user_project WHERE user_id = ? AND project_id = ? AND lifecycle = ?`,
			userID, projectID, domain.LifecycleActive)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("loading assignment to release: %w", err)
		}

		now := domain.FormatTime(s.Now())
		if _, err := t.exec(ctx,
			`UPDATE user_project SET lifecycle = ?, updated_at = ?, row_version = row_version + 1 WHERE id = ?`,
			domain.LifecycleRetired, now, id); err != nil {
			return translateWriteErr(err, "releasing project assignment")
		}
		return t.log(ctx, "user_project", userID+"/"+projectID, domain.ActionRetire,
			`{"lifecycle":["active","retired"]}`, "")
	})
}

// ProjectsForUser returns the ids of projects userID currently holds scope
// over: an active assignment to a project that is itself not retired.
//
// BOTH HALVES MATTER. Excluding a retired assignment is the obvious one.
// Excluding a retired PROJECT is not decorative: a retired project's own
// lifecycle change never touches user_project (retiring a project retires
// its asset/service/circuit links, see RetireProject's releaseLinks calls,
// but an assignment is a fact about a person, not about the project's
// estate), so without the p.lifecycle <> 'retired' clause here a retired
// project would keep granting scope to whoever was once assigned to it,
// indefinitely, with nothing in the UI to show it. See
// TestProjectsForUserExcludesRetiredAssignmentsAndRetiredProjects.
func (s *SQLStore) ProjectsForUser(ctx context.Context, userID string) ([]string, error) {
	var ids []string
	err := s.read(ctx, &ids, `
		SELECT up.project_id
		FROM user_project up
		JOIN project p ON p.id = up.project_id
		WHERE up.user_id = ? AND up.lifecycle = ? AND p.lifecycle <> ?
		ORDER BY up.project_id`,
		userID, domain.LifecycleActive, domain.LifecycleRetired)
	if err != nil {
		return nil, fmt.Errorf("loading projects for user %s: %w", userID, err)
	}
	return ids, nil
}
