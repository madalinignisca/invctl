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

// WP-1.1 item 3: asset_cost becomes project-owner writable, the row half.
// This file is link_scope_test.go's shape applied to a cost line, with one
// extra wrinkle: costTable drives FOUR surfaces from one implementation, and
// only asset_cost widens. service_cost, project_cost and circuit_cost stay
// domain.ScopeTopology and Administrator-only, so this file also has to
// prove that reclassifying asset_cost did not accidentally loosen the other
// three -- and that an Administrator writing any of the four is unaffected.
//
// A second subject lives on the SAME row: costOnAsset.scoped means the line
// may declare a consumer set in asset_cost_consumer, naming arbitrary asset
// ids. Without a check there, a project owner divides their own invoice
// across somebody else's hardware and it lands in that project's totals --
// see TestCostScopeConsumerSetNamesAForeignAsset and its sibling below.

// costScopeFixture sets up two assets -- A1 (the project owner's scope) and
// A2 (unrelated) -- plus one service, one project and one circuit, which is
// all every case below needs. mine is a permit scoped to A1 only.
type costScopeFixture struct {
	s       *SQLStore
	ctx     context.Context
	a1, a2  string
	svc     string
	project string
	circuit string
	permit  domain.Permit
}

func newCostScopeFixture(t *testing.T, e Engine) *costScopeFixture {
	t.Helper()
	s, ctx := newStore(t, e)
	frontend := mustProjectForAssignment(t, s, ctx, "frontend")
	a1 := mustAsset(t, s, ctx, domain.KindServer, "a1", nil)
	a2 := mustAsset(t, s, ctx, domain.KindServer, "a2", nil)
	svc := mustService(t, s, ctx, "svc1")
	providerID := mustProvider(t, s, ctx, "Telenor")
	circuit := mustCircuit(t, s, ctx, providerID, "circuit1", nil)

	permit := domain.ScopedPermit(
		domain.Actor{ID: "po-31", Name: "po-31", Kind: domain.ActorKindUser},
		[]string{frontend},
		domain.ScopedEntities{"asset": {a1: true}})

	return &costScopeFixture{
		s: s, ctx: ctx, a1: a1, a2: a2, svc: svc, project: frontend, circuit: circuit,
		permit: permit,
	}
}

func mustChangesForCost(t *testing.T, f *costScopeFixture, entity, id string) []domain.ChangeLog {
	t.Helper()
	changes, err := f.s.ListChangesForEntity(f.ctx, entity, id, 10)
	if err != nil {
		t.Fatalf("listing changes for %s %s: %v", entity, id, err)
	}
	return changes
}

func newCostSpec() domain.CostSpec {
	return domain.CostSpec{Kind: "acquisition", Period: domain.CostMonthly, AmountMinor: 1000}
}

// TestCostScopeOnAForeignAsset: a project owner scoped to A1 alone tries to
// add a cost line to A2. Refused.
func TestCostScopeOnAForeignAsset(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCostScopeFixture(t, e)
			c, err := domain.NewCost(NewID(), newCostSpec(), f.s.Now())
			if err != nil {
				t.Fatalf("building the cost: %v", err)
			}
			before := len(mustChangesForCost(t, f, "asset_cost", c.ID))
			err = f.s.AddAssetCost(f.ctx, f.permit, f.a2, c)
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("AddAssetCost on a foreign asset = %v, want domain.ErrForbidden", err)
			}
			after := len(mustChangesForCost(t, f, "asset_cost", c.ID))
			if after != before {
				t.Errorf("a refused create wrote %d change_log rows, want %d", after, before)
			}
		})
	}
}

