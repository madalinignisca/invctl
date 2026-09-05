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
	"fmt"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Tracing a cable end to end, through whatever passive gear is in the way.
//
// This is the part of a DCIM that gets slow and subtly wrong, so the shape is
// deliberate: load the whole cable plant once, walk it in Go, and bound the walk
// twice -- a visited set AND a hop limit. Either alone is not enough. A visited
// set stops a loop; a hop limit stops a chain that is merely absurd, which is
// what a mis-patched panel produces before anybody notices.

// TraceHopKind says what got us from the previous hop to this one.
const (
	// HopCable is a link: a real cable between two boxes.
	HopCable = "cable"
	// HopPanel is a pass-through: the same panel, front to rear or back.
	HopPanel = "panel"
)

// TraceHop is one step along a path.
type TraceHop struct {
	Kind        string
	AssetID     string
	AssetName   string
	AssetKind   string
	InterfaceID string
	Interface   string
	// Medium and Length describe the CABLE that reached this hop, when one did.
	Medium string
	Length *int
}

// Trace is a run from one interface to wherever it ends -- a tree, always
// (docs/panel-breakout-design.md D1). A 1:1 run is a tree with one branch.
type Trace struct {
	StartAssetID   string
	StartAsset     string
	StartInterface string
	// Root is the port the caller asked about, and the run beneath it. Never nil.
	Root *TraceNode
}

// TraceNode is one step of a run. Children are its continuations: none for a
// leaf, one for an ordinary hop, several where a rear port breaks out.
type TraceNode struct {
	// Hop is how the run arrived HERE. Zero on the root, which is the port
	// the caller asked about.
	Hop TraceHop
	// Position is which strand of the parent rear port this node came through,
	// and is 0 for every node that is not the far side of a breakout. It is
	// port_pass_through.position, never an index into Children -- see D5.
	Position int
	Children []*TraceNode
	// Outcome and Why are set on a LEAF and empty on an interior node (D3).
	Outcome string
	Why     string
}

// leaf records why a branch ends here. Outcome and Why are set together, and
// only on a node with no children: an interior node has no verdict of its own,
// because with three strands patched and nine looping neither answer is true
// of the whole (docs/panel-breakout-design.md D3).
func (n *TraceNode) leaf(outcome, why string) { n.Outcome, n.Why = outcome, why }

// What a leaf can be. The strings are stable: the page branches on them, the
// tests name them, and a leaf's Why is prose for a person while its Outcome is
// the thing code is allowed to compare.
const (
	// OutcomeComplete: this branch ran out of cable rather than out of patience.
	OutcomeComplete = "complete"
	// OutcomeUnpatched: the port the caller asked about has nothing in it.
	OutcomeUnpatched = "unpatched"
	// OutcomeLooped: this branch arrived somewhere already on its own path --
	// something is patched into its own run.
	OutcomeLooped = "looped"
	// OutcomeHopLimit: traceHopLimit, which bounds one branch.
	OutcomeHopLimit = "hop_limit"
	// OutcomeNodeBudget: traceNodeBudget, which bounds the whole tree.
	OutcomeNodeBudget = "node_budget"
	// OutcomeUnknown: the continuation names an interface the plant does not
	// hold -- a retired asset at the far end of a cable, or a pass-through onto
	// a port that no longer exists.
	OutcomeUnknown = "unknown"
)

// traceHopLimit bounds a path.
//
// A real run crosses two or three panels. Sixty-four is far past anything
// physical and exists so a mis-patched plant produces a bounded, reported
// answer rather than a page that never returns -- which is worse than a wrong
// one, because nobody can see what it was going to say.
const traceHopLimit = 64

// traceNodeBudget bounds the whole TREE, beside the per-branch hop limit.
//
// Depth stopped being the only way a walk gets large the moment one rear port
// could have twelve continuations: a twelve-way break-out into panels that
// themselves break out is 144 leaves at depth two, and traceHopLimit does not
// see that at all. The two bounds answer different questions -- "this run is
// absurdly long" and "this plant fans out faster than anyone wants to read" --
// and neither covers the other.
//
// 512 is comfortably past the 144 the motivating case produces, and small
// enough that a malformed plant renders a bounded page instead of hanging a
// browser. It is a guard against a plant being edited halfway through, not a
// capacity limit anybody should meet.
const traceNodeBudget = 512

