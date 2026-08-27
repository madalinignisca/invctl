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

// Create-and-link, WP-G1 Task 14 (docs/rbac-design.md §4).
//
// EVERY TEST HERE DRIVES THE PERMIT LAYER DIRECTLY, not through
// middleware.RequireAdmin -- CanWrite(RoleProjectOwner) still returns false
// (Task 13 has not landed), so a real HTTP request from a project owner
// would be refused by the gate before reaching any of the code this file
// exercises, and a test that only proved that would prove nothing about
// CreateAssetInProject itself. Task 12's own tests (auth/permit_test.go) set
// this precedent; Task 13's own step 3 is what re-runs the same claims
// through the middleware once it flips CanWrite.

// projectOwnerPermit mints exactly the ScopedPermit
// auth.Authorizer.Permit would build for a project owner assigned to
// projectIDs and nothing else -- no asset/service/circuit scope, since none
// of these tests need an EXISTING linked entity, only the ability to create
// a new one inside a project this permit holds.
func projectOwnerPermit(id string, projectIDs ...string) domain.Permit {
	return domain.ScopedPermit(
		domain.Actor{ID: id, Name: id, Kind: domain.ActorKindUser},
		projectIDs, nil)
}

func TestAProjectOwnerCanCreateAnAssetInTheirOwnProject(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			permit := projectOwnerPermit("po-1", frontend)

			a, err := domain.NewAsset(NewID(), domain.KindServer, "new-web-1", nil, s.Now())
			if err != nil {
				t.Fatalf("building asset: %v", err)
			}
			if err := s.CreateAssetInProject(ctx, permit, frontend, a); err != nil {
				t.Fatalf("CreateAssetInProject: %v", err)
			}

			if _, err := s.GetAsset(ctx, a.ID); err != nil {
				t.Fatalf("the asset was not actually created: %v", err)
			}
			links, err := s.ListProjectAssets(ctx, frontend)
			if err != nil {
				t.Fatalf("listing project assets: %v", err)
			}
			var found bool
			for _, l := range links {
				if l.AssetID == a.ID {
					found = true
					if l.Relation != domain.ProjectOwns {
						t.Errorf("relation = %q, want %q", l.Relation, domain.ProjectOwns)
					}
				}
			}
			if !found {
				t.Error("the new asset was not linked to the project that created it")
			}
		})
	}
}

func TestAProjectOwnerCannotCreateAnAssetInSomebodyElsesProject(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			other := mustProjectForAssignment(t, s, ctx, "other")
			// This permit holds frontend, not other -- the project in the URL.
			permit := projectOwnerPermit("po-2", frontend)

			a, err := domain.NewAsset(NewID(), domain.KindServer, "should-not-exist", nil, s.Now())
			if err != nil {
				t.Fatalf("building asset: %v", err)
			}
			err = s.CreateAssetInProject(ctx, permit, other, a)
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("err = %v, want domain.ErrForbidden", err)
			}

			if _, err := s.GetAsset(ctx, a.ID); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("the asset was created despite the refusal: err = %v", err)
			}
			links, err := s.ListProjectAssets(ctx, other)
			if err != nil {
				t.Fatalf("listing project assets: %v", err)
			}
			for _, l := range links {
				if l.AssetID == a.ID {
					t.Error("the refused asset was linked to the project anyway")
				}
			}
		})
	}
}

// TestScopedPermitCannotLinkIntoAProjectItDoesNotHold is the store-level half
// of the escalation in docs/rbac-design.md §4: LinkProjectAsset (the plain,
// Administrator-only link route's store method) must still refuse a scoped
// permit for a project it was NOT minted with, exactly the way
// CreateAssetInProject already does.
//
// A DELIBERATE, DOCUMENTED GAP THIS TEST DOES NOT CLOSE: within a project the
// permit DOES hold, Covers's Task 12 carve-out cannot tell "link an existing
// asset" apart from "create a new one and link it" -- both write the
// identical project_asset row shape, and that ambiguity is Task 12's own
// stated reason the plain link route "must never be reached with a project
// owner's ScopedPermit" at all. This repository's ROUTING is what is meant
// to guarantee that (POST /projects/{id}/assets stays behind
// middleware.RequireAdmin, and RequireAdmin currently equals
// authz.CanWrite, which is false for every RoleProjectOwner today). See this
// task's report for why that guarantee has not actually been exercised
// end-to-end yet, and TestAProjectOwnerCannotLinkAnExistingAssetToTheirProject
// (internal/web/project_create_test.go) for the HTTP-level test that proves
// what IS true today.
//
// Mutation (Step 5): make Covers admit project_asset for any project, not
// only one the permit holds -- this test must go red.
func TestScopedPermitCannotLinkIntoAProjectItDoesNotHold(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			other := mustProjectForAssignment(t, s, ctx, "other")
			dbProd := mustAsset(t, s, ctx, domain.KindServer, "db-prod", nil)
			// Holds frontend, not other -- the project being linked into.
			permit := projectOwnerPermit("po-3", frontend)

			link, err := domain.NewProjectAssetLink(other, dbProd, domain.ProjectOwns, nil, s.Now())
			if err != nil {
				t.Fatalf("building link: %v", err)
			}
			err = s.LinkProjectAsset(ctx, permit, link)
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("err = %v, want domain.ErrForbidden", err)
			}

			links, err := s.ListProjectAssets(ctx, other)
			if err != nil {
				t.Fatalf("listing project assets: %v", err)
			}
			for _, l := range links {
				if l.AssetID == dbProd {
					t.Error("db-prod was linked to a project this permit does not hold")
				}
			}
		})
	}
}

