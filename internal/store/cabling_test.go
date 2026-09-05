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
	"strings"
	"testing"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Tracing a run through the panels in the way.
//
// The estate every test here builds is the ordinary structured-cabling shape:
//
//	switch ── panel-a(front) ═ panel-a(rear) ── panel-b(rear) ═ panel-b(front) ── server
//
// Four cables and two pass-throughs. Anything that stops at the first cable
// answers "a patch panel", which is true and useless.

func mustPort(t *testing.T, s *SQLStore, ctx context.Context, assetID, name string) string {
	t.Helper()
	i, err := domain.NewInterface(NewID(), assetID, name, "rj45")
	if err != nil {
		t.Fatalf("building port %s: %v", name, err)
	}
	if err := s.CreateInterface(ctx, testPermit, i); err != nil {
		t.Fatalf("creating port %s: %v", name, err)
	}
	return i.ID
}

func mustCable(t *testing.T, s *SQLStore, ctx context.Context, a, b string) string {
	t.Helper()
	l, err := domain.NewLink(NewID(), a, b)
	if err != nil {
		t.Fatalf("building cable: %v", err)
	}
	if err := s.CreateLink(ctx, testPermit, l); err != nil {
		t.Fatalf("creating cable: %v", err)
	}
	return l.ID
}

// mustPatchAt records one strand: a front port at a declared position on a
// rear port. mustPatch is the 1:1 case and is exactly this at position 1.
func mustPatchAt(t *testing.T, s *SQLStore, ctx context.Context, front, rear string, position int) string {
	t.Helper()
	p, err := domain.NewPassThrough(NewID(), domain.PassThroughSpec{
		FrontInterfaceID: front, RearInterfaceID: rear, Position: position,
	}, s.Now())
	if err != nil {
		t.Fatalf("building pass-through at position %d: %v", position, err)
	}
	if err := s.CreatePassThrough(ctx, testPermit, p); err != nil {
		t.Fatalf("creating pass-through at position %d: %v", position, err)
	}
	return p.ID
}

func mustPatch(t *testing.T, s *SQLStore, ctx context.Context, front, rear string) string {
	t.Helper()
	// Position 1 explicitly rather than relying on NewPassThrough's 0 -> 1
	// default: the default is a domain rule with its own test, and a fixture
	// that leans on it tests two things at once.
	return mustPatchAt(t, s, ctx, front, rear, 1)
}

// cablePlant builds the estate above and returns the two end ports.
func cablePlant(t *testing.T, s *SQLStore, ctx context.Context) (swPort, srvPort string) {
	t.Helper()
	site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
	sw := mustAsset(t, s, ctx, domain.KindSwitch, "sw-1", &site)
	pa := mustAsset(t, s, ctx, domain.KindPatchPanel, "panel-a", &site)
	pb := mustAsset(t, s, ctx, domain.KindPatchPanel, "panel-b", &site)
	srv := mustAsset(t, s, ctx, domain.KindServer, "srv-1", &site)

	swPort = mustPort(t, s, ctx, sw, "eth1")
	aFront := mustPort(t, s, ctx, pa, "a-front-1")
	aRear := mustPort(t, s, ctx, pa, "a-rear-1")
	bRear := mustPort(t, s, ctx, pb, "b-rear-1")
	bFront := mustPort(t, s, ctx, pb, "b-front-1")
	srvPort = mustPort(t, s, ctx, srv, "eth0")

	mustCable(t, s, ctx, swPort, aFront)
	mustPatch(t, s, ctx, aFront, aRear)
	mustCable(t, s, ctx, aRear, bRear) // the trunk
	mustPatch(t, s, ctx, bFront, bRear)
	mustCable(t, s, ctx, bFront, srvPort)
	return swPort, srvPort
}

