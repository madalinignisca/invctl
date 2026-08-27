// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package impact_test

import (
	"strings"
	"testing"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/impact"
	"github.com/madalinignisca/invctl/internal/store"
)

// ---------------------------------------------------------------------------
// The compositional guarantee: declaring a HEALTHY topology, with nothing
// taken down on it, must not move a single existing status. For every
// simulation below, Result.Services, Result.WontRestart, Result.Cycles,
// Result.SafeOrder and Result.Iterations must be identical with topology rows
// present and absent.
//
// This began as M3's report-only checkpoint, where it held trivially because
// the seams were gated off. Under M4 it is a real assertion: reachability now
// composes into the answer, so these tests pass only because a healthy network
// genuinely subtracts nothing.
// ---------------------------------------------------------------------------

// impactProjection is every field the headline guarantee actually covers --
// deliberately excluding Cause and LostToIsolation, which are fields the
// reachability work is allowed to add. Comparing this projection rather than the whole struct
// is what makes the assertion fail if a M3 change ever perturbs an existing
// answer, without being so strict it breaks the moment a new field is added.
type impactProjection struct {
	Code           string
	Name           string
	Tier           int
	Status         domain.Status
	Reason         string
	LostInstances  int
	TotalInstances int
	Via            string
}

func projectImpacts(in []impact.ServiceImpact) []impactProjection {
	out := make([]impactProjection, len(in))
	for i, s := range in {
		out[i] = impactProjection{
			Code: s.Code, Name: s.Name, Tier: s.Tier, Status: s.Status, Reason: s.Reason,
			LostInstances: s.LostInstances, TotalInstances: s.TotalInstances, Via: s.Via,
		}
	}
	return out
}

// snapshot is the subset of impact.Result the headline guarantee is about.
type snapshot struct {
	Services    []impactProjection
	WontRestart []impactProjection
	Cycles      [][]string
	SafeOrder   []string
	Iterations  int
}

func snapshotOf(r impact.Result) snapshot {
	return snapshot{
		Services: projectImpacts(r.Services), WontRestart: projectImpacts(r.WontRestart),
		Cycles: r.Cycles, SafeOrder: r.SafeOrder, Iterations: r.Iterations,
	}
}

func assertSameSnapshot(t *testing.T, scenario string, before, after snapshot) {
	t.Helper()
	if len(before.Services) != len(after.Services) {
		t.Fatalf("%s: Services count changed %d -> %d (before=%+v after=%+v)",
			scenario, len(before.Services), len(after.Services), before.Services, after.Services)
	}
	for i := range before.Services {
		if before.Services[i] != after.Services[i] {
			t.Errorf("%s: Services[%d] changed\n before: %+v\n after:  %+v", scenario, i, before.Services[i], after.Services[i])
		}
	}
	if len(before.WontRestart) != len(after.WontRestart) {
		t.Fatalf("%s: WontRestart count changed %d -> %d", scenario, len(before.WontRestart), len(after.WontRestart))
	}
	for i := range before.WontRestart {
		if before.WontRestart[i] != after.WontRestart[i] {
			t.Errorf("%s: WontRestart[%d] changed\n before: %+v\n after:  %+v", scenario, i, before.WontRestart[i], after.WontRestart[i])
		}
	}
	if len(before.Cycles) != len(after.Cycles) {
		t.Errorf("%s: Cycles count changed %d -> %d", scenario, len(before.Cycles), len(after.Cycles))
	}
	if len(before.SafeOrder) != len(after.SafeOrder) {
		t.Errorf("%s: SafeOrder count changed %d -> %d", scenario, len(before.SafeOrder), len(after.SafeOrder))
	} else {
		for i := range before.SafeOrder {
			if before.SafeOrder[i] != after.SafeOrder[i] {
				t.Errorf("%s: SafeOrder[%d] changed %q -> %q", scenario, i, before.SafeOrder[i], after.SafeOrder[i])
			}
		}
	}
	if before.Iterations != after.Iterations {
		t.Errorf("%s: Iterations changed %d -> %d", scenario, before.Iterations, after.Iterations)
	}
}

// headlineScenarios is the minimum set the milestone spec calls for: hv-01,
// rack-a1, vm-sso-1, vm-app-1 and the empty request.
var headlineScenarios = [][]string{
	{"hv-01"},
	{"rack-a1"},
	{"vm-sso-1"},
	{"vm-app-1"},
	{},
}

func snapshotAll(t *testing.T, f *fixture) map[string]snapshot {
	t.Helper()
	out := make(map[string]snapshot, len(headlineScenarios))
	for _, assets := range headlineScenarios {
		result, _ := f.simulate(t, 180, assets...)
		key := scenarioKey(assets)
		out[key] = snapshotOf(result)
	}
	return out
}