// budget is the tree-wide node allowance.
//
// SPENDING IS NEVER REFUSED. A successor the plant actually holds is never
// silently dropped -- dropping one reports a SHORTER trace with no error,
// which is the worst failure shape available here. What the budget refuses is
// EXPANSION: a node created after the allowance is gone becomes a leaf that
// says so. The tree is therefore bounded by the budget plus the fan-out of the
// single node that crossed zero -- one node can overshoot, because expansion
// is depth first and the check happens before each one -- and every strand
// that stopped early carries the reason it stopped.
type budget struct{ left int }

func (b *budget) spend(n int) { b.left -= n }
func (b *budget) spent() bool { return b.left <= 0 }

// plant is the whole cable plant, loaded once.
type plant struct {
	// cable maps an interface to the interface at the other end of its link.
	cable map[string]cableEnd
	// through maps an interface to the ports on the panel's other side.
	//
	// ONE-TO-MANY, and that is the whole of this work package. A twelve-fibre
	// MPO trunk lands on one rear port and breaks out to twelve front ports;
	// map[string]string could hold exactly one of them, so eleven fibres were
	// invisible and the tracer silently reported whichever row loadPlant read
	// last. A FRONT port still has at most one entry -- the partial unique
	// index port_pass_through_front_key enforces that on live rows -- so only
	// the rear side is ever longer than one.
	//
	// Ordered by position, which comes from the query rather than a sort here:
	// the tree must render in the order the strands are physically numbered.
	through map[string][]passThroughEnd
	iface   map[string]ifaceInfo
}

// passThroughEnd is the far side of one pass-through row, seen from whichever
// end the walk is standing on.
type passThroughEnd struct {
	// other is the interface on the panel's opposite side.
	other string
	// position is port_pass_through.position, declared by whoever recorded the
	// patch. Never derived, never renumbered, never an index into anything:
	// strand 7 stays strand 7 when strand 6 is unpatched, because it is a fact
	// about which hole the fibre is in (docs/panel-breakout-design.md D5).
	position int
	// fromRear is true when the interface this entry is filed under is the REAR
	// port, so `other` is one of possibly several front ports.
	//
	// IT IS NOT A DIRECTION PREFERENCE. The successor rule is unchanged --
	// cable first, then pass-through, from either end. This says which side of
	// the row the walk came in on, which is what decides whether `position`
	// describes the step just taken or the step somebody else would take.
	fromRear bool
}

type cableEnd struct {
	other  string
	medium string
	length *int
}

type ifaceInfo struct {
	name      string
	assetID   string
	assetName string
	assetKind string
}

// loadPlant reads every live cable, pass-through and interface.
//
// One read for the lot rather than a query per hop. A trace is a handful of
// steps but the page draws several at once, and an estate's whole cable plant is
// thousands of rows, not millions.
func (s *SQLStore) loadPlant(ctx context.Context) (*plant, error) {
	p := &plant{
		cable:   map[string]cableEnd{},
		through: map[string][]passThroughEnd{},
		iface:   map[string]ifaceInfo{},
	}

	var ifaces []struct {
		ID        string `db:"id"`
		Name      string `db:"name"`
		AssetID   string `db:"asset_id"`
		AssetName string `db:"asset_name"`
		AssetKind string `db:"asset_kind"`
	}
	err := s.read(ctx, &ifaces, `
		SELECT i.id, i.name, i.asset_id, a.name AS asset_name, a.kind AS asset_kind
		FROM interface i
		JOIN asset a ON a.id = i.asset_id
		WHERE a.lifecycle <> ?`, domain.LifecycleRetired)
	if err != nil {
		return nil, fmt.Errorf("reading interfaces: %w", err)
	}
	for _, i := range ifaces {
		p.iface[i.ID] = ifaceInfo{i.Name, i.AssetID, i.AssetName, i.AssetKind}
	}

	var links []struct {
		A      string  `db:"a_interface_id"`
		B      string  `db:"b_interface_id"`
		Medium *string `db:"medium"`
		Length *int    `db:"length_m"`
	}
	if err := s.read(ctx, &links, `
		SELECT a_interface_id, b_interface_id, medium, length_m
		FROM link WHERE lifecycle <> ?`, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("reading cables: %w", err)
	}
	for _, l := range links {
		medium := ""
		if l.Medium != nil {
			medium = *l.Medium
		}
		p.cable[l.A] = cableEnd{other: l.B, medium: medium, length: l.Length}
		p.cable[l.B] = cableEnd{other: l.A, medium: medium, length: l.Length}
	}

	var patches []struct {
		Front    string `db:"front_interface_id"`
		Rear     string `db:"rear_interface_id"`
		Position int    `db:"position"`
	}
	// ORDERED IN SQL, NOT IN GO. The rows are appended in the order they come
	// back, so a globally position-ordered result set gives every per-rear-port
	// slice its strands in position order for free. front_interface_id breaks
	// the tie so two engines cannot disagree about two strands recorded at the
	// same position on different rear ports -- both plain columns, no dialect
	// feature involved.
	if err := s.read(ctx, &patches, `
		SELECT front_interface_id, rear_interface_id, position
		FROM port_pass_through WHERE lifecycle <> ?
		ORDER BY position, front_interface_id`, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("reading pass-throughs: %w", err)
	}
	for _, t := range patches {
		p.through[t.Front] = append(p.through[t.Front],
			passThroughEnd{other: t.Rear, position: t.Position})
		p.through[t.Rear] = append(p.through[t.Rear],
			passThroughEnd{other: t.Front, position: t.Position, fromRear: true})
	}
	return p, nil
}

