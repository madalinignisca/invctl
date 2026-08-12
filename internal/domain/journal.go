// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import "time"

// Journal entries: what a person knows that no column has a place for.
//
// NOT THE AUDIT TRAIL. change_log is what the software did, written by the code
// in the same transaction as the change. This is what somebody chose to say.
// Both land on one timeline and every row says which it is.

// Journal entry kinds.
const (
	// JournalNote is context somebody wanted recorded.
	JournalNote = "note"
	// JournalIncident is what happened, usually written while it was happening.
	JournalIncident = "incident"
	// JournalMaintenance is planned work, before or after.
	JournalMaintenance = "maintenance"
	// JournalDecision is a choice and its reason -- the kind that rots worst
	// when it goes untold, because the next person sees only the outcome.
	JournalDecision = "decision"
)

// JournalKinds is the Go side of the CHECK in migration 00039.
var JournalKinds = []string{JournalNote, JournalIncident, JournalMaintenance, JournalDecision}

// MaxJournalBody bounds one entry.
//
// A fence rather than an opinion about length: this is a note, not a document,
// and a megabyte in the column is a paste accident. Generous enough that nobody
// writing in good faith meets it.
const MaxJournalBody = 8000

// JournalEntry is one note against one entity.
type JournalEntry struct {
	ID         string `db:"id"`
	EntityType string `db:"entity_type"`
	EntityID   string `db:"entity_id"`
	Kind       string `db:"kind"`
	Body       string `db:"body"`
	// Author is an app_user.id and NEVER a name. Same rule as
	// change_log.actor: a CMDB kept forever must carry nothing anybody could
	// ask to have erased, so the UI joins to a display name and scrubbing the
	// user row stops it resolving without touching the note.
	Author     string `db:"author"`
	Lifecycle  string `db:"lifecycle"`
	CreatedAt  string `db:"created_at"`
	UpdatedAt  string `db:"updated_at"`
	RowVersion int    `db:"row_version"`
}

// NewJournalEntry validates and constructs.
func NewJournalEntry(id, entityType, entityID, kind, body, author string, now time.Time) (*JournalEntry, error) {
	ts := FormatTime(now)
	e := &JournalEntry{
		ID: id, EntityType: entityType, EntityID: entityID,
		Kind: kind, Body: body, Author: author,
		Lifecycle: LifecycleActive,
		CreatedAt: ts, UpdatedAt: ts,
	}
	if e.Kind == "" {
		e.Kind = JournalNote
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return e, nil
}

// Validate checks an entry against its rules.
func (e *JournalEntry) Validate() error {
	ve := &ValidationError{}
	e.EntityType = checkRequired(ve, "entity_type", e.EntityType)
	e.EntityID = checkRequired(ve, "entity_id", e.EntityID)
	e.Body = checkRequired(ve, "body", e.Body)
	// The author is set by the handler from the credential, never read from a
	// request payload -- so an empty one is a programming error rather than
	// something an operator can cause, and it is refused rather than defaulted.
	e.Author = checkRequired(ve, "author", e.Author)
	if len(e.Body) > MaxJournalBody {
		ve.Add("body", "is longer than %d characters; a note is not a document", MaxJournalBody)
	}
	checkEnum(ve, "kind", e.Kind, JournalKinds)
	checkEnum(ve, "lifecycle", e.Lifecycle, []string{LifecycleActive, LifecycleRetired})
	return ve.OrNil()
}

// IsRetired reports whether the note has been withdrawn.
func (e *JournalEntry) IsRetired() bool { return e.Lifecycle == LifecycleRetired }

// JournalKindLabels are what the UI shows. Beside the constants so a new kind
// cannot be added without somebody deciding what to call it.
var JournalKindLabels = map[string]string{
	JournalNote:        "note",
	JournalIncident:    "incident",
	JournalMaintenance: "maintenance",
	JournalDecision:    "decision",
}
