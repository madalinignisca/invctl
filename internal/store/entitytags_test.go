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
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Applying tags to entities, piece 2 of WP-G4a (docs/tags-design.md). See
// entitytags.go for the mechanism; this fixture builds one of each taggable
// entity type so every test below can pick whichever it needs.
type entityTagFixture struct {
	s         *SQLStore
	ctx       context.Context
	actor     domain.Actor
	assetID   string
	serviceID string
	projectID string
}

func newEntityTagFixture(t *testing.T, e Engine) *entityTagFixture {
	t.Helper()
	s, ctx := newStore(t, e)

	user, err := domain.NewAppUser(NewID(), "tag-applier", domain.UserSourceLocal, s.Now())
	if err != nil {
		t.Fatalf("building fixture user: %v", err)
	}
	if err := s.CreateUser(ctx, testActor, user); err != nil {
		t.Fatalf("creating fixture user: %v", err)
	}
	actor := domain.UserActor(user)

	envID := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
	assetID := mustAsset(t, s, ctx, domain.KindServer, "app-01", nil, envID)

	svc, err := domain.NewService(NewID(), domain.ServiceSpec{
		Code: "orders", Name: "Orders", Kind: domain.SvcAPI,
		EnvironmentID: envID, Availability: domain.AvailStandalone, Tier: 2,
	}, s.Now())
	if err != nil {
		t.Fatalf("building fixture service: %v", err)
	}
	if err := s.CreateService(ctx, domain.AdministratorPermit(actor), svc); err != nil {
		t.Fatalf("creating fixture service: %v", err)
	}

	proj, err := domain.NewProject(NewID(), domain.ProjectSpec{Code: "orders-proj", Name: "Orders"}, s.Now())
	if err != nil {
		t.Fatalf("building fixture project: %v", err)
	}
	if err := s.CreateProject(ctx, actor, proj); err != nil {
		t.Fatalf("creating fixture project: %v", err)
	}

	return &entityTagFixture{
		s: s, ctx: ctx, actor: actor,
		assetID: assetID, serviceID: svc.ID, projectID: proj.ID,
	}
}

// tag creates a live tag and returns its id.
func (f *entityTagFixture) tag(t *testing.T, code string) string {
	t.Helper()
	tg, err := domain.NewTag(NewID(), code, code, "a fixture tag for the entity-tag store suite", f.actor.ID, f.s.Now())
	if err != nil {
		t.Fatalf("building tag %s: %v", code, err)
	}
	if err := f.s.CreateTag(f.ctx, f.actor, tg); err != nil {
		t.Fatalf("creating tag %s: %v", code, err)
	}
	return tg.ID
}

func (f *entityTagFixture) retire(t *testing.T, tagID string) {
	t.Helper()
	if err := f.s.RetireTag(f.ctx, f.actor, tagID); err != nil {
		t.Fatalf("retiring tag %s: %v", tagID, err)
	}
}

func (f *entityTagFixture) rowVersion(t *testing.T, entityType, entityID string) int {
	t.Helper()
	switch entityType {
	case domain.TagEntityAsset:
		a, err := f.s.GetAsset(f.ctx, entityID)
		if err != nil {
			t.Fatalf("reading asset: %v", err)
		}
		return a.RowVersion
	case domain.TagEntityService:
		svc, err := f.s.GetService(f.ctx, entityID)
		if err != nil {
			t.Fatalf("reading service: %v", err)
		}
		return svc.RowVersion
	default:
		p, err := f.s.GetProject(f.ctx, entityID)
		if err != nil {
			t.Fatalf("reading project: %v", err)
		}
		return p.RowVersion
	}
}

func (f *entityTagFixture) changeCount(t *testing.T, entityType, entityID string) int64 {
	t.Helper()
	n, err := f.s.countOne(f.ctx,
		`SELECT COUNT(*) FROM change_log WHERE entity_type = ? AND entity_id = ?`, entityType, entityID)
	if err != nil {
		t.Fatalf("counting change_log rows for %s %s: %v", entityType, entityID, err)
	}
	return n
}

