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
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// breakoutPlant is spec §2.3: one trunk, two strands, both legitimately
// reaching the same second panel.
//
//	                    ┌─ pos 1 → panel-b/f-1 ─┐
//	sw-1/eth1 ═ panel-a/rear-1                   ├─ panel-b/rear-1 ═ core-1/eth1
//	                    └─ pos 7 → panel-b/f-7 ─┘
//
// IT IS THE FIXTURE A GLOBAL `visited` SET FAILS ON, and it fails quietly:
// strand 1 marks panel-b/rear-1, and strand 7 then stops at panel-b/f-7 with
// no error at all -- reporting a run that ends at a front port, which looks
// exactly like a strand nobody patched onward.
func breakoutPlant(t *testing.T, s *SQLStore, ctx context.Context) (swPort, corePort string, ids map[string]string) {
	t.Helper()
	site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
	sw := mustAsset(t, s, ctx, domain.KindSwitch, "sw-1", &site)
	pa := mustAsset(t, s, ctx, domain.KindPatchPanel, "panel-a", &site)
	pb := mustAsset(t, s, ctx, domain.KindPatchPanel, "panel-b", &site)
	core := mustAsset(t, s, ctx, domain.KindSwitch, "core-1", &site)

	swPort = mustPort(t, s, ctx, sw, "eth1")
	aRear := mustPort(t, s, ctx, pa, "a-rear-1")
	aF1 := mustPort(t, s, ctx, pa, "a-f-1")
	aF7 := mustPort(t, s, ctx, pa, "a-f-7")
	bF1 := mustPort(t, s, ctx, pb, "b-f-1")
	bF7 := mustPort(t, s, ctx, pb, "b-f-7")
	bRear := mustPort(t, s, ctx, pb, "b-rear-1")
	corePort = mustPort(t, s, ctx, core, "eth1")

	// The trunk arrives on panel-a's rear port and breaks out to two of its
	// front ports. Positions 1 and 7, not 1 and 2: a gap is the ordinary case
	// and it is what stops anybody reading Position as an index (D5).
	mustCable(t, s, ctx, swPort, aRear)
	mustPatchAt(t, s, ctx, aF1, aRear, 1)
	mustPatchAt(t, s, ctx, aF7, aRear, 7)

	// Each strand runs to its own front port on panel-b, and panel-b's rear
	// port -- SHARED BY BOTH -- is cabled onward to the core.
	mustCable(t, s, ctx, aF1, bF1)
	mustCable(t, s, ctx, aF7, bF7)
	mustPatchAt(t, s, ctx, bF1, bRear, 1)
	mustPatchAt(t, s, ctx, bF7, bRear, 7)
	mustCable(t, s, ctx, bRear, corePort)

	return swPort, corePort, map[string]string{
		"a-rear-1": aRear, "a-f-1": aF1, "a-f-7": aF7,
		"b-f-1": bF1, "b-f-7": bF7, "b-rear-1": bRear,
	}
}

// TestBothStrandsOfATrunkReachTheFarEnd is the correctness property the whole
// design turns on (docs/panel-breakout-design.md §2.3): visited must be
// PER BRANCH, not global, or the second strand of a converging trunk reports a
// run one hop short with no error at all.
func TestBothStrandsOfATrunkReachTheFarEnd(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			swPort, corePort, ids := breakoutPlant(t, s, ctx)

			trace, err := s.TracePath(ctx, swPort)
			if err != nil {
				t.Fatalf("tracing: %v", err)
			}

			leaves := trace.Leaves()
			if len(leaves) != 2 {
				t.Fatalf("the trunk produced %d ends, want 2 -- one per recorded strand. "+
					"A tracer that follows one continuation per interface answers for one "+
					"fibre of the twelve in the trunk.", len(leaves))
			}
			for i, leaf := range leaves {
				if leaf.Hop.InterfaceID != corePort {
					t.Errorf("strand %d ends at %s/%s, want core-1/eth1. BOTH strands "+
						"legitimately reach panel-b's rear port: a `visited` set shared "+
						"across branches lets the first one consume it and stops the second "+
						"ONE HOP SHORT WITH NO ERROR, which is indistinguishable from a "+
						"strand nobody patched onward.",
						i, leaf.Hop.AssetName, leaf.Hop.Interface)
				}
				if leaf.Outcome != OutcomeComplete {
					t.Errorf("strand %d ended %q (%s), want %q", i, leaf.Outcome, leaf.Why, OutcomeComplete)
				}
			}

			// The strands are labelled with what was DECLARED, in that order.
			// 1 and 7, never 1 and 2: Position is the hole the fibre is in, not
			// an index into Children (D5).
			var branch *TraceNode
			for n := trace.Root; ; n = n.Children[0] {
				if len(n.Children) > 1 {
					branch = n
					break
				}
				if len(n.Children) == 0 {
					t.Fatal("no node in the trace has more than one continuation, so the " +
						"trunk was never followed as a breakout at all")
				}
			}
			if branch.Hop.InterfaceID != ids["a-rear-1"] {
				t.Errorf("the branch is at %s, want panel-a's rear port", branch.Hop.Interface)
			}
			if got := []int{branch.Children[0].Position, branch.Children[1].Position}; got[0] != 1 || got[1] != 7 {
				t.Errorf("the strands are positions %v, want [1 7] in that order", got)
			}
			if c := trace.Counts(); c.Strands != 2 || c.Ends != 2 || c.Loops != 0 {
				t.Errorf("counts = %+v, want 2 strands both reaching an end", c)
			}
		})
	}
}

