package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/gabriel/invctl/internal/domain"
)

// The path walk, against a fixture built to have exactly the shapes that can
// go wrong: a dual-homed pair of hosts (redundancy the union must keep), a
// management shortcut that would be the shortest route if the data-plane rule
// leaked, an island with a placement (the stranded-instance finding), and a
// co-located pair (a path of zero hops).
//
//	   sw-1 ──── sw-2          the cores, peered
//	  /    \    /    \
//	h-1     ×  ×      h-2      each host dual-homed
//	  \______________/
//	         │ mgmt            h-1 ── oob ── h-2: ONE hop, mgmt ports
//	        oob
//	h-3                        island: cabled to nothing
type pathFixture struct {
	s      *SQLStore
	ctx    context.Context
	assets map[string]string
	ports  map[string]string
	svc    map[string]string
}

func newPathFixture(t *testing.T, e Engine) *pathFixture {
	t.Helper()
	s, ctx := newStore(t, e)
	f := &pathFixture{
		s: s, ctx: ctx,
		assets: map[string]string{},
		ports:  map[string]string{},
		svc:    map[string]string{},
	}
	envID := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)

	asset := func(kind, name string) {
		t.Helper()
		f.assets[name] = mustAsset(t, s, ctx, kind, name, nil, envID)
	}
	port := func(asset, name string, mgmt bool) {
		t.Helper()
		iface, err := domain.NewInterface(NewID(), f.assets[asset], name, domain.FFSFP28)
		if err != nil {
			t.Fatalf("building %s/%s: %v", asset, name, err)
		}
		iface.IsMgmt = mgmt
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

	for _, sw := range []string{"sw-1", "sw-2", "oob"} {
		asset(domain.KindSwitch, sw)
	}
	asset(domain.KindServer, "h-1")
	asset(domain.KindServer, "h-2")
	asset(domain.KindServer, "h-3")

	for _, spec := range []struct {
		at, name string
		mgmt     bool
	}{
		{"sw-1", "e1", false}, {"sw-1", "e2", false}, {"sw-1", "peer", false},
		{"sw-2", "e1", false}, {"sw-2", "e2", false}, {"sw-2", "peer", false},
		{"h-1", "eno1", false}, {"h-1", "eno2", false}, {"h-1", "mgmt0", true},
		{"h-2", "eno1", false}, {"h-2", "eno2", false}, {"h-2", "mgmt0", true},
		{"oob", "p1", true}, {"oob", "p2", true},
	} {
		port(spec.at, spec.name, spec.mgmt)
	}

	cable("h-1/eno1", "sw-1/e1")
	cable("h-1/eno2", "sw-2/e1")
	cable("h-2/eno1", "sw-1/e2")
	cable("h-2/eno2", "sw-2/e2")
	cable("sw-1/peer", "sw-2/peer")
	// The management shortcut: h-1 to h-2 in TWO hops through oob, against
	// THREE through the cores. If the data-plane rule leaks, the shortest
	// path search will prefer it and the picture will route production
	// traffic through the console network.
	cable("h-1/mgmt0", "oob/p1")
	cable("h-2/mgmt0", "oob/p2")

	service := func(code, host string) {
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
		if host == "" {
			return
		}
		si, err := domain.NewServiceInstance(NewID(), svc.ID, f.assets[host],
			domain.RuntimeSystemd, 0, s.Now())
		if err != nil {
			t.Fatalf("building instance of %s: %v", code, err)
		}
		if err := s.CreateInstance(ctx, testActor, si); err != nil {
			t.Fatalf("placing %s on %s: %v", code, host, err)
		}
	}
	service("app", "h-1")
	service("db", "h-2")
	service("beside", "h-1") // co-located with app
	service("stranded", "h-3")
	service("nowhere", "") // exists, runs nowhere

	return f
}

