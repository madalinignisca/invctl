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

// 1.0 item 1: interface, ip_address and service_instance move from
// ScopeTopology to domain.ScopeSubjectDerived, the shape journal_entry
// proved (journal_scope_test.go). This file is the same shape repeated
// three times, one per type, with the two extra rules that make ip_address
// and service_instance harder than a single-hop derivation:
//   - ip_address.interface_id is NULLABLE, and an unattached address has no
//     subject at all -- refused, not guessed at.
//   - service_instance is two-ended (service_id AND host_asset_id), and
//     checking only one end is the ReparentAsset trap (9d01318, 82ea6c5)
//     one level down.

// mustService builds and stores a minimal active service, for fixtures that
// only need a real service id to hang a placement or a project link off of.
func mustService(t *testing.T, s *SQLStore, ctx context.Context, code string) string {
	t.Helper()
	env := mustEnvironment(t, s, ctx, code+"-env", domain.EnvRoleProduction)
	svc, err := domain.NewService(NewID(), domain.ServiceSpec{
		Code: code, Name: code, Kind: domain.SvcAPI,
		EnvironmentID: env, Availability: domain.AvailStandalone, Tier: 3,
	}, s.Now())
	if err != nil {
		t.Fatalf("building service %s: %v", code, err)
	}
	if err := s.CreateService(ctx, testPermit, svc); err != nil {
		t.Fatalf("creating service %s: %v", code, err)
	}
	return svc.ID
}

// ---------------------------------------------------------------------------
// interface
// ---------------------------------------------------------------------------

// TestAProjectOwnerCanCreateAnInterfaceOnAnAssetInScope is the point of the
// reclassification: before this, interface was ScopeTopology, so a project
// owner could create a server and never be able to give it a port.
func TestAProjectOwnerCanCreateAnInterfaceOnAnAssetInScope(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			mine := mustAsset(t, s, ctx, domain.KindServer, "vm-mine", nil)
			permit := domain.ScopedPermit(
				domain.Actor{ID: "po-if-1", Name: "po-if-1", Kind: domain.ActorKindUser},
				[]string{frontend}, domain.ScopedEntities{"asset": {mine: true}})

			iface, err := domain.NewInterface(NewID(), mine, "eth0", domain.FFRJ45)
			if err != nil {
				t.Fatalf("building interface: %v", err)
			}
			if err := s.CreateInterface(ctx, permit, iface); err != nil {
				t.Fatalf("CreateInterface on an in-scope asset = %v, want nil", err)
			}
			changes, err := s.ListChangesForEntity(ctx, "interface", iface.ID, 10)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			if len(changes) != 1 {
				t.Errorf("interface create wrote %d change_log rows, want 1", len(changes))
			}
		})
	}
}

// TestAProjectOwnerCannotCreateAnInterfaceOnAnAssetOutOfScope is the other
// half: the interface is brand new and belongs to nobody yet, but its
// subject -- the asset it names -- is not covered.
func TestAProjectOwnerCannotCreateAnInterfaceOnAnAssetOutOfScope(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			mine := mustAsset(t, s, ctx, domain.KindServer, "vm-mine", nil)
			theirs := mustAsset(t, s, ctx, domain.KindServer, "db-prod", nil)
			permit := domain.ScopedPermit(
				domain.Actor{ID: "po-if-2", Name: "po-if-2", Kind: domain.ActorKindUser},
				[]string{frontend}, domain.ScopedEntities{"asset": {mine: true}})

			iface, err := domain.NewInterface(NewID(), theirs, "eth0", domain.FFRJ45)
			if err != nil {
				t.Fatalf("building interface: %v", err)
			}
			if err := s.CreateInterface(ctx, permit, iface); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("CreateInterface on an out-of-scope asset = %v, want domain.ErrForbidden", err)
			}
			changes, err := s.ListChangesForEntity(ctx, "interface", iface.ID, 10)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			if len(changes) != 0 {
				t.Errorf("a refused interface create wrote %d change_log rows, want 0", len(changes))
			}
		})
	}
}

