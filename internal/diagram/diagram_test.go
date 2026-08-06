// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package diagram

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const eps = 1e-9

// node and edge are shorthand so a fixture reads as a graph rather than as a
// wall of struct literals.
func node(id, kind string, layer Layer) Node {
	return Node{ID: id, Label: id, Kind: kind, Layer: layer, Status: "ok"}
}

func edge(from, to string, layer Layer) Edge {
	return Edge{From: from, To: to, Kind: "link", Layer: layer}
}

// TestBuildLaysOutTheShapesThatMatter walks the shapes a neighbourhood
// actually takes: a chain, a star, a diamond, a node nothing touches, a
// neighbourhood with a layer nobody is in, and an ordering whose crossing
// count is known by hand.
//
// Every case is put through the same invariant check afterwards, because the
// properties that make the picture readable -- no two boxes overlapping,
// nothing outside the canvas, no band drawn through another one -- have to
// hold for all of them and not just for the one they were written for.
func TestBuildLaysOutTheShapesThatMatter(t *testing.T) {
	cases := []struct {
		name      string
		in        Input
		wantBands []Layer
		// wantBefore and wantAfter are asserted only when wantExactCrossings
		// is set; elsewhere the invariant check asserts After <= Before.
		wantExactCrossings bool
		wantBefore         int
		wantAfter          int
	}{
		{
			name: "a chain down through all three layers",
			in: Input{
				Subject: "vm-app-1/eth0",
				Nodes: []Node{
					node("orders-api", "service", LayerService),
					node("vm-app-1/eth0", "veth", LayerVirtual),
					node("hv-01", "hypervisor", LayerPhysical),
				},
				Edges: []Edge{
					edge("orders-api", "vm-app-1/eth0", LayerService),
					edge("vm-app-1/eth0", "hv-01", LayerVirtual),
				},
			},
			wantBands:          []Layer{LayerService, LayerVirtual, LayerPhysical},
			wantExactCrossings: true,
		},
		{
			name: "a chain inside one band, drawn as arcs",
			in: Input{
				Subject: "sw-core-1",
				Nodes: []Node{
					node("hv-01", "hypervisor", LayerPhysical),
					node("sw-core-1", "switch", LayerPhysical),
					node("fw-edge-1", "firewall", LayerPhysical),
				},
				Edges: []Edge{
					edge("hv-01", "sw-core-1", LayerPhysical),
					edge("sw-core-1", "fw-edge-1", LayerPhysical),
				},
			},
			wantBands:          []Layer{LayerPhysical},
			wantExactCrossings: true,
		},
		{
			name: "a star: one bridge, leaves above and below",
			in: Input{
				Subject: "hv-01-br0",
				Nodes: []Node{
					node("hv-01-br0", "bridge", LayerVirtual),
					node("vm-app-1", "vm", LayerService),
					node("vm-db-1", "vm", LayerService),
					node("hv-01", "hypervisor", LayerPhysical),
					node("sw-core-1", "switch", LayerPhysical),
				},
				Edges: []Edge{
					edge("hv-01-br0", "vm-app-1", LayerVirtual),
					edge("hv-01-br0", "vm-db-1", LayerVirtual),
					edge("hv-01-br0", "hv-01", LayerVirtual),
					edge("hv-01-br0", "sw-core-1", LayerPhysical),
				},
			},
			wantBands:          []Layer{LayerService, LayerVirtual, LayerPhysical},
			wantExactCrossings: true,
		},
		{
			name: "a diamond: two paths down to the same wire",
			in: Input{
				Subject: "haproxy-edge",
				Nodes: []Node{
					node("haproxy-edge", "service", LayerService),
					node("br0", "bridge", LayerVirtual),
					node("br1", "bridge", LayerVirtual),
					node("bond0", "lag", LayerPhysical),
				},
				Edges: []Edge{
					edge("haproxy-edge", "br0", LayerService),
					edge("haproxy-edge", "br1", LayerService),
					edge("br0", "bond0", LayerVirtual),
					edge("br1", "bond0", LayerVirtual),
				},
			},
			wantBands:          []Layer{LayerService, LayerVirtual, LayerPhysical},
			wantExactCrossings: true,
		},
		{
			name: "a node with no edges at all",
			in: Input{
				Subject: "storage-01",
				Nodes:   []Node{node("storage-01", "storage", LayerPhysical)},
			},
			wantBands:          []Layer{LayerPhysical},
			wantExactCrossings: true,
		},
		{
			name: "an unattached node beside a connected pair keeps its place",
			in: Input{
				Subject: "sw-core-1",
				Nodes: []Node{
					node("hv-01", "hypervisor", LayerPhysical),
					node("pdu-a1", "pdu", LayerPhysical),
					node("sw-core-1", "switch", LayerPhysical),
				},
				Edges: []Edge{edge("hv-01", "sw-core-1", LayerPhysical)},
			},
			wantBands:          []Layer{LayerPhysical},
			wantExactCrossings: true,
		},
		{
			name: "a neighbourhood with no bridges in it drops the virtual band",
			in: Input{
				Subject: "hv-01",
				Nodes: []Node{
					node("orders-api", "service", LayerService),
					node("billing", "service", LayerService),
					node("hv-01", "hypervisor", LayerPhysical),
					node("hv-02", "hypervisor", LayerPhysical),
				},
				Edges: []Edge{
					edge("orders-api", "hv-01", LayerService),
					edge("billing", "hv-02", LayerService),
				},
			},
			wantBands: []Layer{LayerService, LayerPhysical},
			// One crossing in the seed, and it is the anchor's doing: hv-01 is
			// the subject, so it takes the middle slot of a band of two and
			// swaps past hv-02, which puts the two descents in the wrong
			// order. The sweep then reorders the service band instead, which
			// is the trade the anchor rule is supposed to make -- the subject
			// stays put and everything else moves around it.
			wantExactCrossings: true,
			wantBefore:         1,
			wantAfter:          0,
		},
		{
			name: "a reversed bipartite ordering, six crossings by hand",
			// Four services declared left to right, four hosts declared left to
			// right, and every service wired to the host opposite it. Every one
			// of the six pairs of lines is an inversion, so the seed has six
			// crossings and a correct barycentre pass has none. No subject:
			// pinning one would move a node for a reason unrelated to what this
			// case is measuring.
			in: Input{
				Nodes: []Node{
					node("svc-1", "service", LayerService),
					node("svc-2", "service", LayerService),
					node("svc-3", "service", LayerService),
					node("svc-4", "service", LayerService),
					node("host-1", "server", LayerPhysical),
					node("host-2", "server", LayerPhysical),
					node("host-3", "server", LayerPhysical),
					node("host-4", "server", LayerPhysical),
				},
				Edges: []Edge{
					edge("svc-1", "host-4", LayerService),
					edge("svc-2", "host-3", LayerService),
					edge("svc-3", "host-2", LayerService),
					edge("svc-4", "host-1", LayerService),
				},
			},
			wantBands:          []Layer{LayerService, LayerPhysical},
			wantExactCrossings: true,
			wantBefore:         6,
			wantAfter:          0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Build(tc.in)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}

			var gotBands []Layer
			for _, b := range got.Bands {
				gotBands = append(gotBands, b.Layer)
			}
			if !reflect.DeepEqual(gotBands, tc.wantBands) {
				t.Errorf("bands = %v, want %v", gotBands, tc.wantBands)
			}

			if tc.wantExactCrossings {
				if got.Crossings.Before != tc.wantBefore || got.Crossings.After != tc.wantAfter {
					t.Errorf("crossings = %+v, want {Before:%d After:%d}",
						got.Crossings, tc.wantBefore, tc.wantAfter)
				}
			}

			checkInvariants(t, got, tc.in)

			// Determinism, asserted per case rather than once: the shapes
			// differ in which code paths they take through the sweep, and a
			// single fixture would only prove one of them stable.
			again, err := Build(tc.in)
			if err != nil {
				t.Fatalf("Build (second run): %v", err)
			}
			if !reflect.DeepEqual(got, again) {
				t.Error("two Builds of the same input produced different layouts")
			}
		})
	}
}

