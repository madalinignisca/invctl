package store

import (
	"context"
	"errors"
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