// TestAProjectOwnerCanUpdateAnInterfaceOnAnAssetInScope proves the update
// path also derives from the (immutable) owning asset.
func TestAProjectOwnerCanUpdateAnInterfaceOnAnAssetInScope(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			mine := mustAsset(t, s, ctx, domain.KindServer, "vm-mine", nil)
			ifaceID := mustInterface(t, s, ctx, mine, "eth0")
			permit := domain.ScopedPermit(
				domain.Actor{ID: "po-if-3", Name: "po-if-3", Kind: domain.ActorKindUser},
				[]string{frontend}, domain.ScopedEntities{"asset": {mine: true}})

			iface, err := s.GetInterface(ctx, ifaceID)
			if err != nil {
				t.Fatalf("getting interface: %v", err)
			}
			mtu := 9000
			iface.MTU = &mtu
			if err := s.UpdateInterface(ctx, permit, iface); err != nil {
				t.Fatalf("UpdateInterface on an in-scope asset = %v, want nil", err)
			}
		})
	}
}

// TestAProjectOwnerCannotUpdateAnInterfaceOnAnAssetOutOfScope is the negative
// half, driven through the STORED row (asset_id cannot move through
// UpdateInterface at all -- see that method's own doc comment), so there is
// no forged-subject variant to add here the way journal_entry needed one.
func TestAProjectOwnerCannotUpdateAnInterfaceOnAnAssetOutOfScope(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			mine := mustAsset(t, s, ctx, domain.KindServer, "vm-mine", nil)
			theirs := mustAsset(t, s, ctx, domain.KindServer, "db-prod", nil)
			ifaceID := mustInterface(t, s, ctx, theirs, "eth0")
			permit := domain.ScopedPermit(
				domain.Actor{ID: "po-if-4", Name: "po-if-4", Kind: domain.ActorKindUser},
				[]string{frontend}, domain.ScopedEntities{"asset": {mine: true}})

			iface, err := s.GetInterface(ctx, ifaceID)
			if err != nil {
				t.Fatalf("getting interface: %v", err)
			}
			mtu := 9000
			iface.MTU = &mtu
			if err := s.UpdateInterface(ctx, permit, iface); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("UpdateInterface on an out-of-scope asset = %v, want domain.ErrForbidden", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ip_address
// ---------------------------------------------------------------------------

func mustAddress(t *testing.T, s *SQLStore, ctx context.Context, ifaceID, addr string) *domain.IPAddress {
	t.Helper()
	a, err := domain.NewIPAddress(NewID(), addr, &ifaceID, domain.IPRolePrimary)
	if err != nil {
		t.Fatalf("building ip address %s: %v", addr, err)
	}
	if err := s.CreateIPAddress(ctx, testPermit, a); err != nil {
		t.Fatalf("creating ip address %s: %v", addr, err)
	}
	return a
}

// TestAProjectOwnerCanCreateAnAddressOnAnInterfaceInScope: the two-hop
// derivation, ip_address.interface_id -> interface.asset_id.
func TestAProjectOwnerCanCreateAnAddressOnAnInterfaceInScope(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			mine := mustAsset(t, s, ctx, domain.KindServer, "vm-mine", nil)
			ifaceID := mustInterface(t, s, ctx, mine, "eth0")
			permit := domain.ScopedPermit(
				domain.Actor{ID: "po-ip-1", Name: "po-ip-1", Kind: domain.ActorKindUser},
				[]string{frontend}, domain.ScopedEntities{"asset": {mine: true}})

			addr, err := domain.NewIPAddress(NewID(), "10.10.0.1", &ifaceID, domain.IPRolePrimary)
			if err != nil {
				t.Fatalf("building ip address: %v", err)
			}
			if err := s.CreateIPAddress(ctx, permit, addr); err != nil {
				t.Fatalf("CreateIPAddress on an in-scope interface = %v, want nil", err)
			}
		})
	}
}

