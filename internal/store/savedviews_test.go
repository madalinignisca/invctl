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

// mustUserForSavedView creates a plain observer account and returns its id,
// which is all these tests need: a saved view's owner is just an app_user id.
func mustUserForSavedView(t *testing.T, s *SQLStore, ctx context.Context, username string) string {
	t.Helper()
	u := mustUserWithRole(t, s, ctx, username, domain.RoleObserver)
	return u.ID
}

func savedViewPermit(userID string) domain.Permit {
	return domain.ScopedPermit(
		domain.Actor{ID: userID, Name: userID, Kind: domain.ActorKindUser}, nil, nil)
}

// TestAPersonCannotUpdateSomebodyElsesSavedView is the security property of
// this whole work package. A saved view is the first row in this product
// whose SUBJECT is a person, and the only thing standing between one
// person's shortcuts and another's is this check.
func TestAPersonCannotUpdateSomebodyElsesSavedView(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			alice := mustUserForSavedView(t, s, ctx, "alice")
			bob := mustUserForSavedView(t, s, ctx, "bob")

			v, err := domain.NewSavedView(NewID(), alice, domain.SavedViewAsset,
				"Production servers", `{"kind":"server"}`, s.Now())
			if err != nil {
				t.Fatalf("building view: %v", err)
			}
			if err := s.CreateSavedView(ctx, savedViewPermit(alice), v); err != nil {
				t.Fatalf("creating alice's view: %v", err)
			}

			v.Name = "Bob was here"
			if err := s.UpdateSavedView(ctx, savedViewPermit(bob), v); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("UpdateSavedView as another person = %v, want domain.ErrForbidden", err)
			}

			after, err := s.GetSavedView(ctx, v.ID)
			if err != nil {
				t.Fatalf("re-reading: %v", err)
			}
			if after.Name != "Production servers" {
				t.Errorf("name = %q; bob edited alice's view", after.Name)
			}
		})
	}
}

// TestAnAdministratorCannotEditSomebodyElsesSavedView pins a deliberate
// absence. An AdministratorPermit Covers everything, so anyone reading
// domain.Covers would reasonably add "unless administrator" to the owner
// check. Administrators administer the estate, not other people's
// shortcuts, and there is no operational reason to reach one.
func TestAnAdministratorCannotEditSomebodyElsesSavedView(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			alice := mustUserForSavedView(t, s, ctx, "alice")
			admin := mustUserForSavedView(t, s, ctx, "admin")

			v, err := domain.NewSavedView(NewID(), alice, domain.SavedViewAsset,
				"Production servers", `{"kind":"server"}`, s.Now())
			if err != nil {
				t.Fatalf("building view: %v", err)
			}
			if err := s.CreateSavedView(ctx, savedViewPermit(alice), v); err != nil {
				t.Fatalf("creating alice's view: %v", err)
			}

			adminPermit := domain.AdministratorPermit(
				domain.Actor{ID: admin, Name: "admin", Kind: domain.ActorKindUser})
			v.Name = "Administered"
			if err := s.UpdateSavedView(ctx, adminPermit, v); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("UpdateSavedView as an Administrator = %v, want domain.ErrForbidden", err)
			}
		})
	}
}

// TestUpdateSavedViewReadsTheOwnerFromTheStoredRow is the seizure attempt.
// UpdateSavedView takes a *domain.SavedView carrying a UserID; if it trusted
// that field, anybody could name themselves and edit anybody's view. Same
// shape as TestEditingAJournalNoteChecksTheStoredSubjectNotTheSubmittedOne.
func TestUpdateSavedViewReadsTheOwnerFromTheStoredRow(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			alice := mustUserForSavedView(t, s, ctx, "alice")
			bob := mustUserForSavedView(t, s, ctx, "bob")

			v, err := domain.NewSavedView(NewID(), alice, domain.SavedViewAsset,
				"Production servers", `{"kind":"server"}`, s.Now())
			if err != nil {
				t.Fatalf("building view: %v", err)
			}
			if err := s.CreateSavedView(ctx, savedViewPermit(alice), v); err != nil {
				t.Fatalf("creating alice's view: %v", err)
			}

			// THE ATTACK: same view id, but claiming bob owns it.
			forged := *v
			forged.UserID = bob
			forged.Name = "Seized"
			if err := s.UpdateSavedView(ctx, savedViewPermit(bob), &forged); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("UpdateSavedView with a forged owner = %v, want domain.ErrForbidden", err)
			}
		})
	}
}

