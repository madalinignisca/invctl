package diagram

import (
	"math"
	"slices"
	"sort"
	"unicode/utf8"
)

// The metrics. Every one of these is a Go constant rather than a CSS value
// because the layout arithmetic happens here and the stylesheet cannot be
// allowed to disagree with it: a .diag text rule that sets a different
// font-size makes every box in this file the wrong width. The stylesheet's job
// is colour and stroke; size is decided here.
const (
	// LabelFontSize is the px size of a node's label.
	//
	// Every label in this diagram is monospace, and that is not a stylistic
	// choice -- it is what makes a box measurable in Go at all. In a
	// proportional face the advance width of a string is decided by the
	// browser at paint time, and asking from here needs a font library, which
	// is a dependency this project does not take. It also happens to match the
	// house rule that identifiers are monospace and human sentences are not.
	LabelFontSize = 11.5

	// CaptionFontSize is the px size of the kind caption under the label. Ten
	// is the floor in this design; nothing renders smaller than this.
	CaptionFontSize = 10.0

	// CharAdvance is the advance width of one monospace glyph as a fraction of
	// the font size. Around 0.60 is the figure across the font stack in use;
	// the rest is slack, because a box two pixels too wide costs nothing and a
	// box two pixels too narrow clips its own label.
	CharAdvance = 0.62

	// LabelPadX is the horizontal padding inside a box, per side.
	LabelPadX = 10.0

	// MinNodeWidth stops a one-character label from producing a box too small
	// to click.
	MinNodeWidth = 56.0

	// MaxLabelRunes and MaxKindRunes cap what is drawn. Truncation happens
	// here, in Go, and never with SVG textLength, which squashes glyphs and
	// looks broken. The untruncated value survives on Node.Label for the
	// renderer to put in a <title>.
	MaxLabelRunes = 22
	MaxKindRunes  = 14

	// NodeHeight is the box height: a label line, a caption line, and padding.
	NodeHeight = 38.0

	// NodeGap is the horizontal space between two boxes in a band.
	NodeGap = 20.0

	// BandGutter is the vertical space between one band's arc lane and the
	// next band's row. Cross-band edges cross here and nothing else does.
	BandGutter = 72.0

	// MarginX and MarginY are the canvas margins.
	MarginX = 24.0
	MarginY = 20.0

	// Same-band arcs dip below their row into a lane of their own, so they
	// never share space with the cross-band lines in the gutter. Nested spans
	// get deeper arcs; the level is capped so the deepest arc still fits the
	// lane the constants below size.
	ArcDepthBase = 14.0
	ArcDepthStep = 10.0
	MaxArcLevel  = 3
	ArcPad       = 8.0

	// ChannelPad is how far outside a band's row an edge routed through that
	// band turns the corner. The turn has to happen in free space: put it on
	// the row's own boundary and a stroke two pixels wide sits half inside the
	// box it was routed around.
	ChannelPad = 9.0

	// ChannelSpread is the furthest a renderer may move a routed edge sideways
	// from its channel and still be inside the gap the channel was chosen for.
	//
	// A renderer fans edges that join the same pair of nodes so that two of
	// them do not read as one, and it must not do that to a routed edge by
	// more than this: the gap is NodeGap wide, so half of it less a stroke's
	// worth of clearance is all the room there is. Exported because the
	// renderer owns the fan and this package owns the geometry, and the two
	// must not each hold their own idea of how wide a channel is.
	ChannelSpread = NodeGap/2 - 3
)

// TextWidth is the width of a monospace string at a given font size. It is
// exported because the renderer sizes nothing itself, but a caller building a
// legend or an edge label needs the same arithmetic and must not invent a
// second copy of it.
func TextWidth(s string, fontSize float64) float64 {
	return float64(utf8.RuneCountInString(s)) * CharAdvance * fontSize
}

// truncate shortens s to at most max runes, spending the last one on an
// ellipsis so the reader can see that something was cut. It counts runes
// rather than bytes: a label is operator-supplied text and cutting a UTF-8
// sequence in half produces a replacement character in the output.
func truncate(s string, max int) string {
	if max <= 0 || s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	if max == 1 {
		return "…"
	}
	return string(runes[:max-1]) + "…"
}

// boxWidth sizes a node from what will actually be drawn in it.
//
// The kind caption is measured as well as the label. A short label with a long
// kind -- "hv-01" captioned "hypervisor" -- is the common case, and sizing on
// the label alone would let the caption overflow the box it is supposed to be
// inside. The result is rounded up to a whole pixel so every coordinate
// derived from it stays exact.
func boxWidth(label, kind string) float64 {
	w := TextWidth(label, LabelFontSize)
	if c := TextWidth(kind, CaptionFontSize); c > w {
		w = c
	}
	return max(math.Ceil(w+2*LabelPadX), MinNodeWidth)
}

