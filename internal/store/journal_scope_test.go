// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// journalOn builds a note about one entity.
func journalOn(t *testing.T, s *SQLStore, entityType, entityID string) *domain.JournalEntry {
	t.Helper()
	e, err := domain.NewJournalEntry(NewID(), entityType, entityID,
		domain.JournalNote, "a note", "po-11", s.Now())
	if err != nil {
		t.Fatalf("building journal entry: %v", err)
	}
	return e
}

// TestAProjectOwnerCanJournalOnAnAssetInScope is the point of classifying
// journal_entry as ScopeSubjectDerived: a note is in scope exactly when its
// subject is. Before this, journal_entry was ScopeTopology, so a project
// owner could create a server and then not write a single word about it.
func TestAProjectOwnerCanJournalOnAnAssetInScope(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			mine := mustAsset(t, s, ctx, domain.KindServer, "vm-mine", nil)

			permit := domain.ScopedPermit(
				domain.Actor{ID: "po-11", Name: "po-11", Kind: domain.ActorKindUser},
				[]string{frontend},
				domain.ScopedEntities{"asset": {mine: true}})

			note := journalOn(t, s, "asset", mine)
			if err := s.CreateJournalEntry(ctx, permit, note); err != nil {
				t.Fatalf("CreateJournalEntry on an in-scope asset = %v, want nil", err)
			}

			// And it is audited under the note's own id, once.
			changes, err := s.ListChangesForEntity(ctx, "journal_entry", note.ID, 10)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			if len(changes) != 1 {
				t.Errorf("journal create wrote %d change_log rows, want 1", len(changes))
			}
		})
	}
}

// TestAProjectOwnerCannotJournalOnAnAssetOutOfScope is the other half, and
// the one that matters: the subject is what decides, so a note is refused on
// an asset the permit does not cover even though the note itself is new and
// belongs to nobody yet.
func TestAProjectOwnerCannotJournalOnAnAssetOutOfScope(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			mine := mustAsset(t, s, ctx, domain.KindServer, "vm-mine", nil)
			theirs := mustAsset(t, s, ctx, domain.KindServer, "db-prod", nil)

			permit := domain.ScopedPermit(
				domain.Actor{ID: "po-11", Name: "po-11", Kind: domain.ActorKindUser},
				[]string{frontend},
				domain.ScopedEntities{"asset": {mine: true}})

			note := journalOn(t, s, "asset", theirs)
			if err := s.CreateJournalEntry(ctx, permit, note); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("CreateJournalEntry on an out-of-scope asset = %v, want domain.ErrForbidden", err)
			}
			changes, err := s.ListChangesForEntity(ctx, "journal_entry", note.ID, 10)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			if len(changes) != 0 {
				t.Errorf("a refused journal create wrote %d change_log rows, want 0", len(changes))
			}
		})
	}
}

// TestEditingAJournalNoteChecksTheStoredSubjectNotTheSubmittedOne is the
// seizure attempt for this path. UpdateJournalEntry takes a *domain.JournalEntry
// carrying EntityType/EntityID, and if it trusted those a project owner could
// name an asset they own while editing a note attached to one they do not.
func TestEditingAJournalNoteChecksTheStoredSubjectNotTheSubmittedOne(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			mine := mustAsset(t, s, ctx, domain.KindServer, "vm-mine", nil)
			theirs := mustAsset(t, s, ctx, domain.KindServer, "db-prod", nil)

			admin := domain.AdministratorPermit(
				domain.Actor{ID: "admin-1", Name: "admin-1", Kind: domain.ActorKindUser})
			theirNote := journalOn(t, s, "asset", theirs)
			if err := s.CreateJournalEntry(ctx, admin, theirNote); err != nil {
				t.Fatalf("seeding the out-of-scope note: %v", err)
			}

			permit := domain.ScopedPermit(
				domain.Actor{ID: "po-11", Name: "po-11", Kind: domain.ActorKindUser},
				[]string{frontend},
				domain.ScopedEntities{"asset": {mine: true}})

			// THE ATTACK: same note id, but claiming a subject the caller owns.
			forged := *theirNote
			forged.EntityID = mine
			forged.Body = "seized"
			if err := s.UpdateJournalEntry(ctx, permit, &forged); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("UpdateJournalEntry with a forged subject = %v, want domain.ErrForbidden", err)
			}

			after, err := s.GetJournalEntry(ctx, theirNote.ID)
			if err != nil {
				t.Fatalf("re-reading the note: %v", err)
			}
			if after.Body == "seized" {
				t.Error("the note's body was changed by a caller who does not own its subject")
			}
		})
	}
}