func (f *entityTagFixture) lastDiff(t *testing.T, entityType, entityID string) string {
	t.Helper()
	var diff string
	err := f.s.readOne(f.ctx, &diff,
		`SELECT diff FROM change_log WHERE entity_type = ? AND entity_id = ? ORDER BY at DESC, id DESC LIMIT 1`,
		entityType, entityID)
	if err != nil {
		t.Fatalf("reading the last change_log diff for %s %s: %v", entityType, entityID, err)
	}
	return diff
}

// tagsFold reads the CURRENT tags fold for an entity, using the exact
// internal helper SetEntityTags itself uses -- entitytags_test.go is
// white-box, in package store, precisely so this can reach it. Run inside a
// no-op write transaction: nothing here is mutated, so nothing is logged.
func (f *entityTagFixture) tagsFold(t *testing.T, entityType, entityID string) string {
	t.Helper()
	var fold string
	err := f.s.write(f.ctx, domain.AdministratorPermit(f.actor), func(tx *tx) error {
		var err error
		fold, err = entityTagsAudit(f.ctx, tx, entityType, entityID)
		return err
	})
	if err != nil {
		t.Fatalf("reading the tags fold of %s %s: %v", entityType, entityID, err)
	}
	return fold
}

// TestApplyingATagWritesExactlyOneChangeLogRowOnTheParent is the fold's whole
// point (docs/tags-design.md §4): entity_tag is a child table, so without the
// fold applying a tag would leave the asset's own columns untouched and
// write no audit entry at all -- the failure this codebase has been bitten
// by four times.
func TestApplyingATagWritesExactlyOneChangeLogRowOnTheParent(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newEntityTagFixture(t, e)
			dr := f.tag(t, "dr")
			before := f.changeCount(t, domain.TagEntityAsset, f.assetID)

			expected := f.rowVersion(t, domain.TagEntityAsset, f.assetID)
			if err := f.s.SetEntityTags(f.ctx, domain.AdministratorPermit(f.actor), domain.TagEntityAsset, f.assetID, expected, []string{dr}); err != nil {
				t.Fatalf("applying the tag: %v", err)
			}

			after := f.changeCount(t, domain.TagEntityAsset, f.assetID)
			if after != before+1 {
				t.Fatalf("applying a tag wrote %d change_log rows on the asset, want exactly 1", after-before)
			}
			applied, err := f.s.EntityTagsFor(f.ctx, domain.TagEntityAsset, f.assetID)
			if err != nil {
				t.Fatalf("reading applied tags: %v", err)
			}
			if len(applied) != 1 || applied[0].ID != dr {
				t.Fatalf("got %v applied, want [%s]", applied, dr)
			}
		})
	}
}

// TestRemovingATagWritesExactlyOneChangeLogRowOnTheParent is the other half:
// the set-replacement rule cuts both ways.
func TestRemovingATagWritesExactlyOneChangeLogRowOnTheParent(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newEntityTagFixture(t, e)
			dr := f.tag(t, "dr")
			expected := f.rowVersion(t, domain.TagEntityAsset, f.assetID)
			if err := f.s.SetEntityTags(f.ctx, domain.AdministratorPermit(f.actor), domain.TagEntityAsset, f.assetID, expected, []string{dr}); err != nil {
				t.Fatalf("applying the tag: %v", err)
			}
			before := f.changeCount(t, domain.TagEntityAsset, f.assetID)

			expected = f.rowVersion(t, domain.TagEntityAsset, f.assetID)
			if err := f.s.SetEntityTags(f.ctx, domain.AdministratorPermit(f.actor), domain.TagEntityAsset, f.assetID, expected, nil); err != nil {
				t.Fatalf("removing the tag: %v", err)
			}

			after := f.changeCount(t, domain.TagEntityAsset, f.assetID)
			if after != before+1 {
				t.Fatalf("removing a tag wrote %d change_log rows on the asset, want exactly 1", after-before)
			}
			applied, err := f.s.EntityTagsFor(f.ctx, domain.TagEntityAsset, f.assetID)
			if err != nil {
				t.Fatalf("reading applied tags: %v", err)
			}
			if len(applied) != 0 {
				t.Fatalf("got %v applied, want none", applied)
			}
		})
	}
}