func (f *pathFixture) names(g *PathGraph) []string {
	out := make([]string, 0, len(g.Assets))
	for _, a := range g.Assets {
		out = append(out, a.Name)
	}
	return out
}

func TestPathDrawsTheUnionOfShortestRoutes(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newPathFixture(t, e)
			g, err := f.s.Path(f.ctx, PathQuery{
				FromServiceID: f.svc["app"], ToServiceID: f.svc["db"],
			})
			if err != nil {
				t.Fatalf("Path: %v", err)
			}
			if !g.Found || g.Hops != 2 {
				t.Fatalf("Found=%v Hops=%d, want a 2-hop route: host, core, host", g.Found, g.Hops)
			}
			// Walking order: the from-host, the two cores at one hop -- BOTH,
			// because both routes are equally short and drawing one would
			// assert less redundancy than exists -- then the far host.
			want := []string{"h-1", "sw-1", "sw-2", "h-2"}
			if got := f.names(g); !reflect.DeepEqual(got, want) {
				t.Errorf("path assets = %v, want %v", got, want)
			}
			if len(g.Links) != 4 {
				t.Errorf("%d links drawn, want the 4 host-to-core cables; the core peer link "+
					"is not on a shortest route and the mgmt cables are not routes at all", len(g.Links))
			}
			for _, l := range g.Links {
				if l.A.IsMgmt || l.B.IsMgmt {
					t.Errorf("a management cable %s/%s-%s/%s was drawn as path", l.A.AssetID, l.A.Name, l.B.AssetID, l.B.Name)
				}
			}
			// The two services and the declared shape of the question.
			if len(g.Services) != 2 || len(g.Placements) != 2 {
				t.Errorf("services=%d placements=%d, want exactly the two ends decorated",
					len(g.Services), len(g.Placements))
			}
		})
	}
}

func TestPathNeverRoutesThroughManagement(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newPathFixture(t, e)
			g, err := f.s.Path(f.ctx, PathQuery{
				FromAssetID: f.assets["h-1"], ToAssetID: f.assets["h-2"],
			})
			if err != nil {
				t.Fatalf("Path: %v", err)
			}
			// The oob route is 2 hops; the honest answer is 2 hops through a
			// core. Same length -- so the assertion that bites is oob's
			// absence, not the count.
			for _, n := range f.names(g) {
				if n == "oob" {
					t.Fatalf("the path runs through the console switch; the management "+
						"plane leaked into a data path: %v", f.names(g))
				}
			}
			if !g.Found || g.Hops != 2 {
				t.Errorf("Found=%v Hops=%d, want the 2-hop data route", g.Found, g.Hops)
			}
		})
	}
}

func TestPathCoLocatedIsZeroHops(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newPathFixture(t, e)
			g, err := f.s.Path(f.ctx, PathQuery{
				FromServiceID: f.svc["app"], ToServiceID: f.svc["beside"],
			})
			if err != nil {
				t.Fatalf("Path: %v", err)
			}
			if !g.Found || g.Hops != 0 {
				t.Fatalf("Found=%v Hops=%d, want found at zero hops: they share h-1", g.Found, g.Hops)
			}
			if got := f.names(g); !reflect.DeepEqual(got, []string{"h-1"}) {
				t.Errorf("assets = %v, want just the shared host", got)
			}
			if len(g.Links) != 0 {
				t.Errorf("%d links drawn for a co-located pair; traffic never crosses a cable", len(g.Links))
			}
		})
	}
}

func TestPathReportsTheGapItCannotBridge(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newPathFixture(t, e)
			g, err := f.s.Path(f.ctx, PathQuery{
				FromServiceID: f.svc["app"], ToServiceID: f.svc["stranded"],
			})
			if err != nil {
				t.Fatalf("Path: %v", err)
			}
			if g.Found {
				t.Fatal("a route to an uncabled island was found")
			}
			// Both sides still drawn: the gap is the finding and the boxes are
			// its evidence.
			got := f.names(g)
			if !reflect.DeepEqual(got, []string{"h-1", "h-3"}) {
				t.Errorf("assets = %v, want both stranded ends and nothing else", got)
			}
			if !reflect.DeepEqual(g.Unrouted, []string{"h-1", "h-3"}) {
				t.Errorf("Unrouted = %v, want both anchors named", g.Unrouted)
			}
		})
	}
}

