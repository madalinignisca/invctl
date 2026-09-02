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

// WP-1.1 item 2: link moves from ScopeTopology to domain.ScopeSubjectDerived.
// This file is deps_scope_test.go's two-ended shape applied to a cable,
// whose two subjects are both assets: the interface's owning asset at each
// end, one hop away (authorizeInterfaceSubject already established that an
// interface's own scope IS its asset's).
//
// EACH END IS EXERCISED INDEPENDENTLY, deliberately -- a table that only
// ever varied both ends together would pass with a fix that checked just
// one of them.

// linkScopeFixture sets up three assets -- A1 (the project owner's scope),
// A2 and A3 (unrelated) -- and one interface on each, which is all every
// case below needs.
type linkScopeFixture struct {
	s             *SQLStore
	ctx           context.Context
	a1, a2, a3    string
	if1, if2, if3 string
	permit        domain.Permit
}

func newLinkScopeFixture(t *testing.T, e Engine) *linkScopeFixture {
	t.Helper()
	s, ctx := newStore(t, e)
	frontend := mustProjectForAssignment(t, s, ctx, "frontend")
	a1 := mustAsset(t, s, ctx, domain.KindServer, "a1", nil)
	a2 := mustAsset(t, s, ctx, domain.KindServer, "a2", nil)
	a3 := mustAsset(t, s, ctx, domain.KindServer, "a3", nil)
	if1 := mustInterface(t, s, ctx, a1, "eth0")
	if2 := mustInterface(t, s, ctx, a2, "eth0")
	if3 := mustInterface(t, s, ctx, a3, "eth0")

	permit := domain.ScopedPermit(
		domain.Actor{ID: "po-21", Name: "po-21", Kind: domain.ActorKindUser},
		[]string{frontend},
		domain.ScopedEntities{"asset": {a1: true}})

	return &linkScopeFixture{s: s, ctx: ctx, a1: a1, a2: a2, a3: a3, if1: if1, if2: if2, if3: if3, permit: permit}
}

func mustChangesForLink(t *testing.T, f *linkScopeFixture, id string) []domain.ChangeLog {
	t.Helper()
	changes, err := f.s.ListChangesForEntity(f.ctx, "link", id, 10)
	if err != nil {
		t.Fatalf("listing changes for link %s: %v", id, err)
	}
	return changes
}

// TestLinkScopeCoversNeitherAsset: A-if on A2, B-if on A3, a permit scoped
// to A1 alone. Both subjects are out of scope.
func TestLinkScopeCoversNeitherAsset(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newLinkScopeFixture(t, e)
			l, err := domain.NewLink(NewID(), f.if2, f.if3)
			if err != nil {
				t.Fatalf("building link: %v", err)
			}
			before := len(mustChangesForLink(t, f, l.ID))
			err = f.s.CreateLink(f.ctx, f.permit, l)
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("CreateLink covering neither asset = %v, want domain.ErrForbidden", err)
			}
			after := len(mustChangesForLink(t, f, l.ID))
			if after != before {
				t.Errorf("a refused create wrote %d change_log rows, want %d", after, before)
			}
		})
	}
}

// TestLinkScopeCoversAEndOnly: A-if on A1 (in scope), B-if on A3 (out of
// scope). Checking only the A end would let this through.
func TestLinkScopeCoversAEndOnly(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newLinkScopeFixture(t, e)
			l, err := domain.NewLink(NewID(), f.if1, f.if3)
			if err != nil {
				t.Fatalf("building link: %v", err)
			}
			before := len(mustChangesForLink(t, f, l.ID))
			err = f.s.CreateLink(f.ctx, f.permit, l)
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("CreateLink covering only the A end = %v, want domain.ErrForbidden", err)
			}
			after := len(mustChangesForLink(t, f, l.ID))
			if after != before {
				t.Errorf("a refused create wrote %d change_log rows, want %d", after, before)
			}
		})
	}
}

// TestLinkScopeCoversBEndOnly: A-if on A2 (out of scope), B-if on A1 (in
// scope). Checking only the B end would let this through.
func TestLinkScopeCoversBEndOnly(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newLinkScopeFixture(t, e)
			l, err := domain.NewLink(NewID(), f.if2, f.if1)
			if err != nil {
				t.Fatalf("building link: %v", err)
			}
			before := len(mustChangesForLink(t, f, l.ID))
			err = f.s.CreateLink(f.ctx, f.permit, l)
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("CreateLink covering only the B end = %v, want domain.ErrForbidden", err)
			}
			after := len(mustChangesForLink(t, f, l.ID))
			if after != before {
				t.Errorf("a refused create wrote %d change_log rows, want %d", after, before)
			}
		})
	}
}