func scenarioKey(assets []string) string {
	if len(assets) == 0 {
		return "(empty)"
	}
	return strings.Join(assets, ",")
}

// clearSeededTopology retires every forwarder group the fixture arrives with,
// which cascades that group's members, uplinks, attachments and anchors in the
// same transaction. loadNetGraph filters retired rows, so afterwards
// Inputs.Net is nil again and every reachability channel is inert.
//
// It exists because M5 moved the demo topology into the seed. Before that, a
// fresh fixture carried zero net_* rows and "no topology" was simply the state
// a test found itself in; it is now a state a test has to ask for, and one
// worth asking for explicitly rather than by accident.
//
// Note that net_group.code and net_anchor.code are plain UNIQUE, not partial
// on lifecycle, so retiring does NOT free the codes. Ad-hoc topology declared
// by a test still has to use its own, test-scoped codes.
func clearSeededTopology(t *testing.T, f *fixture) {
	t.Helper()
	groups, err := f.store.ListNetGroups(f.ctx)
	if err != nil {
		t.Fatalf("listing seeded net groups: %v", err)
	}
	if len(groups) == 0 {
		t.Fatal("the fixture declared no forwarder groups, so this helper is a no-op and " +
			"every test that depends on it is now asserting against an unknown baseline")
	}
	for _, g := range groups {
		if err := f.store.RetireNetGroup(f.ctx, domain.SystemActor, g.ID); err != nil {
			t.Fatalf("retiring seeded net group %s: %v", g.Code, err)
		}
	}
	assertNoTopology(t, f)
}

// assertNoTopology fails unless the engine would genuinely receive
// Inputs.Net == nil. Every "with no topology..." test rests on this, and
// without it those tests pass just as happily with a full forwarder graph
// present.
func assertNoTopology(t *testing.T, f *fixture) {
	t.Helper()
	g, err := f.store.LoadGraph(f.ctx)
	if err != nil {
		t.Fatalf("loading graph: %v", err)
	}
	if g.Net != nil {
		t.Fatalf("the fixture still carries %d forwarder groups and %d attachments; "+
			"this test's premise is that it carries none",
			len(g.Net.Groups), len(g.Net.Attachments))
	}
}

// assertHasTopology is the mirror: a test comparing "with topology" against
// "without" proves nothing if the first half had none either.
func assertHasTopology(t *testing.T, f *fixture) {
	t.Helper()
	g, err := f.store.LoadGraph(f.ctx)
	if err != nil {
		t.Fatalf("loading graph: %v", err)
	}
	if g.Net == nil {
		t.Fatal("the fixture declared no topology at all, so a with/without comparison " +
			"is comparing two identical runs")
	}
}

// TestHeadlineGuaranteeNoTopology is the first half: with zero net_* rows,
// Inputs.Net is nil and the engine must answer exactly as it did before any of
// this existed. An estate that has not entered its cabling loses nothing.
func TestHeadlineGuaranteeNoTopology(t *testing.T) {
	f := newFixture(t)
	clearSeededTopology(t, f)

	for _, assets := range headlineScenarios {
		result, _ := f.simulate(t, 180, assets...)
		snap := snapshotOf(result)
		key := scenarioKey(assets)
		// A basic sanity check that the scenario actually produced a
		// deterministic, non-exploded answer -- guards against a snapshot
		// full of zero values passing trivially.
		if snap.Iterations == 0 && len(snap.Services) > 0 {
			t.Errorf("%s: %d services affected but Iterations = 0", key, len(snap.Services))
		}
		// And that the silence really is the feature being inert, rather than
		// a topology that happened not to break on this particular scenario.
		if n := len(result.Isolated) + len(result.Partitions) +
			len(result.Unreachable) + len(result.RedundancyLost); n > 0 {
			t.Errorf("%s: %d reachability findings on an estate with no declared topology", key, n)
		}
		if cov := result.Coverage; cov.Modelled+cov.Inherited > 0 {
			t.Errorf("%s: coverage reports %d modelled and %d inherited assets with no topology declared",
				key, cov.Modelled, cov.Inherited)
		}
	}
}

// TestHeadlineGuaranteeWithTopology is the second half: the same scenarios run
// on both sides of the topology boundary. Merely knowing how the estate is
// cabled must not move a single byte of Services, WontRestart, Cycles,
// SafeOrder or Iterations -- only a break in that cabling may.
//
// Since M5 the direction is inverted: the fixture arrives WITH the demo's own
// forwarder graph, so the snapshot is taken first and the topology is retired
// afterwards. That is the same assertion it always was, and a stronger one --
// the subject is now the topology the demo actually ships rather than a
// hand-built stand-in built to be harmless.
func TestHeadlineGuaranteeWithTopology(t *testing.T) {
	f := newFixture(t)
	withTopology := snapshotAll(t, f)

	clearSeededTopology(t, f)

	for _, assets := range headlineScenarios {
		result, _ := f.simulate(t, 180, assets...)
		assertSameSnapshot(t, scenarioKey(assets), withTopology[scenarioKey(assets)], snapshotOf(result))
	}
}

