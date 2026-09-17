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
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// TestUpdateBackendPoolCorrectsAndAudits.
func TestUpdateBackendPoolCorrectsAndAudits(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			svc := mustService(t, s, ctx, "lb-svc")
			pool, err := domain.NewBackendPool(NewID(), svc, "orders-pool-typo", ptr("roundrobin"))
			if err != nil {
				t.Fatalf("building pool: %v", err)
			}
			if err := s.CreateBackendPool(ctx, testPermit, pool); err != nil {
				t.Fatalf("creating pool: %v", err)
			}

			got, err := s.GetBackendPool(ctx, pool.ID)
			if err != nil {
				t.Fatalf("getting pool: %v", err)
			}
			got.Name = "orders-pool"
			got.LBAlgorithm = ptr("leastconn")
			if err := s.UpdateBackendPool(ctx, testPermit, got); err != nil {
				t.Fatalf("updating pool: %v", err)
			}

			after, err := s.GetBackendPool(ctx, pool.ID)
			if err != nil {
				t.Fatalf("re-reading pool: %v", err)
			}
			if after.Name != "orders-pool" {
				t.Errorf("name = %q, want %q", after.Name, "orders-pool")
			}
			if after.LBAlgorithm == nil || *after.LBAlgorithm != "leastconn" {
				t.Errorf("lb_algorithm = %v, want leastconn", after.LBAlgorithm)
			}

			changes, err := s.ListChangesForEntity(ctx, "backend_pool", pool.ID, 10)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			if len(changes) != 2 {
				t.Fatalf("got %d change_log rows, want 2 (create, update)", len(changes))
			}
		})
	}
}

// TestUpdateBackendPoolPinsServiceID proves the pin, rather than assuming it:
// a caller handing UpdateBackendPool a DIFFERENT service_id must have the
// STORED one written, never the submitted one. Re-pointing a pool's service is
// a rebuild, not a correction -- UpdateBackendPool's own doc comment gives the
// reasoning -- and this is the test that would go red if a future edit let it
// through.
//
// TWO THINGS ARE CHECKED, deliberately: the stored column (the UPDATE
// statement never names service_id, so that alone is safe regardless of the
// pin) AND the change_log diff, the same shape
// TestUpdateLinkCannotMoveOrWithdrawACableThatWasNotTouched proves for Link.
// logUpdate diffs the Go STRUCT, not the columns the SQL actually touched, so
// a forged service_id that reaches the struct still reaches the audit trail
// as a claim that the pool moved to another service, even though the row
// itself never did -- and that is the regression the column-only assertion
// alone cannot catch.
func TestUpdateBackendPoolPinsServiceID(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			svcA := mustService(t, s, ctx, "svc-a")
			svcB := mustService(t, s, ctx, "svc-b")
			pool, err := domain.NewBackendPool(NewID(), svcA, "shared-name", nil)
			if err != nil {
				t.Fatalf("building pool: %v", err)
			}
			if err := s.CreateBackendPool(ctx, testPermit, pool); err != nil {
				t.Fatalf("creating pool: %v", err)
			}

			got, err := s.GetBackendPool(ctx, pool.ID)
			if err != nil {
				t.Fatalf("getting pool: %v", err)
			}
			got.ServiceID = svcB // the forged value this method must ignore
			got.Name = "shared-name-corrected"
			if err := s.UpdateBackendPool(ctx, testPermit, got); err != nil {
				t.Fatalf("updating pool: %v", err)
			}

			after, err := s.GetBackendPool(ctx, pool.ID)
			if err != nil {
				t.Fatalf("re-reading pool: %v", err)
			}
			if after.ServiceID != svcA {
				t.Errorf("service_id = %q, want the stored %q -- UpdateBackendPool let a "+
					"submitted service_id through instead of pinning the stored one", after.ServiceID, svcA)
			}

			var diffs []string
			if err := s.DB().Reader.Select(&diffs, s.DB().Reader.Rebind(
				`SELECT COALESCE(diff, '') FROM change_log
				  WHERE entity_id = ? AND entity_type = 'backend_pool' AND action = 'update'`), pool.ID); err != nil {
				t.Fatalf("reading the change log: %v", err)
			}
			if len(diffs) == 0 {
				t.Fatal("no update was logged, so this test is checking nothing")
			}
			for _, d := range diffs {
				if strings.Contains(d, "service_id") {
					t.Errorf("change_log records a service move that did not happen: %s", d)
				}
			}
		})
	}
}

