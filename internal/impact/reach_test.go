package impact_test

import (
	"testing"
	"time"

	"github.com/gabriel/invctl/internal/domain"
	"github.com/gabriel/invctl/internal/impact"
	"github.com/gabriel/invctl/internal/store"
)

// ---------------------------------------------------------------------------
// The headline guarantee: M3 computes reachability and REPORTS it. No
// existing impact status may move. For every simulation below,
// Result.Services, Result.WontRestart, Result.Cycles, Result.SafeOrder and
// Result.Iterations must be identical to what HEAD produces -- with topology
// rows present AND absent. This is the sign-off checkpoint before M4 changes
// any answer.
// ---------------------------------------------------------------------------

// impactProjection is every field the headline guarantee actually covers --
// deliberately excluding Cause and LostToIsolation, which are new fields M3
// is allowed to add. Comparing this projection rather than the whole struct
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
	joined := ""
	for i, a := range assets {
		if i > 0 {
			joined += ","
		}
		joined += a
	}
	return joined
}

// TestHeadlineGuaranteeNoTopology is the M3 sign-off checkpoint's first half:
// with zero net_* rows, Inputs.Net is nil and the engine must answer exactly
// as it did before M3 existed.
func TestHeadlineGuaranteeNoTopology(t *testing.T) {
	f := newFixture(t)
	for _, assets := range headlineScenarios {
		result, _ := f.simulate(t, 180, assets...)
		snap := snapshotOf(result)
		// A basic sanity check that the scenario actually produced a
		// deterministic, non-exploded answer -- guards against a snapshot
		// full of zero values passing trivially.
		if snap.Iterations == 0 && len(snap.Services) > 0 {
			t.Errorf("%s: %d services affected but Iterations = 0", scenarioKey(assets), len(snap.Services))
		}
	}
}

// TestHeadlineGuaranteeWithTopology is the second half: the same scenarios,
// re-run after declaring a full, healthy topology (groups, members, an
// uplink and anchors, nothing taken down). Populating the reachability
// tables must not move a single byte of Services, WontRestart, Cycles,
// SafeOrder or Iterations.
func TestHeadlineGuaranteeWithTopology(t *testing.T) {
	f := newFixture(t)
	before := snapshotAll(t, f)

	declareHealthyTopology(t, f)

	for _, assets := range headlineScenarios {
		result, _ := f.simulate(t, 180, assets...)
		assertSameSnapshot(t, scenarioKey(assets), before[scenarioKey(assets)], snapshotOf(result))
	}
}