// ---------------------------------------------------------------------------
// Union-find determinism
// ---------------------------------------------------------------------------

// TestUnionFindDeterministic runs the same graph repeatedly and checks the
// answer never changes -- union-find has no traversal order to be
// nondeterministic about, but map iteration elsewhere in the pipeline could
// still leak in if a slice were built by ranging a map without sorting.
//
// It runs against the seeded topology, and over two scenarios rather than one:
// hv-01 moves no group at all, while sw-core-2 leaves the core group OK,
// exhausts hv-03's pinned member set and isolates it. A run where every group
// is healthy and every host is in one component would be a weak probe of a
// component-partitioning algorithm.
func TestUnionFindDeterministic(t *testing.T) {
	f := newFixture(t)

	for _, scenario := range []string{"hv-01", "sw-core-2"} {
		var first snapshot
		for i := 0; i < 5; i++ {
			result, _ := f.simulate(t, 180, scenario)
			snap := snapshotOf(result)
			if i == 0 {
				first = snap
				continue
			}
			assertSameSnapshot(t, "repeated "+scenario+" run", first, snap)
		}
	}
}

// ---------------------------------------------------------------------------
// A cyclic network graph must terminate
// ---------------------------------------------------------------------------

// TestCyclicNetworkGraphTerminates builds a peer link between two core
// switches -- a deliberate cycle in the forwarder graph, which is normal for
// redundancy -- and checks the engine still returns instead of hanging.
// Union-find has no recursion and no traversal, so this is a design
// guarantee, not a hope; the test is what makes it provable.
func TestCyclicNetworkGraphTerminates(t *testing.T) {
	f := newFixture(t)
	now := f.store.Now()

	swA := mustServerAsset(t, f, "test-sw-a")
	swB := mustServerAsset(t, f, "test-sw-b")
	host := mustServerAsset(t, f, "test-host")

	groupA := mustNetGroup(t, f, "test-core-a", domain.NetRoleCore, domain.AvailStandalone, nil, nil)
	groupB := mustNetGroup(t, f, "test-core-b", domain.NetRoleCore, domain.AvailStandalone, nil, nil)
	mustNetGroupMember(t, f, groupA, swA)
	mustNetGroupMember(t, f, groupB, swB)

	// The cycle: A -> B and B -> A on the same plane.
	up1, err := domain.NewNetUplink(store.NewID(), groupA, groupB, domain.PlaneData, now)
	if err != nil {
		t.Fatalf("building uplink A->B: %v", err)
	}
	if err := f.store.CreateNetUplink(f.ctx, domain.SystemActor, up1); err != nil {
		t.Fatalf("creating uplink A->B: %v", err)
	}
	up2, err := domain.NewNetUplink(store.NewID(), groupB, groupA, domain.PlaneData, now)
	if err != nil {
		t.Fatalf("building uplink B->A: %v", err)
	}
	if err := f.store.CreateNetUplink(f.ctx, domain.SystemActor, up2); err != nil {
		t.Fatalf("creating uplink B->A: %v", err)
	}

	mustAttach(t, f, host, groupA)

	done := make(chan impact.Result, 1)
	go func() {
		result, err := f.store.Simulate(f.ctx, impact.Request{DownAssetIDs: []string{swA}, WindowSeconds: 180})
		if err != nil {
			t.Errorf("simulating: %v", err)
		}
		done <- result
	}()
	select {
	case <-done:
		// Terminated -- the point of the test.
	case <-timeoutAfter(t):
		t.Fatal("simulation with a cyclic network graph did not terminate")
	}
}

// ---------------------------------------------------------------------------
// The unmodelled default
// ---------------------------------------------------------------------------

// TestUnmodelledAssetIsNeverIsolated: an asset with no net_attachment
// anywhere in its containment ancestry is unmodelled, never isolated, no
// matter what else in the estate goes down. This is the graceful-degradation
// guarantee: absence of topology data is not evidence of disconnection.
func TestUnmodelledAssetIsNeverIsolated(t *testing.T) {
	f := newFixture(t)

	// A forwarder group exists and is fully down, but the host asset under
	// test has no attachment anywhere in its ancestry.
	sw := mustServerAsset(t, f, "test-only-switch")
	group := mustNetGroup(t, f, "test-only-group", domain.NetRoleAccess, domain.AvailStandalone, nil, nil)
	mustNetGroupMember(t, f, group, sw)

	host := mustServerAsset(t, f, "test-unmodelled-host")
	svc := mustSimpleService(t, f, "test-svc-unmodelled", "test-unmodelled")
	mustInstance(t, f, svc, host)

	result, err := f.store.Simulate(f.ctx, impact.Request{DownAssetIDs: []string{sw}, WindowSeconds: 180})
	if err != nil {
		t.Fatalf("simulating: %v", err)
	}
	for _, iso := range result.Isolated {
		if iso.AssetID == host {
			t.Fatalf("unmodelled host %s reported isolated: %+v", host, iso)
		}
	}
}

