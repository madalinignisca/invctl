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
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Filtering an estate list by tag, piece 3 of WP-G4a (docs/tags-design.md
// §5). tagFilterClause's own comment states the shape; these tests prove it
// against real rows on both engines.

// mustTagFilterActor creates a real app_user and returns it as an actor --
// tag.created_by REFERENCES app_user(id), so testActor's opaque "tester" id
// (used freely by suites that never create a tag) fails that foreign key
// here on PostgreSQL and SQLite alike.
func mustTagFilterActor(t *testing.T, s *SQLStore, ctx context.Context, username string) domain.Actor {
	t.Helper()
	user, err := domain.NewAppUser(NewID(), username, domain.UserSourceLocal, s.Now())
	if err != nil {
		t.Fatalf("building fixture user: %v", err)
	}
	if err := s.CreateUser(ctx, testActor, user); err != nil {
		t.Fatalf("creating fixture user: %v", err)
	}
	return domain.UserActor(user)
}

// tagFilterFixture builds one environment, two tags and three assets
// carrying every combination the AND query has to distinguish: one with
// neither tag, one with only `dr`, and one with both `dr` and `pci`.
type tagFilterFixture struct {
	s       *SQLStore
	ctx     context.Context
	actor   domain.Actor
	dr, pci string
	// neither carries no tag at all, drOnly carries just dr, both carries
	// dr and pci -- named for what a test asserts about each, not for order.
	neither, drOnly, both string
}

func newTagFilterFixture(t *testing.T, e Engine) *tagFilterFixture {
	t.Helper()
	s, ctx := newStore(t, e)
	actor := mustTagFilterActor(t, s, ctx, "tag-filter-tester")

	envID := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)

	tag := func(code string) string {
		t.Helper()
		tg, err := domain.NewTag(NewID(), code, code, "a fixture tag for the tag-filter suite", actor.ID, s.Now())
		if err != nil {
			t.Fatalf("building tag %s: %v", code, err)
		}
		if err := s.CreateTag(ctx, actor, tg); err != nil {
			t.Fatalf("creating tag %s: %v", code, err)
		}
		return tg.ID
	}
	dr := tag("dr")
	pci := tag("pci")

	asset := func(name string, tagIDs []string) string {
		t.Helper()
		id := mustAsset(t, s, ctx, domain.KindServer, name, nil, envID)
		var version int
		if err := s.readOne(ctx, &version, `SELECT row_version FROM asset WHERE id = ?`, id); err != nil {
			t.Fatalf("reading row_version of %s: %v", name, err)
		}
		if len(tagIDs) > 0 {
			if err := s.SetEntityTags(ctx, actor, domain.TagEntityAsset, id, version, tagIDs); err != nil {
				t.Fatalf("tagging %s: %v", name, err)
			}
		}
		return id
	}

	neither := asset("neither-tagged", nil)
	drOnly := asset("dr-only", []string{dr})
	both := asset("dr-and-pci", []string{dr, pci})

	return &tagFilterFixture{
		s: s, ctx: ctx, actor: actor,
		dr: dr, pci: pci,
		neither: neither, drOnly: drOnly, both: both,
	}
}

func assetIDs(t *testing.T, rows []AssetRow) []string {
	t.Helper()
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}

// TestListAssetsFiltersByOneTag: the simplest case, one tag named.
func TestListAssetsFiltersByOneTag(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newTagFilterFixture(t, e)

			rows, err := f.s.ListAssets(f.ctx, AssetFilter{TagIDs: []string{f.dr}})
			if err != nil {
				t.Fatalf("ListAssets: %v", err)
			}
			ids := assetIDs(t, rows)
			if !containsID(ids, f.drOnly) || !containsID(ids, f.both) {
				t.Fatalf("filtering by dr = %v, want both dr-only and dr-and-pci", ids)
			}
			if containsID(ids, f.neither) {
				t.Fatalf("filtering by dr matched the untagged asset: %v", ids)
			}
		})
	}
}

