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
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Projects: ownership, sharing, and the audit obligation that comes with both.

type projectFixture struct {
	s        *SQLStore
	ctx      context.Context
	env      string
	projects map[string]string
	assets   map[string]string
	services map[string]string
}

func newProjectFixture(t *testing.T, e Engine) *projectFixture {
	t.Helper()
	s, ctx := newStore(t, e)
	// PINNED CLOCK. A cost line with no explicit window gets valid_from =
	// now(), and every assertion here asks for totals as at costNow. With the
	// real clock those agree only on the day the test was written: once UTC
	// rolled past it, valid_from was LATER than the as-at date, the window
	// filter excluded every line, and six tests started reporting totals of
	// zero. They did not detect a regression -- they expired.
	s = s.WithClock(func() time.Time { return costNow })
	f := &projectFixture{
		s: s, ctx: ctx,
		projects: map[string]string{},
		assets:   map[string]string{},
		services: map[string]string{},
	}
	f.env = mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)

	for _, code := range []string{"orders", "platform"} {
		p, err := domain.NewProject(NewID(), domain.ProjectSpec{Code: code, Name: code}, s.Now())
		if err != nil {
			t.Fatalf("building project %s: %v", code, err)
		}
		if err := s.CreateProject(ctx, testActor, p); err != nil {
			t.Fatalf("creating project %s: %v", code, err)
		}
		f.projects[code] = p.ID
	}

	f.assets["hv-01"] = mustAsset(t, s, ctx, domain.KindHypervisor, "hv-01", nil, f.env)
	hv := f.assets["hv-01"]
	f.assets["vm-app-1"] = mustAsset(t, s, ctx, domain.KindVM, "vm-app-1", &hv, f.env)
	f.assets["sw-core-1"] = mustAsset(t, s, ctx, domain.KindSwitch, "sw-core-1", nil, f.env)

	for _, code := range []string{"orders-api", "pgsql-core"} {
		svc, err := domain.NewService(NewID(), domain.ServiceSpec{
			Code: code, Name: code, Kind: domain.SvcAPI,
			EnvironmentID: f.env, Availability: domain.AvailStandalone, Tier: 2,
		}, s.Now())
		if err != nil {
			t.Fatalf("building service %s: %v", code, err)
		}
		if err := s.CreateService(ctx, testActor, svc); err != nil {
			t.Fatalf("creating service %s: %v", code, err)
		}
		f.services[code] = svc.ID
	}
	return f
}

func (f *projectFixture) link(t *testing.T, project, asset, relation string) error {
	t.Helper()
	l, err := domain.NewProjectAssetLink(f.projects[project], f.assets[asset], relation, nil, f.s.Now())
	if err != nil {
		t.Fatalf("building link: %v", err)
	}
	return f.s.LinkProjectAsset(f.ctx, testActor, l)
}

// linkService is the service half of link, added when the cost rollup needed
// to prove a service's price reaches its project's total.
func (f *projectFixture) linkService(t *testing.T, project, service, relation string) error {
	t.Helper()
	l, err := domain.NewProjectServiceLink(f.projects[project], f.services[service], relation, nil, f.s.Now())
	if err != nil {
		t.Fatalf("building service link: %v", err)
	}
	return f.s.LinkProjectService(f.ctx, testActor, l)
}

func (f *projectFixture) changeRows(t *testing.T, entityType string) int64 {
	t.Helper()
	n, err := f.s.countOne(f.ctx,
		`SELECT COUNT(*) FROM change_log WHERE entity_type = ?`, entityType)
	if err != nil {
		t.Fatalf("counting change_log rows: %v", err)
	}
	return n
}

// TestAtMostOneProjectOwnsAnAsset.
//
// The two inserts come from DIFFERENT projects on purpose. Using one project
// twice would go green on the (project_id, asset_id) primary key even if
// idx_project_asset_owner did not exist at all -- the owner rule is only
// exercised by a second project, and a test that cannot fail without the index
// is not a test of the index.
func TestAtMostOneProjectOwnsAnAsset(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)

			if err := f.link(t, "orders", "hv-01", domain.ProjectOwns); err != nil {
				t.Fatalf("first owner: %v", err)
			}
			err := f.link(t, "platform", "hv-01", domain.ProjectOwns)
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("a second project owned the same asset: %v", err)
			}
			// The message must name the current owner, or the operator is left
			// guessing which of a hundred projects has the slot.
			if !strings.Contains(err.Error(), "orders") {
				t.Errorf("the conflict does not name the owning project: %v", err)
			}

			// A `uses` link from the same second project is fine -- that is the
			// whole asymmetry.
			if err := f.link(t, "platform", "hv-01", domain.ProjectUses); err != nil {
				t.Errorf("a second project could not USE an owned asset: %v", err)
			}
		})
	}
}

