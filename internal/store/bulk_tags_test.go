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

// WP-G4a piece 3: bulk application of a tag from a filtered asset or
// service list (docs/tags-design.md §4a, §5). Built on entityTagFixture,
// the same one piece 2 already uses, extended with a second asset per test
// where a fixture needs to prove selection is exact.

// changesFor returns every change_log row for one entity, newest first --
// helper for the batch-id assertions below.
func (f *entityTagFixture) changesFor(t *testing.T, entityType, entityID string) []domain.ChangeLog {
	t.Helper()
	rows, err := f.s.ListChangesForEntity(f.ctx, entityType, entityID, 50)
	if err != nil {
		t.Fatalf("listing changes for %s %s: %v", entityType, entityID, err)
	}
	return rows
}

// TestApplyTagToSelectionTagsExactlyTheNamedEntities is the submission
// contract, the same one BulkAssignOwnership is held to: only the ids
// passed are touched, and this is what proves it, not an assertion that a
// SEPARATE query would still find the untouched one -- an asset created
// with the SAME kind and environment, simply never named in the selection.
//
// PROVED TO BE ABLE TO FAIL: this is also the "select-all ignores the
// active filter" mutation the delivery brief asks for. Changing
// applyTagToOne's loop in ApplyTagToSelection to range over
// f.s.ListAssets(ctx, AssetFilter{}) instead of the passed-in selections --
// simulating a select-all that reaches past the filtered view to "every
// asset of this type" -- turns this red: untagged ends up carrying the tag
// too. Restored after confirming.
func TestApplyTagToSelectionTagsExactlyTheNamedEntities(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newEntityTagFixture(t, e)
			dr := f.tag(t, "dr")

			untagged := mustAsset(t, f.s, f.ctx, domain.KindServer, "left-out-of-the-selection", nil)
			expected := f.rowVersion(t, domain.TagEntityAsset, f.assetID)

			outcomes, err := f.s.ApplyTagToSelection(f.ctx, f.actor, domain.TagEntityAsset, dr,
				[]TagSelection{{ID: f.assetID, RowVersion: expected}})
			if err != nil {
				t.Fatalf("ApplyTagToSelection: %v", err)
			}
			if len(outcomes) != 1 || !outcomes[0].Tagged() {
				t.Fatalf("outcomes = %+v, want exactly one Tagged", outcomes)
			}

			tags, err := f.s.EntityTagsFor(f.ctx, domain.TagEntityAsset, f.assetID)
			if err != nil {
				t.Fatalf("EntityTagsFor(selected): %v", err)
			}
			if len(tags) != 1 || tags[0].ID != dr {
				t.Fatalf("selected asset tags = %+v, want exactly [dr]", tags)
			}

			untaggedTags, err := f.s.EntityTagsFor(f.ctx, domain.TagEntityAsset, untagged)
			if err != nil {
				t.Fatalf("EntityTagsFor(untouched): %v", err)
			}
			if len(untaggedTags) != 0 {
				t.Fatalf("an asset OUTSIDE the selection was tagged: %+v", untaggedTags)
			}
		})
	}
}

// TestApplyTagToSelectionOneChangeLogRowPerEntitySharingOneBatch:
// docs/tags-design.md §4a's own words, "each entity gets its own change_log
// row sharing one batch id" -- the WP-G7 piece 3 shape, proven here for the
// tag-application path rather than assumed carried over.
func TestApplyTagToSelectionOneChangeLogRowPerEntitySharingOneBatch(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newEntityTagFixture(t, e)
			dr := f.tag(t, "dr")

			second := mustAsset(t, f.s, f.ctx, domain.KindServer, "second-in-the-batch", nil)
			expected1 := f.rowVersion(t, domain.TagEntityAsset, f.assetID)
			expected2 := f.rowVersion(t, domain.TagEntityAsset, second)

			outcomes, err := f.s.ApplyTagToSelection(f.ctx, f.actor, domain.TagEntityAsset, dr, []TagSelection{
				{ID: f.assetID, RowVersion: expected1},
				{ID: second, RowVersion: expected2},
			})
			if err != nil {
				t.Fatalf("ApplyTagToSelection: %v", err)
			}
			if len(outcomes) != 2 {
				t.Fatalf("got %d outcomes, want 2", len(outcomes))
			}

			var batchIDs []string
			for _, id := range []string{f.assetID, second} {
				changes := f.changesFor(t, domain.TagEntityAsset, id)
				var updates []domain.ChangeLog
				for _, c := range changes {
					if c.Action == domain.ActionUpdate {
						updates = append(updates, c)
					}
				}
				if len(updates) != 1 {
					t.Fatalf("asset %s has %d update rows, want exactly 1: %+v", id, len(updates), updates)
				}
				if updates[0].BatchID == nil || *updates[0].BatchID == "" {
					t.Fatalf("asset %s's change_log row carries no batch_id", id)
				}
				batchIDs = append(batchIDs, *updates[0].BatchID)
			}
			if batchIDs[0] != batchIDs[1] {
				t.Fatalf("batch ids differ: %v", batchIDs)
			}
		})
	}
}

