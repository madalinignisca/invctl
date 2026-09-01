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

// Saved views. A view is one person's named set of list filters --
// internal/domain/savedview.go carries the "why a table" rationale. The
// authorization here is the point of the whole work package: saved_view
// classifies ScopeEstateConfig so no PROJECT scope reaches it, and
// authorizeSavedViewOwner below is what lets a person reach their own rows.

// authorizeSavedViewOwner is the whole of saved-view authorization.
//
// A saved view's SUBJECT is a person, so the question is not "may you write
// estate configuration" -- saved_view classifies as ScopeEstateConfig
// precisely so no project scope reaches it -- but "is this row yours".
//
// NO ADMINISTRATOR EXCEPTION, deliberately. An AdministratorPermit Covers
// everything, so it would sail through tx.log; this check is what stops it,
// and it must not grow an "unless administrator" branch. Administrators
// administer the estate, not other people's shortcuts, and no operational
// task requires reading somebody's saved filters.
//
// The narrow permit returned is what the transaction runs under: tx.log
// authorizes ("saved_view", id), and this row's id can no more be in a
// scope resolved before the request than any other freshly minted id.
// Same shape as authorizeJournalSubject in journal.go.
func authorizeSavedViewOwner(p domain.Permit, ownerID, viewID string) (domain.Permit, error) {
	actor := p.Actor()
	if actor.Kind != domain.ActorKindUser || actor.ID == "" || actor.ID != ownerID {
		return nil, fmt.Errorf("writing saved view %s: %w", viewID, domain.ErrForbidden)
	}
	return domain.ScopedPermit(actor, nil, domain.ScopedEntities{
		"saved_view": {viewID: true},
	}), nil
}

// CreateSavedView saves a new view. The submitted owner (v.UserID) is what is
// authorized here -- the row does not exist yet, so it is what the caller is
// asserting, and NewSavedView already required it non-empty.
func (s *SQLStore) CreateSavedView(ctx context.Context, p domain.Permit, v *domain.SavedView) error {
	if err := v.Validate(); err != nil {
		return err
	}
	viewPermit, err := authorizeSavedViewOwner(p, v.UserID, v.ID)
	if err != nil {
		return err
	}
	return s.write(ctx, viewPermit, func(t *tx) error {
		if _, err := t.exec(ctx, `
			INSERT INTO saved_view (id, user_id, entity, name, params, lifecycle, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			v.ID, v.UserID, v.Entity, v.Name, v.Params, v.Lifecycle, v.CreatedAt, v.UpdatedAt); err != nil {
			return translateWriteErr(err, "creating saved view")
		}
		return t.logCreate(ctx, "saved_view", v.ID, v)
	})
}

// UpdateSavedView renames a view or changes its filters. It updates name and
// params only -- never user_id or entity, so a view cannot change hands or
// switch which list it belongs to.
//
// The owner is read from the STORED row, never the submitted struct: v
// carries a UserID field, and trusting it would let anybody name themselves
// as owner and edit anybody's view. Same seizure shape as
// TestEditingAJournalNoteChecksTheStoredSubjectNotTheSubmittedOne.
func (s *SQLStore) UpdateSavedView(ctx context.Context, p domain.Permit, v *domain.SavedView) error {
	if err := v.Validate(); err != nil {
		return err
	}
	stored, err := s.GetSavedView(ctx, v.ID)
	if err != nil {
		return err
	}
	viewPermit, err := authorizeSavedViewOwner(p, stored.UserID, v.ID)
	if err != nil {
		return err
	}
	v.UpdatedAt = domain.FormatTime(s.now())
	return s.write(ctx, viewPermit, func(t *tx) error {
		res, err := t.exec(ctx, `
			UPDATE saved_view
			SET name = ?, params = ?, updated_at = ?, row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			v.Name, v.Params, v.UpdatedAt, v.ID, v.RowVersion)
		if err != nil {
			return translateWriteErr(err, "updating saved view")
		}
		if err := requireVersion(res, "saved_view", v.ID, &v.RowVersion); err != nil {
			return err
		}
		return t.logUpdate(ctx, "saved_view", v.ID, stored, v)
	})
}

// RetireSavedView soft-deletes one. Like every entity here, a saved view is
// never hard-deleted -- lifecycle moves to 'retired'.
func (s *SQLStore) RetireSavedView(ctx context.Context, p domain.Permit, id string) error {
	stored, err := s.GetSavedView(ctx, id)
	if err != nil {
		return err
	}
	viewPermit, err := authorizeSavedViewOwner(p, stored.UserID, id)
	if err != nil {
		return err
	}
	return s.write(ctx, viewPermit, func(t *tx) error {
		if stored.Lifecycle == domain.LifecycleRetired {
			return nil // already retired; not an error and not a second log row
		}
		at := domain.FormatTime(s.now())
		if _, err := t.exec(ctx, `
			UPDATE saved_view SET lifecycle = ?, updated_at = ?, row_version = row_version + 1
			WHERE id = ?`, domain.LifecycleRetired, at, id); err != nil {
			return translateWriteErr(err, "retiring saved view")
		}
		after := *stored
		after.Lifecycle = domain.LifecycleRetired
		after.UpdatedAt = at
		return t.logUpdate(ctx, "saved_view", id, stored, &after)
	})
}

// ListSavedViews returns one person's active views for a list. No permit is
// checked: everyone reads everything is this product's read model
// (docs/rbac-design.md §2), and the caller only ever asks for its own
// signed-in id anyway.
func (s *SQLStore) ListSavedViews(ctx context.Context, userID, entity string) ([]domain.SavedView, error) {
	var views []domain.SavedView
	if err := s.read(ctx, &views, `
		SELECT * FROM saved_view
		WHERE user_id = ? AND entity = ? AND lifecycle = ?
		ORDER BY name`,
		userID, entity, domain.LifecycleActive); err != nil {
		return nil, fmt.Errorf("listing saved views for %s/%s: %w", userID, entity, err)
	}
	return views, nil
}

// GetSavedView reads one, retired or not.
func (s *SQLStore) GetSavedView(ctx context.Context, id string) (*domain.SavedView, error) {
	var v domain.SavedView
	if err := s.readOne(ctx, &v, `SELECT * FROM saved_view WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting saved view %s: %w", id, err)
	}
	return &v, nil
}
