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

// tagFixture is the shared fixture for the tag store tests (WP-G4a piece 1).
// Just a real app_user row: created_by/retired_by carry a foreign key to
// app_user(id), and the resolved-name test needs a display name distinct
// from the id to assert against. Nothing else -- piece 1 builds no asset or
// service, because nothing tags applies to yet.
type tagFixture struct {
	s        *SQLStore
	ctx      context.Context
	actor    domain.Actor
	username string
}

func newTagFixture(t *testing.T, e Engine) *tagFixture {
	t.Helper()
	s, ctx := newStore(t, e)

	const username = "tag-admin"
	user, err := domain.NewAppUser(NewID(), username, domain.UserSourceLocal, s.Now())
	if err != nil {
		t.Fatalf("building fixture user: %v", err)
	}
	if err := s.CreateUser(ctx, testPermit, user); err != nil {
		t.Fatalf("creating fixture user: %v", err)
	}
	actor := domain.UserActor(user)

	return &tagFixture{s: s, ctx: ctx, actor: actor, username: username}
}

// mustTag creates a live tag and returns its id.
func mustTag(t *testing.T, f *tagFixture, code string) string {
	t.Helper()
	tag, err := domain.NewTag(NewID(), code, code, "a fixture tag for the store test suite", f.actor.ID, f.s.Now())
	if err != nil {
		t.Fatalf("building tag %s: %v", code, err)
	}
	if err := f.s.CreateTag(f.ctx, domain.AdministratorPermit(f.actor), tag); err != nil {
		t.Fatalf("creating tag %s: %v", code, err)
	}
	return tag.ID
}

func tagChangeCount(t *testing.T, f *tagFixture, id string) int64 {
	t.Helper()
	n, err := f.s.countOne(f.ctx,
		`SELECT COUNT(*) FROM change_log WHERE entity_type = 'tag' AND entity_id = ?`, id)
	if err != nil {
		t.Fatalf("counting change_log rows for tag %s: %v", id, err)
	}
	return n
}

func lastTagChangeDiff(t *testing.T, f *tagFixture, id string) string {
	t.Helper()
	var diff string
	err := f.s.readOne(f.ctx, &diff,
		`SELECT diff FROM change_log WHERE entity_type = 'tag' AND entity_id = ? ORDER BY at DESC, id DESC LIMIT 1`,
		id)
	if err != nil {
		t.Fatalf("reading the last change_log diff for tag %s: %v", id, err)
	}
	return diff
}

// TestCreatingATagWritesChangeLog covers the ordinary path: a valid tag is
// accepted and creation itself is audited.
func TestCreatingATagWritesChangeLog(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newTagFixture(t, e)
			id := mustTag(t, f, "dr")
			if n := tagChangeCount(t, f, id); n != 1 {
				t.Fatalf("creating a tag wrote %d change_log rows, want 1", n)
			}
		})
	}
}

// TestARetiredTagCodeCanBeUsedAgain proves migration 00056's unique index is
// partial (WHERE retired_at IS NULL). A plain UNIQUE would refuse the second
// insert and an operator could never reuse a name they had retired.
func TestARetiredTagCodeCanBeUsedAgain(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newTagFixture(t, e)
			first := mustTag(t, f, "dr")
			if err := f.s.RetireTag(f.ctx, domain.AdministratorPermit(f.actor), first); err != nil {
				t.Fatalf("retiring: %v", err)
			}

			tag, err := domain.NewTag(NewID(), "dr", "DR", "reused after retirement", f.actor.ID, f.s.Now())
			if err != nil {
				t.Fatalf("building the second tag: %v", err)
			}
			if err := f.s.CreateTag(f.ctx, domain.AdministratorPermit(f.actor), tag); err != nil {
				t.Fatalf("a retired code must be usable again: %v", err)
			}
		})
	}
}

// TestTwoLiveTagsCannotShareACode is the other half of the same index: two
// LIVE tags with one code must be refused. This is the sprawl control the
// whole feature rests on, so it was actually mutated rather than assumed:
// `CREATE UNIQUE INDEX tag_live_code_key ON tag (code) WHERE retired_at IS
// NULL` in both migration files was changed to a plain, non-unique
// `CREATE INDEX tag_live_code_key ON tag (code)`, and
// `go test -run TestTwoLiveTagsCannotShareACode ./internal/store/...` with
// INV_TEST_POSTGRES_DSN set went red on BOTH engines ("two live tags must
// not be able to share one code"). Restoring the unique index turned it
// green again on both. See the piece-1 delivery report for the exact
// before/after output.
func TestTwoLiveTagsCannotShareACode(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newTagFixture(t, e)
			mustTag(t, f, "dr")

			tag, err := domain.NewTag(NewID(), "dr", "DR", "a second, still-live attempt", f.actor.ID, f.s.Now())
			if err != nil {
				t.Fatalf("building the second tag: %v", err)
			}
			if err := f.s.CreateTag(f.ctx, domain.AdministratorPermit(f.actor), tag); err == nil {
				t.Fatal("two live tags must not be able to share one code")
			} else if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("got %v, want a conflict", err)
			}
		})
	}
}

