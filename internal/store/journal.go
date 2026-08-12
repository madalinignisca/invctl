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

// Journal entries. The rules are in domain/journal.go; this persists them.
//
// A NOTE IS DECLARED STATE, so every mutation here writes a change_log row in
// the same transaction like any other. That reads oddly at first -- an audit
// entry about somebody writing a note -- and it is right: the note's CONTENT is
// what a person said, and the fact that it was written, edited or withdrawn is
// a change to the estate's records like any other. Withdrawing an
// inconvenient note without trace is exactly what the audit trail exists to
// prevent.

// JournalRow is an entry with its author resolved for display.
type JournalRow struct {
	domain.JournalEntry
	// AuthorName is the display name joined from app_user, empty when the
	// account has been scrubbed. The note keeps its integrity and simply stops
	// resolving to a person, which is the erasure story the opaque id buys.
	AuthorName string `db:"author_name"`
}

// DisplayAuthor is what a template renders.
func (r JournalRow) DisplayAuthor() string {
	if r.AuthorName != "" {
		return r.AuthorName
	}
	return r.Author
}

// KindLabel names the entry kind for a reader.
func (r JournalRow) KindLabel() string {
	if l, ok := domain.JournalKindLabels[r.Kind]; ok {
		return l
	}
	return r.Kind
}

const journalSelect = `
	SELECT j.*, COALESCE(u.display_name, u.username, '') AS author_name
	FROM journal_entry j
	LEFT JOIN app_user u ON u.id = j.author`

// ListJournal returns the active notes on one entity, newest first.
func (s *SQLStore) ListJournal(ctx context.Context, entityType, entityID string) ([]JournalRow, error) {
	var rows []JournalRow
	if err := s.read(ctx, &rows, journalSelect+`
		WHERE j.entity_type = ? AND j.entity_id = ? AND j.lifecycle = ?
		ORDER BY j.created_at DESC, j.id DESC`,
		entityType, entityID, domain.LifecycleActive); err != nil {
		return nil, fmt.Errorf("listing journal for %s %s: %w", entityType, entityID, err)
	}
	return rows, nil
}

// GetJournalEntry reads one, retired or not.
func (s *SQLStore) GetJournalEntry(ctx context.Context, id string) (*JournalRow, error) {
	var row JournalRow
	if err := s.readOne(ctx, &row, journalSelect+` WHERE j.id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting journal entry %s: %w", id, err)
	}
	return &row, nil
}

// CreateJournalEntry writes a note.
func (s *SQLStore) CreateJournalEntry(ctx context.Context, actor domain.Actor, e *domain.JournalEntry) error {
	if err := e.Validate(); err != nil {
		return err
	}
	e.RowVersion = 1
	return s.write(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO journal_entry (id, entity_type, entity_id, kind, body, author,
			                           lifecycle, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.ID, e.EntityType, e.EntityID, e.Kind, e.Body, e.Author,
			e.Lifecycle, e.CreatedAt, e.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating journal entry")
		}
		return t.logCreate(ctx, "journal_entry", e.ID, e)
	})
}

// UpdateJournalEntry corrects one.
//
// EDITABLE RATHER THAN APPEND-ONLY, which is a deliberate departure from
// change_log and not an oversight. A note is a person's own words and people
// mistype; making them write a second note to fix a typo would fill the
// timeline with corrections nobody wants to read. The edit is audited, so the
// previous wording is recoverable from change_log -- which is the property that
// makes editing safe rather than the ability to edit being the risk.
func (s *SQLStore) UpdateJournalEntry(ctx context.Context, actor domain.Actor, e *domain.JournalEntry) error {
	if err := e.Validate(); err != nil {
		return err
	}
	e.UpdatedAt = domain.FormatTime(s.now())
	return s.write(ctx, actor, func(t *tx) error {
		before, err := getJournalForUpdate(ctx, t, e.ID)
		if err != nil {
			return err
		}
		res, err := t.exec(ctx, `
			UPDATE journal_entry
			SET kind = ?, body = ?, updated_at = ?, row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			e.Kind, e.Body, e.UpdatedAt, e.ID, e.RowVersion)
		if err != nil {
			return translateWriteErr(err, "updating journal entry")
		}
		if err := requireVersion(res, "journal_entry", e.ID, &e.RowVersion); err != nil {
			return err
		}
		return t.logUpdate(ctx, "journal_entry", e.ID, before, e)
	})
}

// RetireJournalEntry withdraws one.
//
// SOFT, like everything else here. A withdrawn note is still a thing that was
// said, and the change_log row recording the withdrawal refers to an entry that
// has to still exist for the trail to mean anything.
func (s *SQLStore) RetireJournalEntry(ctx context.Context, actor domain.Actor, id string) error {
	return s.write(ctx, actor, func(t *tx) error {
		before, err := getJournalForUpdate(ctx, t, id)
		if err != nil {
			return err
		}
		if before.Lifecycle == domain.LifecycleRetired {
			return nil // already withdrawn; not an error and not a second log row
		}
		at := domain.FormatTime(s.now())
		if _, err := t.exec(ctx, `
			UPDATE journal_entry SET lifecycle = ?, updated_at = ?, row_version = row_version + 1
			WHERE id = ?`, domain.LifecycleRetired, at, id); err != nil {
			return translateWriteErr(err, "retiring journal entry")
		}
		after := *before
		after.Lifecycle = domain.LifecycleRetired
		after.UpdatedAt = at
		return t.logUpdate(ctx, "journal_entry", id, before, &after)
	})
}

func getJournalForUpdate(ctx context.Context, t *tx, id string) (*domain.JournalEntry, error) {
	var e domain.JournalEntry
	if err := t.get(ctx, &e, `SELECT * FROM journal_entry WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("reading journal entry %s: %w", id, err)
	}
	return &e, nil
}

// CountJournal reports how many active notes an entity carries, for a panel
// heading that does not have to load them.
func (s *SQLStore) CountJournal(ctx context.Context, entityType, entityID string) (int, error) {
	var n int
	if err := s.readOne(ctx, &n, `
		SELECT COUNT(*) FROM journal_entry
		WHERE entity_type = ? AND entity_id = ? AND lifecycle = ?`,
		entityType, entityID, domain.LifecycleActive); err != nil {
		return 0, fmt.Errorf("counting journal for %s %s: %w", entityType, entityID, err)
	}
	return n, nil
}