// ---------------------------------------------------------------------------
// Pairwise reachability is a relation, not a property
// ---------------------------------------------------------------------------

// TestPairwiseSameSideStillReachable: two hosts behind a failed firewall can
// still talk to each other. Reachability is a relation, not a property --
// reporting them as broken because something upstream of both of them died
// is the false alarm that gets a report ignored.
func TestPairwiseSameSideStillReachable(t *testing.T) {
	f := newFixture(t)
	now := f.store.Now()

	// An access switch both hosts attach to, uplinked to an edge firewall
	// neither host attaches to directly.
	access := mustServerAsset(t, f, "test-access-sw")
	fw := mustServerAsset(t, f, "test-fw-only")
	accessGroup := mustNetGroup(t, f, "test-access-group", domain.NetRoleAccess, domain.AvailStandalone, nil, nil)
	fwGroup := mustNetGroup(t, f, "test-fw-group", domain.NetRoleEdge, domain.AvailStandalone, nil, nil)
	mustNetGroupMember(t, f, accessGroup, access)
	mustNetGroupMember(t, f, fwGroup, fw)

	uplink, err := domain.NewNetUplink(store.NewID(), accessGroup, fwGroup, domain.PlaneData, now)
	if err != nil {
		t.Fatalf("building uplink: %v", err)
	}
	if err := f.store.CreateNetUplink(f.ctx, domain.SystemActor, uplink); err != nil {
		t.Fatalf("creating uplink: %v", err)
	}

	hostA := mustServerAsset(t, f, "test-side-a")
	hostB := mustServerAsset(t, f, "test-side-b")
	mustAttach(t, f, hostA, accessGroup)
	mustAttach(t, f, hostB, accessGroup)

	svcA := mustSimpleService(t, f, "test-svc-a", "test-side-svc-a")
	svcB := mustSimpleService(t, f, "test-svc-b", "test-side-svc-b")
	mustInstance(t, f, svcA, hostA)
	mustInstance(t, f, svcB, hostB)

	dep, err := domain.NewDependency(store.NewID(), domain.DependencySpec{
		ConsumerServiceID: svcA, ProviderEndpointID: mustEndpoint(t, f, svcB, "svcb-http"),
		Nature: domain.NatureHard, FailureMode: "consumer cannot serve",
	}, now)
	if err != nil {
		t.Fatalf("building dependency: %v", err)
	}
	if err := f.store.CreateDependency(f.ctx, domain.SystemActor, dep, nil); err != nil {
		t.Fatalf("creating dependency: %v", err)
	}

	// Take the firewall down: neither host attaches to it, so this must not
	// touch the access group at all, and the hard dependency between the two
	// access-side services must stay unaffected -- they are on the same side
	// of the break.
	result, err := f.store.Simulate(f.ctx, impact.Request{DownAssetIDs: []string{fw}, WindowSeconds: 180})
	if err != nil {
		t.Fatalf("simulating: %v", err)
	}
	for _, si := range result.Services {
		if si.Code == "test-svc-a" {
			t.Fatalf("test-side-svc-a reported affected by an unrelated firewall loss: %+v", si)
		}
	}
	for _, iso := range result.Isolated {
		if iso.AssetID == hostA || iso.AssetID == hostB {
			t.Fatalf("host reported isolated by an unrelated firewall loss: %+v", iso)
		}
	}
}

// ---------------------------------------------------------------------------
// A retired group member is not capacity
// ---------------------------------------------------------------------------

