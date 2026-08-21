// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package seed_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/seed"
	"github.com/madalinignisca/invctl/internal/store"
)

// The demo estate is not decoration: the impact engine's tests assert against
// it, and docs/reachability-design.md's worked examples are quoted from it. So
// the shape of the topology it declares is worth pinning down here, close to
// the data, rather than inferring it from whichever engine test happens to
// break when somebody edits a row.
//
// These tests assert the fixture's SHAPE. What the engine concludes from that
// shape belongs with the engine.
//
// SQLite only, like every other seeded suite in this repo, and that is a
// deliberate scoping rather than an oversight: the seed writes no SQL of its
// own. Every statement it issues comes from a store method the store suite
// already runs against both engines -- including CreateNetAttachment with
// pinned members, which the topology below is the first production caller of.
// The engine harness lives in package store's own test files and is not
// importable from here; duplicating a schema-per-test PostgreSQL harness for a
// package with no queries would be infrastructure without a claim behind it.

type fixture struct {
	store *store.SQLStore
	refs  *seed.Refs
	ctx   context.Context
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "seed.db")
	db, err := store.Open(store.DriverSQLite, dsn)
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	// A fixed test key, not a secret and never used outside this process:
	// StageCustomFields (customfields_test.go in this package) writes real
	// custom values through SetCustomValues, which folds a keyed digest into
	// the audit trail and fails with an error without a key attached
	// (internal/store/customvalues.go).
	s := store.New(db).WithFoldKey(bytes.Repeat([]byte{0x5a}, 32))
	refs, err := seed.Load(ctx, s)
	if err != nil {
		t.Fatalf("seeding fixture: %v", err)
	}
	return &fixture{store: s, refs: refs, ctx: ctx}
}

// eachEngine keeps the call sites shaped like the store suite's engine loop, so
// adding PostgreSQL here later is a change to one function rather than to every
// test below.
func eachEngine(t *testing.T, body func(t *testing.T, f *fixture)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { body(t, newFixture(t)) })
}

// TestSeededGroupsCarryTheValuesTheScenariosTurnOn. Every field checked here
// decides a documented scenario's answer, and each one is a value somebody
// could "tidy" without the engine tests obviously objecting:
// min_healthy on sw-core decides whether losing a core chassis is an outage,
// and failover_mode on fw-edge decides whether losing the primary firewall is
// degraded or merely a note.
func TestSeededGroupsCarryTheValuesTheScenariosTurnOn(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		tests := []struct {
			code         string
			kind         string
			role         string
			availability string
			minHealthy   *int
			failoverMode *string
		}{
			{code: "sw-core", kind: "mclag", role: "core", availability: "active_active", minHealthy: intp(1)},
			{code: "fw-edge", kind: "ha_pair", role: "edge", availability: "active_passive", failoverMode: strp("manual")},
			{code: "sw-oob", kind: "standalone", role: "access", availability: "standalone"},
		}
		for _, tc := range tests {
			t.Run(tc.code, func(t *testing.T) {
				id, ok := f.refs.NetGroups[tc.code]
				if !ok {
					t.Fatalf("the seed declared no %s group", tc.code)
				}
				g, err := f.store.GetNetGroup(f.ctx, id)
				if err != nil {
					t.Fatalf("getting group: %v", err)
				}
				if g.Kind != tc.kind {
					t.Errorf("kind = %q, want %q", g.Kind, tc.kind)
				}
				if g.Role != tc.role {
					t.Errorf("role = %q, want %q", g.Role, tc.role)
				}
				if g.Availability != tc.availability {
					t.Errorf("availability = %q, want %q", g.Availability, tc.availability)
				}
				if !eqIntp(g.MinHealthy, tc.minHealthy) {
					t.Errorf("min_healthy = %v, want %v", show(g.MinHealthy), show(tc.minHealthy))
				}
				if !eqStrp(g.FailoverMode, tc.failoverMode) {
					t.Errorf("failover_mode = %v, want %v -- this is the value that decides whether "+
						"losing the primary reads as degraded or as ok", showS(g.FailoverMode), showS(tc.failoverMode))
				}
			})
		}
	})
}