// TestReorderingTagsIsNotAChange is named in docs/tags-design.md §4 and is
// part of the definition of done: applying the same set in a different
// order must write no audit entry. This codebase has been bitten by the
// INVERSE failure (a set replacement producing no diff at all) four times,
// so the opposite direction -- a PHANTOM diff from unstable ordering --
// gets the same suspicion.
func TestReorderingTagsIsNotAChange(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newEntityTagFixture(t, e)
			dr := f.tag(t, "dr")
			pci := f.tag(t, "pci")

			expected := f.rowVersion(t, domain.TagEntityAsset, f.assetID)
			if err := f.s.SetEntityTags(f.ctx, domain.AdministratorPermit(f.actor), domain.TagEntityAsset, f.assetID, expected, []string{dr, pci}); err != nil {
				t.Fatalf("applying both tags: %v", err)
			}
			before := f.changeCount(t, domain.TagEntityAsset, f.assetID)

			// The identical set, submitted in the opposite order.
			expected = f.rowVersion(t, domain.TagEntityAsset, f.assetID)
			if err := f.s.SetEntityTags(f.ctx, domain.AdministratorPermit(f.actor), domain.TagEntityAsset, f.assetID, expected, []string{pci, dr}); err != nil {
				t.Fatalf("resubmitting the same set in a different order: %v", err)
			}

			after := f.changeCount(t, domain.TagEntityAsset, f.assetID)
			if after != before {
				t.Fatalf("reordering the same tag set wrote %d change_log rows, want 0", after-before)
			}
		})
	}
}

// TestTheTagFoldIsStableAcrossARename is the fold-by-id half of
// docs/tags-design.md §4's amendment, tested directly against the mechanism
// (entityTagsAudit) rather than through a narrative of unrelated saves:
// renaming a tag's code must not change the fold of an entity whose
// membership did not change, because the fold is built from the tag's
// STABLE ID, never its code.
//
// PROVED TO BE ABLE TO FAIL: folding by code instead of id (editing
// foldEntityTags's caller in entityTagsAudit to resolve and sort
// domain.Tag.Code rather than the raw ids) turns this red, exactly as it
// must -- see the piece-2 delivery report for the before/after output.
func TestTheTagFoldIsStableAcrossARename(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newEntityTagFixture(t, e)
			dr := f.tag(t, "dr")
			expected := f.rowVersion(t, domain.TagEntityAsset, f.assetID)
			if err := f.s.SetEntityTags(f.ctx, domain.AdministratorPermit(f.actor), domain.TagEntityAsset, f.assetID, expected, []string{dr}); err != nil {
				t.Fatalf("applying the tag: %v", err)
			}
			before := f.tagsFold(t, domain.TagEntityAsset, f.assetID)

			row, err := f.s.GetTag(f.ctx, dr)
			if err != nil {
				t.Fatalf("reading the tag: %v", err)
			}
			row.Code = "dr-site"
			if err := f.s.UpdateTag(f.ctx, f.actor, &row.Tag); err != nil {
				t.Fatalf("renaming the tag's code: %v", err)
			}

			after := f.tagsFold(t, domain.TagEntityAsset, f.assetID)
			if before != after {
				t.Fatalf("renaming the tag's code changed the entity's tags fold (%q -> %q) though "+
					"membership never changed; fold the tag's STABLE ID, not its code (docs/tags-design.md §4)",
					before, after)
			}
		})
	}
}