func TestPathToItsOwnNetwork(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newPathFixture(t, e)
			s, ctx := f.s, f.ctx

			// h-1 attaches to a core group whose members are the two switches.
			groupID := mustNetGroup(t, s, ctx, "core", domain.NetGroupMCLAG,
				domain.NetRoleCore, domain.AvailActiveActive)
			mustNetGroupMember(t, s, ctx, groupID, f.assets["sw-1"], "member")
			mustNetGroupMember(t, s, ctx, groupID, f.assets["sw-2"], "member")
			na, err := domain.NewNetAttachment(NewID(), f.assets["h-1"], groupID,
				domain.PlaneData, s.Now())
			if err != nil {
				t.Fatalf("building attachment: %v", err)
			}
			members := []domain.NetAttachmentMember{
				{AttachmentID: na.ID, AssetID: f.assets["sw-1"]},
				{AttachmentID: na.ID, AssetID: f.assets["sw-2"]},
			}
			if err := s.CreateNetAttachment(ctx, testActor, na, members); err != nil {
				t.Fatalf("creating attachment: %v", err)
			}

			g, err := s.Path(f.ctx, PathQuery{FromServiceID: f.svc["app"]})
			if err != nil {
				t.Fatalf("Path: %v", err)
			}
			if !g.Found || g.Hops != 1 {
				t.Fatalf("Found=%v Hops=%d, want the one-hop descent onto its chassis", g.Found, g.Hops)
			}
			if got := f.names(g); !reflect.DeepEqual(got, []string{"h-1", "sw-1", "sw-2"}) {
				t.Errorf("assets = %v, want the host and BOTH chassis it attaches through", got)
			}
			if len(g.Links) != 2 {
				t.Errorf("%d links, want h-1's two uplink cables", len(g.Links))
			}

			// The same question about a service whose network is not modelled.
			bare, err := s.Path(f.ctx, PathQuery{FromServiceID: f.svc["stranded"]})
			if err != nil {
				t.Fatalf("Path: %v", err)
			}
			if bare.Found || len(bare.To.AssetIDs) != 0 {
				t.Errorf("Found=%v with %d target chassis for an unattached host; absence of "+
					"topology data must not become a claim", bare.Found, len(bare.To.AssetIDs))
			}
		})
	}
}

func TestPathValidatesItsEnds(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newPathFixture(t, e)
			if _, err := f.s.Path(f.ctx, PathQuery{}); !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("no FROM end: got %v, want ErrInvalid", err)
			}
			if _, err := f.s.Path(f.ctx, PathQuery{
				FromServiceID: f.svc["app"], FromAssetID: f.assets["h-1"],
			}); !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("two FROM ends: got %v, want ErrInvalid", err)
			}
			if _, err := f.s.Path(f.ctx, PathQuery{
				FromServiceID: "no-such-id",
			}); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("unknown service: got %v, want ErrNotFound", err)
			}
			if _, err := f.s.Path(f.ctx, PathQuery{
				FromAssetID: "no-such-id",
			}); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("unknown asset: got %v, want ErrNotFound", err)
			}

			// A service that runs nowhere is a valid question with an honest
			// empty answer, not an error.
			g, err := f.s.Path(f.ctx, PathQuery{
				FromServiceID: f.svc["nowhere"], ToServiceID: f.svc["db"],
			})
			if err != nil {
				t.Fatalf("Path for a placement-less service: %v", err)
			}
			if g.Found || len(g.From.AssetIDs) != 0 {
				t.Errorf("Found=%v anchors=%d for a service with no placements", g.Found, len(g.From.AssetIDs))
			}
		})
	}
}