// TestLinkScopeCoversBothEnds: A-if and B-if both on A1. Both subjects are
// in scope, so the write is allowed and audited once.
func TestLinkScopeCoversBothEnds(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newLinkScopeFixture(t, e)
			otherOnA1 := mustInterface(t, f.s, f.ctx, f.a1, "eth1")
			l, err := domain.NewLink(NewID(), f.if1, otherOnA1)
			if err != nil {
				t.Fatalf("building link: %v", err)
			}
			if err := f.s.CreateLink(f.ctx, f.permit, l); err != nil {
				t.Fatalf("CreateLink covering both ends = %v, want nil", err)
			}
			changes := mustChangesForLink(t, f, l.ID)
			if len(changes) != 1 {
				t.Errorf("an allowed create wrote %d change_log rows, want 1", len(changes))
			}
		})
	}
}

// TestLinkScopeRetireUsesTheStoredEnds proves RetireLink authorizes off the
// STORED row, not anything a caller could submit -- retire takes no link
// struct, only an id, so this also proves the id resolves to the right
// asset pair through the b_interface_id/a_interface_id columns.
func TestLinkScopeRetireUsesTheStoredEnds(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newLinkScopeFixture(t, e)
			l, err := domain.NewLink(NewID(), f.if2, f.if3)
			if err != nil {
				t.Fatalf("building link: %v", err)
			}
			if err := f.s.CreateLink(f.ctx, testPermit, l); err != nil {
				t.Fatalf("seeding the link as an administrator: %v", err)
			}

			if err := f.s.RetireLink(f.ctx, f.permit, l.ID); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("RetireLink on a link covering neither of the caller's assets = %v, want domain.ErrForbidden", err)
			}

			after, err := f.s.GetLink(f.ctx, l.ID)
			if err != nil {
				t.Fatalf("re-reading the link: %v", err)
			}
			if after.Lifecycle == domain.LifecycleRetired {
				t.Error("a refused retire still retired the link")
			}
		})
	}
}

// TestLinkScopeRetireAEndMineBEndForeign is the retire-side sibling
// TestLinkScopeRetireUsesTheStoredEnds does not carry: that test seeds a
// link with BOTH ends foreign, so its A check alone is enough to refuse and
// the B check never has to do anything. Here the A end is the caller's own
// asset and only the B end is foreign, so a mutant that checked the A end
// twice (or dropped the B check outright) would let this retire through.
func TestLinkScopeRetireAEndMineBEndForeign(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newLinkScopeFixture(t, e)
			l, err := domain.NewLink(NewID(), f.if1, f.if3)
			if err != nil {
				t.Fatalf("building link: %v", err)
			}
			if err := f.s.CreateLink(f.ctx, testPermit, l); err != nil {
				t.Fatalf("seeding the link as an administrator: %v", err)
			}

			if err := f.s.RetireLink(f.ctx, f.permit, l.ID); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("RetireLink with the A end mine and the B end foreign = %v, want domain.ErrForbidden", err)
			}

			after, err := f.s.GetLink(f.ctx, l.ID)
			if err != nil {
				t.Fatalf("re-reading the link: %v", err)
			}
			if after.Lifecycle == domain.LifecycleRetired {
				t.Error("a refused retire still retired the link")
			}
		})
	}
}

// TestLinkScopeRetireAEndForeignBEndMine is the mirror: the A end is
// foreign and only the B end belongs to the caller. A mutant that checked
// the B end twice (or dropped the A check) would let this retire through.
func TestLinkScopeRetireAEndForeignBEndMine(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newLinkScopeFixture(t, e)
			l, err := domain.NewLink(NewID(), f.if2, f.if1)
			if err != nil {
				t.Fatalf("building link: %v", err)
			}
			if err := f.s.CreateLink(f.ctx, testPermit, l); err != nil {
				t.Fatalf("seeding the link as an administrator: %v", err)
			}

			if err := f.s.RetireLink(f.ctx, f.permit, l.ID); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("RetireLink with the A end foreign and the B end mine = %v, want domain.ErrForbidden", err)
			}

			after, err := f.s.GetLink(f.ctx, l.ID)
			if err != nil {
				t.Fatalf("re-reading the link: %v", err)
			}
			if after.Lifecycle == domain.LifecycleRetired {
				t.Error("a refused retire still retired the link")
			}
		})
	}
}