// TestRenamingATagDoesNotManufactureATagsDiffOnTheNextUnrelatedSave is the
// narrative version of the same rule: docs/tags-design.md §4's stated
// consequence of getting this wrong is that "the next unrelated save on
// each of them logs a spurious tags diff". An unrelated save (the asset's
// own name) must log exactly one entry, and that entry must not mention
// tags at all.
func TestRenamingATagDoesNotManufactureATagsDiffOnTheNextUnrelatedSave(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newEntityTagFixture(t, e)
			dr := f.tag(t, "dr")
			expected := f.rowVersion(t, domain.TagEntityAsset, f.assetID)
			if err := f.s.SetEntityTags(f.ctx, domain.AdministratorPermit(f.actor), domain.TagEntityAsset, f.assetID, expected, []string{dr}); err != nil {
				t.Fatalf("applying the tag: %v", err)
			}

			row, err := f.s.GetTag(f.ctx, dr)
			if err != nil {
				t.Fatalf("reading the tag: %v", err)
			}
			row.Code = "dr-site"
			if err := f.s.UpdateTag(f.ctx, f.actor, &row.Tag); err != nil {
				t.Fatalf("renaming the tag's code: %v", err)
			}

			before := f.changeCount(t, domain.TagEntityAsset, f.assetID)
			asset, err := f.s.GetAsset(f.ctx, f.assetID)
			if err != nil {
				t.Fatalf("reading the asset: %v", err)
			}
			asset.Name = "app-01-renamed"
			if err := f.s.UpdateAsset(f.ctx, domain.AdministratorPermit(f.actor), &asset.Asset, nil); err != nil {
				t.Fatalf("saving an unrelated field: %v", err)
			}

			after := f.changeCount(t, domain.TagEntityAsset, f.assetID)
			if after != before+1 {
				t.Fatalf("the unrelated save wrote %d change_log rows, want exactly 1", after-before)
			}
			diff := f.lastDiff(t, domain.TagEntityAsset, f.assetID)
			if strings.Contains(diff, `"tags"`) {
				t.Fatalf("the tag rename manufactured a spurious tags diff on an unrelated save: %s", diff)
			}
		})
	}
}

// TestARetiredTagStillDisplaysOnAnEntityThatCarriesIt: docs/tags-design.md
// §2, the same rule a retired custom-field option already follows.
func TestARetiredTagStillDisplaysOnAnEntityThatCarriesIt(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newEntityTagFixture(t, e)
			dr := f.tag(t, "dr")
			expected := f.rowVersion(t, domain.TagEntityAsset, f.assetID)
			if err := f.s.SetEntityTags(f.ctx, domain.AdministratorPermit(f.actor), domain.TagEntityAsset, f.assetID, expected, []string{dr}); err != nil {
				t.Fatalf("applying the tag: %v", err)
			}
			f.retire(t, dr)

			applied, err := f.s.EntityTagsFor(f.ctx, domain.TagEntityAsset, f.assetID)
			if err != nil {
				t.Fatalf("reading applied tags: %v", err)
			}
			if len(applied) != 1 || applied[0].ID != dr || !applied[0].IsRetired() {
				t.Fatalf("got %v, want the retired tag still listed and marked retired", applied)
			}

			// An unrelated resubmission of the SAME set (the picker redraws
			// this tag's checkbox pre-ticked, so the operator's save posts
			// it back) must not be refused, and must not drop it.
			expected = f.rowVersion(t, domain.TagEntityAsset, f.assetID)
			if err := f.s.SetEntityTags(f.ctx, domain.AdministratorPermit(f.actor), domain.TagEntityAsset, f.assetID, expected, []string{dr}); err != nil {
				t.Fatalf("resubmitting a retired tag this entity already holds must be accepted: %v", err)
			}
			applied, err = f.s.EntityTagsFor(f.ctx, domain.TagEntityAsset, f.assetID)
			if err != nil {
				t.Fatalf("reading applied tags: %v", err)
			}
			if len(applied) != 1 {
				t.Fatalf("the retired tag must survive an unrelated resubmission of the same set, got %v", applied)
			}
		})
	}
}

