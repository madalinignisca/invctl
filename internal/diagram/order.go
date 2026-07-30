package diagram

import (
	"slices"
	"sort"
)

// maxPasses caps the barycentre sweeps. Passes alternate direction, and every
// second pair also alternates which neighbours count (see order). Twelve is
// where the curve flattens: measured over four thousand randomly generated
// layered neighbourhoods, 4 passes removed 60.3% of the seed's crossings, 8
// removed 66.9%, 12 removed 68.8%, and 24 removed 69.9% for twice the work. A
// 64-node neighbourhood with 200 edges lays out in about two milliseconds,
// which is not the expensive part of rendering a page.
//
// It is a cap rather than a convergence test on purpose. An earlier version
// stopped after two passes that moved nothing, and that measurably *hurt*
// -- 65.0% against 66.9% on the same corpus -- because a quiet pass in one
// flavour is routinely followed by a productive pass in the other. The best
// ordering seen is kept regardless, so extra passes can only help.
const maxPasses = 12

// order runs crossing reduction and returns the before/after counts.
//
// The shape is the standard layer-by-layer barycentre sweep, with two
// departures that the diagram this serves forces.
//
// The first is that positions are normalised to [0,1] within a band before
// they are averaged. Classic barycentre sorts one layer by raw indices in an
// adjacent layer, where a monotone rescale is harmless. Here a node can have
// neighbours in two differently sized bands at once, and averaging raw indices
// would let a wide band outvote a narrow one for no reason except its size.
//
// The second is that the sweep is run in two flavours and the better result is
// kept. Most edges in this diagram are same-band -- a cable joins two physical
// assets, both of which are in the physical band -- and two cables whose slot
// intervals interleave cross just as visibly as two descents do, so it seemed
// obvious that same-band neighbours should count towards a node's barycentre.
// Measured, it is not obvious at all. Over four thousand random
// neighbourhoods, all three at the same eight passes so that only the flavour
// varies:
//
//	adjacent bands only      46.6% of crossings removed, and 1148 of the
//	                         survivors were between bands
//	same-band included       63.7% removed, and 2374 between bands -- better
//	                         overall and distinctly worse at the crossings a
//	                         reader traces down the page
//	alternating, best kept   66.9% removed, and 973 between bands
//
// So neither flavour dominates and the combination beats both on both counts.
// At the twelve passes this ships with, the alternating schedule removes 68.8%
// and leaves 835 between bands.
//
// So: passes 0-1 use adjacent bands only, passes 2-3 include same-band
// neighbours, and so on; every candidate ordering is scored and the best one
// survives. That also makes the guarantee in the Crossings doc comment --
// After is never worse than Before -- hold by construction rather than by
// hoping the heuristic behaves.
func (s *state) order() Crossings {
	for b := range s.bands {
		s.pinSubject(b)
		s.reindex(b)
	}

	before := s.countCrossings()
	best := s.snapshot()
	bestCount := before

	for pass := range maxPasses {
		down := pass%2 == 0
		sameBand := (pass/2)%2 == 1
		if down {
			for b := range s.bands {
				s.orderBand(b, true, sameBand)
			}
		} else {
			for b := len(s.bands) - 1; b >= 0; b-- {
				s.orderBand(b, false, sameBand)
			}
		}
		if n := s.countCrossings(); n < bestCount {
			bestCount = n
			best = s.snapshot()
		}
	}

	s.restore(best)
	return Crossings{Before: before, After: bestCount}
}

// orderBand sorts one band by the barycentre of each node's neighbours.
//
// down decides which side is the reference: a downward sweep orders a band by
// what is above it, an upward sweep by what is below. sameBand decides whether
// the band's own edges join the reference set. A node with no neighbours in
// the reference set keeps its own position, which is what makes the sort leave
// unattached nodes where the caller put them instead of herding them to one
// end.
func (s *state) orderBand(b int, down, sameBand bool) {
	nodes := s.bands[b]
	if len(nodes) < 2 {
		return
	}

	type keyed struct {
		node int
		bary float64
	}
	// Every barycentre is computed from the positions as they stand on entry,
	// so the result does not depend on the order the band's own nodes happen
	// to be visited in.
	keys := make([]keyed, len(nodes))
	for i, n := range nodes {
		sum, count := 0.0, 0
		for _, m := range s.adj[n] {
			if !votes(s.bandOf[m], b, down, sameBand) {
				continue
			}
			sum += s.normPos(m)
			count++
		}
		if count == 0 {
			keys[i] = keyed{node: n, bary: s.normPos(n)}
			continue
		}
		keys[i] = keyed{node: n, bary: sum / float64(count)}
	}

	// Stable, over a slice already in current order: equal barycentres keep
	// the order they had, which is what keeps the picture from shuffling
	// between two runs that scored the same.
	sort.SliceStable(keys, func(i, j int) bool { return keys[i].bary < keys[j].bary })
	for i, k := range keys {
		nodes[i] = k.node
	}
	s.bands[b] = nodes

	s.pinSubject(b)
	s.reindex(b)
}

// votes reports whether a neighbour sitting in band mb gets a say in the
// ordering of band b on this sweep. A downward sweep listens to what is above,
// an upward sweep to what is below, and sameBand decides whether the band's
// own edges are heard at all.
func votes(mb, b int, down, sameBand bool) bool {
	if mb == b {
		return sameBand
	}
	if down {
		return mb < b
	}
	return mb > b
}

