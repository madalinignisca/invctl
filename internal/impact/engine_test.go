package impact_test

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gabriel/invctl/internal/domain"
	"github.com/gabriel/invctl/internal/impact"
	"github.com/gabriel/invctl/internal/seed"
	"github.com/gabriel/invctl/internal/store"
)

// The engine is tested against the seeded fixture graph rather than against
// hand-built mocks, for two reasons: the fixture is what the demo shows, so a
// passing test means the demo is correct; and a mock would let the engine and
// the store drift apart silently.

type fixture struct {
	store *store.SQLStore
	refs  *seed.Refs
	ctx   context.Context
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "impact.db")
	db, err := store.Open(store.DriverSQLite, dsn)
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	s := store.New(db)
	refs, err := seed.Load(ctx, s)
	if err != nil {
		t.Fatalf("seeding fixture: %v", err)
	}
	return &fixture{store: s, refs: refs, ctx: ctx}
}

// simulate takes down the named assets and returns the result keyed by service
// code, which is what the assertions actually care about.
func (f *fixture) simulate(t *testing.T, windowSeconds int, assetNames ...string) (impact.Result, map[string]impact.ServiceImpact) {
	t.Helper()
	ids := make([]string, 0, len(assetNames))
	for _, name := range assetNames {
		id, ok := f.refs.Assets[name]
		if !ok {
			t.Fatalf("unknown asset %q in fixture", name)
		}
		ids = append(ids, id)
	}
	result, err := f.store.Simulate(f.ctx, impact.Request{
		DownAssetIDs:  ids,
		WindowSeconds: windowSeconds,
	})
	if err != nil {
		t.Fatalf("simulating outage of %v: %v", assetNames, err)
	}
	byCode := make(map[string]impact.ServiceImpact, len(result.Services))
	for _, s := range result.Services {
		byCode[s.Code] = s
	}
	return result, byCode
}

// assertStatus checks a service's status, treating an absent service as ok --
// the result only lists services that are affected.
func assertStatus(t *testing.T, byCode map[string]impact.ServiceImpact, code string, want domain.Status) {
	t.Helper()
	got, present := byCode[code]
	if !present {
		if want != domain.StatusOK {
			t.Errorf("%s: not reported as affected, want %s", code, want)
		}
		return
	}
	if got.Status != want {
		t.Errorf("%s: status = %s, want %s (reason: %s)", code, got.Status, want, got.Reason)
	}
}

// TestQuorumSurvivesOneNode is the headline case from HANDOVER §3.3: losing
// one of three Vault nodes must report ok. If this regresses, every impact
// report becomes noise and people stop reading it.
func TestQuorumSurvivesOneNode(t *testing.T) {
	f := newFixture(t)
	_, byCode := f.simulate(t, 180, "vm-vault-1")

	assertStatus(t, byCode, "vault", domain.StatusOK)

	// And nothing downstream should have moved either.
	assertStatus(t, byCode, "sso", domain.StatusOK)
	assertStatus(t, byCode, "pgsql-core", domain.StatusOK)
}

// TestQuorumLostWithTwoNodes is the other half: two of three is below the
// threshold, so the cluster is genuinely down.
func TestQuorumLostWithTwoNodes(t *testing.T) {
	f := newFixture(t)
	_, byCode := f.simulate(t, 180, "vm-vault-1", "vm-vault-2")

	assertStatus(t, byCode, "vault", domain.StatusDown)
	got := byCode["vault"]
	if got.LostInstances != 2 || got.TotalInstances != 3 {
		t.Errorf("vault: lost %d of %d, want 2 of 3", got.LostInstances, got.TotalInstances)
	}
}

// TestActivePassiveManualFailoverIsDegraded covers HANDOVER §10's requirement
// that losing the primary of a manually-promoted pair reports degraded, not
// down: the data is still there, but somebody has to be paged.
func TestActivePassiveManualFailoverIsDegraded(t *testing.T) {
	f := newFixture(t)
	_, byCode := f.simulate(t, 180, "vm-db-1")

	assertStatus(t, byCode, "pgsql-core", domain.StatusDegraded)
	if reason := byCode["pgsql-core"].Reason; !strings.Contains(reason, "manual") {
		t.Errorf("pgsql-core reason = %q, want it to mention manual promotion", reason)
	}

	// A hard dependency on a degraded provider degrades the consumer -- it
	// does not take it down.
	assertStatus(t, byCode, "sso", domain.StatusDegraded)
	assertStatus(t, byCode, "orders-api", domain.StatusDegraded)
}

// TestActivePassiveLosingStandbyIsFine checks the asymmetry: the standby is
// not serving, so losing it costs nothing right now.
func TestActivePassiveLosingStandbyIsFine(t *testing.T) {
	f := newFixture(t)
	_, byCode := f.simulate(t, 180, "vm-db-2")

	assertStatus(t, byCode, "pgsql-core", domain.StatusOK)
}

