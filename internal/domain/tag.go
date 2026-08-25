// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Package domain: this file holds the tag itself, piece 1 of WP-G4a
// (docs/tags-design.md). A tag is created here, in a registry, before it can
// be applied to anything -- design.md §2's explicit-creation rule, the same
// shape a custom field's definition already follows. Applying a tag to an
// asset or a service (`entity_tag`) is piece 2 and lives elsewhere; nothing
// in this file knows that entities exist.
package domain

import (
	"strings"
	"time"
)

// Taggable entity types, piece 2 of WP-G4a (docs/tags-design.md §3): the
// CHECK on entity_tag.entity_type in migration 00057 limits application to
// exactly these, so a typo cannot invent a new entity kind and adding a
// fourth taggable type later is a deliberate migration, not an accident.
const (
	TagEntityAsset   = "asset"
	TagEntityService = "service"
	TagEntityProject = "project"
)

// TagEntityTypes are the permitted values of entity_tag.entity_type.
var TagEntityTypes = []string{TagEntityAsset, TagEntityService, TagEntityProject}

// Tag is a label an administrator defined, and why it exists. description is
// required for the identical reason custom_field.description is: an
// administrator who cannot say why a tag exists is the origin of the support
// call this feature is built against.
type Tag struct {
	ID          string  `db:"id"`
	Code        string  `db:"code"`
	Label       string  `db:"label"`
	Description string  `db:"description"`
	CreatedBy   string  `db:"created_by"`
	CreatedAt   string  `db:"created_at"`
	RetiredAt   *string `db:"retired_at"`
	RetiredBy   *string `db:"retired_by"`
	RowVersion  int     `db:"row_version"`
}

// validateShape checks a Tag's shape: the rules that hold at every point in
// its life, construction or a later edit alike. It normalises Code and Label
// in place.
//
// THE CODE RULES, STATED RATHER THAN LEFT IMPLICIT (docs/tags-design.md §2
// leaves them open, this is where they are decided):
//
//   - Required, trimmed, at most domain.MaxVocabularyCodeLen (64) characters
//     -- the same bound every other machine-name code in this schema obeys
//     (checkVocabulary), so a tag code is unremarkable next to a vocabulary
//     code or a custom field's.
//   - No whitespace and no control characters -- isVocabularyCode, the same
//     check a custom field's code already passes through. A tag code is a
//     machine name, not a sentence; the human-readable name is Label.
//   - LOWER-CASED before storage. This is the one rule a custom field's code
//     does NOT enforce identically at the domain layer for entity_type-scoped
//     uniqueness, and it is chosen deliberately here because it is exactly
//     the sprawl design.md §2 names: "dr", "DR" and "disaster-recovery" are
//     three different codes to the partial unique index but the first two
//     read as one tag to every human who will ever apply it. Case-folding at
//     construction, not only at query time, means the uniqueness index
//     itself is the enforcement -- two spellings differing only by case can
//     never both be live, because after normalisation they are one string.
//     An administrator who wants "DR" displayed keeps that spelling in
//     Label; Code is machine-facing and never rendered as the tag's name.
func (t *Tag) validateShape(ve *ValidationError) {
	t.Code = strings.ToLower(checkVocabulary(ve, "code", t.Code))
	t.Label = checkRequired(ve, "label", t.Label)
	t.Description = checkRequired(ve, "description", t.Description)
}

// Validate re-checks a Tag after field updates, mirroring
// domain.CustomField.Validate(): a handler that mutated the struct directly
// gets the same first line of defence a fresh NewTag call would, rather than
// leaving the database CHECK as the only backstop.
func (t *Tag) Validate() error {
	ve := &ValidationError{}
	t.validateShape(ve)
	return ve.OrNil()
}

// NewTag validates and constructs a tag. now is the clock, last parameter,
// formatted here -- the shape every constructor in this package follows, so
// the store never generates a timestamp.
func NewTag(id, code, label, description, createdBy string, now time.Time) (*Tag, error) {
	t := &Tag{
		ID:          id,
		Code:        code,
		Label:       label,
		Description: description,
		CreatedBy:   createdBy,
		CreatedAt:   FormatTime(now),
		RowVersion:  1,
	}
	ve := &ValidationError{}
	t.validateShape(ve)
	t.CreatedBy = checkRequired(ve, "created_by", t.CreatedBy)
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	return t, nil
}

// IsRetired reports whether the tag has been retired. docs/tags-design.md
// says a retired tag keeps displaying on things that already carry it
// (piece 2) and is simply not offered for new application -- the same rule
// custom_field.IsRetired already documents for a retired field.
func (t *Tag) IsRetired() bool {
	return t.RetiredAt != nil
}
