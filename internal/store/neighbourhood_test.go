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

// The neighbourhood walk, against both engines.
//
// The fixture is the shape the real estate has, in miniature: a rack of
// switches, a hypervisor whose data NIC is enslaved to a bond, a Linux bridge
// declared as an asset of its own with the bond as its uplink, and a guest
// cabled to that bridge. That chain -- guest, veth, bridge, uplink, bond, NIC,
// cable, switch -- is the whole reason the diagram exists, and a walk that
// cannot follow it end to end is not worth drawing.

type neighbourFixture struct {
	s   *SQLStore
	ctx context.Context

	assets map[string]string // name -> id
	ports  map[string]string // "asset/port" -> interface id
	svc    map[string]string // code -> service id
}

func newNeighbourFixture(t *testing.T, e Engine) *neighbourFixture {
	t.Helper()
	s, ctx := newStore(t, e)
	f := &neighbourFixture{
		s: s, ctx: ctx,
		assets: map[string]string{},
		ports:  map[string]string{},
		svc:    map[string]string{},
	}

	envID := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)

	asset := func(kind, name, parent string) {
		t.Helper()
		var parentID *string
		if parent != "" {
			id := f.assets[parent]
			parentID = &id
		}
		f.assets[name] = mustAsset(t, s, ctx, kind, name, parentID, envID)
	}
	port := func(asset, name, form, master string) {
		t.Helper()
		iface, err := domain.NewInterface(NewID(), f.assets[asset], name, form)
		if err != nil {
			t.Fatalf("building %s/%s: %v", asset, name, err)
		}
		if master != "" {
			id := f.ports[master]
			iface.LagParentID = &id
		}
		if err := s.CreateInterface(ctx, testActor, iface); err != nil {
			t.Fatalf("creating %s/%s: %v", asset, name, err)
		}
		f.ports[asset+"/"+name] = iface.ID
	}
	cable := func(a, b string) {
		t.Helper()
		link, err := domain.NewLink(NewID(), f.ports[a], f.ports[b])
		if err != nil {
			t.Fatalf("building link %s-%s: %v", a, b, err)
		}
		if err := s.CreateLink(ctx, testActor, link); err != nil {
			t.Fatalf("creating link %s-%s: %v", a, b, err)
		}
	}

	asset(domain.KindSite, "dc", "")
	asset(domain.KindRack, "r1", "dc")
	asset(domain.KindRack, "r2", "dc")
	asset(domain.KindSwitch, "sw-1", "r1")
	asset(domain.KindSwitch, "sw-2", "r2")
	asset(domain.KindHypervisor, "hv-1", "r1")
	asset(domain.KindBridge, "br-1", "hv-1")
	asset(domain.KindVM, "vm-1", "hv-1")
	// Nothing cables to this one and nothing contains it with anything else.
	// It exists so that "the walk stops" is asserted against a real absence.
	asset(domain.KindServer, "far-1", "")

	port("sw-1", "Ethernet1", domain.FFSFP28, "")
	port("sw-1", "Ethernet2", domain.FFSFP28, "")
	port("sw-2", "Ethernet1", domain.FFSFP28, "")
	// bond0 before eno1: a member resolves its master by id.
	port("hv-1", "bond0", domain.FFLAG, "")
	port("hv-1", "eno1", domain.FFSFP28, "hv-1/bond0")
	port("br-1", "uplink", domain.FFVirtual, "")
	port("br-1", "vnet0", domain.FFVirtual, "")
	port("vm-1", "eth0", domain.FFVirtual, "")

	cable("hv-1/eno1", "sw-1/Ethernet1")
	cable("sw-1/Ethernet2", "sw-2/Ethernet1")
	cable("br-1/uplink", "hv-1/bond0")
	cable("vm-1/eth0", "br-1/vnet0")

	service := func(code string) {
		t.Helper()
		svc, err := domain.NewService(NewID(), domain.ServiceSpec{
			Code: code, Name: code, Kind: domain.SvcAPI,
			EnvironmentID: envID, Availability: domain.AvailStandalone, Tier: 2,
		}, s.Now())
		if err != nil {
			t.Fatalf("building service %s: %v", code, err)
		}
		if err := s.CreateService(ctx, testActor, svc); err != nil {
			t.Fatalf("creating service %s: %v", code, err)
		}
		f.svc[code] = svc.ID
	}
	place := func(code, host string) {
		t.Helper()
		si, err := domain.NewServiceInstance(NewID(), f.svc[code], f.assets[host],
			domain.RuntimeSystemd, 0, s.Now())
		if err != nil {
			t.Fatalf("building instance of %s: %v", code, err)
		}
		if err := s.CreateInstance(ctx, testActor, si); err != nil {
			t.Fatalf("placing %s on %s: %v", code, host, err)
		}
	}

	service("app")
	service("db")
	service("offsite")
	place("app", "vm-1")
	place("db", "hv-1")
	place("offsite", "far-1")

	f.endpoint(t, "app", "http", 8080, domain.BindHost, nil)
	dbEndpoint := f.endpoint(t, "db", "sql", 5432, domain.BindHost, nil)
	offsiteEndpoint := f.endpoint(t, "offsite", "api", 443, domain.BindHost, nil)

	dep := func(consumer, providerEndpoint string) {
		t.Helper()
		d, err := domain.NewDependency(NewID(), domain.DependencySpec{
			ConsumerServiceID:  f.svc[consumer],
			ProviderEndpointID: &providerEndpoint,
			Nature:             domain.NatureHard,
			FailureMode:        "stops",
		}, s.Now())
		if err != nil {
			t.Fatalf("building dependency: %v", err)
		}
		if err := s.CreateDependency(ctx, testActor, d, nil); err != nil {
			t.Fatalf("creating dependency: %v", err)
		}
	}
	dep("app", dbEndpoint)
	dep("app", offsiteEndpoint)

	return f
}

