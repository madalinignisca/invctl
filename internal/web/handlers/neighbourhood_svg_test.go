// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/diagram"
)

// The renderer's half of the no-line-crosses-a-box guarantee.
//
// internal/diagram proves the ROUTE misses every box. That is worth nothing on
// its own, because the route is a suggestion until something draws it: the
// package that computes the geometry and the package that emits the `d`
// attribute are not the same package, and the defect this replaces was precisely
// a renderer drawing a straight line between two anchors while the layout
// believed something else.
//
// So the contract is asserted from both ends. diagram's checkClearsEveryBox owns
// "the route is clean"; this file owns "the drawn path is the route", and the one
// liberty the renderer takes -- fanning parallel edges sideways so two of them do
// not read as one line -- is asserted to stay inside diagram.ChannelSpread, which
// is the budget that keeps a fanned line in the same gap between boxes.

// pathPoints pulls the coordinate pairs out of an SVG path built from M, L and Q
// commands. Control points are included, in order, which is what a caller
// checking "does it go where it was told" wants.
func pathPoints(t *testing.T, d string) []diagram.Point {
	t.Helper()
	var out []diagram.Point
	fields := strings.Fields(d)
	for i := 0; i < len(fields); i++ {
		switch fields[i] {
		case "M", "L", "Q":
			continue
		}
		x, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			t.Fatalf("path %q: field %q is not a number: %v", d, fields[i], err)
		}
		if i+1 >= len(fields) {
			t.Fatalf("path %q ends on a lone x coordinate", d)
		}
		y, err := strconv.ParseFloat(fields[i+1], 64)
		if err != nil {
			t.Fatalf("path %q: field %q is not a number: %v", d, fields[i+1], err)
		}
		out = append(out, diagram.Point{X: x, Y: y})
		i++
	}
	return out
}

func TestEdgePathFollowsItsWaypoints(t *testing.T) {
	// One channel through a middle band whose row runs y=130..168, entered and
	// left 9px clear of it. The x values are a gap between two boxes.
	base := diagram.Edge{
		From: "backup-agent", To: "hv-01",
		X1: 204, Y1: 58, X2: 242, Y2: 262,
		Waypoints: []diagram.Point{{X: 166, Y: 121}, {X: 166, Y: 177}},
	}

	cases := []struct {
		name              string
		parallel, ofCount int
	}{
		{name: "a lone edge follows the route exactly", parallel: 0, ofCount: 1},
		{name: "the first of two is fanned one way", parallel: 0, ofCount: 2},
		{name: "the second of two is fanned the other", parallel: 1, ofCount: 2},
		{name: "the outermost of five is fanned hardest", parallel: 0, ofCount: 5},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := base
			e.Parallel, e.ParallelCount = tc.parallel, tc.ofCount
			got := pathPoints(t, edgePath(e))

			if len(got) != 2+len(base.Waypoints) {
				t.Fatalf("path visits %d points, want the two anchors and %d waypoints: %q",
					len(got), len(base.Waypoints), edgePath(e))
			}
			if got[0] != (diagram.Point{X: e.X1, Y: e.Y1}) {
				t.Errorf("path starts at %v, want the From anchor %v", got[0], diagram.Point{X: e.X1, Y: e.Y1})
			}
			if last := got[len(got)-1]; last != (diagram.Point{X: e.X2, Y: e.Y2}) {
				t.Errorf("path ends at %v, want the To anchor %v", last, diagram.Point{X: e.X2, Y: e.Y2})
			}

			var drawn []diagram.Point
			for i, want := range base.Waypoints {
				p := got[i+1]
				drawn = append(drawn, p)
				if p.Y != want.Y {
					t.Errorf("waypoint %d is drawn at y=%v, want %v; the layout chose that "+
						"height to clear the row and the renderer may not move it", i, p.Y, want.Y)
				}
				if off := math.Abs(p.X - want.X); off > diagram.ChannelSpread+1e-9 {
					t.Errorf("waypoint %d is drawn %v from its channel at x=%v, over the "+
						"%v budget -- far enough to leave the gap and cross a box",
						i, off, want.X, diagram.ChannelSpread)
				}
			}
			// Both ends of a channel must move together, or the line crosses the
			// row at a slant and the clearance argument no longer holds.
			if drawn[0].X != drawn[1].X {
				t.Errorf("the channel is drawn from x=%v to x=%v: not vertical",
					drawn[0].X, drawn[1].X)
			}
			if tc.ofCount > 1 && drawn[0].X == base.Waypoints[0].X {
				t.Errorf("edge %d of %d was not fanned at all, so it is drawn underneath "+
					"a sibling and the pair reads as one line", tc.parallel, tc.ofCount)
			}
		})
	}
}