// place turns the ordering into coordinates.
//
// Vertically each band owns three things in order: its row of boxes, an arc
// lane sized to whatever same-band arcs it has (nothing, if it has none), and
// then the gutter down to the next band. Nodes never enter a lane or a gutter,
// and an edge enters a row only through a channel -- a gap between two boxes,
// which is where the boxes are not. So no box can ever be crossed by a line and
// the overlap question is closed by construction rather than by tuning.
//
// The channel exists because the "edges never enter a row" version of this rule
// was false for one shape and said nothing about it: an edge between bands two
// apart was drawn as a straight line, and a straight line from band 1 to band 3
// goes through band 2. See placeEdges.
//
// Horizontally each band is centred in the canvas, which is as wide as the
// widest band.
func (s *state) place(cross Crossings) *Layout {
	widths := make([]float64, len(s.nodes))
	for i := range s.nodes {
		n := &s.nodes[i]
		n.Short = truncate(n.Label, MaxLabelRunes)
		n.KindShort = truncate(n.Kind, MaxKindRunes)
		n.H = NodeHeight
		widths[i] = boxWidth(n.Short, n.KindShort)
		n.W = widths[i]
	}

	bandWidth := make([]float64, len(s.bands))
	content := 0.0
	for b, members := range s.bands {
		total := 0.0
		for i, n := range members {
			if i > 0 {
				total += NodeGap
			}
			total += widths[n]
		}
		bandWidth[b] = total
		if total > content {
			content = total
		}
	}

	// X first: it depends only on the ordering, and the arc depths that decide
	// the vertical layout depend on nothing else.
	for b, members := range s.bands {
		x := MarginX + (content-bandWidth[b])/2
		for slot, n := range members {
			s.nodes[n].Index = slot
			s.nodes[n].X = x
			x += widths[n] + NodeGap
		}
	}

	depth := s.arcDepths()
	lane := make([]float64, len(s.bands))
	for i, e := range s.edges {
		if !e.SameBand {
			continue
		}
		b := s.bandOf[s.ends[i][0]]
		if d := depth[i] + ArcPad; d > lane[b] {
			lane[b] = d
		}
	}

	l := &Layout{
		Width:     content + 2*MarginX,
		Bands:     make([]Band, len(s.bands)),
		Crossings: cross,
		index:     make(map[string]addr, len(s.nodes)),
	}
	if s.subject >= 0 {
		l.SubjectID = s.nodes[s.subject].ID
	}

	y := MarginY
	for b, members := range s.bands {
		for _, n := range members {
			s.nodes[n].Y = y
		}
		band := Band{
			Layer:   s.layers[b],
			Y:       y,
			Height:  NodeHeight,
			ArcLane: lane[b],
			Nodes:   make([]Node, len(members)),
		}
		for slot, n := range members {
			band.Nodes[slot] = s.nodes[n]
			l.index[s.nodes[n].ID] = addr{band: b, slot: slot}
		}
		l.Bands[b] = band

		y += NodeHeight + lane[b]
		if b < len(s.bands)-1 {
			y += BandGutter
		}
	}
	l.Height = y + MarginY

	l.Edges = s.placeEdges(depth)
	return l
}

// placeEdges anchors every edge and works out how many of them share a pair of
// nodes.
//
// A cross-band edge leaves the upper box's bottom edge and arrives at the
// lower box's top edge; a same-band edge leaves and arrives at the bottom edge
// and curves through its band's arc lane. Which of From and To is upper is
// decided by the bands, but X1/Y1 always belongs to From, so the renderer
// never has to guess which end it is drawing.
func (s *state) placeEdges(depth []float64) []Edge {
	type pair struct{ a, b int }
	totals := make(map[pair]int, len(s.edges))
	for _, e := range s.ends {
		lo, hi := minMax(e[0], e[1])
		totals[pair{lo, hi}]++
	}
	seen := make(map[pair]int, len(s.edges))

	out := make([]Edge, len(s.edges))
	for i := range s.edges {
		e := s.edges[i]
		from, to := s.nodes[s.ends[i][0]], s.nodes[s.ends[i][1]]

		lo, hi := minMax(s.ends[i][0], s.ends[i][1])
		e.ParallelCount = totals[pair{lo, hi}]
		e.Parallel = seen[pair{lo, hi}]
		seen[pair{lo, hi}]++

		fromBand, toBand := s.bandOf[s.ends[i][0]], s.bandOf[s.ends[i][1]]
		e.X1, e.X2 = from.CenterX(), to.CenterX()
		switch {
		case e.SameBand:
			e.Y1, e.Y2 = from.Bottom(), to.Bottom()
			e.Depth = depth[i]
			e.CX = (e.X1 + e.X2) / 2
			// A quadratic reaches half way to its control point, so the
			// control point sits at twice the depth the curve should show.
			e.CY = from.Bottom() + 2*e.Depth
		case fromBand < toBand:
			e.Y1, e.Y2 = from.Bottom(), to.Y
		default:
			e.Y1, e.Y2 = from.Y, to.Bottom()
		}
		// After the anchors, not inside the switch: the route is interpolated
		// between them, so it needs both to be final.
		if !e.SameBand {
			e.Waypoints = s.route(fromBand, toBand, e)
		}
		out[i] = e
	}
	return out
}

