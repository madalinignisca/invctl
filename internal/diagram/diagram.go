// Package diagram turns a neighbourhood graph into coordinates.
//
// It is pure geometry. No SQL, no HTTP, no SVG, no domain types: it takes
// nodes and edges that already carry a layer and hands back the same graph
// with an x, a y, a width and a slot number on every node, so the renderer can
// be a template that ranges over rectangles and line endpoints, and the layout
// can be tested without a database or a browser.
//
// The layers supply the Y axis, and that is the whole reason this is not
// general graph layout. Every layer that has at least one node becomes a
// horizontal band, ordered so that a service sits above the virtual adjacency
// it resolves down to, which sits above the wire. A layer with no nodes
// produces no band at all rather than an empty stripe -- a neighbourhood with
// no bridges in it should look like a two-layer picture, not like a
// three-layer picture with a hole.
//
// Within a band the only freedom left is left-to-right order, one dimension,
// and that is what the barycentre heuristic in order.go spends itself on.
// There is deliberately no force-directed anything here: a force layout offers
// no overlap guarantee and, unless its seed is pinned, a different picture on
// every render. During an incident two people on the same call need the same
// URL to show them the same picture, so Build is a pure function of its input
// and nothing in it iterates a map without sorting.
//
// What this package does NOT decide: colour, stroke, glyph, which layers are
// visible, and what a status string means. Those are the renderer's, and
// keeping them out is what lets the layout be asserted numerically.
package diagram

import (
	"errors"
	"fmt"
	"strconv"
)

// ErrInvalid is returned by Build for input it will not lay out. Every case is
// a caller bug rather than a data condition -- an edge to a node that is not in
// the node set is a query that forgot to close its frontier, and drawing the
// rest of the picture without saying so is the silently-missing-node lie this
// codebase keeps refusing to tell.
var ErrInvalid = errors.New("invalid diagram input")

// Layer is one of the three transport realities a neighbourhood is drawn in.
//
// The numbers are the stack order and are load-bearing: bands are laid out in
// descending Layer, so LayerService is the top band and LayerPhysical the
// bottom one. A service line resolving down to a wire therefore reads
// downwards, which is the point of stacking them at all.
type Layer int

const (
	// LayerPhysical is wires between ports on physical devices.
	LayerPhysical Layer = 1
	// LayerVirtual is veths, bridges and bonds -- adjacency that exists inside
	// a host rather than in a rack.
	LayerVirtual Layer = 2
	// LayerService is listening sockets and the dependencies between them.
	LayerService Layer = 3
)

// String names the layer for a caption or a CSS class. It is a name, not a
// label: capitalisation and translation belong to the renderer.
func (l Layer) String() string {
	switch l {
	case LayerPhysical:
		return "physical"
	case LayerVirtual:
		return "virtual"
	case LayerService:
		return "service"
	default:
		return "layer" + strconv.Itoa(int(l))
	}
}

// Valid reports whether l is one of the three known layers. Build rejects
// anything else rather than inventing a fourth band, because a node that
// arrived with a zero Layer is a caller that forgot to set it.
func (l Layer) Valid() bool {
	return l == LayerPhysical || l == LayerVirtual || l == LayerService
}

// LayersTopDown returns the layers in the order Build stacks them, so a legend
// or a set of toggles lists them the same way the picture does.
func LayersTopDown() []Layer {
	return []Layer{LayerService, LayerVirtual, LayerPhysical}
}

// Node is one box. The caller fills ID, Label, Kind, Layer and Status; Build
// fills everything from Short down and ignores whatever was in those fields on
// the way in.
type Node struct {
	// ID is the caller's identity for the node and must be unique. It is what
	// edges reference; it is never rendered.
	ID string
	// Label is the operator-facing name, verbatim. The renderer belongs to put
	// this in a <title> so the untruncated value is always recoverable.
	Label string
	// Kind is the vocabulary term shown as a caption -- an asset kind, or
	// whatever the caller uses for a service or an endpoint. It may be empty.
	// Nothing in this package branches on it.
	Kind string
	// Layer decides which band the node lands in.
	Layer Layer
	// Status is passed through untouched for the renderer to map to a class.
	// This package deliberately does not know the vocabulary: the moment it
	// does, layout starts having opinions about health.
	Status string

	// Subject reports whether this node is the asset the neighbourhood was
	// built around. Build sets it from Input.Subject; setting it by hand does
	// nothing.
	Subject bool

	// Short is Label truncated to MaxLabelRunes for drawing. Equal to Label
	// when it fits.
	Short string
	// KindShort is Kind truncated to MaxKindRunes for drawing.
	KindShort string

	// Index is the node's slot in its band, 0 at the left.
	Index int
	// X and Y are the top-left corner of the box.
	X, Y float64
	// W and H are the box size. W is computed from the label, so boxes in a
	// band are not uniform and are not spaced as though they were.
	W, H float64
}

// CenterX is the horizontal middle of the box, which is where edges anchor.
func (n Node) CenterX() float64 { return n.X + n.W/2 }