// endpoint adds a listening socket and returns its id.
func (f *neighbourFixture) endpoint(t *testing.T, code, name string, port int, scope string, addrID *string) string {
	t.Helper()
	e, err := domain.NewEndpoint(NewID(), f.svc[code], name, domain.ProtoTCP, &port, scope)
	if err != nil {
		t.Fatalf("building endpoint %s/%s: %v", code, name, err)
	}
	e.IPAddressID = addrID
	if err := f.s.CreateEndpoint(f.ctx, testActor, e); err != nil {
		t.Fatalf("creating endpoint %s/%s: %v", code, name, err)
	}
	return e.ID
}

func (f *neighbourFixture) names(g *NeighbourhoodGraph) map[string]int {
	out := make(map[string]int, len(g.Assets))
	for _, a := range g.Assets {
		out[a.Name] = a.Hop
	}
	return out
}

// TestNeighbourhoodWalk is the traversal itself: how far it reaches at each
// hop count, and what it refuses to reach.
func TestNeighbourhoodWalk(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newNeighbourFixture(t, e)

			cases := []struct {
				name    string
				from    string
				hops    int
				want    map[string]int
				notWant []string
			}{
				{
					// One hop from a guest: its bridge across the veth, and the
					// hypervisor that contains it.
					name: "one hop from a guest",
					from: "vm-1", hops: 1,
					want:    map[string]int{"vm-1": 0, "br-1": 1, "hv-1": 1},
					notWant: []string{"sw-1", "r1", "dc", "far-1"},
				},
				{
					// Two hops adds the rack and the switch the hypervisor is
					// cabled to -- reached THROUGH the bridge's uplink and the
					// bond, which is the layered chain the feature exists for.
					name: "two hops from a guest",
					from: "vm-1", hops: 2,
					want:    map[string]int{"vm-1": 0, "br-1": 1, "hv-1": 1, "r1": 2, "sw-1": 2},
					notWant: []string{"sw-2", "dc", "far-1"},
				},
				{
					name: "three hops reaches the far switch and the site",
					from: "vm-1", hops: 3,
					want:    map[string]int{"vm-1": 0, "br-1": 1, "hv-1": 1, "r1": 2, "sw-1": 2, "sw-2": 3, "dc": 3},
					notWant: []string{"far-1", "r2"},
				},
				{
					// Seeded from a host, the guests come for free: the walk
					// starts from the subject's whole containment subtree, the
					// same expansion "if I take this away" already uses.
					name: "the subject's subtree is hop zero",
					from: "hv-1", hops: 1,
					want:    map[string]int{"hv-1": 0, "br-1": 0, "vm-1": 0, "r1": 1, "sw-1": 1},
					notWant: []string{"sw-2", "dc", "far-1"},
				},
				{
					name: "an asset nothing touches has a neighbourhood of one",
					from: "far-1", hops: 4,
					want:    map[string]int{"far-1": 0},
					notWant: []string{"dc", "hv-1", "vm-1"},
				},
			}

			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					g, err := f.s.Neighbourhood(f.ctx, NeighbourhoodQuery{
						AssetID: f.assets[tc.from], Hops: tc.hops,
					})
					if err != nil {
						t.Fatalf("walking: %v", err)
					}
					got := f.names(g)
					for name, hop := range tc.want {
						at, ok := got[name]
						if !ok {
							t.Errorf("%s is missing from the neighbourhood", name)
							continue
						}
						if at != hop {
							t.Errorf("%s is at hop %d, want %d", name, at, hop)
						}
					}
					for _, name := range tc.notWant {
						if _, ok := got[name]; ok {
							t.Errorf("%s was reached and should not have been: %v", name, got)
						}
					}
					if len(got) != len(tc.want) {
						t.Errorf("walk returned %v, want exactly %v", got, tc.want)
					}
				})
			}
		})
	}
}