// TestATraceCrossesThePanelsInTheWay is §4.4's structured-result requirement:
// a 1:1 run yields a single chain whose hops, order, kinds and reasons equal
// today's flat list exactly. Asserting the exact sequence is strictly
// stronger than the strings.Contains this test used before the tree, which
// was order-blind.
func TestATraceCrossesThePanelsInTheWay(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			swPort, srvPort := cablePlant(t, s, ctx)

			trace, err := s.TracePath(ctx, swPort)
			if err != nil {
				t.Fatalf("tracing: %v", err)
			}
			hops, single := trace.Chain()
			if !single {
				t.Fatalf("a 1:1 run branched. Every existing run must render as a single " +
					"chain: the tree is what CHANGED, not what a run through two ordinary " +
					"panels means.")
			}
			// EVERY HOP NAMED, IN ORDER. "It goes through panel-b, port
			// b-front-1" is actionable; "these two are connected" is not.
			want := []struct{ kind, where string }{
				{HopCable, "panel-a/a-front-1"},
				{HopPanel, "panel-a/a-rear-1"},
				{HopCable, "panel-b/b-rear-1"},
				{HopPanel, "panel-b/b-front-1"},
				{HopCable, "srv-1/eth0"},
			}
			if len(hops) != len(want) {
				t.Fatalf("the path has %d hops, want %d: %+v", len(hops), len(want), hops)
			}
			for i, w := range want {
				got := hops[i].AssetName + "/" + hops[i].Interface
				if got != w.where || hops[i].Kind != w.kind {
					t.Errorf("hop %d is %s (%s), want %s (%s)", i+1, got, hops[i].Kind, w.where, w.kind)
				}
			}
			// And it says which steps were cable and which were the panel.
			var cables, panels int
			for _, h := range hops {
				switch h.Kind {
				case HopCable:
					cables++
				case HopPanel:
					panels++
				}
			}
			if cables != 3 || panels != 2 {
				t.Errorf("path has %d cable hops and %d panel hops, want 3 and 2", cables, panels)
			}
			leaves := trace.Leaves()
			if len(leaves) != 1 || leaves[0].Hop.InterfaceID != srvPort {
				t.Fatalf("the path ended at %+v, want srv-1 eth0. A tracer that stops at "+
					"the first cable answers \"a patch panel\", which is true and useless.", leaves)
			}
			if leaves[0].Outcome != OutcomeComplete {
				t.Fatalf("the trace did not complete: %s", leaves[0].Why)
			}
		})
	}
}

func TestATraceRunsBothWays(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			swPort, srvPort := cablePlant(t, s, ctx)

			// The same run from the far end. A tracer that only walks one
			// direction is one somebody has to know the "right" end of.
			trace, err := s.TracePath(ctx, srvPort)
			if err != nil {
				t.Fatalf("tracing: %v", err)
			}
			hops, single := trace.Chain()
			if !single || len(hops) == 0 || hops[len(hops)-1].InterfaceID != swPort {
				t.Errorf("tracing from the server ended at %+v, want the switch", hops)
			}
		})
	}
}

// TestAMisPatchedPanelTerminatesRatherThanLooping is the test the plan asks for
// by name, and the reason the walk is bounded twice.
func TestAMisPatchedPanelTerminatesRatherThanLooping(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			pa := mustAsset(t, s, ctx, domain.KindPatchPanel, "panel-a", &site)
			pb := mustAsset(t, s, ctx, domain.KindPatchPanel, "panel-b", &site)

			// Two panels patched into each other, both ways: a real mis-patch,
			// and a cycle a naive walk follows for ever.
			aF := mustPort(t, s, ctx, pa, "a-front")
			aR := mustPort(t, s, ctx, pa, "a-rear")
			bF := mustPort(t, s, ctx, pb, "b-front")
			bR := mustPort(t, s, ctx, pb, "b-rear")
			mustPatch(t, s, ctx, aF, aR)
			mustPatch(t, s, ctx, bF, bR)
			mustCable(t, s, ctx, aR, bR)
			mustCable(t, s, ctx, bF, aF) // closes the loop

			done := make(chan *Trace, 1)
			go func() {
				trace, err := s.TracePath(ctx, aF)
				if err != nil {
					done <- nil
					return
				}
				done <- trace
			}()
			select {
			case trace := <-done:
				if trace == nil {
					t.Fatal("tracing a looped plant errored")
				}
				for _, leaf := range trace.Leaves() {
					if leaf.Outcome == OutcomeComplete {
						t.Error("a looped path reported a leaf as complete; it has no far end")
					}
					if leaf.Why == "" {
						t.Error("a leaf stopped without saying why. \"The path ends here\" " +
							"and \"we gave up\" are different answers.")
					}
				}
				hops, single := trace.Chain()
				if single && len(hops) > traceHopLimit {
					t.Errorf("walked %d hops, past the limit of %d", len(hops), traceHopLimit)
				}
				if trace.Nodes() > traceNodeBudget+1 {
					t.Errorf("the tree has %d nodes, past the node budget of %d", trace.Nodes(), traceNodeBudget)
				}
			case <-timeoutAfter():
				t.Fatal("tracing a looped plant did not terminate. A page that never " +
					"returns is worse than a wrong one: nobody can see what it was " +
					"going to say.")
			}
		})
	}
}