// checkInvariants asserts the properties that make a picture readable at all.
// They are checked for every fixture, not just the one that motivated each.
func checkInvariants(t *testing.T, l *Layout, in Input) {
	t.Helper()

	if l.Crossings.After > l.Crossings.Before {
		t.Errorf("ordering made it worse: %d crossings before, %d after",
			l.Crossings.Before, l.Crossings.After)
	}

	seen := 0
	for b, band := range l.Bands {
		seen += len(band.Nodes)
		if len(band.Nodes) == 0 {
			t.Errorf("band %d (%s) is empty and should not exist", b, band.Layer)
		}
		if band.Y < MarginY-eps {
			t.Errorf("band %d starts at y=%v, above the top margin", b, band.Y)
		}
		if b > 0 {
			gap := band.Y - l.Bands[b-1].Bottom()
			if math.Abs(gap-BandGutter) > eps {
				t.Errorf("gap above band %d is %v, want exactly one gutter of %v",
					b, gap, BandGutter)
			}
		}
		if b == len(l.Bands)-1 && math.Abs(l.Height-(band.Bottom()+MarginY)) > eps {
			t.Errorf("canvas height %v does not close on the last band at %v + margin",
				l.Height, band.Bottom())
		}

		for i, n := range band.Nodes {
			if n.Index != i {
				t.Errorf("node %q sits in slot %d but reports Index %d", n.ID, i, n.Index)
			}
			if n.W < MinNodeWidth-eps {
				t.Errorf("node %q is %v wide, below the %v floor", n.ID, n.W, MinNodeWidth)
			}
			if n.X < MarginX-eps || n.Right() > l.Width-MarginX+eps {
				t.Errorf("node %q spans x=%v..%v, outside the canvas margins of a %v-wide canvas",
					n.ID, n.X, n.Right(), l.Width)
			}
			if math.Abs(n.Y-band.Y) > eps || math.Abs(n.H-NodeHeight) > eps {
				t.Errorf("node %q is at y=%v h=%v, off its band's row at %v", n.ID, n.Y, n.H, band.Y)
			}
			if i > 0 {
				gap := n.X - band.Nodes[i-1].Right()
				if gap < NodeGap-eps {
					t.Errorf("nodes %q and %q are %v apart, closer than the %v gap",
						band.Nodes[i-1].ID, n.ID, gap, NodeGap)
				}
			}
		}
	}
	if seen != len(in.Nodes) {
		t.Errorf("laid out %d nodes, was given %d", seen, len(in.Nodes))
	}

	if len(l.Edges) != len(in.Edges) {
		t.Fatalf("laid out %d edges, was given %d", len(l.Edges), len(in.Edges))
	}
	for i, e := range l.Edges {
		if e.From != in.Edges[i].From || e.To != in.Edges[i].To {
			t.Errorf("edge %d is %q->%q, want the caller's order %q->%q",
				i, e.From, e.To, in.Edges[i].From, in.Edges[i].To)
		}
		from, ok := l.Node(e.From)
		if !ok {
			t.Fatalf("edge %d references %q, which Layout.Node cannot find", i, e.From)
		}
		to, ok := l.Node(e.To)
		if !ok {
			t.Fatalf("edge %d references %q, which Layout.Node cannot find", i, e.To)
		}
		if e.SameBand != (from.Layer == to.Layer) {
			t.Errorf("edge %d reports SameBand=%v for layers %s and %s",
				i, e.SameBand, from.Layer, to.Layer)
		}
		if !e.SameBand {
			// A cross-band edge anchors dead centre; only rails are fanned.
			if math.Abs(e.X1-from.CenterX()) > eps || math.Abs(e.X2-to.CenterX()) > eps {
				t.Errorf("edge %d does not anchor on its own endpoints' centres", i)
			}
			if e.Depth != 0 {
				t.Errorf("edge %d crosses bands and should not dip, got Depth=%v", i, e.Depth)
			}
			checkClearsEveryBox(t, l, i, e)
			continue
		}
		// A rail hangs from anywhere along its boxes' bottom edges -- the fan
		// is the point -- but never outside them, or the vertical would drop
		// through open space and read as unanchored.
		if e.X1 < from.X-eps || e.X1 > from.Right()+eps ||
			e.X2 < to.X-eps || e.X2 > to.Right()+eps {
			t.Errorf("edge %d anchors at x=%v and x=%v, outside its boxes (%v..%v and %v..%v)",
				i, e.X1, e.X2, from.X, from.Right(), to.X, to.Right())
		}
		if len(e.Waypoints) != 2 {
			t.Fatalf("same-band edge %d has %d waypoints, want the 2 ends of its rail",
				i, len(e.Waypoints))
		}
		rail := from.Bottom() + e.Depth
		for w, p := range e.Waypoints {
			if math.Abs(p.Y-rail) > eps {
				t.Errorf("edge %d waypoint %d is at y=%v, off its rail at %v", i, w, p.Y, rail)
			}
		}
		if math.Abs(e.Waypoints[0].X-e.X1) > eps || math.Abs(e.Waypoints[1].X-e.X2) > eps {
			t.Errorf("edge %d's rail runs x=%v..%v but its anchors are at %v and %v; "+
				"the verticals would be slanted", i,
				e.Waypoints[0].X, e.Waypoints[1].X, e.X1, e.X2)
		}
		band := l.Bands[bandIndexOf(l, from.Layer)]
		if e.Depth+ArcPad > band.ArcLane+eps {
			t.Errorf("edge %d dips %v into a lane only %v deep", i, e.Depth, band.ArcLane)
		}
		checkClearsEveryBox(t, l, i, e)
	}

	// The lane guarantees are pairwise too: no two overlapping same-band spans
	// share a rail, and a contained span is always above its container. These
	// are what make the drawn geometry equal to the ordering model for rails,
	// so they are invariants, not implementation details.
	for i := range l.Edges {
		for j := i + 1; j < len(l.Edges); j++ {
			a, b := l.Edges[i], l.Edges[j]
			if !a.SameBand || !b.SameBand {
				continue
			}
			fa, _ := l.Node(a.From)
			fb, _ := l.Node(b.From)
			if fa.Layer != fb.Layer {
				continue
			}
			loA, hiA := math.Min(a.X1, a.X2), math.Max(a.X1, a.X2)
			loB, hiB := math.Min(b.X1, b.X2), math.Max(b.X1, b.X2)
			if loA >= hiB || loB >= hiA {
				continue // Disjoint spans may share a depth freely.
			}
			if math.Abs(a.Depth-b.Depth) < eps {
				t.Errorf("edges %d and %d overlap (x %v..%v and %v..%v) and share depth %v, "+
					"so their rails are collinear and two connections read as one",
					i, j, loA, hiA, loB, hiB, a.Depth)
			}
			if loB >= loA && hiB <= hiA && b.Depth > a.Depth-eps {
				t.Errorf("edge %d (depth %v) contains edge %d (depth %v) but does not run "+
					"below it, so the contained rail pokes through its container",
					i, a.Depth, j, b.Depth)
			}
			if loA >= loB && hiA <= hiB && a.Depth > b.Depth-eps {
				t.Errorf("edge %d (depth %v) contains edge %d (depth %v) but does not run "+
					"below it, so the contained rail pokes through its container",
					j, b.Depth, i, a.Depth)
			}
		}
	}

	if in.Subject != "" {
		if l.SubjectID != in.Subject {
			t.Errorf("SubjectID = %q, want %q", l.SubjectID, in.Subject)
		}
		sub, ok := l.Node(in.Subject)
		if !ok {
			t.Fatalf("subject %q is not in the layout", in.Subject)
		}
		if !sub.Subject {
			t.Errorf("subject %q is not flagged as one", in.Subject)
		}
	}
}