// Bottom is the lower edge of the box.
func (n Node) Bottom() float64 { return n.Y + n.H }

// Right is the right edge of the box.
func (n Node) Right() float64 { return n.X + n.W }

// Edge is one line. The caller fills From, To, Kind and Layer; Build fills the
// geometry.
type Edge struct {
	// From and To are node IDs. Both must exist and they must differ.
	From, To string
	// Kind is the caller's edge vocabulary -- cable, enslavement, containment,
	// dependency, descent. Passed through for styling.
	Kind string
	// Layer is the transport reality the edge belongs to, which is not always
	// the band it is drawn in: a bridge uplink is a layer-2 fact whose two
	// ends sit in different bands.
	Layer Layer

	// FromLayer and ToLayer are the layers of the two endpoints.
	FromLayer, ToLayer Layer
	// SameBand reports whether both endpoints landed in the same band. A
	// same-band edge is drawn as an arc in that band's arc lane; a cross-band
	// edge is drawn as a line through the gutter. They must not look alike --
	// a layered diagram whose adjacency and whose descent share a stroke is
	// three unrelated pictures with lines on top.
	SameBand bool

	// X1, Y1 anchor on From and X2, Y2 on To, always in that order regardless
	// of which end is higher up the page.
	X1, Y1, X2, Y2 float64

	// Depth is how far below the row a same-band arc dips at its lowest point,
	// and 0 for a cross-band edge. Nested arcs get increasing depths so that a
	// span drawn inside another span is visible as a separate line.
	Depth float64
	// CX, CY is the control point for a quadratic curve through that dip:
	// "M X1 Y1 Q CX CY X2 Y2". It is a control point and not a point on the
	// curve, so CY sits twice Depth below the row. Zero for a cross-band edge,
	// which is a straight line.
	CX, CY float64

	// Waypoints are points the edge must pass through, in draw order, between
	// its two anchors. Empty for a same-band arc and for a cross-band edge
	// between adjacent bands, both of which reach their far end without
	// entering a row of boxes.
	//
	// An edge spanning more than one band cannot: a service instance placed
	// directly on bare metal joins band 3 to band 1, and the straight line
	// between them goes through band 2's row. So it is routed through a
	// channel -- the gap between two boxes -- in each band it passes, which is
	// what keeps the guarantee in place's doc comment true rather than nearly
	// true. Points come in pairs, one just outside each side of the row.
	Waypoints []Point

	// Parallel is this edge's index among the edges joining the same pair of
	// nodes, and ParallelCount is how many there are. Two cables between the
	// same two chassis drawn on top of each other say "one wire" when the
	// truth is "two", and redundancy is exactly the fact an operator came here
	// for -- so the renderer fans them out using these.
	Parallel, ParallelCount int
}

// Point is a coordinate on an edge's route.
type Point struct{ X, Y float64 }

// Band is one layer's horizontal row, plus the arc lane underneath it.
type Band struct {
	// Layer is the layer this band draws.
	Layer Layer
	// Y is the top edge of the row of boxes; Height is the row's height.
	Y, Height float64
	// ArcLane is the vertical space reserved directly below the row for this
	// band's own same-band arcs. It is 0 when the band has none, so a band of
	// nodes nobody wired together takes no space it does not use.
	ArcLane float64
	// Nodes are the band's nodes in slot order, left to right.
	Nodes []Node
}

// Bottom is the lower edge of everything the band owns -- its row and its arc
// lane. The gutter to the next band starts here.
func (b Band) Bottom() float64 { return b.Y + b.Height + b.ArcLane }

// Crossings reports how many pairs of edges cross, before and after ordering,
// so a test can assert the heuristic earned its place.
//
// The count is defined per band pair: two edges are compared only when they
// join the same two bands (or both live inside the same band), because that is
// where the comparison is exact -- within a band pair, slot order and x order
// agree, so an inversion is a crossing and nothing else is. An edge that skips
// a band is not counted against the edges of the band it passes behind. The
// number is therefore a comparable objective rather than a pixel census, and
// it is the objective the ordering actually minimises.
//
// Before is measured on the seed ordering, which is the caller's node order
// with the subject already moved to the middle of its band; After is the
// ordering Build returned. After is never greater than Before, because the
// seed competes with every later candidate and the best is kept.
type Crossings struct {
	Before, After int
}

// Layout is the positioned graph.
type Layout struct {
	// Width and Height are the canvas, margins included. They are what belongs
	// in an SVG viewBox.
	Width, Height float64
	// Bands are the non-empty layers, top to bottom.
	Bands []Band
	// Edges are in the caller's order, so the renderer's draw order is the
	// caller's to decide.
	Edges []Edge
	// Crossings is the before/after count.
	Crossings Crossings
	// SubjectID is the anchor node's id, empty when there was none.
	SubjectID string

	index map[string]addr
}

type addr struct{ band, slot int }

// Node looks a positioned node up by id.
func (l *Layout) Node(id string) (Node, bool) {
	a, ok := l.index[id]
	if !ok {
		return Node{}, false
	}
	return l.Bands[a.band].Nodes[a.slot], true
}