func TestAnUnpluggedPortSaysSoRatherThanReturningNothing(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			sw := mustAsset(t, s, ctx, domain.KindSwitch, "sw-1", &site)
			port := mustPort(t, s, ctx, sw, "eth99")

			trace, err := s.TracePath(ctx, port)
			if err != nil {
				t.Fatalf("tracing: %v", err)
			}
			// THIS IS THE TEST THAT PROVES THE ROOT'S OWN LEAF REASON SURVIVED
			// THE TREE -- the one thing the tree walk's own leaf() call on an
			// empty root could have quietly dropped.
			if len(trace.Root.Children) != 0 {
				t.Errorf("an unplugged port produced %d children", len(trace.Root.Children))
			}
			if !strings.Contains(trace.Root.Why, "nothing is plugged") {
				t.Errorf("why = %q, want it to say the port is empty. An empty list and "+
					"\"not cabled\" look identical to a reader otherwise.", trace.Root.Why)
			}
			if trace.Root.Outcome == OutcomeComplete {
				t.Error("an unplugged port reported a complete path")
			}
		})
	}
}

func TestAPassThroughMustStayInsideOnePanel(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			pa := mustAsset(t, s, ctx, domain.KindPatchPanel, "panel-a", &site)
			pb := mustAsset(t, s, ctx, domain.KindPatchPanel, "panel-b", &site)
			aF := mustPort(t, s, ctx, pa, "a-front")
			bR := mustPort(t, s, ctx, pb, "b-rear")

			p, err := domain.NewPassThrough(NewID(), domain.PassThroughSpec{
				FrontInterfaceID: aF, RearInterfaceID: bR,
			}, s.Now())
			if err != nil {
				t.Fatalf("building: %v", err)
			}
			err = s.CreatePassThrough(ctx, testPermit, p)
			if err == nil {
				t.Fatal("two ports on DIFFERENT boxes were joined by a pass-through. " +
					"That is what a cable is, and allowing both would give the tracer " +
					"two ways to cross a gap only one of them is true for.")
			}
			if msg := fieldError(err, "rear_interface_id"); msg == "" {
				t.Errorf("error = %v, want a field failure", err)
			}
		})
	}
}

func TestUnpatchingAPanelBreaksTheRun(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			swPort, srvPort := cablePlant(t, s, ctx)

			patches, err := s.PassThroughsFor(ctx, s.assetOf(t, ctx, "panel-a"))
			if err != nil || len(patches) != 1 {
				t.Fatalf("listing patches: %v (%d)", err, len(patches))
			}
			if err := s.RetirePassThrough(ctx, testPermit, patches[0].ID); err != nil {
				t.Fatalf("unpatching: %v", err)
			}

			trace, err := s.TracePath(ctx, swPort)
			if err != nil {
				t.Fatalf("tracing: %v", err)
			}
			// ANY LEAF, not "the" path: on a tree the honest question is
			// whether any branch still reaches the server, not whether a
			// single flattened chain does.
			for _, leaf := range trace.Leaves() {
				if leaf.Hop.InterfaceID == srvPort {
					t.Error("the run still reaches the server after the panel was unpatched; " +
						"a retired pass-through is one nobody can pass through")
				}
			}
		})
	}
}