// TestAProjectOwnerCannotCreateAnAddressOnAnInterfaceOutOfScope is the
// ordinary out-of-scope half.
func TestAProjectOwnerCannotCreateAnAddressOnAnInterfaceOutOfScope(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			mine := mustAsset(t, s, ctx, domain.KindServer, "vm-mine", nil)
			theirs := mustAsset(t, s, ctx, domain.KindServer, "db-prod", nil)
			ifaceID := mustInterface(t, s, ctx, theirs, "eth0")
			permit := domain.ScopedPermit(
				domain.Actor{ID: "po-ip-2", Name: "po-ip-2", Kind: domain.ActorKindUser},
				[]string{frontend}, domain.ScopedEntities{"asset": {mine: true}})

			addr, err := domain.NewIPAddress(NewID(), "10.10.0.2", &ifaceID, domain.IPRolePrimary)
			if err != nil {
				t.Fatalf("building ip address: %v", err)
			}
			if err := s.CreateIPAddress(ctx, permit, addr); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("CreateIPAddress on an out-of-scope interface = %v, want domain.ErrForbidden", err)
			}
		})
	}
}

// TestAProjectOwnerCannotCreateAnUnattachedAddress is THE case the brief
// calls out as the one a later refactor is most likely to get wrong: an
// address with no interface has no subject at all, so a scoped permit must
// be refused outright rather than treated as "in scope by default" or
// "topology, so refused for an unrelated reason". An Administrator can
// still create one -- their permit covers everything unconditionally and
// never reaches authorizeAddressSubject's refusal branch.
func TestAProjectOwnerCannotCreateAnUnattachedAddress(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			permit := domain.ScopedPermit(
				domain.Actor{ID: "po-ip-3", Name: "po-ip-3", Kind: domain.ActorKindUser},
				[]string{frontend}, domain.ScopedEntities{})

			addr, err := domain.NewIPAddress(NewID(), "10.10.0.3", nil, domain.IPRolePrimary)
			if err != nil {
				t.Fatalf("building ip address: %v", err)
			}
			if err := s.CreateIPAddress(ctx, permit, addr); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("CreateIPAddress unattached = %v, want domain.ErrForbidden", err)
			}
			changes, err := s.ListChangesForEntity(ctx, "ip_address", addr.ID, 10)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			if len(changes) != 0 {
				t.Errorf("a refused unattached address create wrote %d change_log rows, want 0", len(changes))
			}
		})
	}
}

// TestAProjectOwnerCanUpdateAnAddressOnAnInterfaceInScope covers the update
// path, derived from the STORED interface_id (see UpdateIPAddress's own doc
// comment: interface_id cannot move through this method).
func TestAProjectOwnerCanUpdateAnAddressOnAnInterfaceInScope(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			mine := mustAsset(t, s, ctx, domain.KindServer, "vm-mine", nil)
			ifaceID := mustInterface(t, s, ctx, mine, "eth0")
			addr := mustAddress(t, s, ctx, ifaceID, "10.10.0.4")
			permit := domain.ScopedPermit(
				domain.Actor{ID: "po-ip-4", Name: "po-ip-4", Kind: domain.ActorKindUser},
				[]string{frontend}, domain.ScopedEntities{"asset": {mine: true}})

			addr.Role = domain.IPRoleSecondary
			if err := s.UpdateIPAddress(ctx, permit, addr); err != nil {
				t.Fatalf("UpdateIPAddress on an in-scope interface = %v, want nil", err)
			}
		})
	}
}

// TestAProjectOwnerCannotUpdateAnAddressOnAnInterfaceOutOfScope.
func TestAProjectOwnerCannotUpdateAnAddressOnAnInterfaceOutOfScope(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			mine := mustAsset(t, s, ctx, domain.KindServer, "vm-mine", nil)
			theirs := mustAsset(t, s, ctx, domain.KindServer, "db-prod", nil)
			ifaceID := mustInterface(t, s, ctx, theirs, "eth0")
			addr := mustAddress(t, s, ctx, ifaceID, "10.10.0.5")
			permit := domain.ScopedPermit(
				domain.Actor{ID: "po-ip-5", Name: "po-ip-5", Kind: domain.ActorKindUser},
				[]string{frontend}, domain.ScopedEntities{"asset": {mine: true}})

			addr.Role = domain.IPRoleSecondary
			if err := s.UpdateIPAddress(ctx, permit, addr); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("UpdateIPAddress on an out-of-scope interface = %v, want domain.ErrForbidden", err)
			}
		})
	}
}