// TestApplyTagToSelectionSkipsAnEntityChangedSinceShown: the row_version an
// operator was shown is a snapshot, not a hint. An entity that changed
// between the list rendering and this request landing must be skipped and
// reported -- never clobbered, never silently re-tagged against stale data.
func TestApplyTagToSelectionSkipsAnEntityChangedSinceShown(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newEntityTagFixture(t, e)
			dr := f.tag(t, "dr")

			shownVersion := f.rowVersion(t, domain.TagEntityAsset, f.assetID)
			// Somebody else's edit lands after the list was rendered but
			// before this bulk-apply request arrives.
			pci := f.tag(t, "pci")
			liveExpected := f.rowVersion(t, domain.TagEntityAsset, f.assetID)
			if err := f.s.SetEntityTags(f.ctx, f.actor, domain.TagEntityAsset, f.assetID, liveExpected, []string{pci}); err != nil {
				t.Fatalf("simulating a race: %v", err)
			}

			outcomes, err := f.s.ApplyTagToSelection(f.ctx, f.actor, domain.TagEntityAsset, dr,
				[]TagSelection{{ID: f.assetID, RowVersion: shownVersion}})
			if err != nil {
				t.Fatalf("ApplyTagToSelection: %v", err)
			}
			if len(outcomes) != 1 || !outcomes[0].Stale() {
				t.Fatalf("outcomes = %+v, want exactly one Stale", outcomes)
			}

			// Not clobbered: still carries only what the race wrote, not dr.
			tags, err := f.s.EntityTagsFor(f.ctx, domain.TagEntityAsset, f.assetID)
			if err != nil {
				t.Fatalf("EntityTagsFor: %v", err)
			}
			if len(tags) != 1 || tags[0].ID != pci {
				t.Fatalf("asset tags = %+v, want exactly [pci] (the race winner), untouched by the stale request", tags)
			}
		})
	}
}

// TestApplyTagToSelectionRefusesARetiredTag: design.md §4a's rule, applied
// to the whole batch rather than per row -- a retired tag is never offered
// for new application, bulk or otherwise.
func TestApplyTagToSelectionRefusesARetiredTag(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newEntityTagFixture(t, e)
			dr := f.tag(t, "dr")
			f.retire(t, dr)
			expected := f.rowVersion(t, domain.TagEntityAsset, f.assetID)

			_, err := f.s.ApplyTagToSelection(f.ctx, f.actor, domain.TagEntityAsset, dr,
				[]TagSelection{{ID: f.assetID, RowVersion: expected}})
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("err = %v, want domain.ErrInvalid", err)
			}

			tags, err := f.s.EntityTagsFor(f.ctx, domain.TagEntityAsset, f.assetID)
			if err != nil {
				t.Fatalf("EntityTagsFor: %v", err)
			}
			if len(tags) != 0 {
				t.Fatalf("a retired tag was applied despite the refusal: %+v", tags)
			}
		})
	}
}

// TestApplyTagToSelectionRefusesEmptySelection: no rows named is a refusal,
// not a silent no-op batch -- the same rule BulkAssignOwnership enforces.
func TestApplyTagToSelectionRefusesEmptySelection(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newEntityTagFixture(t, e)
			dr := f.tag(t, "dr")

			_, err := f.s.ApplyTagToSelection(f.ctx, f.actor, domain.TagEntityAsset, dr, nil)
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("err = %v, want domain.ErrInvalid", err)
			}
		})
	}
}

// TestApplyTagToSelectionRefusesAnUnknownEntityType guards the entity-type
// allowlist the same way TestSetEntityTagsRefusesAnUntaggableEntityType
// guards SetEntityTags.
func TestApplyTagToSelectionRefusesAnUnknownEntityType(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newEntityTagFixture(t, e)
			dr := f.tag(t, "dr")

			_, err := f.s.ApplyTagToSelection(f.ctx, f.actor, "not_a_real_entity_type", dr,
				[]TagSelection{{ID: "whatever", RowVersion: 1}})
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("err = %v, want domain.ErrInvalid", err)
			}
		})
	}
}

// TestApplyTagToSelectionIsIdempotentOnAnAlreadyTaggedEntity: applying a tag
// the entity already carries reports Tagged, not a failure -- the
// post-condition (the entity carries the tag) already holds.
func TestApplyTagToSelectionIsIdempotentOnAnAlreadyTaggedEntity(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newEntityTagFixture(t, e)
			dr := f.tag(t, "dr")
			expected := f.rowVersion(t, domain.TagEntityAsset, f.assetID)
			if err := f.s.SetEntityTags(f.ctx, f.actor, domain.TagEntityAsset, f.assetID, expected, []string{dr}); err != nil {
				t.Fatalf("applying dr directly: %v", err)
			}

			expected = f.rowVersion(t, domain.TagEntityAsset, f.assetID)
			outcomes, err := f.s.ApplyTagToSelection(f.ctx, f.actor, domain.TagEntityAsset, dr,
				[]TagSelection{{ID: f.assetID, RowVersion: expected}})
			if err != nil {
				t.Fatalf("ApplyTagToSelection: %v", err)
			}
			if len(outcomes) != 1 || !outcomes[0].Tagged() {
				t.Fatalf("outcomes = %+v, want exactly one Tagged", outcomes)
			}
		})
	}
}
