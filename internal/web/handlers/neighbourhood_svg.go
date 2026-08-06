// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"fmt"
	"strings"

	"github.com/gabriel/invctl/internal/diagram"
	"github.com/gabriel/invctl/internal/store"
)

// The scene: a positioned layout turned into rectangles, paths and strings.
//
// Everything here is data. The SVG itself is written in a template like every
// other view in this tool, and nothing in this file emits markup -- a `d`
// attribute built out of float64s is a value, not a document, and it goes
// through html/template's attribute escaping exactly like a name does. That
// distinction is what keeps template.HTML out of the codebase: the day a label
// is pasted into a string of angle brackets is the day an asset called
// `<script>` stops being an asset name.
//
// Nothing here sizes text. Every metric is a diagram package constant, because
// the box widths were computed from those constants in Go and a stylesheet that
// disagreed would make every box the wrong size. The stylesheet's job is colour
// and stroke.

const (
	// railWidth is the coloured left edge on a node box.
	//
	// The signature device of this design is a 3px coloured EDGE rather than a
	// coloured fill: an affected row carries a bar, so a long list stays
	// readable while the shape of the outage is visible from across the room.
	// A diagram that lit every healthy node up would invert the whole palette,
	// where "ok" is deliberately quiet and the eye is spent on what is wrong.
	railWidth = 3.0

	// The two baselines inside a box, measured from its top edge.
	labelBaseline   = 16.0
	captionBaseline = 29.0

	// bandLabelOffset lifts a band's caption into the space above its row.
	bandLabelOffset = 8.0

	// parallelSpread fans edges that join the same pair of nodes. Two cables
	// between the same two chassis drawn on top of each other say "one wire"
	// when the truth is "two", and redundancy is exactly the fact somebody came
	// to this page for.
	parallelSpread = 16.0
)

// svgScene is everything the template draws.
type svgScene struct {
	Width, Height float64
	// Title and Desc are the accessible name and description of the picture.
	// This would otherwise be the one thing on the site a screen reader gets
	// nothing at all from -- every other page is a table.
	Title string
	Desc  string
	Bands []svgBand
	Edges []svgEdge
	Nodes []svgNode
}

// svgBand is one layer's caption. The band's extent is implied by the nodes in
// it; drawing a box around it would add a rectangle that means nothing.
type svgBand struct {
	Layer  string
	Label  string
	LabelY float64
	Count  int
}

// svgNode is one box.
type svgNode struct {
	X, Y, W, H float64
	RailW      float64
	CenterX    float64
	LabelY     float64
	CaptionY   float64
	Label      string
	Caption    string
	// Title is the untruncated name and everything that did not fit, for the
	// <title> a browser shows on hover and a screen reader reads out.
	Title string
	Href  string
	Class string
}

// svgEdge is one line.
type svgEdge struct {
	D     string
	Class string
	Title string
	// Marker names the arrowhead def this line ends in, empty for none. Only a
	// dependency gets one: it is the one edge kind with a direction that the
	// geometry does not already show. A descent reads top-down because the
	// bands do; a cable is undirected and an arrow on it would assert a flow
	// direction that is not in the database.
	Marker string
}

// dependencyMarker picks the arrowhead for a dependency's failure semantics.
// Two defs rather than CSS on one, because an optional dependency's stroke is
// drawn in the faint line colour and its arrow must match -- an arrow darker
// than its own line would promote the edge it sits on.
func dependencyMarker(nature string) string {
	if nature == "optional" {
		return "arrow-dep-faint"
	}
	return "arrow-dep"
}

// buildScene positions everything the renderer needs.
func buildScene(layout *diagram.Layout, nodes []viewNode, edges []viewEdge,
	subject *store.AssetRow) *svgScene {

	decor := make(map[string]viewNode, len(nodes))
	for _, n := range nodes {
		decor[n.node.ID] = n
	}

	scene := &svgScene{Width: layout.Width, Height: layout.Height}

	for _, band := range layout.Bands {
		label := band.Layer.String()
		for _, l := range diagramLayers {
			if l.Layer == band.Layer {
				label = l.Label
			}
		}
		scene.Bands = append(scene.Bands, svgBand{
			Layer:  band.Layer.String(),
			Label:  label,
			LabelY: band.Y - bandLabelOffset,
			Count:  len(band.Nodes),
		})
		for _, n := range band.Nodes {
			d := decor[n.ID]
			class := "node state-" + stateClass(d.state)
			if n.Subject {
				class += " is-subject"
			}
			scene.Nodes = append(scene.Nodes, svgNode{
				X: n.X, Y: n.Y, W: n.W, H: n.H,
				RailW:    railWidth,
				CenterX:  n.CenterX(),
				LabelY:   n.Y + labelBaseline,
				CaptionY: n.Y + captionBaseline,
				Label:    n.Short,
				Caption:  n.KindShort,
				Title:    d.title,
				Href:     d.href,
				Class:    class,
			})
		}
	}

	// Joined BY INDEX, not by a key built from the endpoints.
	//
	// diagram.Layout documents that "Edges are in the caller's order", so
	// layout.Edges[i] is the laid-out form of the edge that went in at i. A map
	// keyed on From+To+Kind loses every parallel edge: two cables between the
	// same pair of assets differ only in their port names, and two dependencies
	// between the same services differ only in nature, so the map kept one and
	// gave its title and its failure semantic to every drawn copy. The picture
	// then fanned out three lines for an MC-LAG peer bond and told the operator
	// all three were the same port -- asserting a port pair and a failure
	// semantic that are not in the database.
	//
	// The lengths must match or the join is meaningless, so it is checked rather
	// than assumed: a silent mismatch would mislabel every edge after the first
	// divergence, which is worse than an obviously broken picture.
	if len(layout.Edges) != len(edges) {
		// Draw the geometry without descriptions rather than the wrong ones.
		for _, e := range layout.Edges {
			scene.Edges = append(scene.Edges, svgEdge{D: edgePath(e), Class: edgeClass(e, "")})
		}
	} else {
		for i, e := range layout.Edges {
			marker := ""
			if edges[i].edge.Kind == edgeDependency {
				marker = dependencyMarker(edges[i].nature)
			}
			scene.Edges = append(scene.Edges, svgEdge{
				D:      edgePath(e),
				Class:  edgeClass(e, edges[i].nature),
				Title:  edges[i].title,
				Marker: marker,
			})
		}
	}

	// A nil subject is a picture with no anchor -- the path page, whose
	// question is a pair rather than a centre. It names the scene itself.
	if subject != nil {
		scene.Title = "Neighbourhood of " + subject.Name
		scene.Desc = sceneDescription(layout, "centred on "+subject.Name)
	} else {
		scene.Desc = sceneDescription(layout, "of the chain between the two ends")
	}
	return scene
}