// TestARetiredTagIsRefusedForNewApplication: docs/tags-design.md §2 and
// §4a, "not offered for new application" -- the store-level belt behind the
// picker's own braces (which never renders a retired tag as an option).
func TestARetiredTagIsRefusedForNewApplication(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newEntityTagFixture(t, e)
			dr := f.tag(t, "dr")
			f.retire(t, dr)

			expected := f.rowVersion(t, domain.TagEntityAsset, f.assetID)
			err := f.s.SetEntityTags(f.ctx, domain.AdministratorPermit(f.actor), domain.TagEntityAsset, f.assetID, expected, []string{dr})
			if err == nil {
				t.Fatal("applying a retired tag for the first time must be refused")
			}
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("got %v, want domain.ErrInvalid", err)
			}
			applied, aerr := f.s.EntityTagsFor(f.ctx, domain.TagEntityAsset, f.assetID)
			if aerr != nil {
				t.Fatalf("reading applied tags: %v", aerr)
			}
			if len(applied) != 0 {
				t.Fatalf("a refused application must not have written anything, got %v", applied)
			}
		})
	}
}

// TestAStaleParentRowVersionGets409: optimistic concurrency on the PARENT
// entity's row_version, the same rule SetCustomValues follows.
func TestAStaleParentRowVersionGets409(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newEntityTagFixture(t, e)
			dr := f.tag(t, "dr")
			stale := f.rowVersion(t, domain.TagEntityAsset, f.assetID)

			// Somebody else's edit lands first, advancing the asset's own
			// row_version.
			asset, err := f.s.GetAsset(f.ctx, f.assetID)
			if err != nil {
				t.Fatalf("reading the asset: %v", err)
			}
			asset.Name = "app-01-moved"
			if err := f.s.UpdateAsset(f.ctx, domain.AdministratorPermit(f.actor), &asset.Asset, nil); err != nil {
				t.Fatalf("advancing the row_version: %v", err)
			}

			// A tag submission still carrying the ORIGINAL token.
			err = f.s.SetEntityTags(f.ctx, domain.AdministratorPermit(f.actor), domain.TagEntityAsset, f.assetID, stale, []string{dr})
			if err == nil {
				t.Fatal("a stale parent row_version must be refused")
			}
			if !errors.Is(err, domain.ErrStale) {
				t.Fatalf("got %v, want domain.ErrStale", err)
			}
		})
	}
}

// TestATagAppliedToTwoEntityTypesIsIndependent: entity_tag is polymorphic
// (docs/tags-design.md §3), and removing a tag from one entity type must not
// touch its application on another -- there is no shared row between them
// beyond the tag_id they both reference.
func TestATagAppliedToTwoEntityTypesIsIndependent(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newEntityTagFixture(t, e)
			dr := f.tag(t, "dr")

			assetVersion := f.rowVersion(t, domain.TagEntityAsset, f.assetID)
			if err := f.s.SetEntityTags(f.ctx, domain.AdministratorPermit(f.actor), domain.TagEntityAsset, f.assetID, assetVersion, []string{dr}); err != nil {
				t.Fatalf("applying to the asset: %v", err)
			}
			serviceVersion := f.rowVersion(t, domain.TagEntityService, f.serviceID)
			if err := f.s.SetEntityTags(f.ctx, domain.AdministratorPermit(f.actor), domain.TagEntityService, f.serviceID, serviceVersion, []string{dr}); err != nil {
				t.Fatalf("applying to the service: %v", err)
			}

			// Removing it from the asset only.
			assetVersion = f.rowVersion(t, domain.TagEntityAsset, f.assetID)
			if err := f.s.SetEntityTags(f.ctx, domain.AdministratorPermit(f.actor), domain.TagEntityAsset, f.assetID, assetVersion, nil); err != nil {
				t.Fatalf("removing from the asset: %v", err)
			}

			assetTags, err := f.s.EntityTagsFor(f.ctx, domain.TagEntityAsset, f.assetID)
			if err != nil {
				t.Fatalf("reading asset tags: %v", err)
			}
			if len(assetTags) != 0 {
				t.Fatalf("the asset must no longer carry the tag, got %v", assetTags)
			}
			serviceTags, err := f.s.EntityTagsFor(f.ctx, domain.TagEntityService, f.serviceID)
			if err != nil {
				t.Fatalf("reading service tags: %v", err)
			}
			if len(serviceTags) != 1 || serviceTags[0].ID != dr {
				t.Fatalf("the service must still carry the tag independently of the asset, got %v", serviceTags)
			}
		})
	}
}