// TestRetiringALinkFreesTheOwnerSlot: the partial index is on active rows, so
// releasing a link must let somebody else own the thing.
func TestRetiringALinkFreesTheOwnerSlot(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			if err := f.link(t, "orders", "hv-01", domain.ProjectOwns); err != nil {
				t.Fatalf("first owner: %v", err)
			}
			if err := f.s.RetireProjectAsset(f.ctx, testActor,
				f.projects["orders"], f.assets["hv-01"]); err != nil {
				t.Fatalf("retiring the link: %v", err)
			}
			if err := f.link(t, "platform", "hv-01", domain.ProjectOwns); err != nil {
				t.Errorf("the owner slot was not freed by retiring the link: %v", err)
			}
		})
	}
}

// TestRetiringAProjectReleasesItsLinks.
//
// Without the cascade a retired project holds the owner slot forever: the
// index only cares that the LINK is active, so nobody could ever own those
// assets again and no screen would explain why.
func TestRetiringAProjectReleasesItsLinks(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			if err := f.link(t, "orders", "hv-01", domain.ProjectOwns); err != nil {
				t.Fatalf("linking: %v", err)
			}
			svc, err := domain.NewProjectServiceLink(
				f.projects["orders"], f.services["orders-api"], domain.ProjectOwns, nil, f.s.Now())
			if err != nil {
				t.Fatalf("building service link: %v", err)
			}
			if err := f.s.LinkProjectService(f.ctx, testActor, svc); err != nil {
				t.Fatalf("linking service: %v", err)
			}

			before := f.changeRows(t, "project_asset")
			if err := f.s.RetireProject(f.ctx, testActor, f.projects["orders"]); err != nil {
				t.Fatalf("retiring the project: %v", err)
			}
			// Counted HERE, before the re-link attempts below add rows of their
			// own. Reading it after them was this test's first bug, and it
			// would have looked like the cascade double-logging.
			released := f.changeRows(t, "project_asset")

			if err := f.link(t, "platform", "hv-01", domain.ProjectOwns); err != nil {
				t.Errorf("a retired project still holds the owner slot: %v", err)
			}
			svc2, _ := domain.NewProjectServiceLink(
				f.projects["platform"], f.services["orders-api"], domain.ProjectOwns, nil, f.s.Now())
			if err := f.s.LinkProjectService(f.ctx, testActor, svc2); err != nil {
				t.Errorf("the service's owner slot was not released: %v", err)
			}

			// The release is audited, not silent: "why can I own this now" has
			// to have an answer in the log.
			if released != before+1 {
				t.Errorf("project_asset change_log rows went %d -> %d across the retirement, "+
					"want exactly one more for the released link", before, released)
			}
		})
	}
}

// TestLinkingIsAudited asserts the DELTA, not that rows exist. Creating the
// project already wrote one, so "at least one row" is green before the link
// is even attempted.
func TestLinkingIsAudited(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)

			before := f.changeRows(t, "project_asset")
			if err := f.link(t, "orders", "hv-01", domain.ProjectUses); err != nil {
				t.Fatalf("linking: %v", err)
			}
			if got := f.changeRows(t, "project_asset"); got != before+1 {
				t.Fatalf("change_log rows went %d -> %d, want exactly one create", before, got)
			}

			// Changing the relation is an update of the same link, not a second
			// create -- the row is keyed on (project, asset).
			if err := f.link(t, "orders", "hv-01", domain.ProjectOwns); err != nil {
				t.Fatalf("changing the relation: %v", err)
			}
			if got := f.changeRows(t, "project_asset"); got != before+2 {
				t.Errorf("change_log rows went to %d, want one more for the update", got)
			}

			assets, err := f.s.ListProjectAssets(f.ctx, f.projects["orders"])
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if len(assets) != 1 {
				t.Fatalf("the link was duplicated rather than updated: %d rows", len(assets))
			}
			if assets[0].Relation != domain.ProjectOwns {
				t.Errorf("relation = %q, want the updated value", assets[0].Relation)
			}
		})
	}
}