func TestPathIsDeterministic(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newPathFixture(t, e)
			q := PathQuery{FromServiceID: f.svc["app"], ToServiceID: f.svc["db"]}
			first, err := f.s.Path(f.ctx, q)
			if err != nil {
				t.Fatalf("first: %v", err)
			}
			second, err := f.s.Path(f.ctx, q)
			if err != nil {
				t.Fatalf("second: %v", err)
			}
			if !reflect.DeepEqual(first, second) {
				t.Error("two identical questions produced different answers; two people " +
					"on the same call would be comparing different pictures")
			}
		})
	}
}

// TestNeighbourhoodDoesNotTransitManagementCabling.
//
// The finding this pins was invisible on a small fixture and decisive on a real
// one: every host has a console cable to the same out-of-band switch, so a walk
// that transits management cabling puts every host two hops from every other
// host. Measured on a 5,000-asset estate, an unrestricted two-hop walk reached
// 5,010 assets against 384 -- and the 60-node budget then serves an
// alphabetical slice of the estate as "what this touches".
//
// The rule is seed-only, not never: the subject's own console switch is
// something it is plugged into, so it appears at hop 1 with its cable drawn,
// and dead-ends there.
//
// This fixture is reused because it already has the shape: h-1 and h-2 each
// have a mgmt0 cabled to `oob` and nothing else joins them except the cores.
func TestNeighbourhoodDoesNotTransitManagementCabling(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newPathFixture(t, e)

			g, err := f.s.Neighbourhood(f.ctx, NeighbourhoodQuery{
				AssetID: f.assets["h-1"], Hops: 1,
			})
			if err != nil {
				t.Fatalf("Neighbourhood: %v", err)
			}
			at := map[string]int{}
			for _, a := range g.Assets {
				at[a.Name] = a.Hop
			}
			if _, ok := at["oob"]; !ok {
				t.Error("the subject's own console switch is missing at one hop; " +
					`"what is this plugged into" includes it`)
			}
			// And the cable to it is drawn, because both ends are in the set.
			var mgmtDrawn bool
			for _, l := range g.Links {
				if l.A.IsMgmt || l.B.IsMgmt {
					mgmtDrawn = true
				}
			}
			if !mgmtDrawn {
				t.Error("the console cable is not drawn even though both its ends are in " +
					"the picture; not traversing an edge and not showing it are different things")
			}

			// Two hops: the cores are legitimately reachable, h-2 through them.
			// What must NOT happen is h-2 arriving via `oob`, and the way to
			// tell is a subject whose ONLY route to the far host is management.
			iso, err := f.s.Neighbourhood(f.ctx, NeighbourhoodQuery{
				AssetID: f.assets["h-3"], Hops: 4,
			})
			if err != nil {
				t.Fatalf("Neighbourhood of the island: %v", err)
			}
			for _, a := range iso.Assets {
				if a.Name != "h-3" {
					t.Errorf("h-3 is cabled to nothing at all, yet the walk reached %q at hop %d",
						a.Name, a.Hop)
				}
			}
		})
	}
}

