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
	"errors"
	"fmt"

	"github.com/madalinignisca/invctl/internal/domain"
)

// WP-G4a piece 3: bulk application of a tag from a filtered asset or service
// list (docs/tags-design.md §4a, §5). Reuses WP-G7 piece 3's shape
// (bulk_ownership.go) rather than inventing a second bulk-mutation pattern:
// select-all applies to the CURRENT FILTERED VIEW, the confirmation names
// the count and the target before the request leaves the browser, each
// entity is its own transaction, and every entity gets its OWN change_log
// row sharing one batch_id.
//
// WHERE IT DIFFERS FROM BulkAssignOwnership, AND WHY: that mutation has an
// eligibility column to gate the UPDATE on (`team_id IS NULL`), which
// doubles as the atomic race guard. Applying a tag has no such column --
// any live entity may carry any live tag regardless of what it already
// carries -- so there is nothing to gate on except the entity's own
// optimistic-concurrency token. A TagSelection therefore carries the
// RowVersion the operator was SHOWN, not a hint: setEntityTags's
// version-guarded UPDATE is what turns "this entity changed between being
// listed and being submitted" into domain.ErrStale, reported below as a
// skip, never a clobber -- the identical "the operator asked for those N"
// reasoning WP-G7 piece 3 gives for its own stale case.

// TagSelection is one row an operator ticked on a filtered list: the id they
// saw and the row_version the page rendered for it, captured at render time
// so a change landing between then and submission is detectable rather than
// silently overwritten.
type TagSelection struct {
	ID         string
	RowVersion int
}

const (
	// TagApplyTagged: the entity carries the tag after this write, whether it
	// was newly added or (idempotently) already there.
	TagApplyTagged = "tagged"
	// TagApplyStale: the row_version submitted no longer matches -- somebody
	// else's declared-state change landed between the list being shown and
	// this request arriving. NOT an error: the same "the operator asked for
	// those N" reasoning as ReassignStale/AssignNoLongerUnowned.
	TagApplyStale = "changed_since_shown"
	// TagApplyFailed: the write itself errored for a reason other than a
	// stale token (an id naming nothing that exists, most likely a
	// hand-built request). Detail carries a safe, generic message -- never a
	// raw driver error.
	TagApplyFailed = "write_failed"
)

// TagApplyOutcome is one entity's result from a bulk tag application -- a
// per-item outcome with a reason, never a bare count (design.md §4a: "10
// tagged, 2 skipped cannot tell the operator whether a skip was a
// colleague's edit or a write failure").
type TagApplyOutcome struct {
	EntityType string
	EntityID   string
	Name       string
	Result     string
	// Detail is set only when Result is TagApplyFailed.
	Detail string
}

// Tagged reports success -- named rather than a bare string comparison
// against TagApplyTagged in every template, mirroring ReassignOutcome.Assigned.
func (o TagApplyOutcome) Tagged() bool { return o.Result == TagApplyTagged }

// Stale reports the row_version-mismatch skip, for the same reason Tagged
// exists: a template compares this, never the raw Result string.
func (o TagApplyOutcome) Stale() bool { return o.Result == TagApplyStale }

// dedupeSelections drops blanks and repeats, keeping the first row_version
// seen for a given id -- a form posting the same checkbox twice must not
// turn into two write attempts against the same entity in one batch.
func dedupeSelections(selections []TagSelection) []TagSelection {
	seen := make(map[string]bool, len(selections))
	out := make([]TagSelection, 0, len(selections))
	for _, sel := range selections {
		if sel.ID == "" || seen[sel.ID] {
			continue
		}
		seen[sel.ID] = true
		out = append(out, sel)
	}
	return out
}