// TracePath follows a cable from one interface to wherever it ends.
func (s *SQLStore) TracePath(ctx context.Context, interfaceID string) (*Trace, error) {
	p, err := s.loadPlant(ctx)
	if err != nil {
		return nil, err
	}
	start, ok := p.iface[interfaceID]
	if !ok {
		return nil, fmt.Errorf("tracing %s: %w", interfaceID, domain.ErrNotFound)
	}
	return p.trace(interfaceID, start), nil
}

// trace walks a plant that is already loaded.
//
// SPLIT FROM THE QUERY DELIBERATELY. The interesting part of this file is the
// two bounds, and the fixture that exercises the second one is more
// pass-throughs than the entire seed estate has. Inserting five hundred rows
// through CreatePassThrough, twice, once per engine, is minutes of suite time
// for a bound that never touches a database -- and this suite has already
// failed a release tag on Go's ten-minute timeout. The plant is the real
// structure the walk runs on, so a test that builds one is a fixture, not a
// mock: loadPlant's only job is to fill it, and that job has its own
// dual-engine test.
func (p *plant) trace(startID string, start ifaceInfo) *Trace {
	t := &Trace{
		StartAssetID: start.assetID, StartAsset: start.assetName,
		StartInterface: start.name,
		Root:           &TraceNode{},
	}

	// TWO BOUNDS, and both are load-bearing.
	//
	// `visited` does more than its name suggests, which mutation testing made
	// obvious: without it the walk does not merely permit loops, it bounces
	// straight back down the cable it arrived on, because that cable is still
	// there and nothing says it has been used. It is what makes the walk
	// directional at all, and only secondarily what stops two panels patched
	// into each other from running for ever.
	//
	// IT IS NOW PER BRANCH: the interfaces on the path from the ROOT to the
	// node being expanded, maintained by adding before a recursion and removing
	// after it. For a path, "already seen anywhere in this walk" and "already
	// seen on the way here" are the same set. FOR A TREE THEY ARE NOT. Two
	// strands of one trunk legitimately reach the same second panel; a global
	// set would let strand 1 consume it and strand 7 would stop one hop short
	// WITH NO ERROR, reporting a run that ends at a front port and looks
	// exactly like a genuinely unpatched one. Per-branch is also the correct
	// definition of a cycle, which the path case only got right by coincidence
	// of having one branch -- and it preserves the directional property above,
	// because the parent is still in the set while its child is expanded.
	//
	// The hop count is the second bound, for a chain that is merely absurd
	// rather than circular -- what a plant being edited looks like halfway
	// through -- and traceNodeBudget is the third, for a plant that is neither
	// long nor circular but simply fans out faster than anyone can read.
	visited := map[string]bool{startID: true}
	b := &budget{left: traceNodeBudget}
	b.spend(1) // the root is a node too
	p.expand(t.Root, startID, "", visited, 0, b)
	return t
}

