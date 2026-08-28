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
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// WP-G1 Task 15: internal/web/handlers/entitytags.go's postEntityTags is ONE
// request that can produce TWO writes -- an optional new tag (entity_type =
// "tag", estate config, ScopeEstateConfig) and the entity's tag set itself
// (entity_type = "asset"/"service"/"project", ScopeProjectLinked for an
// asset or service). The brief's claim is that no special case is needed:
// tx.log's ordinary Covers check, run once per write, already tells the two
// apart. These tests drive the two store calls postEntityTags itself makes
// (CreateTag, then SetEntityTags) directly against the permit layer, the
// same precedent internal/store/project_create_test.go sets and for the
// same reason -- CanWrite(RoleProjectOwner) is still false (Task 13 has not
// landed), so an HTTP request from a project owner would be refused by
// RequireWrite before postEntityTags is ever reached, and a test that only
// proved that would prove nothing about the two-scope claim itself.
//
// Mutation (brief, step "Mutation"): make Covers allow "tag" -> the second
// test (TestAProjectOwnerCannotCreateANewTagWhileTaggingTheirOwnAsset) must
// go red.

// mustEntityTagsScopeUser creates a real app_user row so that a fixture
// tag's created_by (a foreign key) resolves -- domain.Tag.CreatedBy is an
// opaque app_user.id, never a username (CLAUDE.md's rule for every actor
// column), and the FK is enforced whether or not the id also happens to
// match the permit's own actor.
func mustEntityTagsScopeUser(t *testing.T, s *SQLStore, ctx context.Context, username string) string {
	t.Helper()
	user, err := domain.NewAppUser(NewID(), username, domain.UserSourceLocal, s.Now())
	if err != nil {
		t.Fatalf("building fixture user %s: %v", username, err)
	}
	if err := s.CreateUser(ctx, testPermit, user); err != nil {
		t.Fatalf("creating fixture user %s: %v", username, err)
	}
	return user.ID
}

func TestAProjectOwnerCanApplyAnExistingTagToTheirOwnAsset(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			assetID := mustAsset(t, s, ctx, domain.KindServer, "po-tag-asset", nil)

			link, err := domain.NewProjectAssetLink(frontend, assetID, domain.ProjectOwns, nil, s.Now())
			if err != nil {
				t.Fatalf("building link: %v", err)
			}
			if err := s.LinkProjectAsset(ctx, testPermit, link); err != nil {
				t.Fatalf("linking asset into frontend: %v", err)
			}

			adminUserID := mustEntityTagsScopeUser(t, s, ctx, "tag-admin-1")
			existingTag, err := domain.NewTag(NewID(), "existing", "Existing", "a fixture tag", adminUserID, s.Now())
			if err != nil {
				t.Fatalf("building fixture tag: %v", err)
			}
			if err := s.CreateTag(ctx, testPermit, existingTag); err != nil {
				t.Fatalf("creating fixture tag: %v", err)
			}

			asset, err := s.GetAsset(ctx, assetID)
			if err != nil {
				t.Fatalf("reading asset: %v", err)
			}

			// Holds frontend AND the specific asset -- the scope
			// auth.Authorizer.Permit would build for a project owner
			// assigned to frontend with this asset already linked.
			//
			// The actor is a real app_user row, not an arbitrary string:
			// entity_tag.created_by is a foreign key to app_user(id), unlike
			// change_log.actor which is opaque and unconstrained (CLAUDE.md).
			poID := mustEntityTagsScopeUser(t, s, ctx, "po-tags-1")
			permit := domain.ScopedPermit(
				domain.Actor{ID: poID, Name: "po-tags-1", Kind: domain.ActorKindUser},
				[]string{frontend}, domain.ScopedEntities{"asset": {assetID: true}})

			// THE FIRST HALF OF postEntityTags'S TWO WRITES: applying an
			// EXISTING tag to an IN-SCOPE asset must succeed.
			if err := s.SetEntityTags(ctx, permit, domain.TagEntityAsset, assetID, asset.RowVersion,
				[]string{existingTag.ID}); err != nil {
				t.Fatalf("SetEntityTags for an in-scope asset: %v", err)
			}

			applied, err := s.EntityTagsFor(ctx, domain.TagEntityAsset, assetID)
			if err != nil {
				t.Fatalf("reading applied tags: %v", err)
			}
			var found bool
			for _, tg := range applied {
				if tg.ID == existingTag.ID {
					found = true
				}
			}
			if !found {
				t.Error("the existing tag was not applied to the in-scope asset")
			}
		})
	}
}

func TestAProjectOwnerCannotCreateANewTagWhileTaggingTheirOwnAsset(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			assetID := mustAsset(t, s, ctx, domain.KindServer, "po-tag-asset-2", nil)

			link, err := domain.NewProjectAssetLink(frontend, assetID, domain.ProjectOwns, nil, s.Now())
			if err != nil {
				t.Fatalf("building link: %v", err)
			}
			if err := s.LinkProjectAsset(ctx, testPermit, link); err != nil {
				t.Fatalf("linking asset into frontend: %v", err)
			}

			permit := domain.ScopedPermit(
				domain.Actor{ID: "po-tags-2", Name: "po-tags-2", Kind: domain.ActorKindUser},
				[]string{frontend}, domain.ScopedEntities{"asset": {assetID: true}})

			// THE SECOND HALF: postEntityTags creates a NEW tag first, as
			// its own write, entity_type = "tag" -- estate config, refused
			// regardless of the asset being fully in scope.
			creatorID := mustEntityTagsScopeUser(t, s, ctx, "po-tags-2")
			newTag, err := domain.NewTag(NewID(), "brand-new", "Brand new", "typed on the asset page",
				creatorID, s.Now())
			if err != nil {
				t.Fatalf("building the new tag: %v", err)
			}
			err = s.CreateTag(ctx, permit, newTag)
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("CreateTag by a project owner = %v, want domain.ErrForbidden", err)
			}

			// No tag row was created -- entitytags.go:190-192 documents that
			// this write happens BEFORE the entity-tag submission it would
			// have fed, so a refusal here must leave no trace at all, not
			// merely leave the asset's own tag set unchanged.
			if _, err := s.GetTag(ctx, newTag.ID); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("GetTag(%s) = %v, want domain.ErrNotFound -- the refused tag was created anyway",
					newTag.ID, err)
			}
			live, err := s.ListTags(ctx, true)
			if err != nil {
				t.Fatalf("listing tags: %v", err)
			}
			for _, tg := range live {
				if tg.Code == "brand-new" {
					t.Error("a tag with the refused code exists in the tag list")
				}
			}
		})
	}
}