// TestAProjectOwnerCannotUpdateAnUnattachedAddress is the update-side
// counterpart of the create-side refusal: an address that was attached at
// creation and later cleared (via AssignVIP, an Administrator-only path
// today) has no subject either, and an update must be refused the same way
// a create is -- there is nothing an update could carry over that would
// make this address ownable by anyone but an Administrator.
func TestAProjectOwnerCannotUpdateAnUnattachedAddress(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			addr, err := domain.NewIPAddress(NewID(), "10.10.0.6", nil, domain.IPRolePrimary)
			if err != nil {
				t.Fatalf("building ip address: %v", err)
			}
			if err := s.CreateIPAddress(ctx, testPermit, addr); err != nil {
				t.Fatalf("seeding the unattached address: %v", err)
			}
			permit := domain.ScopedPermit(
				domain.Actor{ID: "po-ip-6", Name: "po-ip-6", Kind: domain.ActorKindUser},
				[]string{frontend}, domain.ScopedEntities{})

			addr.Role = domain.IPRoleSecondary
			if err := s.UpdateIPAddress(ctx, permit, addr); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("UpdateIPAddress unattached = %v, want domain.ErrForbidden", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// service_instance -- the two-ended case
// ---------------------------------------------------------------------------

// TestAProjectOwnerCanPlaceAServiceInstanceWhenBothEndsAreInScope is the
// ordinary success case: owning the service and the host is enough.
func TestAProjectOwnerCanPlaceAServiceInstanceWhenBothEndsAreInScope(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			host := mustAsset(t, s, ctx, domain.KindServer, "host-mine", nil)
			svc := mustService(t, s, ctx, "svc-mine")
			permit := domain.ScopedPermit(
				domain.Actor{ID: "po-si-1", Name: "po-si-1", Kind: domain.ActorKindUser},
				[]string{frontend},
				domain.ScopedEntities{"asset": {host: true}, "service": {svc: true}})

			si, err := domain.NewServiceInstance(NewID(), svc, host, domain.RuntimeSystemd, 0, s.Now())
			if err != nil {
				t.Fatalf("building instance: %v", err)
			}
			if err := s.CreateInstance(ctx, permit, si); err != nil {
				t.Fatalf("CreateInstance with both ends in scope = %v, want nil", err)
			}
		})
	}
}

// TestAProjectOwnerCannotPlaceAServiceInstanceOnAnOutOfScopeHost is the
// FIRST half of the ReparentAsset-shaped check, tested independently: the
// service is owned, the host is not. A test that only varied one end at a
// time is exactly what this brief warns would pass with the bug
// half-present, so this case and the next each vary exactly one end.
func TestAProjectOwnerCannotPlaceAServiceInstanceOnAnOutOfScopeHost(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			theirHost := mustAsset(t, s, ctx, domain.KindServer, "host-theirs", nil)
			svc := mustService(t, s, ctx, "svc-mine-2")
			// Only the service is in scope -- the host is not in entities at all.
			permit := domain.ScopedPermit(
				domain.Actor{ID: "po-si-2", Name: "po-si-2", Kind: domain.ActorKindUser},
				[]string{frontend},
				domain.ScopedEntities{"service": {svc: true}})

			si, err := domain.NewServiceInstance(NewID(), svc, theirHost, domain.RuntimeSystemd, 0, s.Now())
			if err != nil {
				t.Fatalf("building instance: %v", err)
			}
			if err := s.CreateInstance(ctx, permit, si); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("CreateInstance on an out-of-scope host = %v, want domain.ErrForbidden", err)
			}
			changes, err := s.ListChangesForEntity(ctx, "service_instance", si.ID, 10)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			if len(changes) != 0 {
				t.Errorf("a refused placement wrote %d change_log rows, want 0", len(changes))
			}
		})
	}
}

