package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gabriel/invctl/internal/domain"
)

// TestInterfaceMACNormalisation covers docs/DECISIONS.md Q2: identity is
// (asset_id, name), and MAC is just an indexed attribute that gets normalised
// on the way in regardless of how an operator pastes it.
func TestInterfaceMACNormalisation(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			assetID := mustAsset(t, s, ctx, domain.KindServer, "srv-net", nil)

			iface, err := domain.NewInterface(NewID(), assetID, "eth0", domain.FFRJ45)
			if err != nil {
				t.Fatalf("building interface: %v", err)
			}
			if err := iface.SetMAC("AA-BB-CC-00-10-01"); err != nil {
				t.Fatalf("setting mac: %v", err)
			}
			if err := s.CreateInterface(ctx, testActor, iface); err != nil {
				t.Fatalf("creating interface: %v", err)
			}

			rows, err := s.ListInterfaces(ctx, assetID)
			if err != nil {
				t.Fatalf("listing interfaces: %v", err)
			}
			if len(rows) != 1 {
				t.Fatalf("got %d interfaces, want 1", len(rows))
			}
			if rows[0].MAC == nil || *rows[0].MAC != "aa:bb:cc:00:10:01" {
				t.Errorf("mac = %v, want normalised lowercase colon form", rows[0].MAC)
			}

			t.Run("a second interface with the same name on the same asset conflicts", func(t *testing.T) {
				dup, err := domain.NewInterface(NewID(), assetID, "eth0", domain.FFRJ45)
				if err != nil {
					t.Fatalf("building duplicate interface: %v", err)
				}
				if err := s.CreateInterface(ctx, testActor, dup); !errors.Is(err, domain.ErrConflict) {
					t.Errorf("creating a duplicate (asset_id, name) = %v, want ErrConflict", err)
				}
			})
		})
	}
}

// TestLinkLifecycle covers the 2026-07-28 decision that a cable is
// soft-delete-only: a port has one active cable, retiring it frees the port
// back up, and a retired link must not appear as anyone's far end.
func TestLinkLifecycle(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			assetA := mustAsset(t, s, ctx, domain.KindSwitch, "sw-1", nil)
			assetB := mustAsset(t, s, ctx, domain.KindServer, "srv-1", nil)
			ifaceA := mustInterface(t, s, ctx, assetA, "eth0")
			ifaceB := mustInterface(t, s, ctx, assetB, "eth0")

			link, err := domain.NewLink(NewID(), ifaceA, ifaceB)
			if err != nil {
				t.Fatalf("building link: %v", err)
			}
			if err := s.CreateLink(ctx, testActor, link); err != nil {
				t.Fatalf("creating link: %v", err)
			}

			t.Run("both ports show the far end", func(t *testing.T) {
				rowsA, err := s.ListInterfaces(ctx, assetA)
				if err != nil {
					t.Fatalf("listing interfaces of A: %v", err)
				}
				if rowsA[0].PeerAsset != "srv-1" || rowsA[0].PeerPort != "eth0" {
					t.Errorf("A's peer = %s/%s, want srv-1/eth0", rowsA[0].PeerAsset, rowsA[0].PeerPort)
				}
				if !rowsA[0].IsPatched() {
					t.Error("A's port does not report itself as patched")
				}
			})

			t.Run("a third interface cannot cable to either patched port", func(t *testing.T) {
				assetC := mustAsset(t, s, ctx, domain.KindServer, "srv-2", nil)
				ifaceC := mustInterface(t, s, ctx, assetC, "eth0")
				bad, err := domain.NewLink(NewID(), ifaceC, ifaceA)
				if err != nil {
					t.Fatalf("building second link: %v", err)
				}
				if err := s.CreateLink(ctx, testActor, bad); !errors.Is(err, domain.ErrConflict) {
					t.Errorf("cabling to an already-patched port = %v, want ErrConflict", err)
				}
			})

			t.Run("retiring frees the port and hides the peer", func(t *testing.T) {
				if err := s.RetireLink(ctx, testActor, link.ID); err != nil {
					t.Fatalf("retiring link: %v", err)
				}

				rowsA, err := s.ListInterfaces(ctx, assetA)
				if err != nil {
					t.Fatalf("listing interfaces of A after retiring: %v", err)
				}
				if rowsA[0].PeerAsset != "" || rowsA[0].PeerPort != "" {
					t.Errorf("A still shows a peer after the cable was retired: %s/%s",
						rowsA[0].PeerAsset, rowsA[0].PeerPort)
				}
				if rowsA[0].IsPatched() {
					t.Error("A still reports itself as patched after the cable was retired")
				}

				rowsB, err := s.ListInterfaces(ctx, assetB)
				if err != nil {
					t.Fatalf("listing interfaces of B after retiring: %v", err)
				}
				if rowsB[0].PeerAsset != "" {
					t.Errorf("B still shows a peer after the cable was retired: %s", rowsB[0].PeerAsset)
				}

				changes, err := s.ListChangesForEntity(ctx, "link", link.ID, 5)
				if err != nil {
					t.Fatalf("listing link changes: %v", err)
				}
				if len(changes) != 2 || changes[0].Action != domain.ActionRetire {
					t.Errorf("link history = %+v, want a create followed by a retire", changes)
				}
			})

			t.Run("the freed port can be re-patched", func(t *testing.T) {
				assetD := mustAsset(t, s, ctx, domain.KindServer, "srv-3", nil)
				ifaceD := mustInterface(t, s, ctx, assetD, "eth0")
				again, err := domain.NewLink(NewID(), ifaceA, ifaceD)
				if err != nil {
					t.Fatalf("building link: %v", err)
				}
				if err := s.CreateLink(ctx, testActor, again); err != nil {
					t.Errorf("re-patching a freed port failed: %v", err)
				}
			})

			t.Run("retiring twice is a no-op, not an error", func(t *testing.T) {
				if err := s.RetireLink(ctx, testActor, link.ID); err != nil {
					t.Errorf("retiring an already-retired link: %v", err)
				}
			})
		})
	}
}