// TestUpdateBackendPoolRefusesAStaleToken.
func TestUpdateBackendPoolRefusesAStaleToken(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			svc := mustService(t, s, ctx, "stale-pool-svc")
			pool, err := domain.NewBackendPool(NewID(), svc, "contended-pool", nil)
			if err != nil {
				t.Fatalf("building pool: %v", err)
			}
			if err := s.CreateBackendPool(ctx, testPermit, pool); err != nil {
				t.Fatalf("creating pool: %v", err)
			}

			first, err := s.GetBackendPool(ctx, pool.ID)
			if err != nil {
				t.Fatalf("first read: %v", err)
			}
			second, err := s.GetBackendPool(ctx, pool.ID)
			if err != nil {
				t.Fatalf("second read: %v", err)
			}

			first.Name = "contended-pool-first"
			if err := s.UpdateBackendPool(ctx, testPermit, first); err != nil {
				t.Fatalf("the first write must succeed: %v", err)
			}

			second.Name = "contended-pool-second"
			err = s.UpdateBackendPool(ctx, testPermit, second)
			if err == nil {
				t.Fatal("the second, stale write succeeded")
			}
			if !errors.Is(err, domain.ErrStale) {
				t.Errorf("error = %v, want domain.ErrStale", err)
			}
		})
	}
}

// TestUpdateBackendPoolRefusesAnInvalidValue.
func TestUpdateBackendPoolRefusesAnInvalidValue(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			svc := mustService(t, s, ctx, "invalid-pool-svc")
			pool, err := domain.NewBackendPool(NewID(), svc, "pool-a", nil)
			if err != nil {
				t.Fatalf("building pool: %v", err)
			}
			if err := s.CreateBackendPool(ctx, testPermit, pool); err != nil {
				t.Fatalf("creating pool: %v", err)
			}

			pool.Name = "   "
			err = s.UpdateBackendPool(ctx, testPermit, pool)
			if err == nil {
				t.Fatal("a blank name was accepted")
			}
			ve, ok := domain.AsValidation(err)
			if !ok {
				t.Fatalf("error = %v (%T), want a *ValidationError", err, err)
			}
			if _, named := ve.Messages()["name"]; !named {
				t.Errorf("the refusal names %v, not %q", ve.Messages(), "name")
			}
		})
	}
}

