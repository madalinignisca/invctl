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

// TestRetireRIRRefusesWhileALiveAggregateNamesIt proves the guard: a
// registry cannot be withdrawn out from under a delegation that still names
// it, and the entity must be left untouched -- no lifecycle flip, no
// change_log row -- when the refusal fires.
func TestRetireRIRRefusesWhileALiveAggregateNamesIt(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			r, err := domain.NewRIR(NewID(), "RIPE-refuse", false)
			if err != nil {
				t.Fatalf("building rir: %v", err)
			}
			if err := s.CreateRIR(ctx, testPermit, r); err != nil {
				t.Fatalf("creating rir: %v", err)
			}
			a, err := domain.NewAggregate(NewID(), "185.10.0.0/22")
			if err != nil {
				t.Fatalf("building aggregate: %v", err)
			}
			a.RIRID = &r.ID
			if err := s.CreateAggregate(ctx, testPermit, a); err != nil {
				t.Fatalf("creating aggregate: %v", err)
			}

			err = s.RetireRIR(ctx, testPermit, r.ID)
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("retiring a registry with a live aggregate = %v, want ErrConflict", err)
			}
			if !containsMsg(err, "aggregate") {
				t.Errorf("refusal %q does not name what is still in the way", err)
			}

			after, err := s.GetRIR(ctx, r.ID)
			if err != nil {
				t.Fatalf("re-reading rir: %v", err)
			}
			if after.Lifecycle != domain.LifecycleActive {
				t.Errorf("lifecycle = %q, want %q -- a refused withdrawal must leave the row untouched",
					after.Lifecycle, domain.LifecycleActive)
			}

			changes, err := s.ListChangesForEntity(ctx, "rir", r.ID, 10)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			if len(changes) != 1 { // create only
				t.Fatalf("got %d change_log rows, want 1 (create only) -- a refused withdrawal "+
					"must not be logged", len(changes))
			}
		})
	}
}

// TestRetireRIRWithdrawsWhenUnreferenced proves the ordinary path.
func TestRetireRIRWithdrawsWhenUnreferenced(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			r, err := domain.NewRIR(NewID(), "APNIC-withdraw", false)
			if err != nil {
				t.Fatalf("building rir: %v", err)
			}
			if err := s.CreateRIR(ctx, testPermit, r); err != nil {
				t.Fatalf("creating rir: %v", err)
			}

			if err := s.RetireRIR(ctx, testPermit, r.ID); err != nil {
				t.Fatalf("retiring rir: %v", err)
			}

			after, err := s.GetRIR(ctx, r.ID)
			if err != nil {
				t.Fatalf("re-reading rir: %v", err)
			}
			if after.Lifecycle != domain.LifecycleRetired {
				t.Errorf("lifecycle = %q, want %q", after.Lifecycle, domain.LifecycleRetired)
			}

			changes, err := s.ListChangesForEntity(ctx, "rir", r.ID, 10)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			if len(changes) != 2 { // create + retire
				t.Fatalf("got %d change_log rows, want 2 (create, retire)", len(changes))
			}
			if changes[0].Action != domain.ActionUpdate {
				t.Errorf("most recent action = %q, want %q (retire logs through logUpdate)",
					changes[0].Action, domain.ActionUpdate)
			}

			// A second withdrawal writes no second audit entry.
			if err := s.RetireRIR(ctx, testPermit, r.ID); err != nil {
				t.Fatalf("retiring an already-retired rir: %v", err)
			}
			changes, err = s.ListChangesForEntity(ctx, "rir", r.ID, 10)
			if err != nil {
				t.Fatalf("listing changes after second retirement: %v", err)
			}
			if len(changes) != 2 {
				t.Fatalf("got %d change_log rows after a second withdrawal, want 2 still -- "+
					"a second withdrawal must not claim a second one happened", len(changes))
			}
		})
	}
}