// TestNeighbourhoodClosesItsFrontier: a cable is drawn only when both of its
// ends are in the picture. One end off-screen is a stub going nowhere, and the
// layout has no node to anchor it to.
func TestNeighbourhoodClosesItsFrontier(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newNeighbourFixture(t, e)

			one, err := f.s.Neighbourhood(f.ctx, NeighbourhoodQuery{AssetID: f.assets["vm-1"], Hops: 1})
			if err != nil {
				t.Fatalf("walking: %v", err)
			}
			if len(one.Links) != 2 {
				t.Fatalf("one hop returned %d links, want 2 (the veth and the uplink)", len(one.Links))
			}
			for _, l := range one.Links {
				if l.A.AssetID == f.assets["sw-1"] || l.B.AssetID == f.assets["sw-1"] {
					t.Error("a cable to a switch outside the node set was returned")
				}
			}

			two, err := f.s.Neighbourhood(f.ctx, NeighbourhoodQuery{AssetID: f.assets["vm-1"], Hops: 2})
			if err != nil {
				t.Fatalf("walking: %v", err)
			}
			if len(two.Links) != 3 {
				t.Fatalf("two hops returned %d links, want 3", len(two.Links))
			}

			t.Run("the enslaved port carries its master", func(t *testing.T) {
				var master string
				for _, l := range two.Links {
					for _, p := range []NeighbourPort{l.A, l.B} {
						if p.AssetID == f.assets["hv-1"] && p.Name == "eno1" {
							master = p.Master
						}
					}
				}
				if master != "bond0" {
					t.Errorf("hv-1/eno1 reports master %q, want bond0; without it the cable to "+
						"the switch and the bridge's uplink look like unrelated facts", master)
				}
			})
		})
	}
}