// TestAProjectOwnerCannotPlaceAnOutOfScopeServiceOnTheirOwnHost is the
// SECOND half: the host is owned, the service is not.
func TestAProjectOwnerCannotPlaceAnOutOfScopeServiceOnTheirOwnHost(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			myHost := mustAsset(t, s, ctx, domain.KindServer, "host-mine-2", nil)
			theirSvc := mustService(t, s, ctx, "svc-theirs")
			// Only the host is in scope -- the service is not in entities at all.
			permit := domain.ScopedPermit(
				domain.Actor{ID: "po-si-3", Name: "po-si-3", Kind: domain.ActorKindUser},
				[]string{frontend},
				domain.ScopedEntities{"asset": {myHost: true}})

			si, err := domain.NewServiceInstance(NewID(), theirSvc, myHost, domain.RuntimeSystemd, 0, s.Now())
			if err != nil {
				t.Fatalf("building instance: %v", err)
			}
			if err := s.CreateInstance(ctx, permit, si); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("CreateInstance with an out-of-scope service = %v, want domain.ErrForbidden", err)
			}
			changes, err := s.ListChangesForEntity(ctx, "service_instance", si.ID, 10)
			if err != nil {
				t.Fatalf("listing changes: %v", err)
			}
			if len(changes) != 0 {
				t.Errorf("a refused placement wrote %d change_log rows, want 0", len(changes))
			}
		})
	}
}

// TestAProjectOwnerCanUpdateAServiceInstanceWhenBothEndsAreInScope covers
// UpdateInstance, derived from the STORED service_id and host_asset_id --
// see UpdateInstance's own comment on why both are carried over rather than
// taken from the submitted struct (the handler never changes the host
// either; see InstanceUpdate's "the HOST is not a field" comment).
func TestAProjectOwnerCanUpdateAServiceInstanceWhenBothEndsAreInScope(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			host := mustAsset(t, s, ctx, domain.KindServer, "host-mine-3", nil)
			svc := mustService(t, s, ctx, "svc-mine-3")
			si, err := domain.NewServiceInstance(NewID(), svc, host, domain.RuntimeSystemd, 0, s.Now())
			if err != nil {
				t.Fatalf("building instance: %v", err)
			}
			if err := s.CreateInstance(ctx, testPermit, si); err != nil {
				t.Fatalf("seeding the instance: %v", err)
			}
			permit := domain.ScopedPermit(
				domain.Actor{ID: "po-si-4", Name: "po-si-4", Kind: domain.ActorKindUser},
				[]string{frontend},
				domain.ScopedEntities{"asset": {host: true}, "service": {svc: true}})

			row, err := s.GetInstance(ctx, si.ID)
			if err != nil {
				t.Fatalf("getting instance: %v", err)
			}
			updated := row.ServiceInstance
			role := "worker"
			updated.Role = &role
			if err := s.UpdateInstance(ctx, permit, &updated); err != nil {
				t.Fatalf("UpdateInstance with both ends in scope = %v, want nil", err)
			}
		})
	}
}