// checkClearsEveryBox asserts the invariant place's doc comment claims: the line
// drawn for e touches no node box.
//
// This existed as prose and as nothing else, and it was false. An edge spanning
// two bands -- a service instance placed on bare metal, joining band 3 to
// band 1 -- was anchored at both ends and drawn as the straight line between
// them, which goes through band 2's row. On the seeded demo that put a
// backup-agent's line through hv-01-br0's box. Every test in this file passed,
// because every one of them asserted where the ANCHORS were and none asked where
// the LINE went.
//
// The route is reconstructed here rather than imported from the renderer, so the
// two could in principle disagree. What keeps them together is that the renderer
// has exactly one liberty -- fanning parallel edges sideways -- and for a routed
// edge that is clamped to ChannelSpread, which is less than half the gap the
// channel sits in. handlers' TestEdgePathFollowsItsWaypoints holds the other end.
func checkClearsEveryBox(t *testing.T, l *Layout, i int, e Edge) {
	t.Helper()

	// Boxes are shrunk a hair before testing. An edge legitimately starts and
	// ends ON a box boundary, and an exact-touch is not a crossing.
	const inset = 0.25

	// Anchors plus waypoints IS the drawn line now -- rails and channel
	// routes alike -- so the check is exact rather than sampled.
	route := edgePolyline(e)

	for _, p := range route {
		if p.X < -eps || p.X > l.Width+eps || p.Y < -eps || p.Y > l.Height+eps {
			t.Errorf("edge %d (%s->%s) routes through (%v, %v), outside the %vx%v canvas",
				i, e.From, e.To, p.X, p.Y, l.Width, l.Height)
		}
	}

	for _, band := range l.Bands {
		for _, n := range band.Nodes {
			if n.ID == e.From || n.ID == e.To {
				continue // Its own endpoints: it is anchored on them.
			}
			box := rect{n.X + inset, n.Y + inset, n.Right() - inset, n.Bottom() - inset}
			for s := 1; s < len(route); s++ {
				if segmentHitsRect(route[s-1], route[s], box) {
					t.Errorf("edge %d (%s->%s) passes through node %q's box "+
						"(x %v..%v, y %v..%v) on the segment (%v,%v)->(%v,%v)",
						i, e.From, e.To, n.ID, n.X, n.Right(), n.Y, n.Bottom(),
						route[s-1].X, route[s-1].Y, route[s].X, route[s].Y)
					return // One report per edge; the rest would be noise.
				}
			}
		}
	}
}