// TestRetireVLANGroupRefusesWhileALiveVLANNumbersWithinIt proves the guard.
func TestRetireVLANGroupRefusesWhileALiveVLANNumbersWithinIt(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			g, err := domain.NewVLANGroup(NewID(), "rack-refuse", nil)
			if err != nil {
				t.Fatalf("building vlan group: %v", err)
			}
			if err := s.CreateVLANGroup(ctx, testPermit, g); err != nil {
				t.Fatalf("creating vlan group: %v", err)
			}
			v, err := domain.NewVLAN(NewID(), 111, "vlan-in-group", &g.ID)
			if err != nil {
				t.Fatalf("building vlan: %v", err)
			}
			if err := s.CreateVLAN(ctx, testPermit, v); err != nil {
				t.Fatalf("creating vlan: %v", err)
			}

			err = s.RetireVLANGroup(ctx, testPermit, g.ID)
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("retiring a group with a live vlan = %v, want ErrConflict", err)
			}
			if !containsMsg(err, "VLAN") {
				t.Errorf("refusal %q does not name what is still in the way", err)
			}

			after, err := s.GetVLANGroup(ctx, g.ID)
			if err != nil {
				t.Fatalf("re-reading vlan group: %v", err)
			}
			if after.Lifecycle != domain.LifecycleActive {
				t.Errorf("lifecycle = %q, want %q", after.Lifecycle, domain.LifecycleActive)
			}

			changes, err := s.ListChangesForEntity(ctx, "vlan_group", g.ID, 10)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			if len(changes) != 1 {
				t.Fatalf("got %d change_log rows, want 1 (create only)", len(changes))
			}
		})
	}
}

// TestRetireVLANGroupWithdrawsWhenEmpty proves the ordinary path.
func TestRetireVLANGroupWithdrawsWhenEmpty(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			g, err := domain.NewVLANGroup(NewID(), "rack-empty", nil)
			if err != nil {
				t.Fatalf("building vlan group: %v", err)
			}
			if err := s.CreateVLANGroup(ctx, testPermit, g); err != nil {
				t.Fatalf("creating vlan group: %v", err)
			}

			if err := s.RetireVLANGroup(ctx, testPermit, g.ID); err != nil {
				t.Fatalf("retiring vlan group: %v", err)
			}

			after, err := s.GetVLANGroup(ctx, g.ID)
			if err != nil {
				t.Fatalf("re-reading vlan group: %v", err)
			}
			if after.Lifecycle != domain.LifecycleRetired {
				t.Errorf("lifecycle = %q, want %q", after.Lifecycle, domain.LifecycleRetired)
			}

			changes, err := s.ListChangesForEntity(ctx, "vlan_group", g.ID, 10)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			if len(changes) != 2 {
				t.Fatalf("got %d change_log rows, want 2 (create, retire)", len(changes))
			}

			if err := s.RetireVLANGroup(ctx, testPermit, g.ID); err != nil {
				t.Fatalf("retiring an already-retired group: %v", err)
			}
			changes, err = s.ListChangesForEntity(ctx, "vlan_group", g.ID, 10)
			if err != nil {
				t.Fatalf("listing changes after second retirement: %v", err)
			}
			if len(changes) != 2 {
				t.Fatalf("got %d change_log rows after a second withdrawal, want 2 still", len(changes))
			}
		})
	}
}

// TestRetireBackendPoolRefusesWhileALiveRoutePointsAtIt proves the first hold.
func TestRetireBackendPoolRefusesWhileALiveRoutePointsAtIt(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			svc := mustService(t, s, ctx, "pool-route-svc")
			frontend := mustEndpoint(t, s, ctx, svc, "front", 8001)
			pool, err := domain.NewBackendPool(NewID(), svc, "fronted-pool", nil)
			if err != nil {
				t.Fatalf("building pool: %v", err)
			}
			if err := s.CreateBackendPool(ctx, testPermit, pool); err != nil {
				t.Fatalf("creating pool: %v", err)
			}
			route, err := domain.NewRoute(NewID(), frontend, "default", pool.ID)
			if err != nil {
				t.Fatalf("building route: %v", err)
			}
			if err := s.CreateRoute(ctx, testPermit, route); err != nil {
				t.Fatalf("creating route: %v", err)
			}

			err = s.RetireBackendPool(ctx, testPermit, pool.ID)
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("retiring a pool with a live route = %v, want ErrConflict", err)
			}
			if !containsMsg(err, "route") {
				t.Errorf("refusal %q does not name what is still in the way", err)
			}

			after, err := s.GetBackendPool(ctx, pool.ID)
			if err != nil {
				t.Fatalf("re-reading pool: %v", err)
			}
			if after.Lifecycle != domain.LifecycleActive {
				t.Errorf("lifecycle = %q, want %q", after.Lifecycle, domain.LifecycleActive)
			}

			changes, err := s.ListChangesForEntity(ctx, "backend_pool", pool.ID, 10)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			if len(changes) != 1 {
				t.Fatalf("got %d change_log rows, want 1 (create only)", len(changes))
			}
		})
	}
}