// TestABreakoutYieldsOneLeafPerRecordedPosition covers §4's four-position case
// and D4-as-corrected together: what appears is what has rows.
func TestABreakoutYieldsOneLeafPerRecordedPosition(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			pa := mustAsset(t, s, ctx, domain.KindPatchPanel, "panel-a", &site)
			rear := mustPort(t, s, ctx, pa, "rear-1")

			// THREE STRANDS OF A TWELVE-FIBRE TRUNK, recorded at 1, 5 and 12.
			// The first draft of the design wanted twelve leaves here, nine of
			// them saying "nothing patched". That is unbuildable and the
			// challenge round found it: port_pass_through holds a row per
			// PATCHED position and nothing anywhere records how many positions a
			// rear port physically has. So the nine free strands are not merely
			// unqueried -- the database does not know they exist, and reporting
			// them would be a claim about a trunk nobody described (D4).
			want := []int{1, 5, 12}
			for _, pos := range want {
				front := mustPort(t, s, ctx, pa, fmt.Sprintf("f-%02d", pos))
				mustPatchAt(t, s, ctx, front, rear, pos)
			}

			trace, err := s.TracePath(ctx, rear)
			if err != nil {
				t.Fatalf("tracing: %v", err)
			}
			leaves := trace.Leaves()
			if len(leaves) != len(want) {
				t.Fatalf("the trunk produced %d ends, want %d -- one per RECORDED position "+
					"and not one per hole in a trunk nobody described", len(leaves), len(want))
			}
			for i, leaf := range leaves {
				if leaf.Position != want[i] {
					t.Errorf("end %d is position %d, want %d -- in position order, and "+
						"position 12 is not renumbered to 3 because 2..4 have no rows (D5)",
						i, leaf.Position, want[i])
				}
			}
			if c := trace.Counts(); c.Strands != 3 {
				t.Errorf("counts = %+v, want 3 strands. The trace says how many it FOUND; "+
					"it must never imply how many the trunk has.", c)
			}
		})
	}
}