type rect struct{ x0, y0, x1, y1 float64 }

// segmentHitsRect reports whether the closed segment a->b shares any point with
// r, by clipping the segment's parameter range against each axis in turn: a
// slab test, which unlike sampling cannot miss a clipped corner.
func segmentHitsRect(a, b Point, r rect) bool {
	t0, t1 := 0.0, 1.0
	for _, ax := range [2]struct{ from, to, lo, hi float64 }{
		{a.X, b.X, r.x0, r.x1},
		{a.Y, b.Y, r.y0, r.y1},
	} {
		d := ax.to - ax.from
		if d == 0 {
			if ax.from < ax.lo || ax.from > ax.hi {
				return false // Parallel to this axis and outside the slab.
			}
			continue
		}
		lo, hi := (ax.lo-ax.from)/d, (ax.hi-ax.from)/d
		if lo > hi {
			lo, hi = hi, lo
		}
		t0, t1 = max(t0, lo), min(t1, hi)
		if t0 > t1 {
			return false
		}
	}
	return true
}

func bandIndexOf(l *Layout, layer Layer) int {
	for i, b := range l.Bands {
		if b.Layer == layer {
			return i
		}
	}
	return -1
}

// TestBuildIsDeterministicOnALargerGraph runs the same input twice and
// compares everything.
//
// The requirement is blunter than "the algorithm is deterministic": two people
// on the same call open the same URL and must see the same picture, and the
// usual way to lose that is a range over a map somewhere in the ordering. The
// fixture is deliberately larger and denser than the shape cases so that the
// sweep has real work to do and any map iteration has room to disagree with
// itself.
func TestBuildIsDeterministicOnALargerGraph(t *testing.T) {
	in := denseFixture()

	first, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for run := range 12 {
		again, err := Build(in)
		if err != nil {
			t.Fatalf("Build (run %d): %v", run, err)
		}
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("run %d produced a different layout from run 0", run)
		}
	}
}

// denseFixture is a neighbourhood shaped like the seeded estate: services on
// top, bridges and veths in the middle, hypervisors and switches at the
// bottom, with same-band cables and cross-band descents mixed together.
func denseFixture() Input {
	in := Input{Subject: "hv-02"}
	for i := 1; i <= 6; i++ {
		in.Nodes = append(in.Nodes, node("svc-"+strconv.Itoa(i), "service", LayerService))
	}
	for i := 1; i <= 4; i++ {
		in.Nodes = append(in.Nodes, node("br-"+strconv.Itoa(i), "bridge", LayerVirtual))
	}
	for i := 1; i <= 5; i++ {
		in.Nodes = append(in.Nodes, node("hv-0"+strconv.Itoa(i), "hypervisor", LayerPhysical))
	}
	in.Nodes = append(in.Nodes,
		node("sw-core-1", "switch", LayerPhysical),
		node("sw-core-2", "switch", LayerPhysical),
	)

	// Services descend to bridges in a deliberately tangled order.
	pairs := [][2]int{{1, 4}, {2, 3}, {3, 1}, {4, 2}, {5, 4}, {6, 1}}
	for _, p := range pairs {
		in.Edges = append(in.Edges, edge(
			"svc-"+strconv.Itoa(p[0]), "br-"+strconv.Itoa(p[1]), LayerService))
	}
	// Bridges land on hypervisors, in reverse.
	for i := 1; i <= 4; i++ {
		in.Edges = append(in.Edges, edge(
			"br-"+strconv.Itoa(i), "hv-0"+strconv.Itoa(6-i), LayerVirtual))
	}
	// Cables, all inside the physical band.
	for i := 1; i <= 5; i++ {
		in.Edges = append(in.Edges,
			edge("hv-0"+strconv.Itoa(i), "sw-core-1", LayerPhysical),
			edge("hv-0"+strconv.Itoa(i), "sw-core-2", LayerPhysical),
		)
	}
	in.Edges = append(in.Edges, edge("sw-core-1", "sw-core-2", LayerPhysical))
	// A service that talks to another service, inside the top band.
	in.Edges = append(in.Edges, edge("svc-1", "svc-5", LayerService))
	return in
}

// TestOrderingReducesCrossingsOnATangledGraph is the point of the heuristic:
// on a graph declared in an order nobody chose for legibility, the picture
// gets meaningfully better, and the number saying so is on the layout for a
// reader to see.
func TestOrderingReducesCrossingsOnATangledGraph(t *testing.T) {
	got, err := Build(denseFixture())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got.Crossings.Before == 0 {
		t.Fatal("fixture is not tangled; it has nothing to prove")
	}
	if got.Crossings.After >= got.Crossings.Before {
		t.Errorf("crossings %d -> %d: the sweep bought nothing",
			got.Crossings.Before, got.Crossings.After)
	}
	// Not an optimality claim -- barycentre makes none. It is a floor under a
	// regression that leaves the ordering essentially untouched.
	if want := got.Crossings.Before / 2; got.Crossings.After > want {
		t.Errorf("crossings %d -> %d, expected at most %d",
			got.Crossings.Before, got.Crossings.After, want)
	}
	t.Logf("crossings %d -> %d", got.Crossings.Before, got.Crossings.After)
}

