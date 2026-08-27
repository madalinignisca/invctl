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

func mustPatch(t *testing.T, s *SQLStore, ctx context.Context, front, rear string) string {
	t.Helper()
	p, err := domain.NewPassThrough(NewID(), domain.PassThroughSpec{
		FrontInterfaceID: front, RearInterfaceID: rear,
	}, s.Now())
	if err != nil {
		t.Fatalf("building pass-through: %v", err)
	}
	if err := s.CreatePassThrough(ctx, testPermit, p); err != nil {
		t.Fatalf("creating pass-through: %v", err)
	}
	return p.ID
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

func TestATraceCrossesThePanelsInTheWay(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			swPort, srvPort := cablePlant(t, s, ctx)

			trace, err := s.TracePath(ctx, swPort)
			if err != nil {
				t.Fatalf("tracing: %v", err)
			}
			if !trace.Complete {
				t.Fatalf("the trace did not complete: %s", trace.Why)
			}
			end, ok := trace.End()
			if !ok || end.InterfaceID != srvPort {
				t.Fatalf("the path ended at %+v, want srv-1 eth0. A tracer that stops at "+
					"the first cable answers \"a patch panel\", which is true and useless.", end)
			}

			// EVERY HOP NAMED. "It goes through panel-b, port b-front-1" is
			// actionable; "these two are connected" is not.
			var names []string
			for _, h := range trace.Hops {
				names = append(names, h.AssetName+"/"+h.Interface)
			}
			joined := strings.Join(names, " → ")
			for _, want := range []string{"panel-a/a-front-1", "panel-a/a-rear-1",
				"panel-b/b-rear-1", "panel-b/b-front-1", "srv-1/eth0"} {
				if !strings.Contains(joined, want) {
					t.Errorf("the path %q does not name %q", joined, want)
				}
			}
			// And it says which steps were cable and which were the panel.
			var cables, panels int
			for _, h := range trace.Hops {
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
			end, ok := trace.End()
			if !ok || end.InterfaceID != swPort {
				t.Errorf("tracing from the server ended at %+v, want the switch", end)
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
				if trace.Complete {
					t.Error("a looped path reported as complete; it has no far end")
				}
				if trace.Why == "" {
					t.Error("the trace stopped without saying why. \"The path ends here\" " +
						"and \"we gave up\" are different answers.")
				}
				if len(trace.Hops) > traceHopLimit {
					t.Errorf("walked %d hops, past the limit of %d", len(trace.Hops), traceHopLimit)
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
			if len(trace.Hops) != 0 {
				t.Errorf("an unplugged port produced %d hops", len(trace.Hops))
			}
			if !strings.Contains(trace.Why, "nothing is plugged") {
				t.Errorf("why = %q, want it to say the port is empty. An empty list and "+
					"\"not cabled\" look identical to a reader otherwise.", trace.Why)
			}
			if trace.Complete {
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
			if end, ok := trace.End(); ok && end.InterfaceID == srvPort {
				t.Error("the run still reaches the server after the panel was unpatched; " +
					"a retired pass-through is one nobody can pass through")
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
