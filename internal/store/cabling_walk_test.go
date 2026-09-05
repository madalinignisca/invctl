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