// TestRouteIsANodeNotAPassthrough is the case HANDOVER §6 calls out: the proxy
// is perfectly healthy, but every member of its backend pool sits on the host
// being rebooted, so anything depending on the routed hostname is down.
//
// This is the finding a spreadsheet cannot produce.
func TestRouteIsANodeNotAPassthrough(t *testing.T) {
	f := newFixture(t)
	_, byCode := f.simulate(t, 180, "vm-app-1")

	// The proxy itself runs on vm-proxy-1 and is untouched.
	assertStatus(t, byCode, "haproxy-edge", domain.StatusOK)

	// Both pool members live on vm-app-1.
	assertStatus(t, byCode, "orders-api", domain.StatusDown)
	assertStatus(t, byCode, "orders-web", domain.StatusDown)

	// So the consumer of the route goes down even though the proxy is up.
	assertStatus(t, byCode, "partner-gateway", domain.StatusDown)
	if via := byCode["partner-gateway"].Via; !strings.Contains(via, "route") {
		t.Errorf("partner-gateway Via = %q, want it to name the route", via)
	}
}

// TestStartupDependencyDoesNotChangeStatus covers the nature the handover
// calls the highest-value one. SSO is serving right now with Vault gone; it
// simply will not come back. Reporting it as down would be wrong, reporting
// nothing would hide a landmine.
func TestStartupDependencyDoesNotChangeStatus(t *testing.T) {
	f := newFixture(t)
	result, byCode := f.simulate(t, 180, "vm-vault-1", "vm-vault-2", "vm-vault-3")

	assertStatus(t, byCode, "vault", domain.StatusDown)

	// SSO has a startup dependency on Vault and no other unmet dependency, so
	// its status must not move.
	assertStatus(t, byCode, "sso", domain.StatusOK)

	codes := wontRestartCodes(result)
	if !contains(codes, "sso") {
		t.Errorf("WontRestart = %v, want it to contain sso", codes)
	}
	if !contains(codes, "pgsql-core") {
		t.Errorf("WontRestart = %v, want it to contain pgsql-core", codes)
	}
}

// TestAsyncToleranceRespectsWindow proves WindowSeconds is not decorative: the
// same outage is invisible for a short reboot and degrading for a long one.
func TestAsyncToleranceRespectsWindow(t *testing.T) {
	f := newFixture(t)

	// The fixture's async edge tolerates 300 seconds of buffering.
	t.Run("short reboot is absorbed", func(t *testing.T) {
		_, byCode := f.simulate(t, 180, "vm-queue-1")
		assertStatus(t, byCode, "rabbitmq", domain.StatusDown)
		assertStatus(t, byCode, "orders-api", domain.StatusOK)
	})

	t.Run("long maintenance window is not", func(t *testing.T) {
		_, byCode := f.simulate(t, 2700, "vm-queue-1")
		assertStatus(t, byCode, "rabbitmq", domain.StatusDown)
		assertStatus(t, byCode, "orders-api", domain.StatusDegraded)
	})
}

// TestOptionalDependencyHasNoEffect: losing metrics is not an outage, and
// reporting it as one is how a report loses its audience.
func TestOptionalDependencyHasNoEffect(t *testing.T) {
	f := newFixture(t)
	_, byCode := f.simulate(t, 180, "vm-k8s-1", "vm-k8s-2")

	assertStatus(t, byCode, "mimir-ingester", domain.StatusDown)
	assertStatus(t, byCode, "orders-api", domain.StatusOK)
}

// TestShardedLosesOneShard: a sharded service with one dead shard is degraded,
// because that slice of the keyspace is gone while the rest still answers.
func TestShardedLosesOneShard(t *testing.T) {
	f := newFixture(t)
	_, byCode := f.simulate(t, 180, "vm-k8s-1")

	assertStatus(t, byCode, "mimir-ingester", domain.StatusDegraded)
}