// TestCostScopeOnTheirOwnAsset: the same project owner adds a cost line to
// A1, which their permit covers. Allowed.
func TestCostScopeOnTheirOwnAsset(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCostScopeFixture(t, e)
			c, err := domain.NewCost(NewID(), newCostSpec(), f.s.Now())
			if err != nil {
				t.Fatalf("building the cost: %v", err)
			}
			if err := f.s.AddAssetCost(f.ctx, f.permit, f.a1, c); err != nil {
				t.Fatalf("AddAssetCost on the owner's own asset = %v, want nil", err)
			}
			changes := mustChangesForCost(t, f, "asset_cost", c.ID)
			if len(changes) != 1 {
				t.Errorf("an allowed create wrote %d change_log rows, want 1", len(changes))
			}

			// Update and retire follow the same subject -- both must also be
			// reachable through the row half this task builds.
			row, err := f.s.GetAssetCost(f.ctx, c.ID)
			if err != nil {
				t.Fatalf("reading the cost back: %v", err)
			}
			row.Cost.Note = strPtr("corrected")
			if err := f.s.UpdateAssetCost(f.ctx, f.permit, &row.Cost); err != nil {
				t.Fatalf("UpdateAssetCost on the owner's own asset cost = %v, want nil", err)
			}
			if err := f.s.RetireAssetCost(f.ctx, f.permit, f.a1, c.ID); err != nil {
				t.Fatalf("RetireAssetCost on the owner's own asset cost = %v, want nil", err)
			}
		})
	}
}

// TestCostScopeServiceCostStaysAdministratorOnly proves the reclassification
// did NOT widen service_cost, even though the caller's permit covers the
// service.
func TestCostScopeServiceCostStaysAdministratorOnly(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCostScopeFixture(t, e)
			// A permit that covers the service too, so a failure here can
			// only be the deliberate ScopeTopology refusal, not an
			// incidental scope miss.
			permit := domain.ScopedPermit(
				domain.Actor{ID: "po-32", Name: "po-32", Kind: domain.ActorKindUser},
				[]string{f.project},
				domain.ScopedEntities{"asset": {f.a1: true}, "service": {f.svc: true}})
			c, err := domain.NewCost(NewID(), newCostSpec(), f.s.Now())
			if err != nil {
				t.Fatalf("building the cost: %v", err)
			}
			err = f.s.AddServiceCost(f.ctx, permit, f.svc, c)
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("AddServiceCost by a project owner = %v, want domain.ErrForbidden", err)
			}
		})
	}
}

// TestCostScopeProjectCostStaysAdministratorOnly: same proof for
// project_cost, using the project owner's OWN project.
func TestCostScopeProjectCostStaysAdministratorOnly(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCostScopeFixture(t, e)
			c, err := domain.NewCost(NewID(), newCostSpec(), f.s.Now())
			if err != nil {
				t.Fatalf("building the cost: %v", err)
			}
			err = f.s.AddProjectCost(f.ctx, f.permit, f.project, c)
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("AddProjectCost by a project owner on their own project = %v, want domain.ErrForbidden", err)
			}
		})
	}
}

// TestCostScopeCircuitCostStaysAdministratorOnly: same proof for
// circuit_cost.
func TestCostScopeCircuitCostStaysAdministratorOnly(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCostScopeFixture(t, e)
			c, err := domain.NewCost(NewID(), newCostSpec(), f.s.Now())
			if err != nil {
				t.Fatalf("building the cost: %v", err)
			}
			err = f.s.AddCircuitCost(f.ctx, f.permit, f.circuit, c)
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("AddCircuitCost by a project owner = %v, want domain.ErrForbidden", err)
			}
		})
	}
}

// TestCostScopeConsumerSetNamesAForeignAsset is TRAP 2: a project owner
// scoped to A1 names A2 as a consumer of a cost line they legitimately own
// on A1. Refused, even though the line itself is theirs to edit.
func TestCostScopeConsumerSetNamesAForeignAsset(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCostScopeFixture(t, e)
			c, err := domain.NewCost(NewID(), newCostSpec(), f.s.Now())
			if err != nil {
				t.Fatalf("building the cost: %v", err)
			}
			if err := f.s.AddAssetCost(f.ctx, f.permit, f.a1, c); err != nil {
				t.Fatalf("seeding the cost line: %v", err)
			}
			before, err := f.s.CostConsumers(f.ctx, c.ID)
			if err != nil {
				t.Fatalf("reading consumers before: %v", err)
			}

			err = f.s.SetCostConsumers(f.ctx, f.permit, c.ID, []string{f.a1, f.a2})
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("SetCostConsumers naming a foreign asset = %v, want domain.ErrForbidden", err)
			}

			after, err := f.s.CostConsumers(f.ctx, c.ID)
			if err != nil {
				t.Fatalf("reading consumers after: %v", err)
			}
			if len(after) != len(before) {
				t.Errorf("a refused SetCostConsumers changed the consumer set: before %v, after %v", before, after)
			}
		})
	}
}