// TestNeighbourhoodWorkloads covers the service layer: what runs on the hosts
// in the picture, what it listens on, and which edges have both ends inside.
func TestNeighbourhoodWorkloads(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newNeighbourFixture(t, e)

			g, err := f.s.Neighbourhood(f.ctx, NeighbourhoodQuery{AssetID: f.assets["vm-1"], Hops: 1})
			if err != nil {
				t.Fatalf("walking: %v", err)
			}

			codes := map[string]bool{}
			for _, svc := range g.Services {
				codes[svc.Code] = true
			}
			if !codes["app"] || !codes["db"] {
				t.Errorf("services = %v, want app (on the guest) and db (on the host)", codes)
			}
			if codes["offsite"] {
				t.Error("a service on an asset outside the neighbourhood was included")
			}

			if len(g.Placements) != 2 {
				t.Errorf("%d placements, want 2", len(g.Placements))
			}
			if len(g.Endpoints) != 2 {
				t.Errorf("%d endpoints, want 2", len(g.Endpoints))
			}
			for _, ep := range g.Endpoints {
				// bind_scope host with no address is 0.0.0.0. It is a fact
				// about the service, not a missing value, and the query must
				// not invent an interface for it.
				if ep.BoundInterfaceID != "" {
					t.Errorf("endpoint %s claims a bound interface it does not have", ep.Name)
				}
			}

			if len(g.Dependencies) != 1 {
				t.Fatalf("%d dependencies drawn, want 1 (app -> db)", len(g.Dependencies))
			}
			d := g.Dependencies[0]
			if d.ConsumerServiceID != f.svc["app"] || d.ProviderServiceID != f.svc["db"] {
				t.Errorf("dependency is %s -> %s, want app -> db", d.ConsumerServiceID, d.ProviderServiceID)
			}
			if d.Via != "endpoint" || d.ProviderPort != 5432 {
				t.Errorf("dependency = via %s port %d, want endpoint 5432", d.Via, d.ProviderPort)
			}

			// The edge to the off-screen service is counted, never silently
			// dropped: a service whose provider is elsewhere must not read as a
			// service that depends on nothing.
			if g.DependenciesOutside != 1 {
				t.Errorf("DependenciesOutside = %d, want 1", g.DependenciesOutside)
			}
		})
	}
}

// TestNeighbourhoodPinnedEndpointResolvesToAPort. The two-tier descent: an
// endpoint pinned to an address resolves to the exact interface and therefore
// to an exact cable; everything else resolves to the host. Nothing in the demo
// fixture sets ip_address_id, so this is the only place that path is exercised.
func TestNeighbourhoodPinnedEndpointResolvesToAPort(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newNeighbourFixture(t, e)

			ifaceID := f.ports["vm-1/eth0"]
			addr, err := domain.NewIPAddress(NewID(), "10.20.30.9", &ifaceID, domain.IPRolePrimary)
			if err != nil {
				t.Fatalf("building address: %v", err)
			}
			if err := f.s.CreateIPAddress(f.ctx, testActor, addr); err != nil {
				t.Fatalf("creating address: %v", err)
			}
			f.endpoint(t, "app", "pinned", 9443, domain.BindVIP, &addr.ID)

			g, err := f.s.Neighbourhood(f.ctx, NeighbourhoodQuery{AssetID: f.assets["vm-1"], Hops: 1})
			if err != nil {
				t.Fatalf("walking: %v", err)
			}
			var found bool
			for _, ep := range g.Endpoints {
				if ep.Name != "pinned" {
					continue
				}
				found = true
				if ep.BoundInterfaceID != ifaceID {
					t.Errorf("bound interface = %q, want %q", ep.BoundInterfaceID, ifaceID)
				}
				if ep.BoundAssetID != f.assets["vm-1"] || ep.BoundPort != "eth0" {
					t.Errorf("bound to %s/%s, want vm-1/eth0", ep.BoundAssetID, ep.BoundPort)
				}
				if ep.AddrText != "10.20.30.9" {
					t.Errorf("addr = %q, want 10.20.30.9", ep.AddrText)
				}
			}
			if !found {
				t.Fatal("the pinned endpoint is missing")
			}
		})
	}
}