// TestContainmentResolvesThroughClosure proves that taking away a hypervisor,
// a rack or the whole site all work through the same code path -- the closure
// table is what makes "reboot this VM" and "this rack loses power" identical
// to the engine.
func TestContainmentResolvesThroughClosure(t *testing.T) {
	f := newFixture(t)

	t.Run("hypervisor takes its guests", func(t *testing.T) {
		_, byCode := f.simulate(t, 180, "hv-01")
		// hv-01 hosts vm-vault-1, vm-db-1 and vm-app-1.
		assertStatus(t, byCode, "vault", domain.StatusOK) // 2 of 3 survive
		assertStatus(t, byCode, "pgsql-core", domain.StatusDegraded)
		assertStatus(t, byCode, "orders-api", domain.StatusDown)
		assertStatus(t, byCode, "partner-gateway", domain.StatusDown)
	})

	t.Run("rack takes its hypervisors", func(t *testing.T) {
		_, byCode := f.simulate(t, 180, "rack-a1")
		// rack-a1 holds hv-01 and hv-02: two of three Vault nodes and both
		// database instances.
		assertStatus(t, byCode, "vault", domain.StatusDown)
		assertStatus(t, byCode, "pgsql-core", domain.StatusDown)
		assertStatus(t, byCode, "sso", domain.StatusDown) // hard dep on the database
	})

	t.Run("site takes everything", func(t *testing.T) {
		result, _ := f.simulate(t, 180, "dc-oslo")
		if len(result.Services) < 8 {
			t.Errorf("losing the site affected only %d services, want the whole estate",
				len(result.Services))
		}
	})
}

// TestCycleDoesNotHang is HANDOVER §10's explicit requirement. The fixture
// contains sso -> pgsql-core -> vault -> sso, which a naive traversal would
// loop on forever.
func TestCycleDoesNotHang(t *testing.T) {
	f := newFixture(t)
	result, _ := f.simulate(t, 180, "rack-a1")

	// Terminating at all is the point; doing so quickly shows the fixed point
	// is converging rather than grinding to the iteration cap.
	if result.Iterations >= 20 {
		t.Errorf("fixed point took %d iterations, suggesting it did not converge", result.Iterations)
	}

	found := false
	for _, cycle := range result.Cycles {
		joined := strings.Join(cycle, ">")
		if strings.Contains(joined, "vault") && strings.Contains(joined, "sso") {
			found = true
		}
	}
	if !found {
		t.Errorf("cycles = %v, want the sso/vault loop reported as a finding", result.Cycles)
	}
}

// TestSafeOrderIsLeafFirst: consumers shut down before the things they depend
// on, so the order can be followed top to bottom.
func TestSafeOrderIsLeafFirst(t *testing.T) {
	f := newFixture(t)
	result, _ := f.simulate(t, 180, "hv-01")

	position := map[string]int{}
	for i, code := range result.SafeOrder {
		position[code] = i
	}
	// orders-web depends on orders-api, so it must be stopped first.
	web, hasWeb := position["orders-web"]
	api, hasAPI := position["orders-api"]
	if hasWeb && hasAPI && web > api {
		t.Errorf("safe order = %v; orders-web depends on orders-api and must come first",
			result.SafeOrder)
	}
}

// TestUnaffectedServicesAreNotReported: an impact report that lists everything
// is the same as no report at all.
func TestUnaffectedServicesAreNotReported(t *testing.T) {
	f := newFixture(t)
	result, byCode := f.simulate(t, 180, "vm-dev-1")

	assertStatus(t, byCode, "orders-api-dev", domain.StatusDown)
	if len(result.Services) != 1 {
		codes := make([]string, 0, len(result.Services))
		for _, s := range result.Services {
			codes = append(codes, s.Code)
		}
		sort.Strings(codes)
		t.Errorf("losing an isolated dev VM affected %v, want only orders-api-dev", codes)
	}
}

// TestNoOutageIsNoImpact guards the degenerate case.
func TestNoOutageIsNoImpact(t *testing.T) {
	f := newFixture(t)
	result, _ := f.simulate(t, 180)

	if len(result.Services) != 0 {
		t.Errorf("empty request produced %d impacts, want none", len(result.Services))
	}
	if len(result.WontRestart) != 0 {
		t.Errorf("empty request produced %d wont-restart entries, want none", len(result.WontRestart))
	}
}

// TestDisabledInstanceIsNotCapacity: an instance nobody expects to be running
// is neither capacity to lose nor a loss when its host goes away.
func TestDisabledInstanceIsNotCapacity(t *testing.T) {
	f := newFixture(t)

	instances, err := f.store.ListInstancesByService(f.ctx, f.refs.Services["vault"])
	if err != nil {
		t.Fatalf("listing vault instances: %v", err)
	}
	// Disable the node on hv-03, leaving a two-node cluster.
	var target string
	for _, i := range instances {
		if i.HostName == "vm-vault-3" {
			target = i.ID
		}
	}
	if target == "" {
		t.Fatal("fixture has no vault instance on vm-vault-3")
	}
	if err := f.store.DisableInstance(f.ctx, domain.SystemActor, target); err != nil {
		t.Fatalf("disabling instance: %v", err)
	}

	// Quorum of a two-node cluster is 2, so losing one is now fatal where it
	// was survivable before.
	_, byCode := f.simulate(t, 180, "vm-vault-1")
	assertStatus(t, byCode, "vault", domain.StatusDown)
	if got := byCode["vault"].TotalInstances; got != 2 {
		t.Errorf("vault total instances = %d, want 2 after disabling one", got)
	}
}

