// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package diagram

// The drawn-crossing count: how many pairs of lines actually cross in the
// picture as placed, as opposed to how many the ordering model predicts.
//
// The two numbers have different jobs and must not be conflated. The slot
// model in order.go is the OBJECTIVE the barycentre sweep optimises: it has to
// be cheap (it runs thousands of times per layout) and it has to be a function
// of the ordering alone, because it is evaluated before any coordinate exists.
// This file is the MEASUREMENT: it runs once, after placement, on the same
// polylines the renderer will draw, and it is the number a claim about
// legibility has to cite. When the two disagree, the drawing is the truth --
// the reader traces lines, not slots.
//
// They are expected to agree almost everywhere, and the residual gaps are
// deliberate, not accidental:
//
//   - A two-band routed edge passes through an intervening band's arc lane at
//     a channel whose x is chosen at placement time. The slot model cannot
//     see it and does not pretend to.
//   - A cross-band edge drifts sideways as it descends, so one leaving a box
//     just outside an arc's span can clip the arc that a vertical drop would
//     have missed. Rare, bounded by the drift, and not worth complicating the
//     objective for.
//   - Renderer liberties -- fanning parallel edges, which this count excludes
//     by excluding pairs that share both endpoints anyway.

// DrawnCrossings counts pairs of edges whose drawn lines cross.
//
// Pairs sharing an endpoint node are not counted, matching the slot model's
// convention: two lines meeting at the box they share is a junction, not a
// crossing. The count is over polylines -- anchors plus waypoints -- which is
// exactly what the renderer draws, so this is a fact about the picture.
func (l *Layout) DrawnCrossings() int {
	polys := make([][]Point, len(l.Edges))
	for i, e := range l.Edges {
		polys[i] = edgePolyline(e)
	}
	total := 0
	for i := range l.Edges {
		for j := i + 1; j < len(l.Edges); j++ {
			if sharesEndpoint(l.Edges[i], l.Edges[j]) {
				continue
			}
			if polylinesCross(polys[i], polys[j]) {
				total++
			}
		}
	}
	return total
}

// edgePolyline is the line an edge is drawn as, vertex by vertex.
func edgePolyline(e Edge) []Point {
	pts := make([]Point, 0, len(e.Waypoints)+2)
	pts = append(pts, Point{X: e.X1, Y: e.Y1})
	pts = append(pts, e.Waypoints...)
	return append(pts, Point{X: e.X2, Y: e.Y2})
}

func sharesEndpoint(a, b Edge) bool {
	return a.From == b.From || a.From == b.To || a.To == b.From || a.To == b.To
}

// polylinesCross reports whether any segment of a crosses any segment of b. A
// pair counts once however many times its segments intersect: the question is
// "do these two lines cross", not "how tangled is the knot".
func polylinesCross(a, b []Point) bool {
	for i := 1; i < len(a); i++ {
		for j := 1; j < len(b); j++ {
			if segmentsCross(a[i-1], a[i], b[j-1], b[j]) {
				return true
			}
		}
	}
	return false
}

// segmentsCross is a proper-crossing test: the open segments share an interior
// point. Touching at an endpoint is not a crossing, which is what makes two
// edges meeting at the same anchor a junction rather than a defect.
func segmentsCross(p1, p2, q1, q2 Point) bool {
	d1 := orient(q1, q2, p1)
	d2 := orient(q1, q2, p2)
	d3 := orient(p1, p2, q1)
	d4 := orient(p1, p2, q2)
	return ((d1 > 0) != (d2 > 0)) && ((d3 > 0) != (d4 > 0))
}

// orient is the signed area of the triangle a,b,c: positive when c is left of
// the directed line a->b.
func orient(a, b, c Point) float64 {
	return (b.X-a.X)*(c.Y-a.Y) - (b.Y-a.Y)*(c.X-a.X)
}