// pinSubject moves the anchor to the middle slot of its band.
//
// The subject is the asset somebody asked about; it should be where the eye
// lands. Centring it is applied to the seed ordering as well as to every
// candidate, so the crossing counts compare like with like and the anchor is
// never traded away for a tidier picture.
func (s *state) pinSubject(b int) {
	if s.subject < 0 || s.bandOf[s.subject] != b {
		return
	}
	nodes := s.bands[b]
	want := len(nodes) / 2
	at := slices.Index(nodes, s.subject)
	if at < 0 || at == want {
		return
	}
	nodes = slices.Delete(nodes, at, at+1)
	s.bands[b] = slices.Insert(nodes, want, s.subject)
}

// reindex rewrites pos for one band after its slice changed.
func (s *state) reindex(b int) {
	for slot, n := range s.bands[b] {
		s.pos[n] = slot
	}
}

// normPos is a node's slot as a fraction of its band's width, so positions in
// bands of different sizes can be averaged together. A lone node sits in the
// middle of its band rather than at one end.
func (s *state) normPos(n int) float64 {
	size := len(s.bands[s.bandOf[n]])
	if size <= 1 {
		return 0.5
	}
	return float64(s.pos[n]) / float64(size-1)
}

// countCrossings counts pairs of edges that cross under the current ordering.
// See the Crossings doc comment for exactly what is and is not counted. The
// loop is quadratic in the edge count, which is fine for a neighbourhood --
// the caller bounds it long before this becomes the expensive part.
func (s *state) countCrossings() int {
	total := 0
	for i := range s.ends {
		for j := i + 1; j < len(s.ends); j++ {
			if s.crosses(s.ends[i], s.ends[j]) {
				total++
			}
		}
	}
	return total
}

// crosses reports whether two edges cross.
func (s *state) crosses(e1, e2 [2]int) bool {
	u1, l1 := s.upperLower(e1)
	u2, l2 := s.upperLower(e2)

	// A rail and a line leaving its band. A same-band rail dips into the arc
	// lane below its row; a cross-band edge whose UPPER endpoint is in that
	// band leaves the bottom of its box and falls straight through that lane.
	// If it leaves from a box strictly inside the rail's span it cannot get
	// out without piercing it: the region between the row and the rail is
	// closed below by the rail and at the sides by the rail's own verticals.
	// For most of this package's life these pairs were silently declared
	// non-crossing, which let the sweep buy one counted crossing by selling
	// several uncounted piercings -- measured at 8528 drawn-but-uncounted
	// crossings over 4000 generated neighbourhoods.
	//
	// The lower endpoint's band needs no term: the line arrives at the TOP of
	// a box there and never enters that band's lane. A two-band edge falling
	// through an intervening band is still uncounted -- its channel is chosen
	// at placement, which the ordering cannot see -- and DrawnCrossings is
	// the measurement that keeps that gap honest.
	same1, same2 := s.bandOf[u1] == s.bandOf[l1], s.bandOf[u2] == s.bandOf[l2]
	if same1 != same2 {
		arcU, arcL, dropU := u1, l1, u2
		if same2 {
			arcU, arcL, dropU = u2, l2, u1
		}
		if s.bandOf[arcU] != s.bandOf[dropU] || dropU == arcU || dropU == arcL {
			return false
		}
		lo, hi := minMax(s.pos[arcU], s.pos[arcL])
		return lo < s.pos[dropU] && s.pos[dropU] < hi
	}

	if s.bandOf[u1] != s.bandOf[u2] || s.bandOf[l1] != s.bandOf[l2] {
		return false
	}

	if s.bandOf[u1] == s.bandOf[l1] {
		// Two arcs under the same band. They cross where their slot intervals
		// interleave without nesting; sharing an endpoint is not a crossing,
		// and one span drawn inside another is not either -- that is what the
		// arc depths in geometry.go keep apart.
		lo1, hi1 := minMax(s.pos[u1], s.pos[l1])
		lo2, hi2 := minMax(s.pos[u2], s.pos[l2])
		return (lo1 < lo2 && lo2 < hi1 && hi1 < hi2) || (lo2 < lo1 && lo1 < hi2 && hi2 < hi1)
	}

	// Two lines between the same pair of bands cross exactly when their
	// endpoints are in opposite order, since x order matches slot order within
	// every band.
	return (s.pos[u1]-s.pos[u2])*(s.pos[l1]-s.pos[l2]) < 0
}

// upperLower orients an edge so the first node is the one in the higher band
// (smaller band index). Same-band edges are returned as given.
func (s *state) upperLower(e [2]int) (int, int) {
	if s.bandOf[e[0]] > s.bandOf[e[1]] {
		return e[1], e[0]
	}
	return e[0], e[1]
}

func minMax(a, b int) (int, int) {
	if a > b {
		return b, a
	}
	return a, b
}

func (s *state) snapshot() [][]int {
	out := make([][]int, len(s.bands))
	for i, b := range s.bands {
		out[i] = slices.Clone(b)
	}
	return out
}

func (s *state) restore(snap [][]int) {
	for i, b := range snap {
		s.bands[i] = slices.Clone(b)
		s.reindex(i)
	}
}