// TestRetireBackendPoolRefusesWhileAMemberIsDeclared proves the second hold,
// independent of the first: a pool with no routes but a live member is still
// in service.
func TestRetireBackendPoolRefusesWhileAMemberIsDeclared(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			svc := mustService(t, s, ctx, "pool-member-svc")
			backend := mustEndpoint(t, s, ctx, svc, "backend-1", 9001)
			pool, err := domain.NewBackendPool(NewID(), svc, "staffed-pool", nil)
			if err != nil {
				t.Fatalf("building pool: %v", err)
			}
			if err := s.CreateBackendPool(ctx, testPermit, pool); err != nil {
				t.Fatalf("creating pool: %v", err)
			}
			member := &domain.BackendMember{PoolID: pool.ID, EndpointID: backend, Weight: 1}
			if err := s.AddBackendMember(ctx, testPermit, member); err != nil {
				t.Fatalf("adding backend member: %v", err)
			}

			err = s.RetireBackendPool(ctx, testPermit, pool.ID)
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("retiring a pool with a live member = %v, want ErrConflict", err)
			}
			if !containsMsg(err, "member") {
				t.Errorf("refusal %q does not name what is still in the way", err)
			}

			after, err := s.GetBackendPool(ctx, pool.ID)
			if err != nil {
				t.Fatalf("re-reading pool: %v", err)
			}
			if after.Lifecycle != domain.LifecycleActive {
				t.Errorf("lifecycle = %q, want %q", after.Lifecycle, domain.LifecycleActive)
			}
		})
	}
}

// TestRetireBackendPoolWithdrawsWhenEmptyAndFreesTheName proves the ordinary
// path AND the whole reason migration 00070's unique index had to become
// partial: a service must be able to re-declare a pool under a name it just
// withdrew. If the index were still a plain table-wide UNIQUE, the second
// CreateBackendPool below would fail.
func TestRetireBackendPoolWithdrawsWhenEmptyAndFreesTheName(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			svc := mustService(t, s, ctx, "pool-reuse-svc")
			pool, err := domain.NewBackendPool(NewID(), svc, "reused-name", nil)
			if err != nil {
				t.Fatalf("building pool: %v", err)
			}
			if err := s.CreateBackendPool(ctx, testPermit, pool); err != nil {
				t.Fatalf("creating pool: %v", err)
			}

			if err := s.RetireBackendPool(ctx, testPermit, pool.ID); err != nil {
				t.Fatalf("retiring pool: %v", err)
			}

			after, err := s.GetBackendPool(ctx, pool.ID)
			if err != nil {
				t.Fatalf("re-reading pool: %v", err)
			}
			if after.Lifecycle != domain.LifecycleRetired {
				t.Errorf("lifecycle = %q, want %q", after.Lifecycle, domain.LifecycleRetired)
			}

			changes, err := s.ListChangesForEntity(ctx, "backend_pool", pool.ID, 10)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			if len(changes) != 2 {
				t.Fatalf("got %d change_log rows, want 2 (create, retire)", len(changes))
			}

			// THE PAYOFF: a service can re-declare a pool under the name it
			// just withdrew. This fails outright if migration 00070's
			// partial unique index did not actually replace the table-wide
			// UNIQUE (service_id, name) constraint.
			again, err := domain.NewBackendPool(NewID(), svc, "reused-name", nil)
			if err != nil {
				t.Fatalf("building the replacement pool: %v", err)
			}
			if err := s.CreateBackendPool(ctx, testPermit, again); err != nil {
				t.Fatalf("declaring a new pool under a withdrawn name failed: %v -- "+
					"the live-scoped unique index from migration 00070 did not do its job", err)
			}

			// A second withdrawal of the original pool writes no second entry.
			if err := s.RetireBackendPool(ctx, testPermit, pool.ID); err != nil {
				t.Fatalf("retiring an already-retired pool: %v", err)
			}
			changes, err = s.ListChangesForEntity(ctx, "backend_pool", pool.ID, 10)
			if err != nil {
				t.Fatalf("listing changes after second retirement: %v", err)
			}
			if len(changes) != 2 {
				t.Fatalf("got %d change_log rows after a second withdrawal, want 2 still", len(changes))
			}
		})
	}
}