// TestUpdatingAServiceInstanceIgnoresASubmittedHostChange is the seizure
// attempt for the two-ended case: UpdateInstance's SQL statement writes
// host_asset_id from the submitted struct, so if the store trusted it
// instead of the stored row, a project owner who owns the service and SOME
// host could submit any other asset id as the new host_asset_id and move
// (or merely re-point the audit trail toward) hardware they do not own.
// This asserts the submitted value is discarded entirely: the row still
// names the original host after the call succeeds.
func TestUpdatingAServiceInstanceIgnoresASubmittedHostChange(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			myHost := mustAsset(t, s, ctx, domain.KindServer, "host-mine-4", nil)
			theirHost := mustAsset(t, s, ctx, domain.KindServer, "host-theirs-2", nil)
			svc := mustService(t, s, ctx, "svc-mine-4")
			si, err := domain.NewServiceInstance(NewID(), svc, myHost, domain.RuntimeSystemd, 0, s.Now())
			if err != nil {
				t.Fatalf("building instance: %v", err)
			}
			if err := s.CreateInstance(ctx, testPermit, si); err != nil {
				t.Fatalf("seeding the instance: %v", err)
			}
			permit := domain.ScopedPermit(
				domain.Actor{ID: "po-si-5", Name: "po-si-5", Kind: domain.ActorKindUser},
				[]string{frontend},
				domain.ScopedEntities{"asset": {myHost: true}, "service": {svc: true}})

			row, err := s.GetInstance(ctx, si.ID)
			if err != nil {
				t.Fatalf("getting instance: %v", err)
			}
			updated := row.ServiceInstance
			updated.HostAssetID = theirHost // THE ATTACK
			role := "worker"
			updated.Role = &role
			if err := s.UpdateInstance(ctx, permit, &updated); err != nil {
				t.Fatalf("UpdateInstance = %v, want nil (the host change is silently discarded)", err)
			}

			after, err := s.GetInstance(ctx, si.ID)
			if err != nil {
				t.Fatalf("re-reading instance: %v", err)
			}
			if after.HostAssetID != myHost {
				t.Errorf("host_asset_id = %s after update, want unchanged %s (submitted host must be ignored)",
					after.HostAssetID, myHost)
			}
		})
	}
}

// TestAProjectOwnerCanRetireAServiceInstanceWhenBothEndsAreInScope covers
// RetireInstance, the third of the three verbs.
func TestAProjectOwnerCanRetireAServiceInstanceWhenBothEndsAreInScope(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			host := mustAsset(t, s, ctx, domain.KindServer, "host-mine-5", nil)
			svc := mustService(t, s, ctx, "svc-mine-5")
			si, err := domain.NewServiceInstance(NewID(), svc, host, domain.RuntimeSystemd, 0, s.Now())
			if err != nil {
				t.Fatalf("building instance: %v", err)
			}
			if err := s.CreateInstance(ctx, testPermit, si); err != nil {
				t.Fatalf("seeding the instance: %v", err)
			}
			permit := domain.ScopedPermit(
				domain.Actor{ID: "po-si-6", Name: "po-si-6", Kind: domain.ActorKindUser},
				[]string{frontend},
				domain.ScopedEntities{"asset": {host: true}, "service": {svc: true}})

			if err := s.RetireInstance(ctx, permit, si.ID); err != nil {
				t.Fatalf("RetireInstance with both ends in scope = %v, want nil", err)
			}
		})
	}
}

// TestAProjectOwnerCannotRetireAServiceInstanceOnAnOutOfScopeHost is
// RetireInstance's half of the two-ended check.
func TestAProjectOwnerCannotRetireAServiceInstanceOnAnOutOfScopeHost(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			frontend := mustProjectForAssignment(t, s, ctx, "frontend")
			theirHost := mustAsset(t, s, ctx, domain.KindServer, "host-theirs-3", nil)
			svc := mustService(t, s, ctx, "svc-mine-6")
			si, err := domain.NewServiceInstance(NewID(), svc, theirHost, domain.RuntimeSystemd, 0, s.Now())
			if err != nil {
				t.Fatalf("building instance: %v", err)
			}
			if err := s.CreateInstance(ctx, testPermit, si); err != nil {
				t.Fatalf("seeding the instance: %v", err)
			}
			permit := domain.ScopedPermit(
				domain.Actor{ID: "po-si-7", Name: "po-si-7", Kind: domain.ActorKindUser},
				[]string{frontend},
				domain.ScopedEntities{"service": {svc: true}})

			if err := s.RetireInstance(ctx, permit, si.ID); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("RetireInstance on an out-of-scope host = %v, want domain.ErrForbidden", err)
			}
		})
	}
}