// TestListAvailableInterfaces covers the "patch to" dropdown: it must offer
// unpatched ports only, and never the one being patched from.
func TestListAvailableInterfaces(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			assetA := mustAsset(t, s, ctx, domain.KindSwitch, "sw-1", nil)
			assetB := mustAsset(t, s, ctx, domain.KindServer, "srv-1", nil)
			assetC := mustAsset(t, s, ctx, domain.KindServer, "srv-2", nil)
			ifaceA := mustInterface(t, s, ctx, assetA, "eth0")
			ifaceB := mustInterface(t, s, ctx, assetB, "eth0")
			ifaceC := mustInterface(t, s, ctx, assetC, "eth0")

			link, err := domain.NewLink(NewID(), ifaceA, ifaceB)
			if err != nil {
				t.Fatalf("building link: %v", err)
			}
			if err := s.CreateLink(ctx, testActor, link); err != nil {
				t.Fatalf("creating link: %v", err)
			}

			opts, err := s.ListAvailableInterfaces(ctx, ifaceC)
			if err != nil {
				t.Fatalf("listing available interfaces: %v", err)
			}
			ids := make(map[string]bool, len(opts))
			for _, o := range opts {
				ids[o.ID] = true
			}
			if ids[ifaceA] || ids[ifaceB] {
				t.Errorf("available interfaces = %v, want the patched ports excluded", opts)
			}
			if ids[ifaceC] {
				t.Error("the excluded interface itself was offered as an option")
			}
		})
	}
}

// TestListPrefixesUtilisation covers the utilisation figure the prefixes page
// shows: addresses actually assigned within the range, not the size of it.
func TestListPrefixesUtilisation(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			envID := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
			prefix, err := domain.NewPrefix(NewID(), "10.40.0.0/24")
			if err != nil {
				t.Fatalf("building prefix: %v", err)
			}
			prefix.EnvironmentID = &envID
			prefix.VLANID = intPtr(40)
			if err := s.CreatePrefix(ctx, testActor, prefix); err != nil {
				t.Fatalf("creating prefix: %v", err)
			}

			assetID := mustAsset(t, s, ctx, domain.KindServer, "srv-util", nil)
			ifaceID := mustInterface(t, s, ctx, assetID, "eth0")
			for _, ip := range []string{"10.40.0.5", "10.40.0.6"} {
				addr, err := domain.NewIPAddress(NewID(), ip, &ifaceID, domain.IPRolePrimary)
				if err != nil {
					t.Fatalf("building address %s: %v", ip, err)
				}
				if err := s.CreateIPAddress(ctx, testActor, addr); err != nil {
					t.Fatalf("creating address %s: %v", ip, err)
				}
			}

			rows, err := s.ListPrefixes(ctx)
			if err != nil {
				t.Fatalf("listing prefixes: %v", err)
			}
			var found *PrefixRow
			for i := range rows {
				if rows[i].CIDRText == "10.40.0.0/24" {
					found = &rows[i]
				}
			}
			if found == nil {
				t.Fatal("the new prefix is not in the list")
			}
			if found.AddressCount != 2 {
				t.Errorf("address count = %d, want 2", found.AddressCount)
			}
			if found.EnvironmentCode != "prod" {
				t.Errorf("environment code = %q, want prod", found.EnvironmentCode)
			}
		})
	}
}