// TestNeighbourhoodStopsAtTheConsoleSwitch is the transitive half: with a
// management-only path between two hosts, the far host must not appear however
// many hops are allowed.
func TestNeighbourhoodStopsAtTheConsoleSwitch(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newPathFixture(t, e)
			s, ctx := f.s, f.ctx

			// A box whose ONLY cable is to the console switch -- the shape the
			// seeded estate's srv-backup-proxy-1 has.
			lonely := mustAsset(t, s, ctx, domain.KindServer, "console-only", nil)
			iface, err := domain.NewInterface(NewID(), lonely, "mgmt0", domain.FFRJ45)
			if err != nil {
				t.Fatalf("building the port: %v", err)
			}
			iface.IsMgmt = true
			if err := s.CreateInterface(ctx, testActor, iface); err != nil {
				t.Fatalf("creating the port: %v", err)
			}
			oobPort, err := domain.NewInterface(NewID(), f.assets["oob"], "p9", domain.FFRJ45)
			if err != nil {
				t.Fatalf("building the oob port: %v", err)
			}
			oobPort.IsMgmt = true
			if err := s.CreateInterface(ctx, testActor, oobPort); err != nil {
				t.Fatalf("creating the oob port: %v", err)
			}
			link, err := domain.NewLink(NewID(), iface.ID, oobPort.ID)
			if err != nil {
				t.Fatalf("building the console cable: %v", err)
			}
			if err := s.CreateLink(ctx, testActor, link); err != nil {
				t.Fatalf("cabling to the console switch: %v", err)
			}

			// From h-1, four hops: oob is one hop away, console-only is two --
			// but only THROUGH oob, which is where the walk must stop.
			g, err := s.Neighbourhood(ctx, NeighbourhoodQuery{AssetID: f.assets["h-1"], Hops: 4})
			if err != nil {
				t.Fatalf("Neighbourhood: %v", err)
			}
			for _, a := range g.Assets {
				if a.Name == "console-only" {
					t.Errorf("the walk transited the console switch to reach %q at hop %d; "+
						"every host shares that switch, so this is how the page becomes "+
						"a picture of the whole estate", a.Name, a.Hop)
				}
			}
		})
	}
}

// TestPathDropsRetiredTransit.
//
// path.go filters no lifecycle of its own: dataPlaneAdjacency takes any link,
// pathAssets any asset. That is safe only because of a cross-file invariant --
// RetireAsset cascade-retires that asset's links, memberships and attachments
// (assets.go) and RetireNetGroup does the same for a group (reach.go). Nothing
// in path.go says it leans on that, so a bulk import that wrote rows directly
// could break it silently. This is the boundary test that would notice.
func TestPathDropsRetiredTransit(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newPathFixture(t, e)

			before, err := f.s.Path(f.ctx, PathQuery{
				FromServiceID: f.svc["app"], ToServiceID: f.svc["db"],
			})
			if err != nil {
				t.Fatalf("Path: %v", err)
			}
			if !before.Found {
				t.Fatal("no route before retiring anything")
			}
			var hadSW1 bool
			for _, n := range f.names(before) {
				if n == "sw-1" {
					hadSW1 = true
				}
			}
			if !hadSW1 {
				t.Fatal("sw-1 is not on the path, so retiring it proves nothing")
			}

			if err := f.s.RetireAsset(f.ctx, testActor, f.assets["sw-1"]); err != nil {
				t.Fatalf("retiring the switch: %v", err)
			}

			after, err := f.s.Path(f.ctx, PathQuery{
				FromServiceID: f.svc["app"], ToServiceID: f.svc["db"],
			})
			if err != nil {
				t.Fatalf("Path after retiring: %v", err)
			}
			for _, n := range f.names(after) {
				if n == "sw-1" {
					t.Error("a retired switch is still drawn on the path; path.go relies on " +
						"RetireAsset cascading to its links, and that invariant just broke")
				}
			}
			// The other chassis still carries the traffic, so the answer is a
			// route through sw-2 rather than no route at all.
			if !after.Found {
				t.Error("no route after retiring one of two chassis; the hosts are dual-homed")
			}
		})
	}
}