// TestRetireRouteRefusesWhileALiveDependencyNamesIt proves the guard.
func TestRetireRouteRefusesWhileALiveDependencyNamesIt(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			providerSvc := mustService(t, s, ctx, "route-provider-svc")
			consumerSvc := mustService(t, s, ctx, "route-consumer-svc")
			frontend := mustEndpoint(t, s, ctx, providerSvc, "front", 8443)
			pool, err := domain.NewBackendPool(NewID(), providerSvc, "route-pool", nil)
			if err != nil {
				t.Fatalf("building pool: %v", err)
			}
			if err := s.CreateBackendPool(ctx, testPermit, pool); err != nil {
				t.Fatalf("creating pool: %v", err)
			}
			route, err := domain.NewRoute(NewID(), frontend, "default", pool.ID)
			if err != nil {
				t.Fatalf("building route: %v", err)
			}
			if err := s.CreateRoute(ctx, testPermit, route); err != nil {
				t.Fatalf("creating route: %v", err)
			}

			dep, err := domain.NewDependency(NewID(), domain.DependencySpec{
				ConsumerServiceID: consumerSvc,
				ProviderRouteID:   &route.ID,
				Nature:            domain.NatureHard,
				FailureMode:       "fails closed",
			}, s.Now())
			if err != nil {
				t.Fatalf("building dependency: %v", err)
			}
			if err := s.CreateDependency(ctx, testPermit, dep, nil); err != nil {
				t.Fatalf("creating dependency: %v", err)
			}

			err = s.RetireRoute(ctx, testPermit, route.ID)
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("retiring a route with a live dependency = %v, want ErrConflict", err)
			}
			if !containsMsg(err, "depend") {
				t.Errorf("refusal %q does not name what is still in the way", err)
			}

			after, err := s.GetRoute(ctx, route.ID)
			if err != nil {
				t.Fatalf("re-reading route: %v", err)
			}
			if after.Lifecycle != domain.LifecycleActive {
				t.Errorf("lifecycle = %q, want %q", after.Lifecycle, domain.LifecycleActive)
			}

			changes, err := s.ListChangesForEntity(ctx, "route", route.ID, 10)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			if len(changes) != 1 {
				t.Fatalf("got %d change_log rows, want 1 (create only)", len(changes))
			}
		})
	}
}

// TestRetireRouteWithdrawsWhenUndepended proves the ordinary path.
func TestRetireRouteWithdrawsWhenUndepended(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			svc := mustService(t, s, ctx, "route-free-svc")
			frontend := mustEndpoint(t, s, ctx, svc, "front", 8444)
			pool, err := domain.NewBackendPool(NewID(), svc, "free-pool", nil)
			if err != nil {
				t.Fatalf("building pool: %v", err)
			}
			if err := s.CreateBackendPool(ctx, testPermit, pool); err != nil {
				t.Fatalf("creating pool: %v", err)
			}
			route, err := domain.NewRoute(NewID(), frontend, "default", pool.ID)
			if err != nil {
				t.Fatalf("building route: %v", err)
			}
			if err := s.CreateRoute(ctx, testPermit, route); err != nil {
				t.Fatalf("creating route: %v", err)
			}

			if err := s.RetireRoute(ctx, testPermit, route.ID); err != nil {
				t.Fatalf("retiring route: %v", err)
			}

			after, err := s.GetRoute(ctx, route.ID)
			if err != nil {
				t.Fatalf("re-reading route: %v", err)
			}
			if after.Lifecycle != domain.LifecycleRetired {
				t.Errorf("lifecycle = %q, want %q", after.Lifecycle, domain.LifecycleRetired)
			}

			changes, err := s.ListChangesForEntity(ctx, "route", route.ID, 10)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			if len(changes) != 2 {
				t.Fatalf("got %d change_log rows, want 2 (create, retire)", len(changes))
			}

			if err := s.RetireRoute(ctx, testPermit, route.ID); err != nil {
				t.Fatalf("retiring an already-retired route: %v", err)
			}
			changes, err = s.ListChangesForEntity(ctx, "route", route.ID, 10)
			if err != nil {
				t.Fatalf("listing changes after second retirement: %v", err)
			}
			if len(changes) != 2 {
				t.Fatalf("got %d change_log rows after a second withdrawal, want 2 still", len(changes))
			}
		})
	}
}