// recount counts crossings from the returned layout alone, using only what a
// renderer can see: which band each endpoint is in and which slot it holds.
//
// It is a deliberate second implementation of the rule in the Crossings doc
// comment. Layout.Crossings.After is a number printed next to a picture, and
// the failure that number invites is describing an ordering other than the one
// that was drawn -- a candidate scored, then not restored. Recomputing it from
// the geometry is the only way to catch that.
func recount(t *testing.T, l *Layout) int {
	t.Helper()

	type end struct{ band, slot int }
	locate := func(id string) end {
		t.Helper()
		for b, band := range l.Bands {
			for _, n := range band.Nodes {
				if n.ID == id {
					return end{band: b, slot: n.Index}
				}
			}
		}
		t.Fatalf("edge endpoint %q is not in the layout", id)
		return end{}
	}

	ends := make([][2]end, len(l.Edges))
	for i, e := range l.Edges {
		a, b := locate(e.From), locate(e.To)
		if a.band > b.band {
			a, b = b, a
		}
		ends[i] = [2]end{a, b}
	}

	total := 0
	for i := range ends {
		for j := i + 1; j < len(ends); j++ {
			u1, l1 := ends[i][0], ends[i][1]
			u2, l2 := ends[j][0], ends[j][1]

			// One rail, one cross-band edge: pierced when the line leaves a
			// box strictly inside the rail's span. Mirrors crosses().
			same1, same2 := u1.band == l1.band, u2.band == l2.band
			if same1 != same2 {
				arcU, arcL, drop := u1, l1, u2
				if same2 {
					arcU, arcL, drop = u2, l2, u1
				}
				if arcU.band == drop.band && drop.slot != arcU.slot && drop.slot != arcL.slot {
					lo, hi := minMax(arcU.slot, arcL.slot)
					if lo < drop.slot && drop.slot < hi {
						total++
					}
				}
				continue
			}

			if u1.band != u2.band || l1.band != l2.band {
				continue
			}
			if u1.band == l1.band {
				lo1, hi1 := minMax(u1.slot, l1.slot)
				lo2, hi2 := minMax(u2.slot, l2.slot)
				if (lo1 < lo2 && lo2 < hi1 && hi1 < hi2) || (lo2 < lo1 && lo1 < hi2 && hi2 < hi1) {
					total++
				}
				continue
			}
			if (u1.slot-u2.slot)*(l1.slot-l2.slot) < 0 {
				total++
			}
		}
	}
	return total
}

// TestGeneratedNeighbourhoodsHoldTheInvariants runs the whole thing over a
// corpus of randomly shaped neighbourhoods.
//
// The hand-written fixtures each pin one behaviour; this pins the properties
// that have to survive shapes nobody thought of -- most importantly that the
// crossing count reported is the crossing count of the picture returned, and
// that ordering never makes things worse than the seed it started from.
func TestGeneratedNeighbourhoodsHoldTheInvariants(t *testing.T) {
	r := rand.New(rand.NewSource(20260729))
	for i := range 400 {
		in := generateNeighbourhood(r)
		got, err := Build(in)
		if err != nil {
			t.Fatalf("graph %d: Build: %v", i, err)
		}
		if got.Crossings.After > got.Crossings.Before {
			t.Fatalf("graph %d: ordering made it worse, %d -> %d",
				i, got.Crossings.Before, got.Crossings.After)
		}
		if n := recount(t, got); n != got.Crossings.After {
			t.Fatalf("graph %d: layout has %d crossings but reports %d -- the number "+
				"describes an ordering that was not drawn", i, n, got.Crossings.After)
		}
		checkInvariants(t, got, in)
		if again, err := Build(in); err != nil || !reflect.DeepEqual(got, again) {
			t.Fatalf("graph %d: not reproducible (err=%v)", i, err)
		}
	}
}

// generateNeighbourhood builds a layered graph shaped like a real one: a
// physical band with cables, a sometimes-empty virtual band hanging off it,
// services on top, and some edges that skip the middle band entirely.
func generateNeighbourhood(r *rand.Rand) Input {
	in := Input{}
	var phys, virt, svc []string
	add := func(prefix string, n int, layer Layer, into *[]string) {
		for i := range n {
			id := prefix + strconv.Itoa(i)
			in.Nodes = append(in.Nodes, node(id, prefix, layer))
			*into = append(*into, id)
		}
	}
	add("p", 1+r.Intn(9), LayerPhysical, &phys)
	add("v", r.Intn(7), LayerVirtual, &virt)
	add("s", r.Intn(9), LayerService, &svc)

	link := func(a, b string, l Layer) {
		if a != b {
			in.Edges = append(in.Edges, edge(a, b, l))
		}
	}
	pick := func(s []string) string { return s[r.Intn(len(s))] }

	for range len(phys) + r.Intn(len(phys)+1) {
		link(pick(phys), pick(phys), LayerPhysical)
	}
	for _, v := range virt {
		link(v, pick(phys), LayerVirtual)
		if len(virt) > 1 && r.Intn(3) == 0 {
			link(v, pick(virt), LayerVirtual)
		}
	}
	for _, s := range svc {
		if len(virt) > 0 && r.Intn(4) > 0 {
			link(s, pick(virt), LayerService)
		} else {
			link(s, pick(phys), LayerService) // skips the virtual band
		}
		if len(svc) > 1 && r.Intn(3) == 0 {
			link(s, pick(svc), LayerService)
		}
	}
	in.Subject = pick(phys)
	return in
}

// TestTheSubjectIsCentredInItsBand pins the anchor rule. The asset somebody
// asked about is where the eye should land, so it takes the middle slot of its
// band whatever the barycentre would rather do with it.
func TestTheSubjectIsCentredInItsBand(t *testing.T) {
	cases := []struct {
		name    string
		count   int
		subject string
		want    int
	}{
		{name: "odd band, subject declared last", count: 5, subject: "p-5", want: 2},
		{name: "even band, subject declared first", count: 4, subject: "p-1", want: 2},
		{name: "band of one", count: 1, subject: "p-1", want: 0},
		{name: "band of two", count: 2, subject: "p-2", want: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := Input{Subject: tc.subject}
			for i := 1; i <= tc.count; i++ {
				in.Nodes = append(in.Nodes, node("p-"+strconv.Itoa(i), "server", LayerPhysical))
			}
			// One edge, so the sweep has an opinion to override.
			if tc.count > 1 {
				in.Edges = append(in.Edges, edge("p-1", "p-"+strconv.Itoa(tc.count), LayerPhysical))
			}

			got, err := Build(in)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			sub, ok := got.Node(tc.subject)
			if !ok {
				t.Fatalf("subject %q missing", tc.subject)
			}
			if sub.Index != tc.want {
				t.Errorf("subject sits in slot %d of %d, want %d", sub.Index, tc.count, tc.want)
			}
		})
	}
}