// TestLinkScopeRetireBothEndsMine is the positive path this task actually
// delivers -- a project owner unpatching their own cable -- and until this
// test existed nothing in the suite exercised a successful project-owner
// retire at all: every green RetireLink call elsewhere used testPermit
// (an administrator). A mutant that made authorizeLinkSubjects, or the
// wiring into RetireLink, refuse every project owner unconditionally could
// have shipped as a silent, permanent 403 with the rest of the suite still
// green.
func TestLinkScopeRetireBothEndsMine(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newLinkScopeFixture(t, e)
			otherOnA1 := mustInterface(t, f.s, f.ctx, f.a1, "eth9")
			l, err := domain.NewLink(NewID(), f.if1, otherOnA1)
			if err != nil {
				t.Fatalf("building link: %v", err)
			}
			if err := f.s.CreateLink(f.ctx, testPermit, l); err != nil {
				t.Fatalf("seeding the link as an administrator: %v", err)
			}
			// The create above already wrote its own change_log row against
			// this same link id, so the assertion below has to be a delta,
			// not an absolute count.
			before := len(mustChangesForLink(t, f, l.ID))

			if err := f.s.RetireLink(f.ctx, f.permit, l.ID); err != nil {
				t.Fatalf("RetireLink with both ends mine = %v, want nil", err)
			}

			after, err := f.s.GetLink(f.ctx, l.ID)
			if err != nil {
				t.Fatalf("re-reading the link: %v", err)
			}
			if after.Lifecycle != domain.LifecycleRetired {
				t.Errorf("lifecycle after an allowed retire = %q, want %q", after.Lifecycle, domain.LifecycleRetired)
			}
			gotChanges := len(mustChangesForLink(t, f, l.ID))
			if gotChanges-before != 1 {
				t.Errorf("an allowed retire wrote %d change_log rows, want 1", gotChanges-before)
			}
		})
	}
}

// TestLinkScopeRetireAnAlreadyRetiredForeignLinkStillRefuses pins the
// authorization-before-early-exit ordering in RetireLink: a link that is
// ALREADY retired (by an administrator) is still refused, not silently
// accepted as a no-op, when a project owner who does not cover either end
// asks to retire it again. This is not closing a disclosure -- reads are
// universal (docs/rbac-design.md §2), so the ordering reveals nothing a
// reader could not already see via GetLink -- but it is the behaviour the
// ordering is meant to guarantee, and worth pinning so a future edit that
// moves the early exit back above the check is caught here rather than by
// re-reading the comment.
func TestLinkScopeRetireAnAlreadyRetiredForeignLinkStillRefuses(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newLinkScopeFixture(t, e)
			l, err := domain.NewLink(NewID(), f.if2, f.if3)
			if err != nil {
				t.Fatalf("building link: %v", err)
			}
			if err := f.s.CreateLink(f.ctx, testPermit, l); err != nil {
				t.Fatalf("seeding the link as an administrator: %v", err)
			}
			if err := f.s.RetireLink(f.ctx, testPermit, l.ID); err != nil {
				t.Fatalf("retiring the link as an administrator: %v", err)
			}

			if err := f.s.RetireLink(f.ctx, f.permit, l.ID); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("RetireLink on an already-retired link covering neither of the caller's assets = %v, want domain.ErrForbidden, not a silent no-op", err)
			}
		})
	}
}

// TestLinkScopeAdministrator: an AdministratorPermit covers every row
// regardless of subject, the same as it does for every other
// subject-derived type.
func TestLinkScopeAdministrator(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newLinkScopeFixture(t, e)
			admin := domain.AdministratorPermit(
				domain.Actor{ID: "admin-1", Name: "admin-1", Kind: domain.ActorKindUser})
			l, err := domain.NewLink(NewID(), f.if2, f.if3)
			if err != nil {
				t.Fatalf("building link: %v", err)
			}
			if err := f.s.CreateLink(f.ctx, admin, l); err != nil {
				t.Fatalf("CreateLink as an administrator = %v, want nil", err)
			}
			if err := f.s.RetireLink(f.ctx, admin, l.ID); err != nil {
				t.Fatalf("RetireLink as an administrator = %v, want nil", err)
			}
		})
	}
}