// TestCreateRefusesAnEmptyDescription: the CHECK constraint is the second
// line of defence, but CreateTag must never even reach it -- NewTag refuses
// first.
func TestCreateRefusesAnEmptyDescription(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newTagFixture(t, e)
			_, err := domain.NewTag(NewID(), "dr", "DR", "   ", f.actor.ID, f.s.Now())
			if err == nil {
				t.Fatal("a tag with an empty description must be refused before it reaches the store")
			}
		})
	}
}

// TestUpdatingATagWritesChangeLogWithOldAndNewCode is "a rename writing one
// row showing old and new" from the brief: renaming a tag's code, label or
// description writes exactly one change_log entry whose diff names both.
func TestUpdatingATagWritesChangeLogWithOldAndNewCode(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newTagFixture(t, e)
			id := mustTag(t, f, "dr")
			before := tagChangeCount(t, f, id)

			row, err := f.s.GetTag(f.ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			row.Code = "dr-site"
			if err := f.s.UpdateTag(f.ctx, domain.AdministratorPermit(f.actor), &row.Tag); err != nil {
				t.Fatalf("renaming: %v", err)
			}

			after := tagChangeCount(t, f, id)
			if after != before+1 {
				t.Fatalf("renaming a tag wrote %d change_log rows, want 1", after-before)
			}
			diff := lastTagChangeDiff(t, f, id)
			if !strings.Contains(diff, "dr") || !strings.Contains(diff, "dr-site") {
				t.Fatalf("the diff must show both the old and new code; got %s", diff)
			}
		})
	}
}

// TestCodeStaysEditable is docs/tags-design.md §4's own requirement: a
// rename is a correction somebody makes deliberately, and it must not be
// impossible or expensive. Nothing here depends on the code being stable.
func TestCodeStaysEditable(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newTagFixture(t, e)
			id := mustTag(t, f, "dr")

			row, err := f.s.GetTag(f.ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			row.Code = "disaster-recovery"
			if err := f.s.UpdateTag(f.ctx, domain.AdministratorPermit(f.actor), &row.Tag); err != nil {
				t.Fatalf("renaming the code must be allowed: %v", err)
			}
			after, err := f.s.GetTag(f.ctx, id)
			if err != nil {
				t.Fatalf("re-reading: %v", err)
			}
			if after.Code != "disaster-recovery" {
				t.Fatalf("got code %q, want disaster-recovery", after.Code)
			}
		})
	}
}

func TestRetiringATagWritesChangeLog(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newTagFixture(t, e)
			id := mustTag(t, f, "dr")
			before := tagChangeCount(t, f, id)

			if err := f.s.RetireTag(f.ctx, domain.AdministratorPermit(f.actor), id); err != nil {
				t.Fatalf("retiring: %v", err)
			}

			after := tagChangeCount(t, f, id)
			if after != before+1 {
				t.Fatalf("retiring wrote %d change_log rows, want 1", after-before)
			}
			row, err := f.s.GetTag(f.ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			if !row.IsRetired() {
				t.Fatal("the tag must be retired")
			}
		})
	}
}

func TestRestoringATagWritesChangeLog(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newTagFixture(t, e)
			id := mustTag(t, f, "dr")
			if err := f.s.RetireTag(f.ctx, domain.AdministratorPermit(f.actor), id); err != nil {
				t.Fatalf("retiring: %v", err)
			}
			before := tagChangeCount(t, f, id)

			if err := f.s.RestoreTag(f.ctx, domain.AdministratorPermit(f.actor), id); err != nil {
				t.Fatalf("restoring: %v", err)
			}

			after := tagChangeCount(t, f, id)
			if after != before+1 {
				t.Fatalf("restoring wrote %d change_log rows, want 1", after-before)
			}
			row, err := f.s.GetTag(f.ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			if row.IsRetired() {
				t.Fatal("the tag must no longer be retired")
			}
		})
	}
}