// TestCostScopeConsumerSetIsAllInScope: the same call with only A1 named
// succeeds.
func TestCostScopeConsumerSetIsAllInScope(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCostScopeFixture(t, e)
			c, err := domain.NewCost(NewID(), newCostSpec(), f.s.Now())
			if err != nil {
				t.Fatalf("building the cost: %v", err)
			}
			if err := f.s.AddAssetCost(f.ctx, f.permit, f.a1, c); err != nil {
				t.Fatalf("seeding the cost line: %v", err)
			}

			if err := f.s.SetCostConsumers(f.ctx, f.permit, c.ID, []string{f.a1}); err != nil {
				t.Fatalf("SetCostConsumers with an all-in-scope set = %v, want nil", err)
			}

			consumers, err := f.s.CostConsumers(f.ctx, c.ID)
			if err != nil {
				t.Fatalf("reading consumers: %v", err)
			}
			if len(consumers) != 1 || consumers[0] != f.a1 {
				t.Errorf("consumers = %v, want [%s]", consumers, f.a1)
			}
		})
	}
}

// TestAuthorizeCostSubjectRefusesEveryTableButAsset is the direct unit test
// for authorizeCostSubject's own "t.entity != costOnAsset.entity" guard
// (fix round 1, item 3). Every real caller already branches on
// t.entity == costOnAsset.entity before calling in, so nothing in
// internal/store's own suite reaches this line through a caller -- it is
// belt-and-braces with no strap otherwise. Called DIRECTLY, and with an
// AdministratorPermit specifically, so a failure here can only be this
// guard: an Administrator's Covers is unconditional, so if this function's
// own refusal were deleted or reordered after the Covers check, this test
// would go green for the wrong reason (Covers happening to also refuse
// costOnService, which it does not -- ScopeTopology is Administrator-only,
// but authorizeCostSubject never calls Covers for a non-asset table at
// all, by design).
func TestAuthorizeCostSubjectRefusesEveryTableButAsset(t *testing.T) {
	admin := domain.AdministratorPermit(
		domain.Actor{ID: "admin-3", Name: "admin-3", Kind: domain.ActorKindUser})
	_, err := authorizeCostSubject(admin, costOnService, "svc-1", "cost-1")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("authorizeCostSubject(admin, costOnService, ...) = %v, want domain.ErrForbidden", err)
	}
}

// TestUpdateCostAuthorizationRunsBeforeTheRetiredCheck is the POSITION test
// for updateCost's early-exit ordering (fix round 1, item 4). The BEHAVIOUR
// -- a retired foreign line refused -- already holds either way the two
// checks are ordered, which is exactly why the earlier report's prose
// argument was not itself proof: get the order backwards (retired check
// first) and this still returns an error, just the WRONG one --
// domain.ErrConflict ("is retired"), which leaks the line's lifecycle to a
// caller who is not authorized to know it exists at all, the same oracle
// RetireDependency and RetireLink were fixed against. Asserting
// errors.Is(err, domain.ErrForbidden) specifically, not merely err != nil,
// is what makes this test fail if a future refactor moves the check back
// above the authorization branch.
func TestUpdateCostAuthorizationRunsBeforeTheRetiredCheck(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCostScopeFixture(t, e)
			c, err := domain.NewCost(NewID(), newCostSpec(), f.s.Now())
			if err != nil {
				t.Fatalf("building the cost: %v", err)
			}
			// On A2, out of f.permit's scope, and retired -- as an
			// Administrator, so the fixture's own setup is never gated by
			// the thing under test.
			if err := f.s.AddAssetCost(f.ctx, testPermit, f.a2, c); err != nil {
				t.Fatalf("seeding the cost line: %v", err)
			}
			if err := f.s.RetireAssetCost(f.ctx, testPermit, f.a2, c.ID); err != nil {
				t.Fatalf("retiring the cost line as an administrator: %v", err)
			}
			row, err := f.s.GetAssetCost(f.ctx, c.ID)
			if err != nil {
				t.Fatalf("reading the cost back: %v", err)
			}
			row.Cost.Note = strPtr("a project owner should never see this accepted or refused for its lifecycle")

			err = f.s.UpdateAssetCost(f.ctx, f.permit, &row.Cost)
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("UpdateAssetCost on a retired, out-of-scope line = %v, want domain.ErrForbidden "+
					"(if this is domain.ErrConflict, authorization moved back below the retired check)", err)
			}
		})
	}
}