// TestApplyingAnUnknownTagIsRefused: 404, never an upsert -- the same ruling
// setCustomValues applies to an unknown field id.
func TestApplyingAnUnknownTagIsRefused(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newEntityTagFixture(t, e)
			expected := f.rowVersion(t, domain.TagEntityAsset, f.assetID)
			err := f.s.SetEntityTags(f.ctx, domain.AdministratorPermit(f.actor), domain.TagEntityAsset, f.assetID, expected, []string{NewID()})
			if err == nil {
				t.Fatal("applying an unknown tag id must be refused")
			}
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("got %v, want domain.ErrNotFound", err)
			}
		})
	}
}

// TestSetEntityTagsRefusesAnUntaggableEntityType is the belt behind
// migration 00057's entity_tag_entity_type_check CHECK: a typo cannot
// invent a new entity kind.
func TestSetEntityTagsRefusesAnUntaggableEntityType(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newEntityTagFixture(t, e)
			dr := f.tag(t, "dr")
			err := f.s.SetEntityTags(f.ctx, domain.AdministratorPermit(f.actor), "circuit", f.assetID, 1, []string{dr})
			if err == nil {
				t.Fatal("an untaggable entity type must be refused")
			}
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("got %v, want domain.ErrInvalid", err)
			}
		})
	}
}

// TestApplyingATagToAProjectWritesExactlyOneChangeLogRow covers the third
// taggable type, which carries no custom fields and therefore no second
// fold to cancel against -- projectAudit's only addition beyond the row
// itself is Tags.
func TestApplyingATagToAProjectWritesExactlyOneChangeLogRow(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newEntityTagFixture(t, e)
			dr := f.tag(t, "dr")
			before := f.changeCount(t, domain.TagEntityProject, f.projectID)

			expected := f.rowVersion(t, domain.TagEntityProject, f.projectID)
			if err := f.s.SetEntityTags(f.ctx, domain.AdministratorPermit(f.actor), domain.TagEntityProject, f.projectID, expected, []string{dr}); err != nil {
				t.Fatalf("applying the tag: %v", err)
			}

			after := f.changeCount(t, domain.TagEntityProject, f.projectID)
			if after != before+1 {
				t.Fatalf("applying a tag to the project wrote %d change_log rows, want exactly 1", after-before)
			}
		})
	}
}

// TestTagIntegrityViolationsFindsAnOrphan proves the store-level compensation
// docs/tags-design.md §3 requires for the missing foreign key: a hand-built
// row pointing at nothing is found, not silently accepted forever.
func TestTagIntegrityViolationsFindsAnOrphan(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newEntityTagFixture(t, e)
			dr := f.tag(t, "dr")

			clean, err := f.s.TagIntegrityViolations(f.ctx)
			if err != nil {
				t.Fatalf("checking integrity: %v", err)
			}
			if len(clean) != 0 {
				t.Fatalf("got %d violations on a clean fixture, want 0", len(clean))
			}

			// A row this package's own writes would never produce -- exactly
			// the hand-written-fix/bad-import scenario design.md §3 names.
			// Written directly through a bare transaction, bypassing
			// SetEntityTags's own validation on purpose: that validation is
			// exactly what a hand-written fix or a bad import bypasses too.
			err = f.s.write(f.ctx, domain.AdministratorPermit(f.actor), func(tx *tx) error {
				_, err := tx.exec(f.ctx, `
					INSERT INTO entity_tag (tag_id, entity_type, entity_id, created_at, created_by)
					VALUES (?, ?, ?, ?, ?)`,
					dr, domain.TagEntityAsset, "does-not-exist", tx.at, f.actor.ID)
				return err
			})
			if err != nil {
				t.Fatalf("inserting the orphan fixture row: %v", err)
			}

			violations, err := f.s.TagIntegrityViolations(f.ctx)
			if err != nil {
				t.Fatalf("checking integrity: %v", err)
			}
			if len(violations) != 1 || violations[0].EntityID != "does-not-exist" {
				t.Fatalf("got %v, want exactly one violation naming does-not-exist", violations)
			}
		})
	}
}