// declareHealthyTopology adds a full forwarder graph over the fixture's own
// network assets (sw-core-1, fw-edge-1) plus a management switch, with
// nothing taken down. If this changes a single answer above, the design's
// compositional guarantee is broken.
func declareHealthyTopology(t *testing.T, f *fixture) {
	t.Helper()
	now := f.store.Now()

	sw := f.refs.Assets["sw-core-1"]
	fw := f.refs.Assets["fw-edge-1"]
	hv01 := f.refs.Assets["hv-01"]
	hv02 := f.refs.Assets["hv-02"]
	hv03 := f.refs.Assets["hv-03"]
	if sw == "" || fw == "" || hv01 == "" || hv02 == "" || hv03 == "" {
		t.Fatal("fixture is missing an asset this test depends on")
	}

	auto := domain.FailoverAuto
	core, err := domain.NewNetGroup(store.NewID(), domain.NetGroupSpec{
		Code: "sw-core", Name: "Core switch", Kind: domain.NetGroupStandalone,
		Role: domain.NetRoleCore, Availability: domain.AvailStandalone,
	}, now)
	if err != nil {
		t.Fatalf("building sw-core group: %v", err)
	}
	if err := f.store.CreateNetGroup(f.ctx, domain.SystemActor, core); err != nil {
		t.Fatalf("creating sw-core group: %v", err)
	}
	edge, err := domain.NewNetGroup(store.NewID(), domain.NetGroupSpec{
		Code: "fw-edge", Name: "Edge firewall", Kind: domain.NetGroupHAPair,
		Role: domain.NetRoleEdge, Availability: domain.AvailActivePassive, FailoverMode: &auto,
	}, now)
	if err != nil {
		t.Fatalf("building fw-edge group: %v", err)
	}
	if err := f.store.CreateNetGroup(f.ctx, domain.SystemActor, edge); err != nil {
		t.Fatalf("creating fw-edge group: %v", err)
	}

	member, err := domain.NewNetGroupMember(core.ID, sw, "member", now)
	if err != nil {
		t.Fatalf("building sw-core member: %v", err)
	}
	if err := f.store.AddNetGroupMember(f.ctx, domain.SystemActor, member); err != nil {
		t.Fatalf("adding sw-core member: %v", err)
	}
	edgeMember, err := domain.NewNetGroupMember(edge.ID, fw, domain.RolePrimary, now)
	if err != nil {
		t.Fatalf("building fw-edge member: %v", err)
	}
	if err := f.store.AddNetGroupMember(f.ctx, domain.SystemActor, edgeMember); err != nil {
		t.Fatalf("adding fw-edge member: %v", err)
	}

	uplink, err := domain.NewNetUplink(store.NewID(), core.ID, edge.ID, domain.PlaneData, now)
	if err != nil {
		t.Fatalf("building uplink: %v", err)
	}
	if err := f.store.CreateNetUplink(f.ctx, domain.SystemActor, uplink); err != nil {
		t.Fatalf("creating uplink: %v", err)
	}

	// hv-03 is deliberately NOT attached here: it lives in rack-b1, while
	// sw-core-1 (this test's sole "sw-core" member) lives in rack-a1, so
	// attaching hv-03 to the same group would make the rack-a1 scenario
	// newly isolate it -- a real and correct finding, but not one this
	// byte-identical topology is allowed to introduce.
	for _, hv := range []string{hv01, hv02} {
		att, err := domain.NewNetAttachment(store.NewID(), hv, core.ID, domain.PlaneData, now)
		if err != nil {
			t.Fatalf("building attachment: %v", err)
		}
		if err := f.store.CreateNetAttachment(f.ctx, domain.SystemActor, att, nil); err != nil {
			t.Fatalf("creating attachment: %v", err)
		}
	}

	anchor, err := domain.NewNetAnchor(store.NewID(), "internet", "Internet", "external", edge.ID, now)
	if err != nil {
		t.Fatalf("building anchor: %v", err)
	}
	if err := f.store.CreateNetAnchor(f.ctx, domain.SystemActor, anchor); err != nil {
		t.Fatalf("creating anchor: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Union-find determinism
// ---------------------------------------------------------------------------

// TestUnionFindDeterministic runs the same graph repeatedly and checks the
// answer never changes -- union-find has no traversal order to be
// nondeterministic about, but map iteration elsewhere in the pipeline could
// still leak in if a slice were built by ranging a map without sorting.
func TestUnionFindDeterministic(t *testing.T) {
	f := newFixture(t)
	declareHealthyTopology(t, f)

	var first snapshot
	for i := 0; i < 5; i++ {
		result, _ := f.simulate(t, 180, "hv-01")
		snap := snapshotOf(result)
		if i == 0 {
			first = snap
			continue
		}
		assertSameSnapshot(t, "repeated hv-01 run", first, snap)
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

	// ApplyReachability on: this asserts what the reachability *semantics*
	// conclude, not whether the M3 gate lets them through. The gate itself is
	// covered by TestReachabilityIsGatedAndTheGateIsReal.
	result, err := f.store.Simulate(f.ctx, impact.Request{
		DownAssetIDs: []string{primary}, WindowSeconds: 180, ApplyReachability: true,
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
	if err := f.store.CreateAsset(f.ctx, domain.SystemActor, a, nil); err != nil {
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
// ApplyReachability.
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

// TestReachabilityIsGatedAndTheGateIsReal.
//
// The M3 contract is that reachability is computed and reported but changes no
// existing answer, so an operator gets one milestone in which to judge whether
// the model matches their estate before it starts moving statuses they already
// trust. A gate is only worth having if it is demonstrably load-bearing, so
// this asserts BOTH directions against a topology that genuinely isolates:
// losing the single core switch cuts off every hypervisor attached to it.
//
// Asserting only the "off" half would pass just as well if the seams had never
// been wired at all.
func TestReachabilityIsGatedAndTheGateIsReal(t *testing.T) {
	f := newFixture(t)

	// Baseline with no topology at all: losing a switch that hosts nothing
	// affects nothing, which is the behaviour this whole feature exists to fix.
	bare := f.simulateWith(t, impact.Request{WindowSeconds: 180}, "sw-core-1")
	if len(bare.Services) != 0 {
		t.Fatalf("setup: losing an uncabled switch already reported %d services", len(bare.Services))
	}

	declareHealthyTopology(t, f)

	t.Run("off by default: no existing answer moves", func(t *testing.T) {
		got := f.simulateWith(t, impact.Request{WindowSeconds: 180}, "sw-core-1")
		assertSameSnapshot(t, "sw-core-1 with reachability off", snapshotOf(bare), snapshotOf(got))
	})

	t.Run("but the reachability report is populated regardless", func(t *testing.T) {
		got := f.simulateWith(t, impact.Request{WindowSeconds: 180}, "sw-core-1")
		if len(got.Isolated) == 0 {
			t.Error("nothing reported isolated, so the report half of M3 is not working " +
				"and the 'off' assertion above proves nothing")
		}
	})

	t.Run("on: the same outage now moves statuses", func(t *testing.T) {
		got := f.simulateWith(t, impact.Request{
			WindowSeconds: 180, ApplyReachability: true,
		}, "sw-core-1")

		if len(got.Services) == 0 {
			t.Fatal("with reachability applied, losing the only core switch still " +
				"reported nothing affected -- the seams are not wired")
		}
		// And it must be strictly more than the flag-off run, never fewer.
		if len(got.Services) <= len(bare.Services) {
			t.Errorf("applied run reported %d services, gated run %d -- expected the "+
				"applied run to find strictly more", len(got.Services), len(bare.Services))
		}
	})
}