// TestPathRefusesADisabledPortAndSaysSo.
//
// `enabled` is declared intent -- an operator saying "this port is
// administratively down" -- so traffic does not cross it and a route through
// one would be a false claim. But "no path" and "no path because somebody shut
// this port" send an operator to two different places, so the second must not
// be flattened into the first.
func TestPathRefusesADisabledPortAndSaysSo(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newPathFixture(t, e)
			s, ctx := f.s, f.ctx

			// h-3 is the island. Cable it to sw-1 through a port that is
			// administratively down, so cabling exists and no route does.
			port, err := domain.NewInterface(NewID(), f.assets["h-3"], "eno1", domain.FFSFP28)
			if err != nil {
				t.Fatalf("building the port: %v", err)
			}
			port.Enabled = false
			if err := s.CreateInterface(ctx, testActor, port); err != nil {
				t.Fatalf("creating the port: %v", err)
			}
			far := mustInterface(t, s, ctx, f.assets["sw-1"], "e9")
			link, err := domain.NewLink(NewID(), port.ID, far)
			if err != nil {
				t.Fatalf("building the cable: %v", err)
			}
			if err := s.CreateLink(ctx, testActor, link); err != nil {
				t.Fatalf("cabling: %v", err)
			}

			g, err := s.Path(ctx, PathQuery{
				FromServiceID: f.svc["app"], ToServiceID: f.svc["stranded"],
			})
			if err != nil {
				t.Fatalf("Path: %v", err)
			}
			if g.Found {
				t.Error("a route was found across an administratively disabled port; " +
					"traffic does not cross one, so that is a false claim")
			}
			if len(g.Blocked) == 0 {
				t.Fatal("no blocking port named: the cabling is there and a port is shut, " +
					"which is the one answer somebody can act on")
			}
			joined := strings.Join(g.Blocked, " ")
			for _, want := range []string{"h-3", "eno1", "disabled", "sw-1"} {
				if !strings.Contains(joined, want) {
					t.Errorf("the blocking port %q does not mention %q; an operator needs "+
						"the box and the port, and which end to go and look at", joined, want)
				}
			}

			// And the ordinary no-cabling case must NOT claim a blocked port.
			plain, err := s.Path(ctx, PathQuery{
				FromServiceID: f.svc["app"], ToAssetID: f.assets["oob"],
			})
			if err != nil {
				t.Fatalf("Path to the console switch: %v", err)
			}
			if len(plain.Blocked) > 0 {
				t.Errorf("a genuinely uncabled pair blamed a port: %v", plain.Blocked)
			}
		})
	}
}

// TestPathToItsOwnNetworkExcludesItself: a forwarder that both attaches to a
// group and is a member of it must not become its own destination.
func TestPathToItsOwnNetworkExcludesItself(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newPathFixture(t, e)
			s, ctx := f.s, f.ctx

			groupID := mustNetGroup(t, s, ctx, "core2", domain.NetGroupMCLAG,
				domain.NetRoleCore, domain.AvailActiveActive)
			// h-1 is a member of the group AND attaches to it.
			mustNetGroupMember(t, s, ctx, groupID, f.assets["h-1"], "member")
			mustNetGroupMember(t, s, ctx, groupID, f.assets["sw-1"], "member")
			na, err := domain.NewNetAttachment(NewID(), f.assets["h-1"], groupID,
				domain.PlaneData, s.Now())
			if err != nil {
				t.Fatalf("building attachment: %v", err)
			}
			members := []domain.NetAttachmentMember{
				{AttachmentID: na.ID, AssetID: f.assets["h-1"]},
				{AttachmentID: na.ID, AssetID: f.assets["sw-1"]},
			}
			if err := s.CreateNetAttachment(ctx, testActor, na, members); err != nil {
				t.Fatalf("creating attachment: %v", err)
			}

			g, err := s.Path(ctx, PathQuery{FromAssetID: f.assets["h-1"]})
			if err != nil {
				t.Fatalf("Path: %v", err)
			}
			for _, id := range g.To.AssetIDs {
				if id == f.assets["h-1"] {
					t.Error("h-1 is its own destination, which draws a zero-hop path from a " +
						"box to itself -- a picture of nothing")
				}
			}
			if len(g.To.AssetIDs) == 0 {
				t.Error("dropping the self-anchor emptied the target; sw-1 is still a " +
					"chassis it attaches through and is the real answer")
			}
		})
	}
}