// TestARefusedCreateAndLinkLeavesNeitherTheAssetNorTheLink proves the
// transaction rolls back WHOLE: a committed asset with a refused link would
// be an orphan only an Administrator could find and fix.
//
// Deliberately NOT a permission-shaped refusal: CreateAssetInProject checks
// domain.PermitHoldsProject BEFORE any transaction opens (its own doc
// comment explains why -- failing fast on an unheld project should not cost
// a rack query or a write-pool connection), so a refusal for THAT reason
// never reaches the code this test exists to pin. This uses
// domain.AdministratorPermit -- which always holds every project, so the
// early check never fires -- against a projectID that names no real project
// row at all, so the LINK insert fails on project_asset's own foreign key
// constraint while the ASSET insert has already, by construction of the
// mutation below, had every chance to commit on its own.
//
// Mutation (Step 5): commit the asset insert in its own transaction before
// the link -- this must fail, because the asset would then survive a
// refused link.
func TestARefusedCreateAndLinkLeavesNeitherTheAssetNorTheLink(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			noSuchProject := NewID()

			a, err := domain.NewAsset(NewID(), domain.KindServer, "rolled-back", nil, s.Now())
			if err != nil {
				t.Fatalf("building asset: %v", err)
			}
			if err := s.CreateAssetInProject(ctx, testPermit, noSuchProject, a); err == nil {
				t.Fatal("expected the create-and-link to be refused (no such project)")
			}

			if _, err := s.GetAsset(ctx, a.ID); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("the asset row survived a refused link: err = %v", err)
			}
			count, err := s.countOne(ctx,
				`SELECT COUNT(*) FROM project_asset WHERE asset_id = ?`, a.ID)
			if err != nil {
				t.Fatalf("counting project_asset rows: %v", err)
			}
			if count != 0 {
				t.Errorf("a project_asset row for the refused asset exists: count = %d", count)
			}
		})
	}
}

// TestPermitHoldsProjectAndCoversDeliberatelyDisagree pins the divergence
// that closes the escalation. An earlier revision of this test asserted the
// OPPOSITE -- that Covers's carve-out and PermitHoldsProject agreed for a
// held project -- and that agreement was the bug: it meant the entity half
// of a link id was never consulted, so holding one project authorized
// linking anything in the estate into it.
//
// They answer different questions and must be allowed to differ.
// PermitHoldsProject asks only "is this project yours", which is the right
// question for a store method about to mint a narrow transaction permit for
// a row that does not exist yet (see CreateAssetInProject). Covers asks
// "may you write THIS row", and for a link row that needs the entity too.
func TestPermitHoldsProjectAndCoversDeliberatelyDisagree(t *testing.T) {
	permit := projectOwnerPermit("po-5", "frontend")
	if got := domain.PermitHoldsProject(permit, "frontend"); !got {
		t.Error("PermitHoldsProject(frontend) = false, want true")
	}
	if got := domain.PermitHoldsProject(permit, "other"); got {
		t.Error("PermitHoldsProject(other) = true, want false")
	}
	// Holds the project, does not cover the entity -- so the link is refused
	// even though the project half matches.
	if permit.Covers("project_asset", "frontend/db-prod") {
		t.Error("Covers admitted a link row for an entity outside the permit's scope")
	}
}

// TestAProjectOwnerCannotLinkAnExistingAssetToTheirProject is the escalation
// docs/rbac-design.md §4 forbids, written as a test against the store.
//
// It is the SAME-project case, and it is the one that mattered: linking into
// a project you do NOT hold was always refused, so a test covering only that
// case passes with the bug fully present. Proven at runtime before the fix:
// LinkProjectAsset returned nil here, and AssetIDsForProjects then reported
// db-prod inside the owner's scope, which is hop two of the escalation.
func TestAProjectOwnerCannotLinkAnExistingAssetToTheirProject(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			dbProd := mustAsset(t, s, ctx, domain.KindServer, "db-prod", nil)
			// Holds frontend -- the project being linked INTO. Only the
			// asset is out of scope.
			permit := projectOwnerPermit("po-6", frontend)

			link, err := domain.NewProjectAssetLink(frontend, dbProd, domain.ProjectOwns, nil, s.Now())
			if err != nil {
				t.Fatalf("building link: %v", err)
			}
			if err := s.LinkProjectAsset(ctx, permit, link); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("LinkProjectAsset = %v, want domain.ErrForbidden", err)
			}

			links, err := s.ListProjectAssets(ctx, frontend)
			if err != nil {
				t.Fatalf("listing project assets: %v", err)
			}
			for _, l := range links {
				if l.AssetID == dbProd {
					t.Error("db-prod was linked into the owner's project")
				}
			}
			// Hop two: had the link landed, scope resolution would hand this
			// owner write on db-prod at their next request.
			ids, err := s.AssetIDsForProjects(ctx, []string{frontend})
			if err != nil {
				t.Fatalf("AssetIDsForProjects: %v", err)
			}
			for _, id := range ids {
				if id == dbProd {
					t.Error("db-prod entered the owner's write scope")
				}
			}
		})
	}
}