// stateClass maps an observed state onto the modifier the rest of the UI uses
// for the same fact, so a box that is down is the same colour here as it is in
// a table. `unknown` is muted rather than alarming: "nobody is watching this"
// calls for looking at the collector, not at the box.
func stateClass(state string) string {
	switch state {
	case "down":
		return "down"
	case "degraded":
		return "degraded"
	case "up":
		return "ok"
	default:
		return "unknown"
	}
}

func edgeClass(e diagram.Edge, nature string) string {
	class := "edge edge-" + e.Kind + " layer-" + e.Layer.String()
	if !e.SameBand {
		// A descent and an adjacency must not share a stroke. One says "this
		// socket reaches the wire through here"; the other says "these two
		// ports are joined". Drawing them alike is how a layered diagram
		// becomes three unrelated pictures with lines on top.
		class += " edge-cross"
	}
	if nature != "" {
		class += " nature-" + nature
	}
	return class
}

// edgePath is the line itself: the layout's polyline, drawn verbatim.
//
// A same-band edge runs as a rail through its band's arc lane; an edge
// spanning more than one band threads a channel through each row in its way.
// Both arrive here as waypoints the layout chose to miss every box, so the
// renderer follows them exactly -- Layout.DrawnCrossings measures the same
// polyline, and the freedom to differ from it is the freedom to make that
// measurement a lie.
//
// The one liberty left is fanning cross-band parallels sideways, so two
// descents between the same pair of boxes do not read as one line. A routed
// edge is fanned only within its channel -- ChannelSpread is how much room
// that is -- and a rail is not fanned at all: parallel rails were already
// pulled apart by depth and anchor, which is what frees the lane from
// sideways clutter.
func edgePath(e diagram.Edge) string {
	offset := 0.0
	if e.ParallelCount > 1 && !e.SameBand {
		offset = (float64(e.Parallel) - float64(e.ParallelCount-1)/2) * parallelSpread
	}
	if len(e.Waypoints) > 0 {
		if e.SameBand {
			offset = 0
		}
		offset = min(max(offset, -diagram.ChannelSpread), diagram.ChannelSpread)
		var b strings.Builder
		fmt.Fprintf(&b, "M %.1f %.1f", e.X1, e.Y1)
		for _, p := range e.Waypoints {
			fmt.Fprintf(&b, " L %.1f %.1f", p.X+offset, p.Y)
		}
		fmt.Fprintf(&b, " L %.1f %.1f", e.X2, e.Y2)
		return b.String()
	}
	if offset == 0 {
		return fmt.Sprintf("M %.1f %.1f L %.1f %.1f", e.X1, e.Y1, e.X2, e.Y2)
	}
	return fmt.Sprintf("M %.1f %.1f Q %.1f %.1f %.1f %.1f",
		e.X1, e.Y1, (e.X1+e.X2)/2+offset, (e.Y1+e.Y2)/2, e.X2, e.Y2)
}

// sceneDescription is what a screen reader is told the picture shows, and what
// a sighted reader gets on hover. It names the bands and their sizes rather
// than describing shapes, because the bands are the content.
func sceneDescription(layout *diagram.Layout, framing string) string {
	parts := make([]string, 0, len(layout.Bands))
	for _, band := range layout.Bands {
		label := band.Layer.String()
		for _, l := range diagramLayers {
			if l.Layer == band.Layer {
				label = strings.ToLower(l.Label)
			}
		}
		parts = append(parts, fmt.Sprintf("%s in the %s layer",
			pluraliseCount(len(band.Nodes), "node", "nodes"), label))
	}
	return fmt.Sprintf("A layered diagram %s: %s, joined by %s. "+
		"The same information is in the table below.",
		framing, strings.Join(parts, ", "),
		pluraliseCount(len(layout.Edges), "connection", "connections"))
}