// TestRelinkingIdenticallyWritesNoAuditRow: diffJSON returns no change, so
// there is nothing to record. A log full of no-op rows is a log nobody reads.
func TestRelinkingIdenticallyWritesNoAuditRow(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			if err := f.link(t, "orders", "hv-01", domain.ProjectOwns); err != nil {
				t.Fatalf("linking: %v", err)
			}
			before := f.changeRows(t, "project_asset")
			if err := f.link(t, "orders", "hv-01", domain.ProjectOwns); err != nil {
				t.Fatalf("relinking: %v", err)
			}
			if got := f.changeRows(t, "project_asset"); got != before {
				t.Errorf("an identical relink wrote %d audit rows", got-before)
			}
		})
	}
}

// TestProjectListCountsOwnedAndUsedSeparately. The counts drive the list page,
// and conflating them would make a project that owns nothing look like one
// that owns everything it touches.
func TestProjectListCountsOwnedAndUsedSeparately(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			if err := f.link(t, "orders", "hv-01", domain.ProjectOwns); err != nil {
				t.Fatalf("linking: %v", err)
			}
			if err := f.link(t, "orders", "sw-core-1", domain.ProjectUses); err != nil {
				t.Fatalf("linking: %v", err)
			}

			rows, err := f.s.ListProjects(f.ctx, ProjectFilter{})
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			var orders *ProjectRow
			for i := range rows {
				if rows[i].Code == "orders" {
					orders = &rows[i]
				}
			}
			if orders == nil {
				t.Fatal("the project is missing from the list")
			}
			if orders.OwnedAssets != 1 || orders.UsedAssets != 1 {
				t.Errorf("owned=%d used=%d, want 1 and 1 -- counted separately",
					orders.OwnedAssets, orders.UsedAssets)
			}
			// And a retired project drops out of the default list.
			if err := f.s.RetireProject(f.ctx, testActor, f.projects["orders"]); err != nil {
				t.Fatalf("retiring: %v", err)
			}
			after, err := f.s.ListProjects(f.ctx, ProjectFilter{})
			if err != nil {
				t.Fatalf("listing after retirement: %v", err)
			}
			for _, r := range after {
				if r.Code == "orders" {
					t.Error("a retired project is still in the default list")
				}
			}
		})
	}
}

// TestProjectRejectsWhatItShould covers the constructor and the store.
func TestProjectRejectsWhatItShould(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)

			if _, err := domain.NewProject(NewID(), domain.ProjectSpec{Name: "no code"}, f.s.Now()); !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("a project with no code: %v, want ErrInvalid", err)
			}
			if _, err := domain.NewProjectAssetLink("p", "a", "sponsors", nil, f.s.Now()); !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("an invented relation: %v, want ErrInvalid", err)
			}

			// Duplicate code: the unique index is the authority.
			dup, err := domain.NewProject(NewID(), domain.ProjectSpec{Code: "orders", Name: "Clash"}, f.s.Now())
			if err != nil {
				t.Fatalf("building: %v", err)
			}
			if err := f.s.CreateProject(f.ctx, testActor, dup); !errors.Is(err, domain.ErrConflict) {
				t.Errorf("a duplicate code: %v, want ErrConflict", err)
			}

			// A link to an asset that does not exist is refused by the foreign
			// key rather than stored as a dangling row.
			bad, err := domain.NewProjectAssetLink(f.projects["orders"], "no-such-asset", domain.ProjectUses, nil, f.s.Now())
			if err != nil {
				t.Fatalf("building: %v", err)
			}
			if err := f.s.LinkProjectAsset(f.ctx, testActor, bad); err == nil {
				t.Error("a link to a non-existent asset was stored")
			}
		})
	}
}

// TestProjectsForAssetsGroupsByEntity feeds the pills on pages that know
// nothing about projects.
func TestProjectsForAssetsGroupsByEntity(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProjectFixture(t, e)
			if err := f.link(t, "orders", "hv-01", domain.ProjectOwns); err != nil {
				t.Fatalf("linking: %v", err)
			}
			if err := f.link(t, "platform", "hv-01", domain.ProjectUses); err != nil {
				t.Fatalf("linking: %v", err)
			}

			byAsset, err := f.s.ProjectsForAssets(f.ctx, []string{f.assets["hv-01"], f.assets["sw-core-1"]})
			if err != nil {
				t.Fatalf("loading: %v", err)
			}
			if got := len(byAsset[f.assets["hv-01"]]); got != 2 {
				t.Errorf("hv-01 has %d project links, want both the owner and the user", got)
			}
			if _, ok := byAsset[f.assets["sw-core-1"]]; ok {
				t.Error("an unlinked asset has an entry; the map would render an empty pill row")
			}
		})
	}
}