// TestListAssetsFiltersByTwoTagsIsAND is design.md §5's whole point: naming
// two tags must return only the entity carrying BOTH, never the union.
//
// PROVED TO BE ABLE TO FAIL: swapping tagFilterClause's HAVING clause from
// `= ?` (the dynamic count) to `>= 1` -- the OR an implementer reaches for
// by instinct -- turns this red: dr-only then matches too, because it
// carries at least one of the named tags. Restored after confirming.
func TestListAssetsFiltersByTwoTagsIsAND(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newTagFilterFixture(t, e)

			rows, err := f.s.ListAssets(f.ctx, AssetFilter{TagIDs: []string{f.dr, f.pci}})
			if err != nil {
				t.Fatalf("ListAssets: %v", err)
			}
			ids := assetIDs(t, rows)
			if len(ids) != 1 || ids[0] != f.both {
				t.Fatalf("filtering by [dr, pci] = %v, want exactly [%s]", ids, f.both)
			}
		})
	}
}

// TestListAssetsEmptyTagFilterReturnsEverything is the guard design.md §5
// demands explicitly: no tags asked for must mean "do not filter", not
// "filter, but match nothing" and not, if the guard were removed, "match
// everything by an accident of HAVING COUNT(...) = 0" -- which happens to
// read the same as correct on an unfiltered list, so it has to be tested
// against a list that ALSO carries a non-tag filter, to prove the empty tag
// clause adds nothing to the query at all.
//
// PROVED TO BE ABLE TO FAIL: deleting the `len(ids) == 0` guard in
// tagFilterClause (so an empty ids list still builds the subquery) turns
// this red on both engines -- every asset disappears, because `HAVING
// COUNT(DISTINCT et.tag_id) = 0` is satisfied only by a row with no
// entity_tag join at all, and the LEFT-JOIN-free subquery here never
// produces one. Restored after confirming.
func TestListAssetsEmptyTagFilterReturnsEverything(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newTagFilterFixture(t, e)

			rows, err := f.s.ListAssets(f.ctx, AssetFilter{TagIDs: nil})
			if err != nil {
				t.Fatalf("ListAssets: %v", err)
			}
			ids := assetIDs(t, rows)
			for _, want := range []string{f.neither, f.drOnly, f.both} {
				if !containsID(ids, want) {
					t.Fatalf("an empty tag filter dropped an asset: got %v, want it to include %s", ids, want)
				}
			}
		})
	}
}

// TestListAssetsFiltersByARetiredTag: design.md §2's rule made literal for
// filtering -- "a retired tag can still be FILTERED on", because things
// still carry it. Only application is refused for a retired tag, never the
// filter.
func TestListAssetsFiltersByARetiredTag(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newTagFilterFixture(t, e)
			if err := f.s.RetireTag(f.ctx, f.actor, f.dr); err != nil {
				t.Fatalf("retiring dr: %v", err)
			}

			rows, err := f.s.ListAssets(f.ctx, AssetFilter{TagIDs: []string{f.dr}})
			if err != nil {
				t.Fatalf("ListAssets: %v", err)
			}
			ids := assetIDs(t, rows)
			if !containsID(ids, f.drOnly) || !containsID(ids, f.both) {
				t.Fatalf("filtering by a retired tag = %v, want the assets that still carry it", ids)
			}
		})
	}
}

// TestListAssetsTagFilterDeduplicatesRepeatedIDs: a duplicate tag id in the
// filter (a hand-built query string, or a browser resubmitting a form
// oddly) must not silently demand a count no entity can satisfy.
func TestListAssetsTagFilterDeduplicatesRepeatedIDs(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newTagFilterFixture(t, e)

			rows, err := f.s.ListAssets(f.ctx, AssetFilter{TagIDs: []string{f.dr, f.dr}})
			if err != nil {
				t.Fatalf("ListAssets: %v", err)
			}
			ids := assetIDs(t, rows)
			if !containsID(ids, f.drOnly) || !containsID(ids, f.both) {
				t.Fatalf("a duplicated tag id in the filter matched nothing: %v", ids)
			}
		})
	}
}