// TestEdgePathDrawsTheLayoutsPolyline guards the two shapes against each
// other: an adjacent-band line stays the straight line the layout placed, and
// a rail is drawn exactly along its waypoints -- with no parallel fan, because
// parallel rails were already pulled apart by depth and anchor, and fanning
// them sideways would slant the verticals out of their boxes.
func TestEdgePathDrawsTheLayoutsPolyline(t *testing.T) {
	cross := diagram.Edge{X1: 100, Y1: 58, X2: 200, Y2: 130}
	if got, want := edgePath(cross), "M 100.0 58.0 L 200.0 130.0"; got != want {
		t.Errorf("adjacent-band edge = %q, want %q", got, want)
	}

	rail := diagram.Edge{
		SameBand: true, X1: 100, Y1: 58, X2: 300, Y2: 58, Depth: 14,
		Waypoints: []diagram.Point{{X: 100, Y: 72}, {X: 300, Y: 72}},
	}
	want := "M 100.0 58.0 L 100.0 72.0 L 300.0 72.0 L 300.0 58.0"
	if got := edgePath(rail); got != want {
		t.Errorf("rail = %q, want %q", got, want)
	}

	parallelRail := rail
	parallelRail.Parallel, parallelRail.ParallelCount = 1, 2
	if got := edgePath(parallelRail); got != want {
		t.Errorf("a parallel rail = %q, want it drawn unfanned at %q -- the layout "+
			"already separated parallels by depth", got, want)
	}

	fanned := cross
	fanned.Parallel, fanned.ParallelCount = 1, 2
	if got := edgePath(fanned); !strings.Contains(got, "Q") {
		t.Errorf("a fanned adjacent-band edge = %q, want a curve so the pair does not "+
			"read as one line", got)
	}
}

// TestDependencyMarker: the arrow matches the stroke it sits on. An optional
// dependency is drawn in the faint line colour; giving it the standard arrow
// would promote the least consequential edge in the picture.
func TestDependencyMarker(t *testing.T) {
	for nature, want := range map[string]string{
		"hard": "arrow-dep", "soft": "arrow-dep", "async": "arrow-dep",
		"startup": "arrow-dep", "": "arrow-dep",
		"optional": "arrow-dep-faint",
	} {
		if got := dependencyMarker(nature); got != want {
			t.Errorf("dependencyMarker(%q) = %q, want %q", nature, got, want)
		}
	}
}

// TestChannelSpreadFitsAChannel is arithmetic, not behaviour, and that is the
// point: ChannelSpread is a budget the renderer spends and NodeGap is the space
// it has to fit in, they live in different files, and nothing else would notice
// if one moved.
func TestChannelSpreadFitsAChannel(t *testing.T) {
	if diagram.ChannelSpread <= 0 {
		t.Fatalf("ChannelSpread = %v, so parallel routed edges cannot be told apart",
			diagram.ChannelSpread)
	}
	if diagram.ChannelSpread >= diagram.NodeGap/2 {
		t.Errorf("ChannelSpread = %v is at least half of NodeGap = %v, so a fanned edge "+
			"leaves its channel and crosses the box beside it",
			diagram.ChannelSpread, diagram.NodeGap/2)
	}
}