// TestAnEmptyLayerCollapsesRatherThanLeavingAGap compares the same
// neighbourhood with and without a middle layer. The two-band picture must be
// exactly one band and one gutter shorter -- not the same height with a hole
// where the bridges would have been.
func TestAnEmptyLayerCollapsesRatherThanLeavingAGap(t *testing.T) {
	withMiddle := Input{
		Subject: "hv-01",
		Nodes: []Node{
			node("orders-api", "service", LayerService),
			node("br0", "bridge", LayerVirtual),
			node("hv-01", "hypervisor", LayerPhysical),
		},
		Edges: []Edge{
			edge("orders-api", "br0", LayerService),
			edge("br0", "hv-01", LayerVirtual),
		},
	}
	without := Input{
		Subject: "hv-01",
		Nodes: []Node{
			node("orders-api", "service", LayerService),
			node("hv-01", "hypervisor", LayerPhysical),
		},
		Edges: []Edge{edge("orders-api", "hv-01", LayerService)},
	}

	three, err := Build(withMiddle)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	two, err := Build(without)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if len(three.Bands) != 3 || len(two.Bands) != 2 {
		t.Fatalf("band counts = %d and %d, want 3 and 2", len(three.Bands), len(two.Bands))
	}
	for _, b := range two.Bands {
		if b.Layer == LayerVirtual {
			t.Error("a virtual band was drawn for a neighbourhood with no virtual nodes")
		}
	}
	if want := three.Height - (NodeHeight + BandGutter); math.Abs(two.Height-want) > eps {
		t.Errorf("collapsed height = %v, want %v: the empty layer left a gap behind",
			two.Height, want)
	}
}

// TestBoxWidthComesFromTheLabel covers the sizing rule and the truncation that
// goes with it. The renderer draws Short and puts Label in a <title>, so the
// full value has to survive whatever the box could not fit.
func TestBoxWidthComesFromTheLabel(t *testing.T) {
	long := strings.Repeat("x", MaxLabelRunes+18)
	in := Input{
		Subject: "a",
		Nodes: []Node{
			{ID: "a", Label: "a", Kind: "vm", Layer: LayerPhysical, Status: "ok"},
			{ID: "b", Label: "sw-core-1", Kind: "switch", Layer: LayerPhysical, Status: "ok"},
			{ID: "c", Label: long, Kind: "hypervisor", Layer: LayerPhysical, Status: "ok"},
			{ID: "d", Label: "hv", Kind: "hypervisor", Layer: LayerPhysical, Status: "ok"},
		},
	}

	got, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	byID := map[string]Node{}
	for _, n := range got.Bands[0].Nodes {
		byID[n.ID] = n
	}

	if byID["a"].W != MinNodeWidth {
		t.Errorf("a one-character label got %v, want the %v floor", byID["a"].W, MinNodeWidth)
	}
	if byID["b"].W <= byID["a"].W {
		t.Errorf("a nine-character label (%v) is no wider than a one-character one (%v)",
			byID["b"].W, byID["a"].W)
	}
	if byID["c"].Label != long {
		t.Error("the full label did not survive truncation; the <title> would lie")
	}
	if r := []rune(byID["c"].Short); len(r) != MaxLabelRunes || r[len(r)-1] != '…' {
		t.Errorf("Short = %q, want %d runes ending in an ellipsis", byID["c"].Short, MaxLabelRunes)
	}
	// "hv" is two characters; "hypervisor" is ten. Sizing on the label alone
	// would let the caption hang out of its own box.
	if want := boxWidth("hypervisor", ""); byID["d"].W < TextWidth("hypervisor", CaptionFontSize) {
		t.Errorf("box for a short label with a long kind is %v, narrower than the caption (%v, cf %v)",
			byID["d"].W, TextWidth("hypervisor", CaptionFontSize), want)
	}

	// Uniform spacing over non-uniform boxes: the gap is constant and the
	// widths are not.
	nodes := got.Bands[0].Nodes
	for i := 1; i < len(nodes); i++ {
		if gap := nodes[i].X - nodes[i-1].Right(); math.Abs(gap-NodeGap) > eps {
			t.Errorf("gap before %q is %v, want %v", nodes[i].ID, gap, NodeGap)
		}
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{in: "hv-01", max: 22, want: "hv-01"},
		{in: "", max: 22, want: ""},
		{in: "abcdef", max: 4, want: "abc…"},
		{in: "abcdef", max: 1, want: "…"},
		{in: "abcdef", max: 0, want: ""},
		{in: "abcdef", max: -1, want: ""},
		{in: "aöücdef", max: 4, want: "aöü…"},
		{in: "aöü", max: 3, want: "aöü"},
	}
	for _, tc := range cases {
		if got := truncate(tc.in, tc.max); got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
	}
}

// TestParallelEdgesAreCounted covers the redundancy trap: two cables between
// the same pair of chassis drawn on top of each other say "one wire", and
// redundancy is exactly the fact an operator came to the diagram for. The
// layout cannot draw them apart, but it can tell the renderer there are two.
func TestParallelEdgesAreCounted(t *testing.T) {
	in := Input{
		Subject: "sw-core-1",
		Nodes: []Node{
			node("sw-core-1", "switch", LayerPhysical),
			node("sw-core-2", "switch", LayerPhysical),
			node("hv-01", "hypervisor", LayerPhysical),
		},
		Edges: []Edge{
			edge("sw-core-1", "sw-core-2", LayerPhysical),
			edge("sw-core-2", "sw-core-1", LayerPhysical), // same pair, other way round
			edge("sw-core-1", "hv-01", LayerPhysical),
		},
	}
	got, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got.Edges[0].ParallelCount != 2 || got.Edges[1].ParallelCount != 2 {
		t.Errorf("peer-link ParallelCounts = %d and %d, want 2 and 2",
			got.Edges[0].ParallelCount, got.Edges[1].ParallelCount)
	}
	if got.Edges[0].Parallel == got.Edges[1].Parallel {
		t.Errorf("both peer links got Parallel=%d; they would draw on top of each other",
			got.Edges[0].Parallel)
	}
	if got.Edges[2].ParallelCount != 1 || got.Edges[2].Parallel != 0 {
		t.Errorf("the single cable got Parallel=%d of %d, want 0 of 1",
			got.Edges[2].Parallel, got.Edges[2].ParallelCount)
	}
}