// TestSeededGroupMembership pins which chassis form each forwarding unit, and
// the primary/standby roles the shared availability evaluator reads.
func TestSeededGroupMembership(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		tests := []struct {
			group string
			want  []string // "asset:role", in the store's own order
		}{
			{"sw-core", []string{"sw-core-1:member", "sw-core-2:member"}},
			{"fw-edge", []string{"fw-edge-1:primary", "fw-edge-2:standby"}},
			{"sw-oob", []string{"sw-oob-1:member"}},
		}
		for _, tc := range tests {
			t.Run(tc.group, func(t *testing.T) {
				rows, err := f.store.ListNetGroupMembers(f.ctx, f.refs.NetGroups[tc.group])
				if err != nil {
					t.Fatalf("listing members: %v", err)
				}
				got := make([]string, len(rows))
				for i, r := range rows {
					got[i] = r.AssetName + ":" + r.Role
				}
				if strings.Join(got, ",") != strings.Join(tc.want, ",") {
					t.Errorf("members = %v, want %v", got, tc.want)
				}
			})
		}
	})
}

// TestSeededUplinks: one group-to-group edge on the data plane, and none at
// all out of the out-of-band switch. A mgmt uplink from sw-oob into the core
// would quietly join the management switch to the data-plane graph, which is
// the exact trap the derivation code documents.
func TestSeededUplinks(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		core, err := f.store.ListNetUplinks(f.ctx, f.refs.NetGroups["sw-core"])
		if err != nil {
			t.Fatalf("listing sw-core uplinks: %v", err)
		}
		if len(core) != 1 {
			t.Fatalf("sw-core has %d uplinks, want exactly 1 (group-to-group, even though two "+
				"physical cables back it)", len(core))
		}
		if core[0].UpstreamCode != "fw-edge" {
			t.Errorf("sw-core uplinks to %q, want fw-edge", core[0].UpstreamCode)
		}
		if core[0].Plane != "data" {
			t.Errorf("sw-core -> fw-edge is on plane %q, want data", core[0].Plane)
		}

		for _, code := range []string{"fw-edge", "sw-oob"} {
			rows, err := f.store.ListNetUplinks(f.ctx, f.refs.NetGroups[code])
			if err != nil {
				t.Fatalf("listing %s uplinks: %v", code, err)
			}
			if len(rows) != 0 {
				t.Errorf("%s declares %d uplinks, want none", code, len(rows))
			}
		}
	})
}

// TestSeededAttachmentsPinTheRightChassis is the row the whole reachability
// model exists for. hv-01 and hv-02 are pinned to both core chassis and
// survive losing either; hv-03 is pinned to sw-core-2 alone, so it is cut off
// by a loss the group itself shrugs off. A "tidying" edit that gave hv-03 both
// chassis would silently delete the scenario.
func TestSeededAttachmentsPinTheRightChassis(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		tests := []struct {
			group string
			want  []string // "asset/plane -> pinned chassis, comma separated"
		}{
			{"sw-core", []string{
				"hv-01/data -> sw-core-1,sw-core-2",
				"hv-02/data -> sw-core-1,sw-core-2",
				"hv-03/data -> sw-core-2",
			}},
			// No pins on a group of one: "attached to the group as a whole"
			// says exactly as much, without rows that can rot out of step with
			// the cable plant.
			{"sw-oob", []string{
				"hv-01/mgmt -> ",
				"hv-02/mgmt -> ",
				"hv-03/mgmt -> ",
				// The half-installed box: a management attachment and no
				// data-plane one, which is what "console patched, uplink not"
				// looks like once it is declared rather than merely cabled.
				"srv-backup-proxy-1/mgmt -> ",
			}},
			{"fw-edge", nil},
		}
		for _, tc := range tests {
			t.Run(tc.group, func(t *testing.T) {
				rows, err := f.store.ListNetAttachments(f.ctx, f.refs.NetGroups[tc.group])
				if err != nil {
					t.Fatalf("listing attachments: %v", err)
				}
				got := make([]string, len(rows))
				for i, r := range rows {
					got[i] = r.AssetName + "/" + r.Plane + " -> " + strings.Join(r.Members, ",")
				}
				if strings.Join(got, "|") != strings.Join(tc.want, "|") {
					t.Errorf("attachments =\n  %v\nwant\n  %v", got, tc.want)
				}
			})
		}
	})
}