// serviceTagFilterFixture is tagFilterFixture's service twin -- just enough
// to prove ListServices honours the same tagFilterClause.
type serviceTagFilterFixture struct {
	s                     *SQLStore
	ctx                   context.Context
	dr, pci               string
	neither, drOnly, both string
}

func newServiceTagFilterFixture(t *testing.T, e Engine) *serviceTagFilterFixture {
	t.Helper()
	s, ctx := newStore(t, e)
	actor := mustTagFilterActor(t, s, ctx, "service-tag-filter-tester")

	envID := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)

	tag := func(code string) string {
		t.Helper()
		tg, err := domain.NewTag(NewID(), code, code, "a fixture tag for the service tag-filter suite", actor.ID, s.Now())
		if err != nil {
			t.Fatalf("building tag %s: %v", code, err)
		}
		if err := s.CreateTag(ctx, actor, tg); err != nil {
			t.Fatalf("creating tag %s: %v", code, err)
		}
		return tg.ID
	}
	dr := tag("dr")
	pci := tag("pci")

	service := func(code string, tagIDs []string) string {
		t.Helper()
		svc, err := domain.NewService(NewID(), domain.ServiceSpec{
			Code: code, Name: code, Kind: domain.SvcAPI,
			EnvironmentID: envID, Availability: domain.AvailStandalone, Tier: 2,
		}, s.Now())
		if err != nil {
			t.Fatalf("building service %s: %v", code, err)
		}
		if err := s.CreateService(ctx, actor, svc); err != nil {
			t.Fatalf("creating service %s: %v", code, err)
		}
		if len(tagIDs) > 0 {
			if err := s.SetEntityTags(ctx, actor, domain.TagEntityService, svc.ID, svc.RowVersion, tagIDs); err != nil {
				t.Fatalf("tagging service %s: %v", code, err)
			}
		}
		return svc.ID
	}

	neither := service("svc-neither", nil)
	drOnly := service("svc-dr-only", []string{dr})
	both := service("svc-dr-and-pci", []string{dr, pci})

	return &serviceTagFilterFixture{
		s: s, ctx: ctx, dr: dr, pci: pci,
		neither: neither, drOnly: drOnly, both: both,
	}
}

func serviceIDs(t *testing.T, rows []ServiceRow) []string {
	t.Helper()
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}

// TestListServicesFiltersByTwoTagsIsAND is TestListAssetsFiltersByTwoTagsIsAND's
// service twin -- the same shared tagFilterClause, proven against the other
// caller.
func TestListServicesFiltersByTwoTagsIsAND(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newServiceTagFilterFixture(t, e)

			rows, err := f.s.ListServices(f.ctx, ServiceFilter{TagIDs: []string{f.dr, f.pci}})
			if err != nil {
				t.Fatalf("ListServices: %v", err)
			}
			ids := serviceIDs(t, rows)
			if len(ids) != 1 || ids[0] != f.both {
				t.Fatalf("filtering services by [dr, pci] = %v, want exactly [%s]", ids, f.both)
			}
		})
	}
}

// TestListServicesEmptyTagFilterReturnsEverything is the service half of the
// empty-filter guard.
func TestListServicesEmptyTagFilterReturnsEverything(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newServiceTagFilterFixture(t, e)

			rows, err := f.s.ListServices(f.ctx, ServiceFilter{TagIDs: nil})
			if err != nil {
				t.Fatalf("ListServices: %v", err)
			}
			ids := serviceIDs(t, rows)
			for _, want := range []string{f.neither, f.drOnly, f.both} {
				found := false
				for _, id := range ids {
					if id == want {
						found = true
					}
				}
				if !found {
					t.Fatalf("an empty tag filter dropped a service: got %v, want it to include %s", ids, want)
				}
			}
		})
	}
}
