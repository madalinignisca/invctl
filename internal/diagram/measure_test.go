// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package diagram

import (
	"math"
	"testing"
)

// The drawn-crossing count and the properties that make it match the ordering
// model. The unit cases build Edge values directly, because Build immediately
// runs the optimiser and a test that needs a crossing to SURVIVE ordering
// would be fighting the very code that removes crossings.

func seg(from, to string, x1, y1, x2, y2 float64) Edge {
	return Edge{From: from, To: to, X1: x1, Y1: y1, X2: x2, Y2: y2}
}

func TestDrawnCrossingsCountsAProperCrossing(t *testing.T) {
	l := &Layout{Edges: []Edge{
		seg("a", "d", 0, 0, 100, 100),
		seg("b", "c", 0, 100, 100, 0),
	}}
	if got := l.DrawnCrossings(); got != 1 {
		t.Errorf("an X of two segments counts %d crossings, want 1", got)
	}
}

func TestDrawnCrossingsIgnoresAJunction(t *testing.T) {
	// Two lines meeting at a shared node are a junction: the convention the
	// slot model uses, so the census must use it too or the two numbers
	// would disagree on every star-shaped neighbourhood.
	l := &Layout{Edges: []Edge{
		seg("hub", "a", 50, 0, 0, 100),
		seg("hub", "b", 50, 0, 100, 100),
	}}
	if got := l.DrawnCrossings(); got != 0 {
		t.Errorf("two edges sharing a node count %d crossings, want 0", got)
	}
}

func TestDrawnCrossingsSeesRailGeometry(t *testing.T) {
	rail := func(from, to string, x1, x2, depth float64) Edge {
		e := seg(from, to, x1, 38, x2, 38)
		e.SameBand = true
		e.Depth = depth
		e.Waypoints = []Point{{X: x1, Y: 38 + depth}, {X: x2, Y: 38 + depth}}
		return e
	}

	interleaved := &Layout{Edges: []Edge{
		rail("a", "c", 10, 110, 14),
		rail("b", "d", 60, 160, 24),
	}}
	if got := interleaved.DrawnCrossings(); got != 1 {
		t.Errorf("interleaved rails at distinct depths count %d crossings, want exactly 1 -- "+
			"one vertical through the other's run", got)
	}

	nested := &Layout{Edges: []Edge{
		rail("a", "d", 10, 160, 24),
		rail("b", "c", 60, 110, 14),
	}}
	if got := nested.DrawnCrossings(); got != 0 {
		t.Errorf("a rail nested under a deeper one counts %d crossings, want 0", got)
	}

	pierced := &Layout{Edges: []Edge{
		rail("a", "c", 10, 110, 14),
		seg("b", "x", 60, 38, 60, 200), // leaves a box inside the span, falls through the lane
	}}
	if got := pierced.DrawnCrossings(); got != 1 {
		t.Errorf("a drop through a rail's span counts %d crossings, want 1", got)
	}
}

// TestSharedEndpointNestedRailsDoNotCross pins the defect that forced rails.
//
// Two spans sharing an endpoint, one inside the other, were drawn as
// quadratics and ALWAYS crossed: near the shared anchor the long curve is
// necessarily shallower than the short one -- its control point is far away
// -- so the short arc dipped below it and had to come back up, whatever
// depths were assigned. Measured over 4000 generated neighbourhoods, 36608
// drawn crossings existed for exactly this reason and nothing counted them.
// Rails end the argument by construction: flat profiles at distinct depths,
// anchors fanned so the verticals are distinct too.
func TestSharedEndpointNestedRailsDoNotCross(t *testing.T) {
	in := Input{
		Subject: "b",
		Nodes: []Node{
			node("a", "switch", LayerPhysical),
			node("b", "switch", LayerPhysical),
			node("c", "switch", LayerPhysical),
		},
		// The subject rule pins b to the centre slot, so a-b nests inside a-c
		// sharing the endpoint a, whichever sides a and c land on.
		Edges: []Edge{
			edge("a", "b", LayerPhysical),
			edge("a", "c", LayerPhysical),
		},
	}
	got, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	checkInvariants(t, got, in)

	inner, outer := got.Edges[0], got.Edges[1]
	if !(outer.Depth > inner.Depth) {
		t.Fatalf("the containing span runs at %v and the contained at %v; "+
			"outer must be deeper", outer.Depth, inner.Depth)
	}
	if got.DrawnCrossings() != 0 {
		t.Errorf("nested spans sharing endpoint %q still cross as drawn", "a")
	}

	// The mechanism, not just the outcome: at the shared node the two anchors
	// are distinct, deeper innermost, or the verticals would coincide and the
	// deeper rail's drop would pierce the shallower rail's run.
	a, _ := got.Node("a")
	offInner := math.Abs(inner.X1 - a.CenterX())
	offOuter := math.Abs(outer.X1 - a.CenterX())
	if math.Abs(inner.X1-outer.X1) < eps {
		t.Errorf("both rails anchor at x=%v on %q; their verticals coincide", inner.X1, "a")
	}
	if offOuter >= offInner {
		t.Errorf("the deeper rail anchors %v from centre and the shallower %v; "+
			"deepest belongs innermost so its drop clears the shallower run",
			offOuter, offInner)
	}
}

// TestARailPierceIsCountedAndMinimised covers the ordering model's new term.
//
// A dependency between two services is a rail under the service band; a third
// service's descent leaves a box strictly inside that span and falls straight
// through the rail. The model declared these pairs non-crossing for as long
// as it existed, which let the sweep buy one counted crossing by selling
// several drawn ones -- 8528 of them over 4000 generated neighbourhoods.
//
// The subject rule pins the piercing service to the centre slot, so no
// ordering can move it out of the span: the crossing is unavoidable, the
// model must report exactly 1, and the census must agree with it.
func TestARailPierceIsCountedAndMinimised(t *testing.T) {
	in := Input{
		Subject: "s-mid",
		Nodes: []Node{
			node("s-left", "service", LayerService),
			node("s-mid", "service", LayerService),
			node("s-right", "service", LayerService),
			node("hv", "hypervisor", LayerPhysical),
		},
		Edges: []Edge{
			edge("s-left", "s-right", LayerService),
			edge("s-left", "hv", LayerService),
			edge("s-mid", "hv", LayerService),
			edge("s-right", "hv", LayerService),
		},
	}
	got, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	checkInvariants(t, got, in)

	if got.Crossings.After != 1 {
		t.Errorf("the model reports %d crossings, want exactly 1: the centre descent "+
			"cannot leave the span, and nothing else crosses", got.Crossings.After)
	}
	if drawn := got.DrawnCrossings(); drawn != 1 {
		t.Errorf("the census reports %d crossings where the model reports %d; "+
			"for rails and adjacent-band lines the two must agree", drawn, got.Crossings.After)
	}
}