// TestNeighbourhoodBoundsItself covers the two limits and the honesty
// obligation that comes with each: the hop count is clamped rather than
// trusted, and a node budget that cuts something says how much it cut.
func TestNeighbourhoodBoundsItself(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newNeighbourFixture(t, e)

			t.Run("the hop count is clamped", func(t *testing.T) {
				huge, err := f.s.Neighbourhood(f.ctx, NeighbourhoodQuery{
					AssetID: f.assets["vm-1"], Hops: 9999,
				})
				if err != nil {
					t.Fatalf("walking: %v", err)
				}
				if huge.Hops != NeighbourhoodMaxHops {
					t.Errorf("Hops = %d, want it clamped to %d", huge.Hops, NeighbourhoodMaxHops)
				}
				zero, err := f.s.Neighbourhood(f.ctx, NeighbourhoodQuery{AssetID: f.assets["vm-1"]})
				if err != nil {
					t.Fatalf("walking: %v", err)
				}
				if zero.Hops != 1 {
					t.Errorf("Hops = %d for an unset hop count, want 1", zero.Hops)
				}
			})

			t.Run("the node budget reports what it cut", func(t *testing.T) {
				g, err := f.s.Neighbourhood(f.ctx, NeighbourhoodQuery{
					AssetID: f.assets["vm-1"], Hops: 3, MaxAssets: 3,
				})
				if err != nil {
					t.Fatalf("walking: %v", err)
				}
				if len(g.Assets) != 3 {
					t.Errorf("%d assets returned, want the budget of 3", len(g.Assets))
				}
				if g.AssetsElided != 4 {
					t.Errorf("AssetsElided = %d, want 4; a silently missing node is a lie", g.AssetsElided)
				}
				// The cut falls on the furthest things, never on the subject.
				if g.Assets[0].ID != f.assets["vm-1"] {
					t.Error("the subject was cut by its own node budget")
				}
			})
		})
	}
}

// TestNeighbourhoodOfARetiredAsset. Soft delete only: retiring an asset keeps
// the row, so its page -- and therefore its diagram -- has to keep working.
// Retired NEIGHBOURS are excluded, because a decommissioned box is not part of
// today's picture; the subject is not, because the page it is on still exists.
func TestNeighbourhoodOfARetiredAsset(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newNeighbourFixture(t, e)

			if err := f.s.RetireAsset(f.ctx, testPermit, f.assets["vm-1"]); err != nil {
				t.Fatalf("retiring: %v", err)
			}

			g, err := f.s.Neighbourhood(f.ctx, NeighbourhoodQuery{AssetID: f.assets["vm-1"], Hops: 2})
			if err != nil {
				t.Fatalf("walking: %v", err)
			}
			got := f.names(g)
			if _, ok := got["vm-1"]; !ok {
				t.Error("the retired subject is missing from its own diagram")
			}

			// From the bridge, the retired guest must not appear.
			fromBridge, err := f.s.Neighbourhood(f.ctx, NeighbourhoodQuery{AssetID: f.assets["br-1"], Hops: 2})
			if err != nil {
				t.Fatalf("walking from the bridge: %v", err)
			}
			if _, ok := f.names(fromBridge)["vm-1"]; ok {
				t.Error("a retired asset appears as a live neighbour")
			}
		})
	}
}

// TestNeighbourhoodOfNothingIsNotFound: an id that resolves to no asset is a
// 404, never an empty picture that reads as "this box is connected to nothing".
func TestNeighbourhoodOfNothingIsNotFound(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newNeighbourFixture(t, e)

			_, err := f.s.Neighbourhood(f.ctx, NeighbourhoodQuery{
				AssetID: "00000000-0000-0000-0000-000000000000", Hops: 2,
			})
			if !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("err = %v, want ErrNotFound", err)
			}
			if _, err := f.s.Neighbourhood(f.ctx, NeighbourhoodQuery{}); !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("err for an empty id = %v, want ErrInvalid", err)
			}
		})
	}
}