// mustInterface creates an interface and returns its id.
func mustInterface(t *testing.T, s *SQLStore, ctx context.Context, assetID, name string) string {
	t.Helper()
	iface, err := domain.NewInterface(NewID(), assetID, name, domain.FFRJ45)
	if err != nil {
		t.Fatalf("building interface %s: %v", name, err)
	}
	if err := s.CreateInterface(ctx, testActor, iface); err != nil {
		t.Fatalf("creating interface %s: %v", name, err)
	}
	return iface.ID
}

// The store refuses to move a port or rebond it, whatever the caller passes.
//
// AT THE STORE, not only at the handler. InterfaceUpdate never reads asset_id
// or lag_parent_id from a form, so a request cannot carry them today -- which
// means a web test cannot reach this guard at all, and deleting it leaves the
// whole suite green. The next caller will not be an HTML form, and a port that
// silently changes chassis rewrites the cable map and every path through it.
func TestUpdateInterfaceCannotMoveOrRebondAPort(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			homeID := mustAsset(t, s, ctx, domain.KindServer, "srv-home", nil)
			awayID := mustAsset(t, s, ctx, domain.KindServer, "srv-away", nil)

			iface, err := domain.NewInterface(NewID(), homeID, "eth0", domain.FFRJ45)
			if err != nil {
				t.Fatalf("building interface: %v", err)
			}
			if err := s.CreateInterface(ctx, testActor, iface); err != nil {
				t.Fatalf("creating interface: %v", err)
			}
			bond, err := domain.NewInterface(NewID(), homeID, "bond0", domain.FFRJ45)
			if err != nil {
				t.Fatalf("building bond: %v", err)
			}
			if err := s.CreateInterface(ctx, testActor, bond); err != nil {
				t.Fatalf("creating bond: %v", err)
			}

			// A caller asking for all three at once: rename, move, rebond.
			tampered := *iface
			tampered.Name = "eth0-renamed"
			tampered.AssetID = awayID
			tampered.LagParentID = &bond.ID
			if err := s.UpdateInterface(ctx, testActor, &tampered); err != nil {
				t.Fatalf("updating interface: %v", err)
			}

			after, err := s.GetInterface(ctx, iface.ID)
			if err != nil {
				t.Fatalf("reloading interface: %v", err)
			}
			// The rename took: without this the rest proves only that nothing
			// happened at all.
			if after.Name != "eth0-renamed" {
				t.Fatalf("name = %q, want the rename to have taken", after.Name)
			}
			if after.AssetID != homeID {
				t.Errorf("the port moved chassis: asset_id = %q, want %q", after.AssetID, homeID)
			}
			if after.LagParentID != nil {
				t.Errorf("the port was bonded by an update: lag_parent_id = %v, want nil", after.LagParentID)
			}

			// And the port is still listed where it was, which is what any
			// reader of the topology actually looks at.
			home, err := s.ListInterfaces(ctx, homeID)
			if err != nil {
				t.Fatalf("listing home interfaces: %v", err)
			}
			var found bool
			for _, row := range home {
				if row.ID == iface.ID {
					found = true
				}
			}
			if !found {
				t.Error("the port is no longer listed on the asset it belongs to")
			}

			// THE AUDIT MUST NOT CLAIM IT MOVED. What actually stops the move
			// is the UPDATE column list, which never mentions asset_id -- but
			// logUpdate diffs the struct it was HANDED against the stored row,
			// so a tampered field reaches the change_log even though it never
			// reached the table. The entry would read "asset_id: srv-home ->
			// srv-away" for a port that did not move: a permanent, append-only
			// record of something that never happened, which is worse than a
			// missing one because nothing contradicts it.
			changes, err := s.ListChangesForEntity(ctx, "interface", iface.ID, 10)
			if err != nil {
				t.Fatalf("reading the change log: %v", err)
			}
			// Updates only. A create carries a FULL SNAPSHOT by design, which
			// names every column including these two -- checking every entry
			// makes the assertion fail on the create and prove nothing about
			// the update.
			var updates int
			for _, c := range changes {
				if c.Action != "update" {
					continue
				}
				updates++
				for _, phantom := range []string{"asset_id", "lag_parent_id"} {
					if strings.Contains(c.Diff, phantom) {
						t.Errorf("the audit trail claims %s changed, but the port did not move: %s",
							phantom, c.Diff)
					}
				}
			}
			if updates == 0 {
				t.Fatal("no update entry in the audit trail, so nothing above was checked")
			}
		})
	}
}