// expand grows one node into its continuations.
//
// previous is the interface this node was reached from, kept apart from
// `visited` because the two answer different questions. At the far end of any
// run the only cable is the one we arrived on -- treating that as a cycle
// reported every complete path as a loop, which is what the first version did.
func (p *plant) expand(n *TraceNode, current, previous string, visited map[string]bool, depth int, b *budget) {
	if depth >= traceHopLimit {
		n.leaf(OutcomeHopLimit, fmt.Sprintf("stopped after %d hops. A path this long is almost "+
			"certainly a mis-patch rather than a real run.", traceHopLimit))
		return
	}
	if b.spent() {
		n.leaf(OutcomeNodeBudget, fmt.Sprintf("stopped here: the trace has already reached %d "+
			"steps in total. A plant that fans out this far is being edited or mis-patched "+
			"rather than read.", traceNodeBudget))
		return
	}

	// A cable first: leaving the box is the interesting move. UNCHANGED, and
	// deliberately so -- the successor rule did not move. A trace starting at a
	// rear port with a trunk plugged into it follows that trunk, exactly as it
	// did before breakout existed. The only step that branches is the next one.
	if end, ok := p.cable[current]; ok && !visited[end.other] {
		info, known := p.iface[end.other]
		if !known {
			n.leaf(OutcomeUnknown, "the far end of a cable is on an asset that has been retired")
			return
		}
		b.spend(1)
		child := &TraceNode{Hop: TraceHop{
			Kind: HopCable, AssetID: info.assetID, AssetName: info.assetName,
			AssetKind: info.assetKind, InterfaceID: end.other, Interface: info.name,
			Medium: end.medium, Length: end.length,
		}}
		n.Children = append(n.Children, child)
		visited[end.other] = true
		p.expand(child, end.other, current, visited, depth+1, b)
		delete(visited, end.other) // the branch is done with it; a sibling is not
		return
	}

	// Then through the panel we just arrived at -- EVERY strand of it. This is
	// the one line of the successor rule that changed.
	var strands []passThroughEnd
	for _, e := range p.through[current] {
		if !visited[e.other] {
			strands = append(strands, e)
		}
	}
	if len(strands) > 0 {
		b.spend(len(strands))
		for _, e := range strands {
			child := &TraceNode{}
			// Position labels the far side of a BREAKOUT: the parent was the
			// rear port and this node is one of the front ports behind it. Going
			// the other way the position describes a step somebody else would
			// take, not this one, so it stays 0. The value is whatever was
			// declared -- a lone strand recorded at position 7 reports 7 (D5).
			if e.fromRear {
				child.Position = e.position
			}
			n.Children = append(n.Children, child)

			info, known := p.iface[e.other]
			if !known {
				// The strand is real -- it has a row -- but its far side is on a
				// retired asset or a port that is gone. IT STILL GETS A NODE:
				// dropping it would leave a trunk short one strand with nothing
				// saying so, which is precisely the silent truncation this whole
				// design is arranged to prevent. The interface id is all there is
				// to name it by; the row that carried a name has gone.
				child.Hop = TraceHop{Kind: HopPanel, InterfaceID: e.other}
				child.leaf(OutcomeUnknown, "a pass-through lands on an interface that no longer exists")
				continue
			}
			child.Hop = TraceHop{
				Kind: HopPanel, AssetID: info.assetID, AssetName: info.assetName,
				AssetKind: info.assetKind, InterfaceID: e.other, Interface: info.name,
			}
			visited[e.other] = true
			p.expand(child, e.other, current, visited, depth+1, b)
			delete(visited, e.other)
		}
		return
	}

	// Nothing further. Which of the several ways that can happen is the answer,
	// so it is spelled out rather than left as an empty list. A continuation
	// that is somewhere this BRANCH has already been, and is not simply the way
	// it came, is a genuine loop: something is patched into its own run.
	looped := false
	if end, ok := p.cable[current]; ok && end.other != previous && visited[end.other] {
		looped = true
	}
	for _, e := range p.through[current] {
		if e.other != previous && visited[e.other] {
			looped = true
		}
	}
	switch {
	case depth == 0 && !looped:
		n.leaf(OutcomeUnpatched, "nothing is plugged into this port")
	case looped:
		n.leaf(OutcomeLooped, "the path loops back on itself — something is patched into its own run")
	default:
		n.leaf(OutcomeComplete, "the path ends here")
	}
}