// TestRestoreIsRefusedWhenALiveTagHoldsTheCode: restoring must not produce
// two live tags with one code -- the code was freed for reuse the moment
// the original was retired.
func TestRestoreIsRefusedWhenALiveTagHoldsTheCode(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newTagFixture(t, e)
			first := mustTag(t, f, "dr")
			if err := f.s.RetireTag(f.ctx, domain.AdministratorPermit(f.actor), first); err != nil {
				t.Fatalf("retiring: %v", err)
			}
			mustTag(t, f, "dr") // a new, live tag now holds the freed code

			if err := f.s.RestoreTag(f.ctx, domain.AdministratorPermit(f.actor), first); err == nil {
				t.Fatal("restoring must be refused while a live tag holds the same code")
			} else if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("got %v, want a conflict", err)
			}
		})
	}
}

// TestAStaleRowVersionIsRefused is 409, not 422 -- refusalStatus's own rule
// at the HTTP layer, and domain.ErrStale here at the store layer.
func TestAStaleRowVersionIsRefused(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newTagFixture(t, e)
			id := mustTag(t, f, "dr")

			row, err := f.s.GetTag(f.ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			// Somebody else's edit lands first, advancing row_version.
			row.Label = "DR (first editor)"
			if err := f.s.UpdateTag(f.ctx, domain.AdministratorPermit(f.actor), &row.Tag); err != nil {
				t.Fatalf("first update: %v", err)
			}

			// A second editor, still holding the ORIGINAL (now stale) token.
			stale, err := f.s.GetTag(f.ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			stale.RowVersion = 1 // the token the second editor's form was rendered with
			stale.Label = "DR (second editor)"
			err = f.s.UpdateTag(f.ctx, domain.AdministratorPermit(f.actor), &stale.Tag)
			if err == nil {
				t.Fatal("a stale row_version must be refused")
			}
			if !errors.Is(err, domain.ErrStale) {
				t.Fatalf("got %v, want domain.ErrStale", err)
			}
		})
	}
}

// TestRetireLeavesAnAlreadyRetiredTagAlone: idempotent, like
// RetireCustomField -- a second retirement is a no-op, not a refusal, and
// writes no second change_log row.
func TestRetireLeavesAnAlreadyRetiredTagAlone(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newTagFixture(t, e)
			id := mustTag(t, f, "dr")
			if err := f.s.RetireTag(f.ctx, domain.AdministratorPermit(f.actor), id); err != nil {
				t.Fatalf("retiring: %v", err)
			}
			before := tagChangeCount(t, f, id)

			if err := f.s.RetireTag(f.ctx, domain.AdministratorPermit(f.actor), id); err != nil {
				t.Fatalf("retiring an already-retired tag must be a no-op, not an error: %v", err)
			}
			if after := tagChangeCount(t, f, id); after != before {
				t.Fatalf("retiring twice wrote %d extra change_log rows, want 0", after-before)
			}
		})
	}
}

func TestTagRegistryResolvesTheCreatorWithoutStoringAUsername(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newTagFixture(t, e)
			id := mustTag(t, f, "dr")
			row, err := f.s.GetTag(f.ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			if row.CreatedBy == f.username {
				t.Fatal("created_by holds the username; it must hold the opaque app_user.id")
			}
			if row.CreatedByName != f.username {
				t.Fatalf("got display name %q, want %q resolved by join", row.CreatedByName, f.username)
			}
		})
	}
}

func TestListTagsOrdersByCodeAndSplitsRetired(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newTagFixture(t, e)
			mustTag(t, f, "zeta")
			mustTag(t, f, "alpha")
			retired := mustTag(t, f, "middle")
			if err := f.s.RetireTag(f.ctx, domain.AdministratorPermit(f.actor), retired); err != nil {
				t.Fatalf("retiring: %v", err)
			}

			live, err := f.s.ListTags(f.ctx, false)
			if err != nil {
				t.Fatalf("listing live: %v", err)
			}
			if len(live) != 2 || live[0].Code != "alpha" || live[1].Code != "zeta" {
				t.Fatalf("got %v, want [alpha zeta] and no retired tag", codes(live))
			}

			all, err := f.s.ListTags(f.ctx, true)
			if err != nil {
				t.Fatalf("listing all: %v", err)
			}
			if len(all) != 3 {
				t.Fatalf("got %d tags including retired, want 3", len(all))
			}
		})
	}
}

func codes(rows []TagRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Code
	}
	return out
}