// TestRetireCostAuthorizationRunsBeforeTheAlreadyRetiredCheck is
// TestUpdateCostAuthorizationRunsBeforeTheRetiredCheck's sibling for
// retireCost: the "already retired -> nil" early exit sits below
// authorization today. Get that backwards and a project owner retiring an
// already-retired, out-of-scope line gets nil back -- success reported for
// a write that never happened, and a distinguishable THIRD answer alongside
// ErrForbidden (exists, not theirs) and ErrNotFound (no such row) that lets
// a caller map which foreign asset costs are already retired.
func TestRetireCostAuthorizationRunsBeforeTheAlreadyRetiredCheck(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCostScopeFixture(t, e)
			c, err := domain.NewCost(NewID(), newCostSpec(), f.s.Now())
			if err != nil {
				t.Fatalf("building the cost: %v", err)
			}
			if err := f.s.AddAssetCost(f.ctx, testPermit, f.a2, c); err != nil {
				t.Fatalf("seeding the cost line: %v", err)
			}
			if err := f.s.RetireAssetCost(f.ctx, testPermit, f.a2, c.ID); err != nil {
				t.Fatalf("retiring the cost line as an administrator: %v", err)
			}

			err = f.s.RetireAssetCost(f.ctx, f.permit, f.a2, c.ID)
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("RetireAssetCost on an already-retired, out-of-scope line = %v, want "+
					"domain.ErrForbidden (if this is nil, authorization moved back below the "+
					"already-retired check)", err)
			}
		})
	}
}

// TestCostScopeAdministrator: an AdministratorPermit covers every write on
// every one of the four surfaces, regardless of subject.
func TestCostScopeAdministrator(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCostScopeFixture(t, e)
			admin := domain.AdministratorPermit(
				domain.Actor{ID: "admin-2", Name: "admin-2", Kind: domain.ActorKindUser})

			assetCost, err := domain.NewCost(NewID(), newCostSpec(), f.s.Now())
			if err != nil {
				t.Fatalf("building the asset cost: %v", err)
			}
			if err := f.s.AddAssetCost(f.ctx, admin, f.a2, assetCost); err != nil {
				t.Fatalf("AddAssetCost as an administrator = %v, want nil", err)
			}
			if err := f.s.SetCostConsumers(f.ctx, admin, assetCost.ID, []string{f.a1, f.a2}); err != nil {
				t.Fatalf("SetCostConsumers as an administrator = %v, want nil", err)
			}
			if err := f.s.RetireAssetCost(f.ctx, admin, f.a2, assetCost.ID); err != nil {
				t.Fatalf("RetireAssetCost as an administrator = %v, want nil", err)
			}

			serviceCost, err := domain.NewCost(NewID(), newCostSpec(), f.s.Now())
			if err != nil {
				t.Fatalf("building the service cost: %v", err)
			}
			if err := f.s.AddServiceCost(f.ctx, admin, f.svc, serviceCost); err != nil {
				t.Fatalf("AddServiceCost as an administrator = %v, want nil", err)
			}

			projectCost, err := domain.NewCost(NewID(), newCostSpec(), f.s.Now())
			if err != nil {
				t.Fatalf("building the project cost: %v", err)
			}
			if err := f.s.AddProjectCost(f.ctx, admin, f.project, projectCost); err != nil {
				t.Fatalf("AddProjectCost as an administrator = %v, want nil", err)
			}

			circuitCost, err := domain.NewCost(NewID(), newCostSpec(), f.s.Now())
			if err != nil {
				t.Fatalf("building the circuit cost: %v", err)
			}
			if err := f.s.AddCircuitCost(f.ctx, admin, f.circuit, circuitCost); err != nil {
				t.Fatalf("AddCircuitCost as an administrator = %v, want nil", err)
			}
		})
	}
}