func wontRestartCodes(r impact.Result) []string {
	codes := make([]string, 0, len(r.WontRestart))
	for _, s := range r.WontRestart {
		codes = append(codes, s.Code)
	}
	sort.Strings(codes)
	return codes
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// TestWontRestartExcludesAlreadyDownServices: the landmine list is for things
// that are serving now and will not come back. A service already reported as
// down is neither, and listing it in both places buries the finding that the
// section exists to surface.
func TestWontRestartExcludesAlreadyDownServices(t *testing.T) {
	f := newFixture(t)

	// Losing rack-a1 takes both database instances and two of three Vault
	// nodes, so pgsql-core is down outright -- and it also has a startup
	// dependency on Vault.
	result, byCode := f.simulate(t, 180, "rack-a1")

	assertStatus(t, byCode, "pgsql-core", domain.StatusDown)
	for _, s := range result.WontRestart {
		if s.Status == domain.StatusDown {
			t.Errorf("%s is down but appears in WontRestart; that list is for services still serving", s.Code)
		}
	}

	// And the positive case still holds: losing only the Vault nodes leaves
	// pgsql-core serving with an unmet startup dependency.
	result2, byCode2 := f.simulate(t, 180, "vm-vault-1", "vm-vault-2", "vm-vault-3")
	assertStatus(t, byCode2, "pgsql-core", domain.StatusOK)
	if !contains(wontRestartCodes(result2), "pgsql-core") {
		t.Errorf("WontRestart = %v, want pgsql-core", wontRestartCodes(result2))
	}
}

// TestWontRestartIsReachableFromASingleHost. The simulate button on an asset
// page takes one asset, so if no single-host outage ever produces a
// WontRestart entry, the highest-value output of the engine is unreachable in
// the product regardless of the engine being correct.
func TestWontRestartIsReachableFromASingleHost(t *testing.T) {
	f := newFixture(t)
	result, byCode := f.simulate(t, 180, "vm-sso-1")

	assertStatus(t, byCode, "sso", domain.StatusDown)

	// The storefront caches its OIDC keys at boot, so it keeps serving. It
	// degrades only because sign-ins now fail through orders-api's soft
	// dependency on SSO -- it is still up.
	assertStatus(t, byCode, "orders-web", domain.StatusDegraded)

	// And it will not come back if anything restarts it, which is the finding
	// the status column alone would never show.
	codes := wontRestartCodes(result)
	if !contains(codes, "orders-web") {
		t.Errorf("WontRestart = %v, want orders-web", codes)
	}
}

// TestWontRestartNamesTheStartupDependency. A service can be degraded because
// of one edge and unable to restart because of a completely different one.
// Reporting the status edge in the landmine list sends the operator to the
// wrong dependency, which is worse than reporting nothing.
func TestWontRestartNamesTheStartupDependency(t *testing.T) {
	f := newFixture(t)
	result, byCode := f.simulate(t, 180, "vm-sso-1")

	// Its status came from the hard edge to orders-api...
	if via := byCode["orders-web"].Via; !strings.Contains(via, "orders-api") {
		t.Errorf("status Via = %q, want it to name orders-api", via)
	}

	// ...but the reason it will not restart is the startup edge to SSO.
	var found bool
	for _, s := range result.WontRestart {
		if s.Code != "orders-web" {
			continue
		}
		found = true
		if !strings.Contains(s.Via, "sso") {
			t.Errorf("WontRestart Via = %q, want it to name the unmet startup dependency (sso)", s.Via)
		}
		if !strings.Contains(s.Reason, "startup dependency") {
			t.Errorf("WontRestart Reason = %q, want it to explain the startup dependency", s.Reason)
		}
	}
	if !found {
		t.Fatal("orders-web is not in WontRestart")
	}
}

// TestRetiredPoolMemberIsNotCapacity. LoadGraph excludes retired services but
// still loads every endpoint and pool member, so a member pointing at a retired
// service had an unknown status -- which scored as alive and made a dead pool
// look merely degraded.
func TestRetiredPoolMemberIsNotCapacity(t *testing.T) {
	f := newFixture(t)

	// Retire one of the two backends behind the edge route.
	if err := f.store.RetireService(f.ctx, domain.SystemActor, f.refs.Services["orders-web"]); err != nil {
		t.Fatalf("retiring orders-web: %v", err)
	}

	// vm-app-1 hosts the only remaining live backend, orders-api.
	_, byCode := f.simulate(t, 180, "vm-app-1")

	assertStatus(t, byCode, "orders-api", domain.StatusDown)

	// Every live member of the pool is gone, so the route is down and its hard
	// consumer is down -- not degraded on the strength of a retired member.
	assertStatus(t, byCode, "partner-gateway", domain.StatusDown)
}
