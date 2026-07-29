package store

import (
	"testing"

	"github.com/gabriel/invctl/internal/domain"
)

// TestAWithdrawnPlacementFreesItsSlot.
//
// The defect that made migration 00002 worth doing before sign-off. A placement
// was withdrawn by writing desired_state='disabled' -- and DisableInstance
// logged that as ActionRetire, so the code already meant "this placement is
// gone" while the column said "it is deliberately not running". The withdrawn
// row kept its slot in UNIQUE (service_id, host_asset_id, ordinal), so a
// rebuilt VM could never be recorded where it was: CreateInstance returned
// ErrConflict, which the handler rendered as a 422 against a field the operator
// could do nothing about.
//
// Uniqueness is now a partial index over live placements only.
func TestAWithdrawnPlacementFreesItsSlot(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			envID := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
			host := mustAsset(t, s, ctx, domain.KindServer, "app-01", nil, envID)
			svc, err := domain.NewService(NewID(), domain.ServiceSpec{
				Code: "orders", Name: "Orders", Kind: domain.SvcAPI,
				EnvironmentID: envID, Availability: domain.AvailStandalone, Tier: 2,
			}, s.Now())
			if err != nil {
				t.Fatalf("building service: %v", err)
			}
			if err := s.CreateService(ctx, testActor, svc); err != nil {
				t.Fatalf("creating service: %v", err)
			}
			place := func() (*domain.ServiceInstance, error) {
				si, err := domain.NewServiceInstance(NewID(), svc.ID, host, domain.RuntimeSystemd, 0, s.Now())
				if err != nil {
					t.Fatalf("building instance: %v", err)
				}
				return si, s.CreateInstance(ctx, testActor, si)
			}

			first, err := place()
			if err != nil {
				t.Fatalf("first placement: %v", err)
			}

			// While it is live, the slot is still taken -- the constraint must
			// not have been loosened into uselessness.
			if _, err := place(); err == nil {
				t.Error("a second live placement on the same host and ordinal was accepted; " +
					"the partial index no longer enforces anything")
			}

			if err := s.RetireInstance(ctx, testActor, first.ID); err != nil {
				t.Fatalf("retiring: %v", err)
			}

			// The VM was rebuilt and the service redeployed to the same slot.
			if _, err := place(); err != nil {
				t.Fatalf("re-placing after withdrawal: %v; a rebuilt host can never be "+
					"recorded where it was", err)
			}

			// The withdrawn row and its history stay: soft delete, not deletion.
			total, err := s.countOne(ctx,
				`SELECT COUNT(*) FROM service_instance WHERE service_id = ?`, svc.ID)
			if err != nil {
				t.Fatalf("counting: %v", err)
			}
			if total != 2 {
				t.Errorf("%d rows for the service, want 2: the withdrawn placement should be "+
					"kept, not deleted", total)
			}

			// And it is not capacity. The impact graph must see one instance.
			g, err := s.LoadGraph(ctx)
			if err != nil {
				t.Fatalf("loading graph: %v", err)
			}
			if n := len(g.Instances[svc.ID]); n != 1 {
				t.Errorf("the graph sees %d instances, want 1: a withdrawn placement counted as "+
					"capacity would make \"1 of 2 lost\" out of a service that has one", n)
			}
		})
	}
}