// A live edge cannot be declared THROUGH a withdrawn socket.
//
// RetireEndpoint refuses to withdraw a socket that has live dependants, which
// only holds in one direction: nothing stopped declaring a dependant on an
// already-retired socket afterwards. Two ordinary requests in the wrong order,
// no race -- and the result is what the retire guard exists to prevent.
// LoadGraph drops retired sockets, the impact engine skips a dependency whose
// provider is missing from the map, and the edge stays on the service page
// while quietly vanishing from every simulation. Found by a security review.
func TestADependencyCannotBeDeclaredThroughAWithdrawnEndpoint(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			consumer, _, epID := servicePairWithEndpoint(t, s, ctx, 5432)

			// It works while the socket is live.
			if err := declareDependency(s, ctx, consumer, epID); err != nil {
				t.Fatalf("declaring on a live socket: %v", err)
			}
			// Withdraw the edge, then the socket.
			deps, err := s.ListUpstream(ctx, consumer)
			if err != nil || len(deps) == 0 {
				t.Fatalf("reading the edge back: %v (%d)", err, len(deps))
			}
			if err := s.RetireDependency(ctx, testActor, deps[0].ID); err != nil {
				t.Fatalf("retiring the edge: %v", err)
			}
			if err := s.RetireEndpoint(ctx, testActor, epID); err != nil {
				t.Fatalf("withdrawing the socket: %v", err)
			}

			err = declareDependency(s, ctx, consumer, epID)
			if err == nil {
				t.Fatal("a dependency was declared through a withdrawn socket")
			}
			if !errors.Is(err, domain.ErrConflict) {
				t.Errorf("got %v, want a conflict", err)
			}
		})
	}
}

// A socket still serving a pool cannot be withdrawn either.
//
// It is reachable without any dependency naming it, and LoadGraph keeps a
// membership row whose endpoint has gone from the map -- which the engine skips
// in silence, so a pool loses capacity with nothing marked down. The read audit
// in migration 00020 never mentioned backend_member until a review asked why.
func TestAPoolBackendCannotBeWithdrawn(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			_, provider, epID := servicePairWithEndpoint(t, s, ctx, 8080)

			pool := &domain.BackendPool{ID: NewID(), ServiceID: provider, Name: "web", LBAlgorithm: strPtr("roundrobin")}
			if err := s.CreateBackendPool(ctx, testActor, pool); err != nil {
				t.Fatalf("creating the pool: %v", err)
			}
			if err := s.AddBackendMember(ctx, testActor,
				&domain.BackendMember{PoolID: pool.ID, EndpointID: epID, Weight: 1}); err != nil {
				t.Fatalf("adding the backend: %v", err)
			}

			err := s.RetireEndpoint(ctx, testActor, epID)
			if err == nil {
				t.Fatal("a socket still serving a pool was withdrawn")
			}
			if !errors.Is(err, domain.ErrConflict) {
				t.Errorf("got %v, want a conflict", err)
			}
		})
	}
}

// servicePairWithEndpoint builds a consumer, a provider and one socket on the
// provider, and returns their ids.
func servicePairWithEndpoint(t *testing.T, s *SQLStore, ctx context.Context, port int) (string, string, string) {
	t.Helper()
	env := mustEnvironment(t, s, ctx, "prod-ep", domain.EnvRoleProduction)
	mk := func(code string) string {
		svc, err := domain.NewService(NewID(), domain.ServiceSpec{
			Code: code, Name: code, Kind: domain.SvcAPI,
			EnvironmentID: env, Availability: domain.AvailStandalone, Tier: 3,
		}, s.Now())
		if err != nil {
			t.Fatalf("building %s: %v", code, err)
		}
		if err := s.CreateService(ctx, testActor, svc); err != nil {
			t.Fatalf("creating %s: %v", code, err)
		}
		return svc.ID
	}
	consumer, provider := mk("consumer-ep"), mk("provider-ep")

	ep, err := domain.NewEndpoint(NewID(), provider, "sock", domain.ProtoTCP, &port, domain.BindHost)
	if err != nil {
		t.Fatalf("building the endpoint: %v", err)
	}
	if err := s.CreateEndpoint(ctx, testActor, ep); err != nil {
		t.Fatalf("creating the endpoint: %v", err)
	}
	return consumer, provider, ep.ID
}

func declareDependency(s *SQLStore, ctx context.Context, consumer, endpointID string) error {
	d, err := domain.NewDependency(NewID(), domain.DependencySpec{
		ConsumerServiceID: consumer, ProviderEndpointID: &endpointID,
		Nature: domain.NatureHard, FailureMode: "it stops"}, s.Now())
	if err != nil {
		return err
	}
	return s.CreateDependency(ctx, testActor, d, nil)
}