// TestRetiredGroupMemberIsNotCapacity: a retired chassis in a group must not
// count towards its installed members, mirroring net_group's own R1 rule.
func TestRetiredGroupMemberIsNotCapacity(t *testing.T) {
	f := newFixture(t)
	now := f.store.Now()

	primary := mustServerAsset(t, f, "test-retired-primary")
	standby := mustServerAsset(t, f, "test-retired-standby")
	auto := domain.FailoverAuto
	group := mustNetGroup(t, f, "test-retired-group", domain.NetRoleEdge, domain.AvailActivePassive, nil, &auto)
	mustNetGroupMemberWithRole(t, f, group, primary, domain.RolePrimary)
	standbyMember, err := domain.NewNetGroupMember(group, standby, domain.RoleStandby, now)
	if err != nil {
		t.Fatalf("building standby member: %v", err)
	}
	if err := f.store.AddNetGroupMember(f.ctx, domain.SystemActor, standbyMember); err != nil {
		t.Fatalf("adding standby member: %v", err)
	}
	// Retire the standby: it must no longer count as capacity, so losing the
	// primary with only a retired standby left must report the group (and
	// anything behind it) down, not degraded.
	if err := f.store.RetireNetGroupMember(f.ctx, domain.SystemActor, group, standby); err != nil {
		t.Fatalf("retiring standby member: %v", err)
	}

	host := mustServerAsset(t, f, "test-retired-host")
	mustAttach(t, f, host, group)
	svc := mustSimpleService(t, f, "test-svc-retired", "test-retired-svc")
	mustInstance(t, f, svc, host)

	result, err := f.store.Simulate(f.ctx, impact.Request{
		DownAssetIDs: []string{primary}, WindowSeconds: 180,
	})
	if err != nil {
		t.Fatalf("simulating: %v", err)
	}
	var found bool
	for _, si := range result.Services {
		if si.Code == "test-svc-retired" {
			found = true
			if si.Status != domain.StatusDown {
				t.Errorf("test-svc-retired status = %s, want down (retired standby must not count as capacity)", si.Status)
			}
		}
	}
	if !found {
		t.Fatal("test-svc-retired not reported affected at all")
	}
}

// ---------------------------------------------------------------------------
// Test helpers -- building an ad-hoc topology the fixture cannot yet
// demonstrate on its own (docs/reachability-design.md notes the seed has only
// two management cables and no groups; M5 extends it).
// ---------------------------------------------------------------------------

func mustServerAsset(t *testing.T, f *fixture, name string) string {
	t.Helper()
	a, err := domain.NewAsset(store.NewID(), domain.KindServer, name, nil, f.store.Now())
	if err != nil {
		t.Fatalf("building asset %s: %v", name, err)
	}
	if err := f.store.CreateAsset(f.ctx, domain.AdministratorPermit(domain.SystemActor), a, nil); err != nil {
		t.Fatalf("creating asset %s: %v", name, err)
	}
	return a.ID
}

func mustNetGroup(t *testing.T, f *fixture, code, role, availability string, minHealthy *int, failoverMode *string) string {
	t.Helper()
	g, err := domain.NewNetGroup(store.NewID(), domain.NetGroupSpec{
		Code: code, Name: code, Kind: domain.NetGroupStandalone, Role: role,
		Availability: availability, MinHealthy: minHealthy, FailoverMode: failoverMode,
	}, f.store.Now())
	if err != nil {
		t.Fatalf("building net group %s: %v", code, err)
	}
	if err := f.store.CreateNetGroup(f.ctx, domain.SystemActor, g); err != nil {
		t.Fatalf("creating net group %s: %v", code, err)
	}
	return g.ID
}

func mustNetGroupMember(t *testing.T, f *fixture, groupID, assetID string) {
	t.Helper()
	mustNetGroupMemberWithRole(t, f, groupID, assetID, "member")
}

func mustNetGroupMemberWithRole(t *testing.T, f *fixture, groupID, assetID, role string) {
	t.Helper()
	m, err := domain.NewNetGroupMember(groupID, assetID, role, f.store.Now())
	if err != nil {
		t.Fatalf("building net group member: %v", err)
	}
	if err := f.store.AddNetGroupMember(f.ctx, domain.SystemActor, m); err != nil {
		t.Fatalf("adding net group member: %v", err)
	}
}

func mustAttach(t *testing.T, f *fixture, assetID, groupID string) {
	t.Helper()
	a, err := domain.NewNetAttachment(store.NewID(), assetID, groupID, domain.PlaneData, f.store.Now())
	if err != nil {
		t.Fatalf("building attachment: %v", err)
	}
	if err := f.store.CreateNetAttachment(f.ctx, domain.SystemActor, a, nil); err != nil {
		t.Fatalf("creating attachment: %v", err)
	}
}

func mustSimpleService(t *testing.T, f *fixture, code, name string) string {
	t.Helper()
	prodEnvID := f.refs.Environments["prod"]
	svc, err := domain.NewService(store.NewID(), domain.ServiceSpec{
		Code: code, Name: name, Kind: domain.SvcAPI, EnvironmentID: prodEnvID,
		Availability: domain.AvailStandalone, Tier: 3,
	}, f.store.Now())
	if err != nil {
		t.Fatalf("building service %s: %v", code, err)
	}
	if err := f.store.CreateService(f.ctx, domain.SystemActor, svc); err != nil {
		t.Fatalf("creating service %s: %v", code, err)
	}
	return svc.ID
}