// Leaves is every end of the run, left to right: position order within a
// breakout, and depth first through nested ones.
func (t *Trace) Leaves() []*TraceNode {
	var out []*TraceNode
	var walk func(*TraceNode)
	walk = func(n *TraceNode) {
		if len(n.Children) == 0 {
			out = append(out, n)
			return
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	if t.Root != nil {
		walk(t.Root)
	}
	return out
}

// Nodes counts every step in the tree, the start port included. It is what the
// node budget bounds, and what a test asserts against it.
func (t *Trace) Nodes() int {
	n := 0
	var walk func(*TraceNode)
	walk = func(node *TraceNode) {
		n++
		for _, c := range node.Children {
			walk(c)
		}
	}
	if t.Root != nil {
		walk(t.Root)
	}
	return n
}

// TraceCounts is what a whole trace reports. COUNTS, NEVER A VERDICT: with
// three strands patched and one looping, neither "complete" nor "incomplete"
// is true of the trace, and a summary bool over a tree is exactly the figure
// that looks more certain than it is (D3).
//
// It counts the strands that HAVE ROWS and nothing else. Nothing anywhere
// records how many positions a rear port physically has, so this must never
// grow a "free" or "total" field: "nine are free" is a claim about a trunk
// nobody described (D4 as corrected 2026-09-05).
type TraceCounts struct {
	Strands int // leaves in total -- ends of runs, not positions on a trunk
	Ends    int // leaves that reached a far end
	Loops   int
	Stopped int // hop limit or node budget
	Unknown int
}

func (t *Trace) Counts() TraceCounts {
	var c TraceCounts
	for _, leaf := range t.Leaves() {
		c.Strands++
		switch leaf.Outcome {
		case OutcomeComplete, OutcomeUnpatched:
			c.Ends++
		case OutcomeLooped:
			c.Loops++
		case OutcomeHopLimit, OutcomeNodeBudget:
			c.Stopped++
		case OutcomeUnknown:
			c.Unknown++
		}
	}
	return c
}

// Chain flattens a run with no breakout in it into the flat hop list this
// tracer returned before WP-B4, and reports false the moment any node has more
// than one continuation.
//
// TWO CALLERS AND NO OTHERS: the tests that pin the 1:1 case against the exact
// list it produced before the type changed, and any future reader who needs
// "the path" and must be TOLD when there isn't one. It is deliberately not the
// page's route -- a helper that quietly returns the first branch of a tree is
// the "one function, two shapes" API D1 rejected.
func (t *Trace) Chain() ([]TraceHop, bool) {
	if t.Root == nil {
		return nil, false
	}
	var hops []TraceHop
	for n := t.Root; len(n.Children) > 0; {
		if len(n.Children) > 1 {
			return nil, false
		}
		n = n.Children[0]
		hops = append(hops, n.Hop)
	}
	return hops, true
}

// TraceRow is one line of the rendered trace.
type TraceRow struct {
	// Step is the depth from the start port. 0 is the port itself, which
	// reproduces the numbering the page had before the result became a tree.
	Step int
	Node *TraceNode
	// Strand says whether to label this row with its position.
	//
	// THE DATA IS ALWAYS HONEST AND THE LABEL IS NOT ALWAYS USEFUL. Node.
	// Position carries whatever was declared, including the 1 on every
	// ordinary 1:1 panel, because that is what the row says (D5). Printing
	// "strand 1" on every panel hop in the estate would be noise that implies a
	// breakout where there is none. So: label it when the parent actually
	// branched, or when the position is not 1 -- a lone strand recorded at
	// position 7 is worth saying out loud even though nothing branched.
	Strand bool
}

// Rows flattens the tree for rendering, depth first and in position order.
//
// FLATTENED IN GO, NOT IN THE TEMPLATE. html/template can recurse, but the
// alternative is a template computing its own depth and branch labels, which
// is business logic in a template and is untestable without asserting on
// markup -- which this work package explicitly does not do. A run with no
// breakout in it flattens to exactly the numbered rows the page had before.
func (t *Trace) Rows() []TraceRow {
	var out []TraceRow
	if t.Root == nil {
		return out
	}
	out = append(out, TraceRow{Step: 0, Node: t.Root})
	var walk func(n *TraceNode, step int)
	walk = func(n *TraceNode, step int) {
		branched := len(n.Children) > 1
		for _, c := range n.Children {
			out = append(out, TraceRow{
				Step:   step,
				Node:   c,
				Strand: branched || c.Position != 0 && c.Position != 1,
			})
			walk(c, step+1)
		}
	}
	walk(t.Root, 1)
	return out
}

// ---------- patching a panel ----------

// PassThroughRow is one front-to-rear mapping, with both ports named.
type PassThroughRow struct {
	domain.PassThrough
	FrontName string `db:"front_name"`
	RearName  string `db:"rear_name"`
}

// panelPatchSelect is named for what it reads rather than as
// passThroughSelect, which gosec flags as a possible hardcoded credential --
// its heuristic matches the substring "pass". Renaming is better than
// suppressing: a nolint here would be a permanent instruction to ignore a rule
// that is right far more often than it is wrong.
const panelPatchSelect = `
	SELECT p.*, f.name AS front_name, r.name AS rear_name
	FROM port_pass_through p
	JOIN interface f ON f.id = p.front_interface_id
	JOIN interface r ON r.id = p.rear_interface_id`

// PassThroughsFor lists the live patches inside one asset.
//
// GROUPED BY REAR PORT, THEN BY POSITION. It ordered by front-port name until
// breakout arrived, which puts strand 10 before strand 2 the moment positions
// mean anything -- and interleaves two trunks besides. This view is where
// somebody stands in front of the panel and reads the list off against the
// physical trunk, so the trunk has to be contiguous and its strands in order.
// (r.name is still text-sorted, so rear-10 precedes rear-2. A panel has a
// handful of rear ports and hundreds of strands; fixing text-sorted port names
// is a separate problem from this one.)
func (s *SQLStore) PassThroughsFor(ctx context.Context, assetID string) ([]PassThroughRow, error) {
	var rows []PassThroughRow
	err := s.read(ctx, &rows, panelPatchSelect+
		` WHERE f.asset_id = ? AND p.lifecycle <> ?
		  ORDER BY r.name, p.position, f.name`,
		assetID, domain.LifecycleRetired)
	if err != nil {
		return nil, fmt.Errorf("listing pass-throughs for %s: %w", assetID, err)
	}
	return rows, nil
}

// CreatePassThrough records what a panel does between two of its own ports.
func (s *SQLStore) CreatePassThrough(ctx context.Context, permit domain.Permit, p *domain.PassThrough) error {
	p.RowVersion = 1
	if err := p.Validate(); err != nil {
		return err
	}
	return s.write(ctx, permit, func(t *tx) error {
		// BOTH ENDS ON ONE ASSET. A pass-through is what happens inside a panel;
		// two ports on different boxes are joined by a cable, and letting this
		// table express that would give the tracer two ways to cross a gap that
		// only one of them is true for.
		var assets []string
		if err := t.selectAll(ctx, &assets,
			`SELECT asset_id FROM interface WHERE id IN (?, ?)`,
			p.FrontInterfaceID, p.RearInterfaceID); err != nil {
			return fmt.Errorf("reading the ports: %w", err)
		}
		if len(assets) != 2 {
			ve := &domain.ValidationError{}
			ve.Add("rear_interface_id", "choose two ports that exist")
			return ve
		}
		if assets[0] != assets[1] {
			ve := &domain.ValidationError{}
			ve.Add("rear_interface_id", "both ports must be on the same panel — "+
				"two boxes are joined by a cable, not by a pass-through")
			return ve
		}

		_, err := t.exec(ctx, `
			INSERT INTO port_pass_through (id, front_interface_id, rear_interface_id,
			                               position, lifecycle, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			p.ID, p.FrontInterfaceID, p.RearInterfaceID, p.Position,
			p.Lifecycle, p.CreatedAt, p.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "recording the pass-through")
		}
		return t.logCreate(ctx, "port_pass_through", p.ID, p)
	})
}

// RetirePassThrough unpatches a port.
func (s *SQLStore) RetirePassThrough(ctx context.Context, p domain.Permit, id string) error {
	var before domain.PassThrough
	if err := s.readOne(ctx, &before,
		`SELECT * FROM port_pass_through WHERE id = ?`, id); err != nil {
		return fmt.Errorf("getting pass-through %s: %w", id, err)
	}
	at := domain.FormatTime(s.now())
	return s.write(ctx, p, func(t *tx) error {
		_, err := t.exec(ctx,
			`UPDATE port_pass_through SET lifecycle = ?, updated_at = ?,
			                              row_version = row_version + 1
			 WHERE id = ?`, domain.LifecycleRetired, at, id)
		if err != nil {
			return translateWriteErr(err, "retiring the pass-through")
		}
		after := before
		after.Lifecycle, after.UpdatedAt = domain.LifecycleRetired, at
		return t.logUpdate(ctx, "port_pass_through", id, &before, &after)
	})
}
