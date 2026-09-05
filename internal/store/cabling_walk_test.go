// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import "testing"

// testPlant builds a cable plant in memory, the way loadPlant would have.
//
// Every helper writes BOTH directions, exactly as loadPlant does, because a
// one-directional fixture would make the walk look directional when the whole
// point is that it is not.
type testPlant struct{ p *plant }

func newTestPlant() *testPlant {
	return &testPlant{&plant{
		cable:   map[string]cableEnd{},
		through: map[string][]passThroughEnd{},
		iface:   map[string]ifaceInfo{},
	}}
}

// port returns the id it filed, which is "asset/name" so a failure message
// reads like the rack rather than like a UUID.
func (tp *testPlant) port(asset, kind, name string) string {
	id := asset + "/" + name
	tp.p.iface[id] = ifaceInfo{name: name, assetID: asset, assetName: asset, assetKind: kind}
	return id
}

func (tp *testPlant) cable(a, b string) {
	tp.p.cable[a] = cableEnd{other: b}
	tp.p.cable[b] = cableEnd{other: a}
}

// patch records one strand. CALL IN POSITION ORDER: loadPlant's ORDER BY is
// what guarantees that in production and Task 1's dual-engine test is what
// pins it, so a fixture that shuffled here would be testing its own builder.
func (tp *testPlant) patch(front, rear string, position int) {
	tp.p.through[front] = append(tp.p.through[front],
		passThroughEnd{other: rear, position: position})
	tp.p.through[rear] = append(tp.p.through[rear],
		passThroughEnd{other: front, position: position, fromRear: true})
}

func (tp *testPlant) trace(t *testing.T, start string) *Trace {
	t.Helper()
	info, ok := tp.p.iface[start]
	if !ok {
		t.Fatalf("no such port in the fixture: %s", start)
	}
	return tp.p.trace(start, info)
}

// TestTheWalkRunsOnAPlantBuiltInMemory is the smallest possible proof that the
// in-memory fixture and the loaded one are the same structure. Everything in
// this file rests on it.
func TestTheWalkRunsOnAPlantBuiltInMemory(t *testing.T) {
	tp := newTestPlant()
	sw := tp.port("sw-1", "switch", "eth1")
	aF := tp.port("panel-a", "patch_panel", "a-front-1")
	aR := tp.port("panel-a", "patch_panel", "a-rear-1")
	srv := tp.port("srv-1", "server", "eth0")
	tp.cable(sw, aF)
	tp.patch(aF, aR, 1)
	tp.cable(aR, srv)

	trace := tp.trace(t, sw)
	hops, single := trace.Chain()
	if !single {
		t.Fatalf("a 1:1 fixture branched into more than one chain")
	}
	if len(hops) != 3 {
		t.Fatalf("got %d hops, want 3 (cable, panel, cable): %+v", len(hops), hops)
	}
	if hops[len(hops)-1].InterfaceID != srv {
		t.Fatalf("the path ended at %+v, want %s", hops[len(hops)-1], srv)
	}
	leaves := trace.Leaves()
	if len(leaves) != 1 || leaves[0].Outcome != OutcomeComplete {
		t.Fatalf("leaves = %+v, want one complete end", leaves)
	}
}

// TestALoopingStrandIsReportedEvenWhenASiblingIsFine is the regression for a
// silent drop this code shipped with, found by review rather than by any test
// here.
//
// The walk used to filter every already-visited strand out and then report a
// loop only if NOTHING was left to follow. So a rear port carrying one good
// strand and one mis-patched one reported the good strand and dropped the
// other completely: no node, no leaf, no outcome, absent from Leaves() and
// Counts(), and not charged to the node budget. A trunk one strand short with
// nothing anywhere saying so.
//
// THAT IS THE FAILURE THE WHOLE DESIGN IS ARRANGED AGAINST -- §2.3's
// per-branch visited set and the node budget both exist to stop a trace coming
// back quietly shorter than the plant -- and it arrived through the one path
// neither of them watched. It needs no exotic topology: any rear port with two
// recorded positions where one is mis-patched reaches it.
//
// TestALoopingStrandDoesNotTruncateItsSiblings covers the neighbouring case
// and cannot catch this one: there the looping strand is the ONLY entry left
// after filtering, so the old code fell through to the block that reports it.
func TestALoopingStrandIsReportedEvenWhenASiblingIsFine(t *testing.T) {
	tp := newTestPlant()
	f1 := tp.port("panel-a", "patch_panel", "a-front-1")
	f2 := tp.port("panel-a", "patch_panel", "a-front-2")
	f3 := tp.port("panel-a", "patch_panel", "a-front-3")
	rear := tp.port("panel-a", "patch_panel", "a-rear-1")
	// A three-strand trunk. Strand 3's front port is cabled back to strand 1's,
	// so a walk starting at strand 3 arrives at the rear port having already
	// been through a-front-1 -- and finds a-front-3, its own root, hanging off
	// the same rear port. One strand is fine (2) and one loops (3).
	//
	// Each front port appears exactly once, because port_pass_through_front_key
	// says so: a fixture patching one front port at two positions is not a
	// harder case, it is data the schema refuses.
	tp.cable(f3, f1)
	tp.patch(f1, rear, 1)
	tp.patch(f2, rear, 2)
	tp.patch(f3, rear, 3)

	trace := tp.trace(t, f3)

	var looped, ended int
	var seen []string
	for _, leaf := range trace.Leaves() {
		seen = append(seen, leaf.Hop.Interface+":"+leaf.Outcome)
		switch leaf.Outcome {
		case OutcomeLooped:
			looped++
		case OutcomeComplete, OutcomeUnpatched:
			ended++
		}
	}
	if looped != 1 {
		t.Errorf("the mis-patched strand produced %d looped leaves, want 1. A strand with "+
			"a row that appears nowhere in the trace is a trunk reported short with "+
			"nothing saying so, which is the silent shortening this design refuses. "+
			"Leaves were %v", looped, seen)
	}
	if ended == 0 {
		t.Errorf("the healthy sibling strand was lost while reporting the looping one; "+
			"reporting a fault must not cost the strands that are fine. Leaves were %v", seen)
	}
	if got := len(trace.Leaves()); got != 2 {
		t.Errorf("the rear port carries two strands besides the one arrived on and the "+
			"trace has %d leaves, want 2: %v", got, seen)
	}
}