func mustInstance(t *testing.T, f *fixture, serviceID, hostAssetID string) string {
	t.Helper()
	inst, err := domain.NewServiceInstance(store.NewID(), serviceID, hostAssetID, domain.RuntimeSystemd, 1, f.store.Now())
	if err != nil {
		t.Fatalf("building instance: %v", err)
	}
	if err := f.store.CreateInstance(f.ctx, domain.SystemActor, inst); err != nil {
		t.Fatalf("creating instance: %v", err)
	}
	return inst.ID
}

func mustEndpoint(t *testing.T, f *fixture, serviceID, name string) *string {
	t.Helper()
	port := 80
	ep, err := domain.NewEndpoint(store.NewID(), serviceID, name, domain.ProtoTCP, &port, domain.BindHost)
	if err != nil {
		t.Fatalf("building endpoint %s: %v", name, err)
	}
	if err := f.store.CreateEndpoint(f.ctx, domain.SystemActor, ep); err != nil {
		t.Fatalf("creating endpoint %s: %v", name, err)
	}
	id := ep.ID
	return &id
}

func timeoutAfter(t *testing.T) <-chan time.Time {
	t.Helper()
	return time.After(5 * time.Second)
}

// simulateWith runs a simulation with an explicit Request, so a test can toggle
// ReportReachabilityOnly.
func (f *fixture) simulateWith(t *testing.T, req impact.Request, assetNames ...string) impact.Result {
	t.Helper()
	for _, name := range assetNames {
		id, ok := f.refs.Assets[name]
		if !ok {
			t.Fatalf("unknown asset %q in fixture", name)
		}
		req.DownAssetIDs = append(req.DownAssetIDs, id)
	}
	result, err := f.store.Simulate(f.ctx, req)
	if err != nil {
		t.Fatalf("simulating %v: %v", assetNames, err)
	}
	return result
}