// TestUpdateRoutePinsFrontendAndPool proves the two pins UpdateRoute's own
// doc comment claims: handed a DIFFERENT frontend_endpoint_id AND a DIFFERENT
// backend_pool_id, it must write the STORED ones. Re-pointing a route is a
// different act from correcting its match value -- doing it through a
// correction form is the seizure surface the plan calls out by name.
//
// The stored columns AND the change_log diff are both checked, for the reason
// TestUpdateBackendPoolPinsServiceID's own comment gives: the UPDATE statement
// never names either foreign key, so the row is safe regardless of the pin --
// it is logUpdate's struct diff that would otherwise carry a false claim that
// this route was re-pointed.
func TestUpdateRoutePinsFrontendAndPool(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			svc := mustService(t, s, ctx, "routing-svc")
			frontendA := mustEndpoint(t, s, ctx, svc, "front-a", 8443)
			frontendB := mustEndpoint(t, s, ctx, svc, "front-b", 8444)
			poolA, err := domain.NewBackendPool(NewID(), svc, "pool-a", nil)
			if err != nil {
				t.Fatalf("building pool A: %v", err)
			}
			if err := s.CreateBackendPool(ctx, testPermit, poolA); err != nil {
				t.Fatalf("creating pool A: %v", err)
			}
			poolB, err := domain.NewBackendPool(NewID(), svc, "pool-b", nil)
			if err != nil {
				t.Fatalf("building pool B: %v", err)
			}
			if err := s.CreateBackendPool(ctx, testPermit, poolB); err != nil {
				t.Fatalf("creating pool B: %v", err)
			}

			route, err := domain.NewRoute(NewID(), frontendA, "host_header", poolA.ID)
			if err != nil {
				t.Fatalf("building route: %v", err)
			}
			route.MatchValue = ptr("orders.example.com")
			if err := s.CreateRoute(ctx, testPermit, route); err != nil {
				t.Fatalf("creating route: %v", err)
			}

			got, err := s.GetRoute(ctx, route.ID)
			if err != nil {
				t.Fatalf("getting route: %v", err)
			}
			// The forged values this method must ignore.
			got.FrontendEndpointID = frontendB
			got.BackendPoolID = poolB.ID
			got.MatchValue = ptr("orders-corrected.example.com")
			if err := s.UpdateRoute(ctx, testPermit, got); err != nil {
				t.Fatalf("updating route: %v", err)
			}

			after, err := s.GetRoute(ctx, route.ID)
			if err != nil {
				t.Fatalf("re-reading route: %v", err)
			}
			if after.FrontendEndpointID != frontendA {
				t.Errorf("frontend_endpoint_id = %q, want the stored %q -- UpdateRoute let a "+
					"submitted frontend through", after.FrontendEndpointID, frontendA)
			}
			if after.BackendPoolID != poolA.ID {
				t.Errorf("backend_pool_id = %q, want the stored %q -- UpdateRoute let a "+
					"submitted pool through, which is exactly the seizure surface the plan "+
					"calls out by name", after.BackendPoolID, poolA.ID)
			}
			// The genuinely correctable field DID go through.
			if after.MatchValue == nil || *after.MatchValue != "orders-corrected.example.com" {
				t.Errorf("match_value = %v, want the correction to have taken", after.MatchValue)
			}

			var diffs []string
			if err := s.DB().Reader.Select(&diffs, s.DB().Reader.Rebind(
				`SELECT COALESCE(diff, '') FROM change_log
				  WHERE entity_id = ? AND entity_type = 'route' AND action = 'update'`), route.ID); err != nil {
				t.Fatalf("reading the change log: %v", err)
			}
			if len(diffs) == 0 {
				t.Fatal("no update was logged, so this test is checking nothing")
			}
			for _, d := range diffs {
				if strings.Contains(d, "frontend_endpoint_id") {
					t.Errorf("change_log records a frontend move that did not happen: %s", d)
				}
				if strings.Contains(d, "backend_pool_id") {
					t.Errorf("change_log records a pool re-point that did not happen: %s -- "+
						"exactly the seizure the plan calls out by name", d)
				}
			}
		})
	}
}

// TestUpdateRouteRefusesAStaleToken.
func TestUpdateRouteRefusesAStaleToken(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			svc := mustService(t, s, ctx, "stale-route-svc")
			frontend := mustEndpoint(t, s, ctx, svc, "front", 9443)
			pool, err := domain.NewBackendPool(NewID(), svc, "contended-route-pool", nil)
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

			first, err := s.GetRoute(ctx, route.ID)
			if err != nil {
				t.Fatalf("first read: %v", err)
			}
			second, err := s.GetRoute(ctx, route.ID)
			if err != nil {
				t.Fatalf("second read: %v", err)
			}

			first.Priority = 50
			if err := s.UpdateRoute(ctx, testPermit, first); err != nil {
				t.Fatalf("the first write must succeed: %v", err)
			}

			second.Priority = 60
			err = s.UpdateRoute(ctx, testPermit, second)
			if err == nil {
				t.Fatal("the second, stale write succeeded")
			}
			if !errors.Is(err, domain.ErrStale) {
				t.Errorf("error = %v, want domain.ErrStale", err)
			}
		})
	}
}

// TestUpdateRouteRefusesAnInvalidValue.
func TestUpdateRouteRefusesAnInvalidValue(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			svc := mustService(t, s, ctx, "invalid-route-svc")
			frontend := mustEndpoint(t, s, ctx, svc, "front", 7443)
			pool, err := domain.NewBackendPool(NewID(), svc, "invalid-route-pool", nil)
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

			bogus := "obfuscate"
			route.TLSTermination = &bogus
			err = s.UpdateRoute(ctx, testPermit, route)
			if err == nil {
				t.Fatal("an unknown tls_termination was accepted")
			}
			ve, ok := domain.AsValidation(err)
			if !ok {
				t.Fatalf("error = %v (%T), want a *ValidationError", err, err)
			}
			if _, named := ve.Messages()["tls_termination"]; !named {
				t.Errorf("the refusal names %v, not %q", ve.Messages(), "tls_termination")
			}
		})
	}
}