// TestALoopingStrandDoesNotTruncateItsSiblings covers the other failure shape a
// shared visited set would introduce: one strand loops, but that must not take
// its unrelated sibling with it.
func TestALoopingStrandDoesNotTruncateItsSiblings(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			pa := mustAsset(t, s, ctx, domain.KindPatchPanel, "panel-a", &site)
			pb := mustAsset(t, s, ctx, domain.KindPatchPanel, "panel-b", &site)
			pc := mustAsset(t, s, ctx, domain.KindPatchPanel, "panel-c", &site)
			srv := mustAsset(t, s, ctx, domain.KindServer, "srv-1", &site)

			aRear := mustPort(t, s, ctx, pa, "a-rear-1")
			aF1 := mustPort(t, s, ctx, pa, "a-f-1")
			aF2 := mustPort(t, s, ctx, pa, "a-f-2")
			mustPatchAt(t, s, ctx, aF1, aRear, 1)
			mustPatchAt(t, s, ctx, aF2, aRear, 2)

			// Strand 1: an ordinary, complete run.
			srvPort := mustPort(t, s, ctx, srv, "eth0")
			mustCable(t, s, ctx, aF1, srvPort)

			// Strand 2 into a mis-patch: panel-b's rear port itself breaks out
			// two ways, and the second way comes back to it through panel-c.
			bRear := mustPort(t, s, ctx, pb, "b-rear-1")
			bF1 := mustPort(t, s, ctx, pb, "b-f-1")
			bF2 := mustPort(t, s, ctx, pb, "b-f-2")
			cRear := mustPort(t, s, ctx, pc, "c-rear-1")
			cF1 := mustPort(t, s, ctx, pc, "c-f-1")
			mustPatchAt(t, s, ctx, bF1, bRear, 1)
			mustPatchAt(t, s, ctx, bF2, bRear, 2)
			mustPatchAt(t, s, ctx, cF1, cRear, 1)
			mustCable(t, s, ctx, aF2, bF2)
			mustCable(t, s, ctx, bRear, cRear)
			mustCable(t, s, ctx, cF1, bF1) // closes it: back to panel-b's rear

			done := make(chan *Trace, 1)
			go func() {
				trace, err := s.TracePath(ctx, aRear)
				if err != nil {
					done <- nil
					return
				}
				done <- trace
			}()
			select {
			case trace := <-done:
				if trace == nil {
					t.Fatal("tracing a plant with one looping strand errored")
				}
				var reachedServer, looped int
				for _, leaf := range trace.Leaves() {
					if leaf.Hop.InterfaceID == srvPort && leaf.Outcome == OutcomeComplete {
						reachedServer++
					}
					if leaf.Outcome == OutcomeLooped {
						looped++
					}
				}
				if reachedServer != 1 {
					t.Errorf("the clean strand reached the server %d times, want once. A "+
						"branch that gives up must not take its siblings with it.", reachedServer)
				}
				if looped == 0 {
					t.Error("no branch reported a loop, so the mis-patched strand either ran " +
						"out of hops or was silently dropped -- both are the wrong answer here")
				}
				if trace.Nodes() > traceNodeBudget+len(trace.Root.Children) {
					t.Errorf("the tree has %d nodes; a loop is not supposed to grow one", trace.Nodes())
				}
			case <-timeoutAfter():
				t.Fatal("tracing a plant with one looping strand did not terminate")
			}
		})
	}
}

// TestTracingUpFromOneFrontPortIsASingleChain is the asymmetry in §2.2: down
// from the trunk there are many answers, up from one strand there is one.
//
// It also pins the Position rule from the other side. Going front -> rear the
// position describes a step somebody ELSE would take, not this one, so the hop
// carries 0 -- while the row itself says 7.
func TestTracingUpFromOneFrontPortIsASingleChain(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			pa := mustAsset(t, s, ctx, domain.KindPatchPanel, "panel-a", &site)
			sw := mustAsset(t, s, ctx, domain.KindSwitch, "sw-1", &site)

			rear := mustPort(t, s, ctx, pa, "a-rear-1")
			front := mustPort(t, s, ctx, pa, "a-f-7")
			swPort := mustPort(t, s, ctx, sw, "eth1")
			mustPatchAt(t, s, ctx, front, rear, 7)
			mustCable(t, s, ctx, rear, swPort)

			trace, err := s.TracePath(ctx, front)
			if err != nil {
				t.Fatalf("tracing: %v", err)
			}
			hops, single := trace.Chain()
			if !single {
				t.Fatalf("tracing up from one strand branched; it has one answer")
			}
			if len(hops) != 2 || hops[0].InterfaceID != rear || hops[1].InterfaceID != swPort {
				t.Fatalf("the chain is %+v, want the rear port then the switch", hops)
			}
			for _, n := range []*TraceNode{trace.Root.Children[0]} {
				if n.Position != 0 {
					t.Errorf("the front -> rear hop is labelled strand %d. Position labels "+
						"the FAR SIDE of a breakout; going this way it names a step this run "+
						"did not take.", n.Position)
				}
			}
			if leaves := trace.Leaves(); len(leaves) != 1 || leaves[0].Outcome != OutcomeComplete {
				t.Errorf("leaves = %+v, want one complete end", leaves)
			}
		})
	}
}