// TestAPersonCanManageTheirOwnSavedViews is the other half: the refusals
// above must not have been achieved by refusing everybody.
func TestAPersonCanManageTheirOwnSavedViews(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			alice := mustUserForSavedView(t, s, ctx, "alice")

			v, err := domain.NewSavedView(NewID(), alice, domain.SavedViewAsset,
				"Production servers", `{"kind":"server"}`, s.Now())
			if err != nil {
				t.Fatalf("building view: %v", err)
			}
			if err := s.CreateSavedView(ctx, savedViewPermit(alice), v); err != nil {
				t.Fatalf("CreateSavedView = %v, want nil", err)
			}
			v.Name = "Prod servers"
			if err := s.UpdateSavedView(ctx, savedViewPermit(alice), v); err != nil {
				t.Fatalf("UpdateSavedView = %v, want nil", err)
			}
			if err := s.RetireSavedView(ctx, savedViewPermit(alice), v.ID); err != nil {
				t.Fatalf("RetireSavedView = %v, want nil", err)
			}
			views, err := s.ListSavedViews(ctx, alice, domain.SavedViewAsset)
			if err != nil {
				t.Fatalf("ListSavedViews: %v", err)
			}
			if len(views) != 0 {
				t.Errorf("a retired view is still listed: %+v", views)
			}
		})
	}
}

// TestTwoPeopleMayEachHaveAViewOfTheSameName pins the partial unique index:
// it is per user AND per entity, so a shared name is not a collision.
func TestTwoPeopleMayEachHaveAViewOfTheSameName(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			alice := mustUserForSavedView(t, s, ctx, "alice")
			bob := mustUserForSavedView(t, s, ctx, "bob")

			for _, owner := range []string{alice, bob} {
				v, err := domain.NewSavedView(NewID(), owner, domain.SavedViewAsset,
					"Production servers", `{"kind":"server"}`, s.Now())
				if err != nil {
					t.Fatalf("building view: %v", err)
				}
				if err := s.CreateSavedView(ctx, savedViewPermit(owner), v); err != nil {
					t.Fatalf("creating %s's view: %v", owner, err)
				}
			}
		})
	}
}

// TestUpdateSavedViewLogsOnlyTheFieldsItActuallyWrites guards the shape of
// the logged diff itself: UpdateSavedView's UPDATE statement writes name and
// params only, so the "after" value it logs must be built from the stored
// row plus exactly those two fields -- never the caller's submitted struct
// wholesale, which could carry a forged Entity or CreatedAt the database
// never touched.
func TestUpdateSavedViewLogsOnlyTheFieldsItActuallyWrites(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			alice := mustUserForSavedView(t, s, ctx, "alice")

			v, err := domain.NewSavedView(NewID(), alice, domain.SavedViewAsset,
				"Production servers", `{"kind":"server"}`, s.Now())
			if err != nil {
				t.Fatalf("building view: %v", err)
			}
			if err := s.CreateSavedView(ctx, savedViewPermit(alice), v); err != nil {
				t.Fatalf("creating: %v", err)
			}

			// A real edit (Name) so a diff genuinely gets written, plus a
			// forged Entity the UPDATE statement never touches.
			v.Name = "Prod servers"
			v.Entity = domain.SavedViewService
			if err := s.UpdateSavedView(ctx, savedViewPermit(alice), v); err != nil {
				t.Fatalf("UpdateSavedView: %v", err)
			}

			changes, err := s.ListChangesForEntity(ctx, "saved_view", v.ID, 10)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			if len(changes) != 2 {
				t.Fatalf("got %d change_log rows, want exactly 2 (create + update)", len(changes))
			}
			// ListChangesForEntity orders newest first; the update is [0].
			diff := changes[0].Diff
			if !strings.Contains(diff, "Prod servers") {
				t.Errorf("the update diff lost the actual name change: %s", diff)
			}
			if strings.Contains(diff, "entity") || strings.Contains(diff, domain.SavedViewService) {
				t.Errorf("the update diff records a change to entity, which the UPDATE never wrote: %s", diff)
			}
		})
	}
}

// TestTheAuditRowForASavedViewRedactsTheParams: change_log is kept forever,
// and a permanent record of what somebody repeatedly searches the estate for
// is a behavioural profile nothing needs.
func TestTheAuditRowForASavedViewRedactsTheParams(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			alice := mustUserForSavedView(t, s, ctx, "alice")

			v, err := domain.NewSavedView(NewID(), alice, domain.SavedViewAsset,
				"Production servers", `{"kind":"server","q":"secret-project"}`, s.Now())
			if err != nil {
				t.Fatalf("building view: %v", err)
			}
			if err := s.CreateSavedView(ctx, savedViewPermit(alice), v); err != nil {
				t.Fatalf("creating: %v", err)
			}

			changes, err := s.ListChangesForEntity(ctx, "saved_view", v.ID, 10)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			if len(changes) != 1 {
				t.Fatalf("got %d change_log rows, want exactly 1", len(changes))
			}
			if strings.Contains(changes[0].Diff, "secret-project") {
				t.Errorf("the audit trail carries the view's params: %s", changes[0].Diff)
			}
			if !strings.Contains(changes[0].Diff, "Production servers") {
				t.Errorf("the audit trail lost the view's name, which is what makes the entry mean anything: %s", changes[0].Diff)
			}
		})
	}
}