// TestNestedArcsGetDifferentDepths keeps a span drawn inside another span
// visible as its own line rather than as a thicker version of the one around
// it, and keeps every arc inside the lane reserved for it.
func TestNestedArcsGetDifferentDepths(t *testing.T) {
	in := Input{
		Nodes: []Node{
			node("p-1", "server", LayerPhysical),
			node("p-2", "server", LayerPhysical),
			node("p-3", "server", LayerPhysical),
			node("p-4", "server", LayerPhysical),
		},
		Edges: []Edge{
			edge("p-2", "p-3", LayerPhysical), // innermost
			edge("p-1", "p-4", LayerPhysical), // contains it
		},
	}
	got, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	inner, outer := got.Edges[0], got.Edges[1]
	if !(outer.Depth > inner.Depth) {
		t.Errorf("outer arc dips %v and inner %v; the containing span must go deeper",
			outer.Depth, inner.Depth)
	}
	if inner.Depth <= 0 {
		t.Errorf("inner arc has depth %v; it would be drawn flat along the row", inner.Depth)
	}
	band := got.Bands[0]
	if outer.Depth+ArcPad > band.ArcLane+eps {
		t.Errorf("deepest arc %v does not fit its lane of %v", outer.Depth, band.ArcLane)
	}
	// The rail runs at exactly the assigned depth: the flat profile is what
	// makes "deeper" mean below at EVERY shared x, which is the property the
	// depth assignment exists to buy.
	if want := band.Y + NodeHeight + outer.Depth; math.Abs(outer.Waypoints[0].Y-want) > eps {
		t.Errorf("outer rail at y=%v, want %v", outer.Waypoints[0].Y, want)
	}
}

// TestCrossBandEdgesAnchorOnTheFacingEdges checks the one thing a renderer
// cannot recover for itself: X1,Y1 always belongs to From, whichever end
// happens to be the upper one.
func TestCrossBandEdgesAnchorOnTheFacingEdges(t *testing.T) {
	in := Input{
		Nodes: []Node{
			node("svc", "service", LayerService),
			node("hv", "hypervisor", LayerPhysical),
		},
		Edges: []Edge{
			edge("svc", "hv", LayerService), // declared downward
			edge("hv", "svc", LayerService), // declared upward
		},
	}
	got, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	top, bottom := got.Bands[0].Nodes[0], got.Bands[1].Nodes[0]

	down := got.Edges[0]
	if math.Abs(down.Y1-top.Bottom()) > eps || math.Abs(down.Y2-bottom.Y) > eps {
		t.Errorf("downward edge runs y=%v..%v, want %v..%v",
			down.Y1, down.Y2, top.Bottom(), bottom.Y)
	}
	up := got.Edges[1]
	if math.Abs(up.Y1-bottom.Y) > eps || math.Abs(up.Y2-top.Bottom()) > eps {
		t.Errorf("upward edge runs y=%v..%v, want %v..%v",
			up.Y1, up.Y2, bottom.Y, top.Bottom())
	}
	if down.FromLayer != LayerService || down.ToLayer != LayerPhysical {
		t.Errorf("edge layers = %s -> %s, want service -> physical", down.FromLayer, down.ToLayer)
	}
	// Two bands, adjacent once the empty virtual band collapses: nothing in
	// between, so nothing to route around.
	if len(down.Waypoints) != 0 {
		t.Errorf("an edge between adjacent bands was routed through %d waypoints; "+
			"the gutter between them is empty and a straight line is the honest picture",
			len(down.Waypoints))
	}
}

// TestAnEdgeSpanningTwoBandsIsRoutedRoundTheMiddleOne pins the shape that was
// broken: a service placed directly on bare metal.
//
// The estate has plenty of these -- a backup agent or a node_exporter running on
// the hypervisor itself rather than in a VM -- so the edge joins the top band to
// the bottom one and the middle band is full of bridges it has nothing to do
// with. It was drawn as the straight line between its anchors, through them.
//
// checkClearsEveryBox already asserts the outcome for every fixture in this file.
// This test exists because reverting the fix failed only
// TestGeneratedNeighbourhoodsHoldTheInvariants -- no hand-written fixture had
// this shape, so the whole guarantee rested on a randomised generator continuing
// to produce it. It also asserts the mechanism and not just the outcome: a line
// that misses the boxes by luck and one that goes through a channel look the same
// to a box test, and only one of them stays true when the layout changes.
func TestAnEdgeSpanningTwoBandsIsRoutedRoundTheMiddleOne(t *testing.T) {
	in := Input{
		Nodes: []Node{
			node("backup-agent", "instance", LayerService),
			node("br0", "bridge", LayerVirtual),
			node("br1", "bridge", LayerVirtual),
			node("br2", "bridge", LayerVirtual),
			node("hv-01", "hypervisor", LayerPhysical),
		},
		// No edge touches the middle band at all: it is in the way and nothing
		// more, which is exactly the case a layered layout has to handle.
		Edges:   []Edge{edge("backup-agent", "hv-01", LayerService)},
		Subject: "hv-01",
	}
	got, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	checkInvariants(t, got, in)

	if len(got.Bands) != 3 {
		t.Fatalf("got %d bands, want all three -- the middle one has to be present "+
			"for this test to be testing anything", len(got.Bands))
	}
	middle := got.Bands[1]
	e := got.Edges[0]

	if len(e.Waypoints) != 2 {
		t.Fatalf("edge spanning two bands has %d waypoints, want 2 (in and out of one "+
			"channel); with none it is a straight line through %d boxes",
			len(e.Waypoints), len(middle.Nodes))
	}
	in1, out := e.Waypoints[0], e.Waypoints[1]

	if math.Abs(in1.X-out.X) > eps {
		t.Errorf("the two waypoints are at x=%v and x=%v; a channel is vertical, so "+
			"crossing the row at a slant can still clip a box", in1.X, out.X)
	}
	if in1.Y > middle.Y-eps || out.Y < middle.Bottom()+eps {
		t.Errorf("waypoints at y=%v and y=%v do not straddle the row at y=%v..%v, so "+
			"the corner turns inside it", in1.Y, out.Y, middle.Y, middle.Bottom())
	}
	if !inAChannel(middle, in1.X) {
		t.Errorf("the route crosses the middle band at x=%v, which is not in a gap "+
			"between its boxes: %s", in1.X, describeBand(middle))
	}
	// Fanning a parallel edge must not push the crossing out of that gap.
	for _, off := range []float64{-ChannelSpread, ChannelSpread} {
		if !inAChannel(middle, in1.X+off) {
			t.Errorf("x=%v is in a channel but x=%v (fanned by ChannelSpread=%v) is not, "+
				"so a renderer obeying that budget still draws through a box",
				in1.X, in1.X+off, ChannelSpread)
		}
	}
}