// route returns the waypoints an edge from band fromBand to band toBand needs to
// reach its far end without crossing a row of boxes on the way, in draw order:
// From's end first, whichever direction that is down the page.
//
// Adjacent bands need none -- the gutter between them is empty by construction.
// Every band strictly between the two gets a pair: the edge turns into a channel
// just above the row, runs straight down through it, and turns back out just
// below. Two right-angle turns rather than a curve, because the whole value of
// the manoeuvre is that the reader can see the line went *between* two boxes and
// not behind them, and a curve at this scale is ambiguous about which.
func (s *state) route(fromBand, toBand int, e Edge) []Point {
	step := 1
	if fromBand > toBand {
		step = -1
	}
	var pts []Point
	for b := fromBand + step; b != toBand; b += step {
		top, ok := s.bandTop(b)
		if !ok {
			continue // A band with no nodes has nothing to route around.
		}
		bottom := top + NodeHeight

		// Aim for where the straight line would have been at this depth, so a
		// route that has to detour detours as little as it can.
		want := e.X2
		if e.Y2 != e.Y1 {
			t := ((top+bottom)/2 - e.Y1) / (e.Y2 - e.Y1)
			want = e.X1 + t*(e.X2-e.X1)
		}
		x := s.channelX(b, want)

		enter, exit := Point{X: x, Y: top - ChannelPad}, Point{X: x, Y: bottom + ChannelPad}
		if step < 0 {
			enter, exit = exit, enter
		}
		pts = append(pts, enter, exit)
	}
	return pts
}

// bandTop is the y of band b's row of boxes, and false if the band is empty.
func (s *state) bandTop(b int) (float64, bool) {
	if b < 0 || b >= len(s.bands) || len(s.bands[b]) == 0 {
		return 0, false
	}
	return s.nodes[s.bands[b][0]].Y, true
}

// channelX picks the x of a vertical channel through band b -- a place where a
// line can cross the row without touching a box -- as close to want as the
// band's boxes allow.
//
// The candidates are the midpoint of every gap between consecutive boxes, plus
// the free space outside the leftmost and rightmost. A band always has at least
// those two, so this always returns something a line can use; with one node it
// returns a side, which is correct.
func (s *state) channelX(b int, want float64) float64 {
	members := s.bands[b]
	best, dist := 0.0, math.Inf(1)
	consider := func(x float64) {
		if d := math.Abs(x - want); d < dist {
			best, dist = x, d
		}
	}

	// Slots are ordered left to right by place, so consecutive members bound
	// consecutive gaps.
	first, last := s.nodes[members[0]], s.nodes[members[len(members)-1]]
	consider(first.X - NodeGap/2)
	consider(last.Right() + NodeGap/2)
	for i := 1; i < len(members); i++ {
		consider((s.nodes[members[i-1]].Right() + s.nodes[members[i]].X) / 2)
	}
	return best
}

// arcDepths assigns each same-band edge a dip depth, deep enough that a span
// drawn inside another span is a separate visible line rather than a thicker
// version of the same one. Nesting level is how many of the band's other arcs
// this one strictly contains, capped so the deepest arc still fits its lane.
//
// Edges that are not same-band get 0.
func (s *state) arcDepths() []float64 {
	depth := make([]float64, len(s.edges))

	type arc struct {
		idx    int
		lo, hi int
	}
	byBand := make([][]arc, len(s.bands))
	for i := range s.edges {
		if !s.edges[i].SameBand {
			continue
		}
		b := s.bandOf[s.ends[i][0]]
		lo, hi := minMax(s.pos[s.ends[i][0]], s.pos[s.ends[i][1]])
		byBand[b] = append(byBand[b], arc{idx: i, lo: lo, hi: hi})
	}

	for _, arcs := range byBand {
		// Sorted by span so the level of a containing arc is decided after
		// everything it contains. The tie-break on index keeps two arcs with
		// identical spans in edge order.
		sorted := slices.Clone(arcs)
		sort.SliceStable(sorted, func(i, j int) bool {
			si := sorted[i].hi - sorted[i].lo
			sj := sorted[j].hi - sorted[j].lo
			if si != sj {
				return si < sj
			}
			return sorted[i].idx < sorted[j].idx
		})
		level := make(map[int]int, len(sorted))
		for _, a := range sorted {
			best := 0
			for _, other := range sorted {
				if other.idx == a.idx {
					continue
				}
				inside := other.lo >= a.lo && other.hi <= a.hi &&
					(other.hi-other.lo) < (a.hi-a.lo)
				if inside && level[other.idx]+1 > best {
					best = level[other.idx] + 1
				}
			}
			if best > MaxArcLevel {
				best = MaxArcLevel
			}
			level[a.idx] = best
			depth[a.idx] = ArcDepthBase + float64(best)*ArcDepthStep
		}
	}
	return depth
}