// TestReachabilityAppliesByDefault is the M4 inversion of the M3 gate test.
//
// M3's contract was that reachability computed and reported but moved nothing,
// so an operator got one milestone in which to judge the model against their
// estate before it started changing statuses they already trusted. M4 is that
// judgement having been made: the zero-value Request now composes reachability
// into the answer, and ReportReachabilityOnly is what preserves the old view.
//
// Both directions are still asserted, for the same reason they were in M3. A
// test that only checked the default would pass identically if
// ReportReachabilityOnly silently did nothing, and a test that only checked
// ReportReachabilityOnly would pass if the seams had never been wired.
//
// The subject is sw-core-2, not sw-core-1, and the difference is the whole
// point of M5's fixture. Both are members of an active_active group with
// min_healthy = 1, so losing either leaves the GROUP healthy. Only sw-core-2
// exhausts a host's pinned member set: hv-03 is single-homed to it, so hv-03
// is cut off while the core is fine. Pointed at sw-core-1 this test would
// assert nothing, because the correct answer there is zero services.
//
// Every run below happens before the topology is retired; the untopologied
// baseline is taken last, on the same database, so the comparison never
// straddles two seeded fixtures.
func TestReachabilityAppliesByDefault(t *testing.T) {
	f := newFixture(t)

	applied := f.simulateWith(t, impact.Request{WindowSeconds: 180}, "sw-core-2")
	reportOnly := f.simulateWith(t, impact.Request{
		WindowSeconds: 180, ReportReachabilityOnly: true,
	}, "sw-core-2")

	// The baseline: the same loss with no declared cabling at all. Losing a
	// switch that hosts nothing affects nothing, which is the behaviour this
	// whole feature exists to fix.
	clearSeededTopology(t, f)
	bare := f.simulateWith(t, impact.Request{WindowSeconds: 180}, "sw-core-2")
	if len(bare.Services) != 0 {
		t.Fatalf("setup: losing an uncabled switch already reported %d services", len(bare.Services))
	}

	t.Run("the default request now moves statuses", func(t *testing.T) {
		if len(applied.Services) == 0 {
			t.Fatal("losing the core chassis that hv-03 is single-homed to still reported " +
				"nothing affected -- reachability is not composing into the answer")
		}
		// And it must be strictly more than the untopologied run, never fewer.
		if len(applied.Services) <= len(bare.Services) {
			t.Errorf("applied run reported %d services, untopologied run %d -- expected the "+
				"applied run to find strictly more", len(applied.Services), len(bare.Services))
		}
	})

	t.Run("ReportReachabilityOnly still holds every answer still", func(t *testing.T) {
		assertSameSnapshot(t, "sw-core-2 report-only", snapshotOf(bare), snapshotOf(reportOnly))
	})

	t.Run("and reports isolation in either mode", func(t *testing.T) {
		for _, mode := range []struct {
			name   string
			result impact.Result
		}{
			{"default", applied},
			{"report-only", reportOnly},
		} {
			if len(mode.result.Isolated) == 0 {
				t.Errorf("%s: nothing reported isolated, so the report half is not working "+
					"and the assertions above prove less than they appear to", mode.name)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// M4: a partitioned provider is not a failed provider
// ---------------------------------------------------------------------------

// declarePartitionableTopology builds three forwarder groups arranged so that
// taking down one of them splits the estate in two without isolating either
// half:
//
//	group-a (sw-core-1)  <- hv-01, hv-02
//	     \
//	      group-mid (fw-edge-1)     <- the only path between the halves
//	     /
//	group-b (sw-acc-b)   <- hv-03
//
// Losing fw-edge-1 leaves every host attached to a live group, so nothing is
// isolated, and yet hv-01 can no longer reach hv-03. That is a partition
// proper, and it is the only shape that exercises Seam 2 -- an outage that
// isolates a host instead kills its instances at Seam 1, and the dependency
// edge never gets asked about.
func declarePartitionableTopology(t *testing.T, f *fixture) {
	t.Helper()
	declareSplitTopology(t, f, []string{"hv-01", "hv-02"}, []string{"hv-03"})
}

// declareSplitTopology is declarePartitionableTopology with the two sides
// chosen by the caller, so a test can put a route's frontend and its backends
// on opposite sides of the break.
func declareSplitTopology(t *testing.T, f *fixture, sideA, sideB []string) {
	t.Helper()
	now := f.store.Now()

	// The seeded topology has to go first, and not only because sw-core-1 and
	// fw-edge-1 are already members of a group (which the partial unique index
	// on net_group_member(asset_id) would catch loudly). The quieter reason is
	// net_attachment: its uniqueness is per (asset, group, plane), so a second
	// data-plane attachment of hv-01 to a DIFFERENT group is accepted with no
	// error at all -- and hv-01 would then reach hv-03 through the surviving
	// seeded sw-core group no matter what this topology cut. The partition
	// would simply never happen, and the failure would surface several layers
	// away as "sso not affected".
	clearSeededTopology(t, f)

	// The fixture has two network assets and this topology needs three, so the
	// second access switch is created here rather than added to the seed --
	// the demo estate should not grow a switch that exists only for a test.
	rackB := f.refs.Assets["rack-b1"]
	accB, err := domain.NewAsset(store.NewID(), domain.KindSwitch, "sw-acc-b", &rackB, now)
	if err != nil {
		t.Fatalf("building sw-acc-b: %v", err)
	}
	if err := f.store.CreateAsset(f.ctx, domain.AdministratorPermit(domain.SystemActor), accB, nil); err != nil {
		t.Fatalf("creating sw-acc-b: %v", err)
	}

	group := func(code, name string, memberAsset string) *domain.NetGroup {
		t.Helper()
		g, err := domain.NewNetGroup(store.NewID(), domain.NetGroupSpec{
			Code: code, Name: name, Kind: domain.NetGroupStandalone,
			Role: domain.NetRoleCore, Availability: domain.AvailStandalone,
		}, now)
		if err != nil {
			t.Fatalf("building %s: %v", code, err)
		}
		if err := f.store.CreateNetGroup(f.ctx, domain.SystemActor, g); err != nil {
			t.Fatalf("creating %s: %v", code, err)
		}
		m, err := domain.NewNetGroupMember(g.ID, memberAsset, "member", now)
		if err != nil {
			t.Fatalf("building %s member: %v", code, err)
		}
		if err := f.store.AddNetGroupMember(f.ctx, domain.SystemActor, m); err != nil {
			t.Fatalf("adding %s member: %v", code, err)
		}
		return g
	}

	a := group("group-a", "A-side access", f.refs.Assets["sw-core-1"])
	mid := group("group-mid", "Transit", f.refs.Assets["fw-edge-1"])
	b := group("group-b", "B-side access", accB.ID)

	for _, pair := range [][2]string{{a.ID, mid.ID}, {b.ID, mid.ID}} {
		up, err := domain.NewNetUplink(store.NewID(), pair[0], pair[1], domain.PlaneData, now)
		if err != nil {
			t.Fatalf("building uplink: %v", err)
		}
		if err := f.store.CreateNetUplink(f.ctx, domain.SystemActor, up); err != nil {
			t.Fatalf("creating uplink: %v", err)
		}
	}

	attach := func(assetID, groupID string) {
		t.Helper()
		att, err := domain.NewNetAttachment(store.NewID(), assetID, groupID, domain.PlaneData, now)
		if err != nil {
			t.Fatalf("building attachment: %v", err)
		}
		if err := f.store.CreateNetAttachment(f.ctx, domain.SystemActor, att, nil); err != nil {
			t.Fatalf("creating attachment: %v", err)
		}
	}
	for _, hv := range sideA {
		attach(f.refs.Assets[hv], a.ID)
	}
	for _, hv := range sideB {
		attach(f.refs.Assets[hv], b.ID)
	}
}

// TestPartitionedProviderIsNotReportedAsFailed is the defect M3's gate was
// hiding: Seam 2 forces a partitioned provider's status to down before
// Propagate runs, which is right for deciding the consumer's fate and wrong
// for explaining it. Until this test, the report said "hard dependency on
// pgsql-core is down" about a database that was up, healthy and not even
// listed among the affected services -- during an incident, that sends
// somebody to inspect a green service while the actual break is in the path.
//
// sso lives on hv-03 and depends hard on pgsql-core, which lives entirely on
// hv-01 and hv-02. Losing the transit group cuts the two halves apart without
// isolating either, so sso is the consumer whose provider is fine.
func TestPartitionedProviderIsNotReportedAsFailed(t *testing.T) {
	f := newFixture(t)
	declarePartitionableTopology(t, f)

	result, byCode := f.simulate(t, 180, "fw-edge-1")

	sso, ok := byCode["sso"]
	if !ok {
		t.Fatalf("sso not affected by the partition; got %d services: %v",
			len(result.Services), codesOf(result.Services))
	}

	// The premise: the provider really is healthy. If pgsql-core were itself
	// affected, "is down" would have been the truth and this test would be
	// asserting nothing.
	if pg, affected := byCode["pgsql-core"]; affected {
		t.Fatalf("premise broken: pgsql-core is itself %s (%s), so the provider is "+
			"not healthy and this scenario proves nothing", pg.Status, pg.Reason)
	}

	if strings.Contains(sso.Reason, "is down") {
		t.Errorf("sso reason says its provider is down, but pgsql-core is healthy "+
			"and merely unreachable -- this sends an operator to the wrong service.\n got: %q", sso.Reason)
	}
	if !strings.Contains(sso.Reason, "unreachable") {
		t.Errorf("sso reason does not say the provider is unreachable\n got: %q", sso.Reason)
	}
	if sso.Cause != impact.CauseReachability {
		t.Errorf("sso cause = %q, want %q: the edge propagated it, but the network "+
			"broke it, and Cause is what tells an operator where to go",
			sso.Cause, impact.CauseReachability)
	}

	// And the partition must be reported in its own right, not only implied by
	// a service's reason string.
	if len(result.Partitions) == 0 {
		t.Error("no partition reported, so the reason string above is the only " +
			"evidence the network was involved at all")
	}
}

func codesOf(in []impact.ServiceImpact) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, s.Code+"="+string(s.Status))
	}
	return out
}

// TestPartitionThroughARouteIsNotReportedAsFailed is the same defect one hop
// further down, and it survives the direct-endpoint fix.
//
// routeStatuses folds a route's member reachability into the route's own
// status before phasePropagate ever sees it, so a route whose backends are
// merely unreachable from its frontend arrives looking exactly like a route
// whose backends crashed. Comparing the provider's status against the reach
// level of the consumer's own hop cannot tell them apart: that hop is fine.
// Only the network-free status can.
//
// The fixture's route runs haproxy-edge (hv-02) to a pool of orders-api and
// orders-web (hv-01), consumed hard by partner-gateway (hv-03). Splitting
// hv-01 from hv-02 and hv-03 leaves the consumer able to reach the proxy and
// the proxy unable to reach anything it fronts.
func TestPartitionThroughARouteIsNotReportedAsFailed(t *testing.T) {
	f := newFixture(t)
	declareSplitTopology(t, f, []string{"hv-02", "hv-03"}, []string{"hv-01"})

	result, byCode := f.simulate(t, 180, "fw-edge-1")

	gw, ok := byCode["partner-gateway"]
	if !ok {
		t.Fatalf("partner-gateway not affected; got: %v", codesOf(result.Services))
	}

	// The premise: the proxy itself is fine. If haproxy-edge were down, "is
	// down" would be the truth about the route and this proves nothing.
	if hap, affected := byCode["haproxy-edge"]; affected {
		t.Fatalf("premise broken: haproxy-edge is itself %s (%s)", hap.Status, hap.Reason)
	}

	if strings.Contains(gw.Reason, "is down") {
		t.Errorf("partner-gateway reason says its route is down, but the proxy is healthy "+
			"and the break is between it and its backends\n got: %q", gw.Reason)
	}
	if !strings.Contains(gw.Reason, "unreachable") {
		t.Errorf("partner-gateway reason does not name the break as a reachability one\n got: %q", gw.Reason)
	}
	if gw.Cause != impact.CauseReachability {
		t.Errorf("partner-gateway cause = %q, want %q", gw.Cause, impact.CauseReachability)
	}
}