// inAChannel reports whether a vertical line at x crosses band b's row without
// touching a box: either between two of them, or clear of both ends.
func inAChannel(b Band, x float64) bool {
	for _, n := range b.Nodes {
		if x > n.X-eps && x < n.Right()+eps {
			return false
		}
	}
	return true
}

func describeBand(b Band) string {
	var sb strings.Builder
	for i, n := range b.Nodes {
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "%s spans %v..%v", n.ID, n.X, n.Right())
	}
	return sb.String()
}

// TestBuildRefusesInputItCannotHonour. Every case here is a caller bug, and
// each one is refused rather than drawn around: an edge to a node that is not
// in the set is a query whose frontier did not close, and quietly dropping the
// line would produce a picture that is wrong in exactly the way nobody checks.
func TestBuildRefusesInputItCannotHonour(t *testing.T) {
	ok := node("a", "server", LayerPhysical)

	cases := []struct {
		name string
		in   Input
	}{
		{name: "a node with no id", in: Input{Nodes: []Node{{Label: "x", Layer: LayerPhysical}}}},
		{name: "a node with no label", in: Input{Nodes: []Node{{ID: "a", Layer: LayerPhysical}}}},
		{
			name: "a node with no layer",
			in:   Input{Nodes: []Node{{ID: "a", Label: "a"}}},
		},
		{
			name: "a node in a fourth layer",
			in:   Input{Nodes: []Node{{ID: "a", Label: "a", Layer: Layer(4)}}},
		},
		{
			name: "two nodes with the same id",
			in:   Input{Nodes: []Node{ok, node("a", "vm", LayerService)}},
		},
		{
			name: "an edge to a node that is not here",
			in:   Input{Nodes: []Node{ok}, Edges: []Edge{edge("a", "ghost", LayerPhysical)}},
		},
		{
			name: "an edge from a node that is not here",
			in:   Input{Nodes: []Node{ok}, Edges: []Edge{edge("ghost", "a", LayerPhysical)}},
		},
		{
			name: "an edge from a node to itself",
			in:   Input{Nodes: []Node{ok}, Edges: []Edge{edge("a", "a", LayerPhysical)}},
		},
		{
			name: "an edge with no layer",
			in:   Input{Nodes: []Node{ok, node("b", "vm", LayerService)}, Edges: []Edge{{From: "a", To: "b"}}},
		},
		{
			name: "a subject that is not in the node set",
			in:   Input{Nodes: []Node{ok}, Subject: "somewhere-else"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Build(tc.in)
			if err == nil {
				t.Fatalf("Build succeeded and returned %+v", got)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("error %v does not wrap ErrInvalid", err)
			}
			if got != nil {
				t.Error("Build returned a layout alongside an error")
			}
		})
	}
}

// TestBuildDoesNotMutateItsInput. Build is called from a handler holding
// slices it did not necessarily own, and the geometry fields it fills would
// otherwise leak back into the caller's data.
func TestBuildDoesNotMutateItsInput(t *testing.T) {
	in := denseFixture()
	beforeNodes := slices.Clone(in.Nodes)
	beforeEdges := slices.Clone(in.Edges)

	if _, err := Build(in); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !reflect.DeepEqual(in.Nodes, beforeNodes) {
		t.Error("Build wrote into the caller's node slice")
	}
	if !reflect.DeepEqual(in.Edges, beforeEdges) {
		t.Error("Build wrote into the caller's edge slice")
	}
}

// TestAnEmptyNeighbourhoodIsNotAnError. A store that legitimately found
// nothing gets an empty canvas, not a failure: the handler's job is to say
// "nothing here", and it cannot do that if the layout refuses to exist.
func TestAnEmptyNeighbourhoodIsNotAnError(t *testing.T) {
	got, err := Build(Input{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(got.Bands) != 0 || len(got.Edges) != 0 {
		t.Errorf("got %d bands and %d edges, want none", len(got.Bands), len(got.Edges))
	}
	if got.Width != 2*MarginX || got.Height != 2*MarginY {
		t.Errorf("canvas = %v x %v, want the bare margins", got.Width, got.Height)
	}
	if _, ok := got.Node("anything"); ok {
		t.Error("an empty layout resolved a node id")
	}
}

func TestLayerNaming(t *testing.T) {
	cases := []struct {
		layer Layer
		want  string
		valid bool
	}{
		{layer: LayerPhysical, want: "physical", valid: true},
		{layer: LayerVirtual, want: "virtual", valid: true},
		{layer: LayerService, want: "service", valid: true},
		{layer: Layer(0), want: "layer0"},
		{layer: Layer(9), want: "layer9"},
	}
	for _, tc := range cases {
		if got := tc.layer.String(); got != tc.want {
			t.Errorf("Layer(%d).String() = %q, want %q", int(tc.layer), got, tc.want)
		}
		if got := tc.layer.Valid(); got != tc.valid {
			t.Errorf("Layer(%d).Valid() = %v, want %v", int(tc.layer), got, tc.valid)
		}
	}
	if got := LayersTopDown(); !reflect.DeepEqual(got, []Layer{LayerService, LayerVirtual, LayerPhysical}) {
		t.Errorf("LayersTopDown() = %v, want service, virtual, physical", got)
	}
}