// TestAFanOutPastTheNodeBudgetSaysSoOnEveryBranchItStopped is why Task 2 split
// the walk from the query: this fixture is 520 pass-throughs, and inserting
// them through CreatePassThrough once per engine would cost the suite minutes
// for a bound that never touches a database.
func TestAFanOutPastTheNodeBudgetSaysSoOnEveryBranchItStopped(t *testing.T) {
	tp := newTestPlant()
	rear := tp.port("panel-a", "patch_panel", "rear-1")
	width := traceNodeBudget + 8
	for i := 1; i <= width; i++ {
		front := tp.port("panel-a", "patch_panel", fmt.Sprintf("f-%03d", i))
		tp.patch(front, rear, i)
	}

	trace := tp.trace(t, rear)

	// NOT ONE STRAND FEWER. A budget that dropped successors would report a
	// SHORTER trunk with no error, which is the failure shape this design
	// exists to prevent; what the budget refuses is EXPANSION, not existence.
	if got := len(trace.Root.Children); got != width {
		t.Fatalf("the fan-out produced %d strands, want %d -- every strand with a row "+
			"appears, and the ones that could not be followed say so", got, width)
	}
	var stopped int
	for _, leaf := range trace.Leaves() {
		if leaf.Outcome == OutcomeNodeBudget {
			stopped++
			if leaf.Why == "" {
				t.Error("a branch stopped on the budget and said nothing. \"The path ends " +
					"here\" and \"we gave up\" are different answers.")
			}
		}
	}
	if stopped < 2 {
		t.Errorf("%d branches reported the budget, want it said PER BRANCH -- a single "+
			"summary would leave every other strand looking like a complete run", stopped)
	}
	// Bounded: the budget, plus at most the fan-out of the one node that
	// crossed zero. Depth-first expansion checks before each node, so only one
	// can overshoot.
	if max := traceNodeBudget + width; trace.Nodes() > max {
		t.Errorf("the tree has %d nodes, past the bound of %d", trace.Nodes(), max)
	}
}

// TestRowsLabelAStrandWhenAStrandIsWorthLabelling pins Rows' one rendering
// contract: a 1:1 chain numbers 0,1,2,... with no strand label, a breakout
// labels every branch, and a lone strand recorded off position 1 is worth
// saying even though nothing branched.
func TestRowsLabelAStrandWhenAStrandIsWorthLabelling(t *testing.T) {
	t.Run("a 1:1 chain has no strand label and numbers 0,1,2,...", func(t *testing.T) {
		tp := newTestPlant()
		sw := tp.port("sw-1", "switch", "eth1")
		aF := tp.port("panel-a", "patch_panel", "a-front-1")
		aR := tp.port("panel-a", "patch_panel", "a-rear-1")
		srv := tp.port("srv-1", "server", "eth0")
		tp.cable(sw, aF)
		tp.patch(aF, aR, 1)
		tp.cable(aR, srv)

		trace := tp.trace(t, sw)
		rows := trace.Rows()
		if len(rows) != 4 {
			t.Fatalf("got %d rows, want 4 (start + 3 hops): %+v", len(rows), rows)
		}
		for i, r := range rows {
			if r.Step != i {
				t.Errorf("row %d has Step %d, want %d", i, r.Step, i)
			}
			if r.Strand {
				t.Errorf("row %d is labelled a strand on a 1:1 run with no breakout", i)
			}
		}
	})

	t.Run("a two-way breakout labels both branches", func(t *testing.T) {
		tp := newTestPlant()
		rear := tp.port("panel-a", "patch_panel", "rear-1")
		f1 := tp.port("panel-a", "patch_panel", "f-1")
		f2 := tp.port("panel-a", "patch_panel", "f-2")
		tp.patch(f1, rear, 1)
		tp.patch(f2, rear, 2)

		trace := tp.trace(t, rear)
		rows := trace.Rows()
		if len(rows) != 3 {
			t.Fatalf("got %d rows, want 3 (start + 2 branches): %+v", len(rows), rows)
		}
		for _, r := range rows[1:] {
			if !r.Strand {
				t.Errorf("row %+v is part of a breakout and is not labelled a strand", r)
			}
		}
	})

	t.Run("a lone strand recorded at position 7 is labelled even though nothing branched", func(t *testing.T) {
		tp := newTestPlant()
		rear := tp.port("panel-a", "patch_panel", "rear-1")
		f7 := tp.port("panel-a", "patch_panel", "f-7")
		tp.patch(f7, rear, 7)

		trace := tp.trace(t, rear)
		rows := trace.Rows()
		if len(rows) != 2 {
			t.Fatalf("got %d rows, want 2 (start + 1 hop): %+v", len(rows), rows)
		}
		if !rows[1].Strand {
			t.Error("a lone strand at position 7 is not labelled; a reader would assume " +
				"an ordinary 1:1 pass-through")
		}
	})
}
