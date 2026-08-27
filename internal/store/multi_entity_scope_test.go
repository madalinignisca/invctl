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

// WP-G1 Task 15: "multi-entity writes" -- one request, several rows, each
// its own change_log entry sharing a batch_id. There is no ONE check for
// these because a single check is wrong by construction: either it blocks
// legitimate in-scope work alongside the out-of-scope rows in the same
// batch, or it lets an out-of-scope row ride along with an in-scope one.
// Each entity is checked individually because each is its own transaction
// and its own tx.log call.
//
// bulkApplyTag: PARTIAL SUCCESS IS THE CORRECT OUTCOME, matching the
// per-item outcomes this path already reports for a stale row_version or a
// write failure -- an out-of-scope row is simply one more reason a single
// item can come back TagApplyFailed while its siblings succeed.
//
// ProjectRetire: THE OPPOSITE -- refused WHOLESALE, because "project" is
// ScopeEstateConfig and RetireProject's own change_log entry against the
// project row is written and Covers-checked BEFORE releaseLinks runs, in
// the SAME transaction. A project owner retiring their own project would
// otherwise release every link and revoke their own scope mid-transaction;
// here it never gets that far, because the very first write in the
// transaction is already refused and rolls everything back.

func TestABulkTagApplyTagsTheInScopeAssetsAndRefusesTheRest(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")

			inScope := mustAsset(t, s, ctx, domain.KindServer, "bulk-tag-in-scope", nil)
			link, err := domain.NewProjectAssetLink(frontend, inScope, domain.ProjectOwns, nil, s.Now())
			if err != nil {
				t.Fatalf("building link: %v", err)
			}
			if err := s.LinkProjectAsset(ctx, testPermit, link); err != nil {
				t.Fatalf("linking in-scope asset: %v", err)
			}
			outOfScope := mustAsset(t, s, ctx, domain.KindServer, "bulk-tag-out-of-scope", nil)

			inScopeRow, err := s.GetAsset(ctx, inScope)
			if err != nil {
				t.Fatalf("GetAsset(inScope): %v", err)
			}
			outOfScopeRow, err := s.GetAsset(ctx, outOfScope)
			if err != nil {
				t.Fatalf("GetAsset(outOfScope): %v", err)
			}

			tagAdmin := mustEntityTagsScopeUser(t, s, ctx, "bulk-tag-admin")
			tag, err := domain.NewTag(NewID(), "bulk-partial", "Bulk partial", "a fixture tag", tagAdmin, s.Now())
			if err != nil {
				t.Fatalf("building tag: %v", err)
			}
			if err := s.CreateTag(ctx, testPermit, tag); err != nil {
				t.Fatalf("creating tag: %v", err)
			}

			poID := mustEntityTagsScopeUser(t, s, ctx, "bulk-tag-po")
			permit := domain.ScopedPermit(
				domain.Actor{ID: poID, Name: "bulk-tag-po", Kind: domain.ActorKindUser},
				[]string{frontend}, domain.ScopedEntities{"asset": {inScope: true}})

			outcomes, err := s.ApplyTagToSelection(ctx, permit, domain.TagEntityAsset, tag.ID,
				[]TagSelection{
					{ID: inScope, RowVersion: inScopeRow.RowVersion},
					{ID: outOfScope, RowVersion: outOfScopeRow.RowVersion},
				})
			if err != nil {
				t.Fatalf("ApplyTagToSelection: %v", err)
			}
			if len(outcomes) != 2 {
				t.Fatalf("outcomes = %+v, want exactly 2", outcomes)
			}
			byID := map[string]TagApplyOutcome{}
			for _, o := range outcomes {
				byID[o.EntityID] = o
			}
			if got := byID[inScope].Result; got != TagApplyTagged {
				t.Errorf("in-scope outcome = %q, want %q", got, TagApplyTagged)
			}
			if got := byID[outOfScope].Result; got != TagApplyFailed {
				t.Errorf("out-of-scope outcome = %q, want %q", got, TagApplyFailed)
			}

			inScopeTags, err := s.EntityTagsFor(ctx, domain.TagEntityAsset, inScope)
			if err != nil {
				t.Fatalf("EntityTagsFor(inScope): %v", err)
			}
			var inScopeTagged bool
			for _, tg := range inScopeTags {
				if tg.ID == tag.ID {
					inScopeTagged = true
				}
			}
			if !inScopeTagged {
				t.Error("the in-scope asset was not tagged")
			}

			outOfScopeTags, err := s.EntityTagsFor(ctx, domain.TagEntityAsset, outOfScope)
			if err != nil {
				t.Fatalf("EntityTagsFor(outOfScope): %v", err)
			}
			if len(outOfScopeTags) != 0 {
				t.Error("the out-of-scope asset was tagged despite being refused")
			}
		})
	}
}

func TestProjectRetireIsAdministratorOnlyAndReleasesEveryLink(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			assetID := mustAsset(t, s, ctx, domain.KindServer, "project-retire-asset", nil)
			link, err := domain.NewProjectAssetLink(frontend, assetID, domain.ProjectOwns, nil, s.Now())
			if err != nil {
				t.Fatalf("building link: %v", err)
			}
			if err := s.LinkProjectAsset(ctx, testPermit, link); err != nil {
				t.Fatalf("linking asset: %v", err)
			}

			poID := mustEntityTagsScopeUser(t, s, ctx, "project-retire-po")
			// Holds the project AND the linked asset -- the strongest scope
			// a project owner assigned to frontend could ever have. Even so,
			// RetireProject must be refused wholesale: "project" is estate
			// config, and this permit's project half exists only to prove
			// the refusal is NOT merely "you don't hold this project".
			permit := domain.ScopedPermit(
				domain.Actor{ID: poID, Name: "project-retire-po", Kind: domain.ActorKindUser},
				[]string{frontend}, domain.ScopedEntities{"asset": {assetID: true}})

			if err := s.RetireProject(ctx, permit, frontend); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("RetireProject = %v, want domain.ErrForbidden", err)
			}

			project, err := s.GetProject(ctx, frontend)
			if err != nil {
				t.Fatalf("GetProject: %v", err)
			}
			if project.Lifecycle == domain.LifecycleRetired {
				t.Error("the project was retired despite the refusal")
			}
			links, err := s.ListProjectAssets(ctx, frontend)
			if err != nil {
				t.Fatalf("ListProjectAssets: %v", err)
			}
			var stillLinked bool
			for _, l := range links {
				if l.AssetID == assetID && l.Lifecycle == domain.LifecycleActive {
					stillLinked = true
				}
			}
			if !stillLinked {
				t.Error("the asset's project link was released despite the whole retirement being refused")
			}
		})
	}
}