// ApplyTagToSelection applies tagID to exactly the entities named in
// selections -- the fix offered directly from a filtered asset or service
// list (design.md §4a).
//
// selections IS THE SUBMISSION CONTRACT, NOT A HINT (the same rule
// BulkAssignOwnership's ids parameter is held to). Every id -- including one
// arriving via "select all" -- is one the operator was shown and explicitly
// selected from whatever the current filter most recently rendered; nothing
// here re-derives "everything matching this filter" to act on instead of
// what was named, and no id outside this list is ever touched.
//
// The tag itself is validated ONCE, before the loop: it must exist and must
// not be retired (design.md §2, §4a -- "not offered for new application").
// A tag already applied to some of these entities and not others is exactly
// what this call is for, so no per-entity retirement re-check happens inside
// the loop; the tag cannot become retired mid-batch without a second write
// this codebase serialises through the same row_version machinery every
// other write goes through.
func (s *SQLStore) ApplyTagToSelection(ctx context.Context, actor domain.Actor, entityType, tagID string, selections []TagSelection) ([]TagApplyOutcome, error) {
	switch entityType {
	case domain.TagEntityAsset, domain.TagEntityService, domain.TagEntityProject:
	default:
		return nil, fmt.Errorf("bulk applying a tag: %q is not a taggable entity type: %w", entityType, domain.ErrInvalid)
	}
	if tagID == "" {
		return nil, fmt.Errorf("bulk applying a tag: no tag was chosen: %w", domain.ErrInvalid)
	}
	tag, err := s.GetTag(ctx, tagID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, fmt.Errorf("bulk applying tag %s: no such tag: %w", tagID, domain.ErrInvalid)
		}
		return nil, fmt.Errorf("bulk applying a tag: %w", err)
	}
	if tag.IsRetired() {
		return nil, fmt.Errorf(
			"bulk applying tag %s (%s): the tag is retired and takes no new application: %w",
			tagID, tag.Code, domain.ErrInvalid)
	}

	selections = dedupeSelections(selections)
	if len(selections) == 0 {
		return nil, fmt.Errorf("bulk applying a tag: no entities were selected: %w", domain.ErrInvalid)
	}

	batchID := NewID()
	outcomes := make([]TagApplyOutcome, 0, len(selections))
	for _, sel := range selections {
		outcomes = append(outcomes, s.applyTagToOne(ctx, actor, entityType, sel, tagID, batchID))
	}
	return outcomes, nil
}

// applyTagToOne folds tagID into one entity's tag set and writes its own
// change_log entry, sharing batchID with the rest of this call's siblings.
//
// EACH ENTITY IS ITS OWN TRANSACTION: setEntityTags already opens one via
// s.write, so a write failure partway through the batch cannot roll back an
// entity that already succeeded -- the same reasoning
// BulkAssignOwnership.assignOneEntity documents for the identical shape.
func (s *SQLStore) applyTagToOne(ctx context.Context, actor domain.Actor, entityType string, sel TagSelection, tagID, batchID string) TagApplyOutcome {
	name := s.bestEffortName(ctx, entityType, sel.ID)

	existing, err := s.EntityTagsFor(ctx, entityType, sel.ID)
	if err != nil {
		return TagApplyOutcome{EntityType: entityType, EntityID: sel.ID, Name: name,
			Result: TagApplyFailed, Detail: "could not read its current tags"}
	}
	desired := make([]string, 0, len(existing)+1)
	found := false
	for _, t := range existing {
		desired = append(desired, t.ID)
		if t.ID == tagID {
			found = true
		}
	}
	if !found {
		desired = append(desired, tagID)
	}

	// The version-guarded UPDATE inside setEntityTags IS the atomic race
	// check: sel.RowVersion is a snapshot of what the operator was shown, not
	// what is true now, so a mismatch here means this exact entity changed
	// under it between then and this request landing.
	err = s.setEntityTags(ctx, actor, entityType, sel.ID, sel.RowVersion, desired, batchID)
	switch {
	case err == nil:
		return TagApplyOutcome{EntityType: entityType, EntityID: sel.ID, Name: name, Result: TagApplyTagged}
	case errors.Is(err, domain.ErrStale):
		return TagApplyOutcome{EntityType: entityType, EntityID: sel.ID, Name: name,
			Result: TagApplyStale, Detail: "modified by someone else since this list was shown"}
	default:
		return TagApplyOutcome{EntityType: entityType, EntityID: sel.ID, Name: name,
			Result: TagApplyFailed, Detail: "the write failed"}
	}
}