// Input is a neighbourhood: the nodes and edges a store query produced, plus
// the asset it was built around.
//
// Node and edge order is part of the input. Build is deterministic given the
// same slices, and it seeds its ordering from them, so a caller that assembles
// them by ranging a map gets a stable picture only by accident. Every query
// feeding this has an explicit ORDER BY for that reason.
type Input struct {
	Nodes []Node
	Edges []Edge
	// Subject is the id of the anchor node, which Build places in the middle
	// slot of its band. It may be empty, in which case nothing is anchored.
	Subject string
}

// Build positions the neighbourhood.
//
// It never mutates the caller's slices, and it returns an error rather than a
// half-drawn picture for input it cannot honour.
func Build(in Input) (*Layout, error) {
	s, err := newState(in)
	if err != nil {
		return nil, err
	}
	cross := s.order()
	return s.place(cross), nil
}

// newState validates the input and builds the working representation: nodes
// grouped into bands, an adjacency list, and each edge resolved to a pair of
// node indices.
func newState(in Input) (*state, error) {
	nodes := make([]Node, len(in.Nodes))
	copy(nodes, in.Nodes)

	byID := make(map[string]int, len(nodes))
	for i := range nodes {
		n := &nodes[i]
		if n.ID == "" {
			return nil, fmt.Errorf("%w: node %d has no id", ErrInvalid, i)
		}
		if n.Label == "" {
			return nil, fmt.Errorf("%w: node %q has no label", ErrInvalid, n.ID)
		}
		if !n.Layer.Valid() {
			return nil, fmt.Errorf("%w: node %q has unknown layer %d", ErrInvalid, n.ID, int(n.Layer))
		}
		if _, dup := byID[n.ID]; dup {
			return nil, fmt.Errorf("%w: duplicate node id %q", ErrInvalid, n.ID)
		}
		byID[n.ID] = i
		n.Subject = false
	}

	subject := -1
	if in.Subject != "" {
		i, ok := byID[in.Subject]
		if !ok {
			return nil, fmt.Errorf("%w: subject %q is not in the node set", ErrInvalid, in.Subject)
		}
		subject = i
		nodes[i].Subject = true
	}

	edges := make([]Edge, len(in.Edges))
	copy(edges, in.Edges)
	ends := make([][2]int, len(edges))
	adj := make([][]int, len(nodes))
	for i := range edges {
		e := &edges[i]
		if !e.Layer.Valid() {
			return nil, fmt.Errorf("%w: edge %d (%q -> %q) has unknown layer %d",
				ErrInvalid, i, e.From, e.To, int(e.Layer))
		}
		if e.From == e.To {
			return nil, fmt.Errorf("%w: edge %d joins %q to itself", ErrInvalid, i, e.From)
		}
		a, ok := byID[e.From]
		if !ok {
			return nil, fmt.Errorf("%w: edge %d references unknown node %q", ErrInvalid, i, e.From)
		}
		b, ok := byID[e.To]
		if !ok {
			return nil, fmt.Errorf("%w: edge %d references unknown node %q", ErrInvalid, i, e.To)
		}
		ends[i] = [2]int{a, b}
		adj[a] = append(adj[a], b)
		adj[b] = append(adj[b], a)
		e.FromLayer = nodes[a].Layer
		e.ToLayer = nodes[b].Layer
	}

	s := &state{
		nodes:   nodes,
		edges:   edges,
		ends:    ends,
		adj:     adj,
		bandOf:  make([]int, len(nodes)),
		pos:     make([]int, len(nodes)),
		subject: subject,
	}
	s.groupIntoBands()
	for i := range s.edges {
		s.edges[i].SameBand = s.bandOf[ends[i][0]] == s.bandOf[ends[i][1]]
	}
	return s, nil
}

// state is the working representation. Nothing here is exported and nothing
// here iterates a map: every loop that can affect the picture walks a slice.
type state struct {
	nodes []Node
	edges []Edge
	ends  [][2]int // edge index -> the two node indices
	adj   [][]int  // node index -> neighbouring node indices, in edge order

	bands  [][]int // band index -> node indices, in slot order
	layers []Layer // band index -> layer
	bandOf []int   // node index -> band index
	pos    []int   // node index -> slot within its band

	subject int // node index, or -1
}

// groupIntoBands puts every node in the band for its layer, keeping the
// caller's order within the band, and drops layers nobody is in. Bands run
// top-to-bottom in descending layer.
func (s *state) groupIntoBands() {
	for _, layer := range LayersTopDown() {
		var members []int
		for i := range s.nodes {
			if s.nodes[i].Layer == layer {
				members = append(members, i)
			}
		}
		if len(members) == 0 {
			continue
		}
		b := len(s.bands)
		s.bands = append(s.bands, members)
		s.layers = append(s.layers, layer)
		for slot, n := range members {
			s.bandOf[n] = b
			s.pos[n] = slot
		}
	}
}