// assetOf resolves a name for the tests above.
func (s *SQLStore) assetOf(t *testing.T, ctx context.Context, name string) string {
	t.Helper()
	rows, err := s.ListAssets(ctx, AssetFilter{Query: name})
	if err != nil || len(rows) == 0 {
		t.Fatalf("finding %s: %v", name, err)
	}
	return rows[0].ID
}

// timeoutAfter bounds the loop test, so a tracer that never returns fails the
// suite instead of hanging it.
func timeoutAfter() <-chan time.Time { return time.After(20 * time.Second) }

// TestThePlantHoldsEveryStrandOfABreakoutInPositionOrder pins what
// map[string]string could not hold.
func TestThePlantHoldsEveryStrandOfABreakoutInPositionOrder(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			pa := mustAsset(t, s, ctx, domain.KindPatchPanel, "panel-a", &site)
			rear := mustPort(t, s, ctx, pa, "rear-1")

			// Recorded out of order, with a gap, and named so that NAME order
			// and POSITION order disagree: f-10 sorts before f-2 as text.
			f10 := mustPort(t, s, ctx, pa, "f-10")
			f2 := mustPort(t, s, ctx, pa, "f-2")
			f7 := mustPort(t, s, ctx, pa, "f-7")
			mustPatchAt(t, s, ctx, f10, rear, 10)
			mustPatchAt(t, s, ctx, f2, rear, 2)
			mustPatchAt(t, s, ctx, f7, rear, 7)

			p, err := s.loadPlant(ctx)
			if err != nil {
				t.Fatalf("loading the plant: %v", err)
			}

			ends := p.through[rear]
			if len(ends) != 3 {
				t.Fatalf("the rear port holds %d strands, want 3. A map[string]string kept "+
					"whichever row came back last, so eleven fibres of a twelve-fibre trunk "+
					"were invisible to the tracer.", len(ends))
			}
			wantID := []string{f2, f7, f10}
			wantPos := []int{2, 7, 10}
			for i, got := range ends {
				if got.other != wantID[i] || got.position != wantPos[i] {
					t.Errorf("strand %d is %s at position %d, want %s at position %d -- "+
						"ordered by position, not by insertion and not by name",
						i, got.other, got.position, wantID[i], wantPos[i])
				}
				if !got.fromRear {
					t.Errorf("strand %d is filed under the REAR port and does not say so; "+
						"whether a hop is the far side of a breakout depends on it", i)
				}
			}
			// The front side is still one-to-one, and knows which side it is.
			for _, front := range wantID {
				got := p.through[front]
				if len(got) != 1 || got[0].other != rear || got[0].fromRear {
					t.Errorf("front port %s holds %+v, want exactly one entry pointing at "+
						"the rear and not flagged as the rear side", front, got)
				}
			}
		})
	}
}

// TestPatchesAreListedInStrandOrderNotNameOrder defeats the old ordering
// (front-port name) with a fixture built exactly to disagree with it: strand
// 10 is named so it sorts before strand 2 as text. Grouped by rear port then
// position is what lets somebody read a panel's own patching table off
// against the physical trunk it represents.
func TestPatchesAreListedInStrandOrderNotNameOrder(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			site := mustAsset(t, s, ctx, domain.KindSite, "dc-a", nil)
			pa := mustAsset(t, s, ctx, domain.KindPatchPanel, "panel-a", &site)
			rear := mustPort(t, s, ctx, pa, "rear-1")

			// Named so NAME order and POSITION order disagree: f-10 sorts
			// before f-2 as text.
			f10 := mustPort(t, s, ctx, pa, "f-10")
			f2 := mustPort(t, s, ctx, pa, "f-2")
			mustPatchAt(t, s, ctx, f10, rear, 10)
			mustPatchAt(t, s, ctx, f2, rear, 2)

			rows, err := s.PassThroughsFor(ctx, pa)
			if err != nil {
				t.Fatalf("listing pass-throughs: %v", err)
			}
			if len(rows) != 2 {
				t.Fatalf("got %d rows, want 2", len(rows))
			}
			if rows[0].Position != 2 || rows[1].Position != 10 {
				t.Errorf("positions came back %d, %d, want 2, 10 -- ordered by "+
					"position, not by front-port name which would put f-10 first",
					rows[0].Position, rows[1].Position)
			}
		})
	}
}