// TestTheVMsInheritRatherThanDeclare. The VMs and k8s nodes get no attachment
// row at all: they resolve to their hypervisor's through asset_closure, which
// is what nearest-ancestor-wins is for. Adding a row per VM would make the
// fixture stop demonstrating the thing it exists to demonstrate, and would be
// the easy "fix" for anyone who saw a VM reported as unmodelled and reached
// for the obvious lever.
//
// The assertion is on the CLAIM -- no guest declares its own attachment --
// rather than on a list of names. It was a name census until the fixture
// gained a bare-metal server with a management attachment of its own, which
// broke the test while breaking nothing it was protecting. A census fails on
// any new asset; the claim fails only on the mistake.
func TestTheVMsInheritRatherThanDeclare(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		attached := map[string]bool{}
		for code := range f.refs.NetGroups {
			rows, err := f.store.ListNetAttachments(f.ctx, f.refs.NetGroups[code])
			if err != nil {
				t.Fatalf("listing attachments of %s: %v", code, err)
			}
			for _, r := range rows {
				attached[r.AssetName] = true
			}
		}

		guests := 0
		for name, id := range f.refs.Assets {
			a, err := f.store.GetAsset(f.ctx, id)
			if err != nil {
				t.Fatalf("reading %s: %v", name, err)
			}
			if a.Kind != domain.KindVM && a.Kind != domain.KindK8sNode {
				continue
			}
			guests++
			if attached[name] {
				t.Errorf("%s is a %s and declares its own attachment; guests must inherit "+
					"their host's through asset_closure", name, a.Kind)
			}
		}
		if guests == 0 {
			t.Fatal("no guests in the fixture, so this proves nothing")
		}
		// And the hypervisors, which are what the guests inherit FROM, do
		// still declare theirs.
		for _, hv := range []string{"hv-01", "hv-02", "hv-03"} {
			if !attached[hv] {
				t.Errorf("%s declares no attachment, so its guests inherit nothing", hv)
			}
		}
	})
}

// TestSeededAnchors. An anchor's scope is 1:1 with endpoint.exposure, and a
// misplaced anchor is the single highest-leverage wrong row in this model --
// one row silently changes every external-reachability verdict in the estate.
func TestSeededAnchors(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		rows, err := f.store.ListNetAnchors(f.ctx)
		if err != nil {
			t.Fatalf("listing anchors: %v", err)
		}
		got := make([]string, len(rows))
		for i, r := range rows {
			env := ""
			if r.EnvironmentID != nil {
				env = envCode(t, f, *r.EnvironmentID)
			}
			got[i] = r.Code + "/" + r.Scope + "/" + r.GroupCode + "/" + env
		}
		// ListNetAnchors orders by scope then code.
		want := []string{
			"partner-transit/cross_env/fw-edge/",
			"dev-net/environment/sw-core/dev",
			"prod-net/environment/sw-core/prod",
			"internet/external/fw-edge/",
		}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("anchors =\n  %v\nwant\n  %v", got, want)
		}
		for _, r := range rows {
			if r.Plane != "data" {
				t.Errorf("anchor %s is on plane %q; an anchor is a data-plane point of presence", r.Code, r.Plane)
			}
		}
	})
}

// TestTheDeclaredTopologyMatchesTheCablePlant runs derivation against an
// untouched fixture and asserts it has nothing to propose.
//
// That is a real consistency check, not a tautology: derivation reads `link`
// rows and proposes the attachments and uplinks they imply, skipping anything
// already declared. An empty proposal therefore means every cable the seed
// lays is accounted for by a declared row, and every declared row has a cable
// behind it in the direction derivation would have chosen. Add a cable and
// forget its attachment -- the most likely way this fixture rots -- and this
// fails.
func TestTheDeclaredTopologyMatchesTheCablePlant(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		proposal, err := f.store.DeriveNetworkProposal(f.ctx)
		if err != nil {
			t.Fatalf("deriving: %v", err)
		}
		for _, a := range proposal.Attachments {
			t.Errorf("the cable plant implies an undeclared attachment: %s -> %s on plane %s",
				a.AssetName, a.GroupCode, a.Plane)
		}
		for _, u := range proposal.Uplinks {
			t.Errorf("the cable plant implies an undeclared uplink: %s -> %s on plane %s",
				u.GroupCode, u.UpstreamCode, u.Plane)
		}
		// The sw-core-1 <-> sw-core-2 peer link is intra-group, so derivation
		// drops it as a peer link rather than counting it ambiguous. If this
		// ever goes non-zero the two chassis have stopped being one group.
		if proposal.SkippedAmbiguous != 0 {
			t.Errorf("derivation skipped %d cable(s) as ambiguous; the seed's only forwarder-to-forwarder "+
				"peer link is intra-group and should not be counted", proposal.SkippedAmbiguous)
		}
	})
}

func envCode(t *testing.T, f *fixture, id string) string {
	t.Helper()
	for code, envID := range f.refs.Environments {
		if envID == id {
			return code
		}
	}
	t.Fatalf("unknown environment id %s", id)
	return ""
}

func intp(n int) *int       { return &n }
func strp(s string) *string { return &s }

func eqIntp(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func eqStrp(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func show(p *int) any {
	if p == nil {
		return "nil"
	}
	return *p
}

func showS(p *string) any {
	if p == nil {
		return "nil"
	}
	return *p
}
