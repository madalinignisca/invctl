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
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/madalinignisca/invctl/internal/domain"
)

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

// apiFixture is a small estate with the one shape that makes AllowsAll differ
// from AllowsAny: two core switches in {prod, dev}.
type apiFixture struct {
	s     *SQLStore
	ctx   context.Context
	actor domain.Actor

	envs     map[string]string // code -> id
	assets   map[string]string // name -> id
	services map[string]string // code -> id
}

func newAPIFixture(t *testing.T, e Engine) *apiFixture {
	t.Helper()
	s, ctx := newStore(t, e)
	f := &apiFixture{
		s: s, ctx: ctx, actor: testActor,
		envs:     map[string]string{},
		assets:   map[string]string{},
		services: map[string]string{},
	}

	// EVERY ENVIRONMENT DIFFERS ON EVERY ATTRIBUTE, and that is load-bearing
	// rather than decorative.
	//
	// The shared mustEnvironment helper hardcodes in_scope = true and
	// criticality = 2, so an estate built with it has six environments that are
	// identical except for their codes. Against that fixture, a scope predicate
	// weakened with an extra qualifier -- `AND e2.in_scope = TRUE`, or anything
	// on criticality or role -- passes every test on both engines, because the
	// qualifier is true of every row. In production the same mutation means an
	// environment flagged in_scope = false stops counting against a token, and
	// AllowsAll degrades toward AllowsAny for precisely the assets that straddle
	// a monitored and an unmonitored environment: the boundary case the whole
	// predicate exists for.
	//
	// So `dev` is deliberately in_scope = false while `prod` is true, and
	// sw-core-1 below straddles the two.
	for _, env := range []struct {
		code, role  string
		inScope     bool
		criticality int
	}{
		{"prod", domain.EnvRoleProduction, true, 1},
		{"dev", domain.EnvRoleDev, false, 4},
		{"staging", domain.EnvRoleStaging, false, 3},
		{"shared", domain.EnvRoleShared, true, 5},
		{"transit", domain.EnvRoleTransit, false, 2},
		{"dr", domain.EnvRoleDR, true, 5},
	} {
		f.environment(t, env.code, env.role, env.inScope, env.criticality)
	}

	// Placement: a site holding a rack holding the boxes. Both are in `shared`
	// so that they are visible to somebody -- an asset in no environment is
	// visible to nobody, which is tested separately.
	site := f.asset(t, domain.KindSite, "dc-1", nil, "shared")
	rack := f.asset(t, domain.KindRack, "r14", &site, "shared")

	// The boundary devices. In BOTH prod and dev, which is what a {dev} token
	// must not be able to read.
	f.asset(t, domain.KindSwitch, "sw-core-1", &rack, "prod", "dev")
	f.asset(t, domain.KindSwitch, "sw-core-2", &rack, "prod", "dev")

	// A hypervisor in the rack and a VM on it. The VM's site and rack are two
	// and three hops up, which is why placement resolves through asset_closure.
	hv := f.asset(t, domain.KindHypervisor, "hv-01", &rack, "prod")
	f.asset(t, domain.KindVM, "vm-db-2", &hv, "prod")
	f.asset(t, domain.KindServer, "dev-box", &rack, "dev")
	f.asset(t, domain.KindServer, "staging-box", &rack, "staging")

	// THE OTHER STRADDLE, and in a real estate it is the commonest one there
	// is: a management or utility environment PLUS a workload environment. A
	// jump host lives in `shared` because operations reaches it from there, and
	// in `prod` because that is what it reaches.
	//
	// Without it, `shared` appears only on dc-1 and r14 -- assets that are in
	// nothing else -- and no test can tell the correct predicate apart from one
	// weakened with `AND e2.role <> 'shared'` ("surely a shared environment
	// should not count against a token"). That mutation survived the entire
	// suite once, and it made dc-1 and r14 fully readable rows for a {dev}
	// token: id, name, kind, lifecycle and placement, not merely a leaked name.
	f.asset(t, domain.KindServer, "mgmt-jump", &rack, "shared", "prod")

	// EVERY ENVIRONMENT HAS AN ASSET THAT IS IN IT AND NOTHING ELSE, and the
	// guard below enforces that rather than trusting this comment.
	//
	// An environment with no exclusive member is invisible to the visibility
	// property: nothing changes when a predicate stops counting it, so a
	// mutation keyed on that environment's code, role or criticality survives
	// every test. `transit` and `dr` used to be empty and `staging` had only a
	// straddle-free single member, which is precisely how `AND e2.code <>
	// 'staging'` passed the whole suite while disclosing staging-box as a full
	// row to a {dr} token that owns nothing in the estate. An empty environment
	// is not safe, it is merely not yet exploited -- the disclosure arrives with
	// the first asset somebody puts in it, silently.
	// A THREE-ENVIRONMENT ASSET. Without one, every membership in the estate is
	// a set of size 1 or 2, so a scope of size 3, 4 or 5 can never be the
	// difference between visible and hidden and the subset rule is only ever
	// exercised at its two smallest widths. The reviewer could construct no
	// weakening that lives only there, which is precisely the argument that has
	// cost three rounds already: not safe, merely not yet exploited.
	f.asset(t, domain.KindSwitch, "sw-span-1", &rack, "prod", "dev", "staging")
	f.asset(t, domain.KindFirewall, "fw-transit", &rack, "transit")
	f.asset(t, domain.KindServer, "dr-box", &rack, "dr")

	f.service(t, "billing-api", domain.SvcAPI, "prod", "vm-db-2")
	f.service(t, "dev-api", domain.SvcAPI, "dev", "dev-box")
	// The same coverage rule for the service path, which has its own predicate:
	// one service per remaining environment, so no environment is invisible to
	// the service half of the property either.
	f.bareService(t, "staging-svc", "staging")
	f.bareService(t, "shared-svc", "shared")
	f.bareService(t, "transit-svc", "transit")
	f.bareService(t, "dr-svc", "dr")

	f.address(t, "sw-core-1", "eth0", "10.9.0.1")
	f.address(t, "vm-db-2", "eth0", "10.2.0.14")
	// A SECOND ADDRESS ON ONE HOST. With one address each, "publish the
	// address" and "publish every address" are the same answer, so a
	// decoration that collapsed the list -- MIN(addr_text) with a GROUP BY, say
	// -- would be indistinguishable from the correct one.
	f.address(t, "vm-db-2", "eth1", "10.2.0.15")
	f.address(t, "dev-box", "eth0", "10.3.0.5")
	f.address(t, "staging-box", "eth0", "10.4.0.5")
	f.address(t, "mgmt-jump", "eth0", "10.5.0.5")
	f.address(t, "fw-transit", "eth0", "10.6.0.1")
	f.address(t, "dr-box", "eth0", "10.7.0.5")

	// An FHRP-style virtual address: no interface, therefore no asset,
	// therefore no environment.
	vip, err := domain.NewIPAddress(NewID(), "10.0.0.254", nil, domain.IPRoleVIP)
	if err != nil {
		t.Fatalf("building the virtual address: %v", err)
	}
	if err := s.CreateIPAddress(ctx, testActor, vip); err != nil {
		t.Fatalf("creating the virtual address: %v", err)
	}
	return f
}

// environment creates an environment with the attributes it is given, rather
// than the uniform ones mustEnvironment hardcodes. See the comment in
// newAPIFixture for why the variation matters.
func (f *apiFixture) environment(t *testing.T, code, role string, inScope bool, criticality int) string {
	t.Helper()
	env, err := domain.NewEnvironment(NewID(), code, code, role, inScope, criticality, f.s.Now())
	if err != nil {
		t.Fatalf("building environment %s: %v", code, err)
	}
	if err := f.s.CreateEnvironment(f.ctx, testPermit, env); err != nil {
		t.Fatalf("creating environment %s: %v", code, err)
	}
	f.envs[code] = env.ID
	return env.ID
}

// asset creates an asset in the named environments and remembers its id.
func (f *apiFixture) asset(t *testing.T, kind, name string, parent *string, envCodes ...string) string {
	t.Helper()
	ids := make([]string, 0, len(envCodes))
	for _, code := range envCodes {
		id, ok := f.envs[code]
		if !ok {
			t.Fatalf("the fixture has no environment %q", code)
		}
		ids = append(ids, id)
	}
	id := mustAsset(t, f.s, f.ctx, kind, name, parent, ids...)
	f.assets[name] = id
	return id
}

// service creates a service in one environment and places it on one host.
func (f *apiFixture) service(t *testing.T, code, kind, envCode, host string) string {
	t.Helper()
	svc, err := domain.NewService(NewID(), domain.ServiceSpec{
		Code: code, Name: code, Kind: kind,
		EnvironmentID: f.envs[envCode], Availability: domain.AvailStandalone, Tier: 2,
	}, f.s.Now())
	if err != nil {
		t.Fatalf("building service %s: %v", code, err)
	}
	if err := f.s.CreateService(f.ctx, testActor, svc); err != nil {
		t.Fatalf("creating service %s: %v", code, err)
	}
	f.services[code] = svc.ID

	si, err := domain.NewServiceInstance(NewID(), svc.ID, f.assets[host], "systemd", 0, f.s.Now())
	if err != nil {
		t.Fatalf("building the placement of %s: %v", code, err)
	}
	if err := f.s.CreateInstance(f.ctx, testActor, si); err != nil {
		t.Fatalf("placing %s: %v", code, err)
	}
	return svc.ID
}

// bareService creates a service with no placement. Enough for the paging
// tests, which care about how many rows survive the predicate and not about
// where they run.
func (f *apiFixture) bareService(t *testing.T, code, envCode string) string {
	t.Helper()
	svc, err := domain.NewService(NewID(), domain.ServiceSpec{
		Code: code, Name: code, Kind: domain.SvcAPI,
		EnvironmentID: f.envs[envCode], Availability: domain.AvailStandalone, Tier: 3,
	}, f.s.Now())
	if err != nil {
		t.Fatalf("building service %s: %v", code, err)
	}
	if err := f.s.CreateService(f.ctx, testActor, svc); err != nil {
		t.Fatalf("creating service %s: %v", code, err)
	}
	f.services[code] = svc.ID
	return svc.ID
}

// placementID resolves a service's one placement, so a test can retire the
// placement rather than either entity it joins.
func (f *apiFixture) placementID(t *testing.T, serviceID string) string {
	t.Helper()
	rows, err := f.s.ListInstancesByService(f.ctx, serviceID)
	if err != nil {
		t.Fatalf("loading placements: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("service has %d placements, want exactly 1", len(rows))
	}
	return rows[0].ID
}

// address attaches an interface and an address to an asset.
func (f *apiFixture) address(t *testing.T, host, iface, addr string) string {
	t.Helper()
	i, err := domain.NewInterface(NewID(), f.assets[host], iface, "rj45")
	if err != nil {
		t.Fatalf("building interface %s: %v", iface, err)
	}
	if err := f.s.CreateInterface(f.ctx, testActor, i); err != nil {
		t.Fatalf("creating interface %s: %v", iface, err)
	}
	a, err := domain.NewIPAddress(NewID(), addr, &i.ID, domain.IPRolePrimary)
	if err != nil {
		t.Fatalf("building address %s: %v", addr, err)
	}
	if err := f.s.CreateIPAddress(f.ctx, testActor, a); err != nil {
		t.Fatalf("creating address %s: %v", addr, err)
	}
	return a.ID
}

func mustScope(t *testing.T, codes ...string) domain.EnvironmentScope {
	t.Helper()
	scope, err := domain.NewEnvironmentScope(codes)
	if err != nil {
		t.Fatalf("building the scope %v: %v", codes, err)
	}
	return scope
}

// everyEnvironment is the scope a "sees the whole estate" credential has to
// spell out, because there is no wildcard.
func everyEnvironment(t *testing.T) domain.EnvironmentScope {
	t.Helper()
	return mustScope(t, "prod", "dev", "staging", "shared", "transit", "dr")
}

func containsName(rows []APIAssetRow, name string) bool {
	for _, r := range rows {
		if r.Name == name {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// The visibility property
// ---------------------------------------------------------------------------

// The rule every test below derives its expectation from, rather than restating
// it by hand:
//
//	a token scoped to S sees entity A if and only if A's environment set is
//	non-empty AND A's environment set is a subset of S.
//
// WHY A DERIVED PROPERTY AND NOT MORE NAMED CASES. Three separate weakenings of
// the scope predicate survived a suite full of named tests -- `in_scope`,
// `role <> 'shared'`, `code <> 'staging'` -- and each fix hardened against the
// one just found. A named test can only encode the mutation somebody already
// thought of; this loop encodes the RULE, so it fails for a weakening nobody has
// imagined yet, including one keyed on an environment added years from now.
//
// The named tests stay. They explain WHY a boundary device is hidden in a way a
// loop cannot, and a reader meeting this file needs that. The property is the
// net; the named tests are the documentation.
//
// Expectations are computed from the estate as it actually is, read back out of
// the database. Writing the expected names out by hand would reintroduce the
// exact failure this replaces: a hand-written list does not notice a newly
// added environment, and neither did we, three times.

// estateFacts is the fixture's estate as the database holds it.
type estateFacts struct {
	envCodes []string

	assetEnvs   map[string][]string // asset id -> environment codes
	assetName   map[string]string
	assetLife   map[string]string
	serviceEnv  map[string]string // service id -> its one environment code
	serviceName map[string]string
	serviceLife map[string]string
	addrEnvs    map[string][]string // address id -> its host's environment codes
	addrText    map[string]string
	addrHost    map[string]string // address id -> host asset id, "" when none
	addrLife    map[string]string // address id -> host lifecycle, "" when none
}

// snapshot reads the estate back out of the database.
func (f *apiFixture) snapshot(t *testing.T) *estateFacts {
	t.Helper()
	e := &estateFacts{
		assetEnvs: map[string][]string{}, assetName: map[string]string{},
		assetLife: map[string]string{}, serviceEnv: map[string]string{},
		serviceName: map[string]string{}, serviceLife: map[string]string{},
		addrEnvs: map[string][]string{}, addrText: map[string]string{},
		addrHost: map[string]string{}, addrLife: map[string]string{},
	}

	if err := f.s.read(f.ctx, &e.envCodes, `SELECT code FROM environment ORDER BY code`); err != nil {
		t.Fatalf("reading environments: %v", err)
	}

	var assets []struct {
		ID        string `db:"id"`
		Name      string `db:"name"`
		Lifecycle string `db:"lifecycle"`
	}
	if err := f.s.read(f.ctx, &assets, `SELECT id, name, lifecycle FROM asset`); err != nil {
		t.Fatalf("reading assets: %v", err)
	}
	for _, a := range assets {
		e.assetName[a.ID] = a.Name
		e.assetLife[a.ID] = a.Lifecycle
	}

	var members []struct {
		AssetID string `db:"asset_id"`
		Code    string `db:"code"`
	}
	if err := f.s.read(f.ctx, &members, `
		SELECT ae.asset_id, en.code
		FROM asset_environment ae
		JOIN environment en ON en.id = ae.environment_id`); err != nil {
		t.Fatalf("reading memberships: %v", err)
	}
	for _, m := range members {
		e.assetEnvs[m.AssetID] = append(e.assetEnvs[m.AssetID], m.Code)
	}

	var services []struct {
		ID        string `db:"id"`
		Code      string `db:"code"`
		Lifecycle string `db:"lifecycle"`
		Env       string `db:"env"`
	}
	if err := f.s.read(f.ctx, &services, `
		SELECT sv.id, sv.code, sv.lifecycle, en.code AS env
		FROM service sv
		JOIN environment en ON en.id = sv.environment_id`); err != nil {
		t.Fatalf("reading services: %v", err)
	}
	for _, sv := range services {
		e.serviceName[sv.ID] = sv.Code
		e.serviceLife[sv.ID] = sv.Lifecycle
		e.serviceEnv[sv.ID] = sv.Env
	}

	var addrs []struct {
		ID       string  `db:"id"`
		AddrText string  `db:"addr_text"`
		AssetID  *string `db:"asset_id"`
	}
	if err := f.s.read(f.ctx, &addrs, `
		SELECT ip.id, ip.addr_text, i.asset_id
		FROM ip_address ip
		LEFT JOIN interface i ON i.id = ip.interface_id`); err != nil {
		t.Fatalf("reading addresses: %v", err)
	}
	for _, a := range addrs {
		e.addrText[a.ID] = a.AddrText
		if a.AssetID == nil {
			continue // no interface: no asset, no environments, no reader
		}
		e.addrHost[a.ID] = *a.AssetID
		e.addrLife[a.ID] = e.assetLife[*a.AssetID]
		e.addrEnvs[a.ID] = e.assetEnvs[*a.AssetID]
	}
	return e
}

// scopeCases returns the scopes the property is checked against: every
// singleton, every pair, every complement of a singleton (that is, every
// environment but one), and the full set.
//
// Singletons alone would satisfy the rule as stated, but they only ever
// exercise EQUALITY -- an asset in exactly the one environment. Pairs are what
// exercise the SUBSET half, which is where AllowsAll and AllowsAny differ and
// therefore where the interesting weakenings live.
func scopeCases(codes []string) [][]string {
	sorted := append([]string(nil), codes...)
	sort.Strings(sorted)
	var out [][]string
	for _, c := range sorted {
		out = append(out, []string{c})
	}
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			out = append(out, []string{sorted[i], sorted[j]})
		}
	}
	// The complement of each singleton: every environment but one. These are
	// the widths between a pair and the whole estate, and they are where an
	// asset in three environments is the difference between visible and hidden.
	// Without them the subset rule is only ever exercised at |S| <= 2 and at
	// |S| = everything, and a weakening living in between would pass.
	for _, omit := range sorted {
		var rest []string
		for _, c := range sorted {
			if c != omit {
				rest = append(rest, c)
			}
		}
		if len(rest) > 2 && len(rest) < len(sorted) {
			out = append(out, rest)
		}
	}
	return append(out, sorted)
}

// visibleUnder is the rule itself: non-empty, and a subset.
func visibleUnder(envs, scope []string) bool {
	if len(envs) == 0 {
		return false
	}
	in := make(map[string]bool, len(scope))
	for _, c := range scope {
		in[c] = true
	}
	for _, c := range envs {
		if !in[c] {
			return false
		}
	}
	return true
}

func joined(v []string) string {
	if len(v) == 0 {
		return "none"
	}
	s := append([]string(nil), v...)
	sort.Strings(s)
	return strings.Join(s, ",")
}

// TestAssetVisibilityIsExactlyTheSubsetRule.
func TestAssetVisibilityIsExactlyTheSubsetRule(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)
			facts := f.snapshot(t)

			for _, codes := range scopeCases(facts.envCodes) {
				rows, err := f.s.APIListAssets(f.ctx, APIAssetFilter{
					Scope: mustScope(t, codes...), Limit: 500,
				})
				if err != nil {
					t.Fatalf("listing for scope {%s}: %v", joined(codes), err)
				}
				got := make(map[string]bool, len(rows))
				for _, r := range rows {
					got[r.ID] = true
				}
				for id, name := range facts.assetName {
					envs := facts.assetEnvs[id]
					want := visibleUnder(envs, codes) && facts.assetLife[id] != domain.LifecycleRetired
					switch {
					case got[id] && !want:
						t.Errorf("asset %s was visible to scope {%s} but its environments are {%s}: "+
							"a token sees an asset only when EVERY environment it is in is inside the scope",
							name, joined(codes), joined(envs))
					case !got[id] && want:
						t.Errorf("asset %s was NOT visible to scope {%s} although its environments {%s} "+
							"are a subset of it: a strict predicate is not an empty one",
							name, joined(codes), joined(envs))
					}
				}
			}
		})
	}
}

// TestServiceVisibilityIsExactlyTheSubsetRule. A service reaches its
// environment through environment_id rather than a join table, so the rule is
// derived the way that entity actually gets its set.
func TestServiceVisibilityIsExactlyTheSubsetRule(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)
			facts := f.snapshot(t)

			for _, codes := range scopeCases(facts.envCodes) {
				rows, err := f.s.APIListServices(f.ctx, mustScope(t, codes...), "", 500)
				if err != nil {
					t.Fatalf("listing for scope {%s}: %v", joined(codes), err)
				}
				got := make(map[string]bool, len(rows))
				for _, r := range rows {
					got[r.ID] = true
				}
				for id, name := range facts.serviceName {
					envs := []string{facts.serviceEnv[id]}
					want := visibleUnder(envs, codes) && facts.serviceLife[id] != domain.LifecycleRetired
					switch {
					case got[id] && !want:
						t.Errorf("service %s was visible to scope {%s} but it is in {%s}",
							name, joined(codes), joined(envs))
					case !got[id] && want:
						t.Errorf("service %s was NOT visible to scope {%s} although it is in {%s}",
							name, joined(codes), joined(envs))
					}
				}
			}
		})
	}
}

// TestAddressVisibilityIsExactlyTheSubsetRule. An address inherits the
// environment set of the asset holding its interface; one with no interface
// reaches no asset and is therefore visible to nobody, which the rule already
// says because its set is empty.
func TestAddressVisibilityIsExactlyTheSubsetRule(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)
			facts := f.snapshot(t)

			for _, codes := range scopeCases(facts.envCodes) {
				rows, err := f.s.APIListAddresses(f.ctx, mustScope(t, codes...), "", 500)
				if err != nil {
					t.Fatalf("listing for scope {%s}: %v", joined(codes), err)
				}
				got := make(map[string]bool, len(rows))
				for _, r := range rows {
					got[r.ID] = true
				}
				for id, text := range facts.addrText {
					envs := facts.addrEnvs[id]
					want := visibleUnder(envs, codes) && facts.addrLife[id] != domain.LifecycleRetired
					host := facts.addrHost[id]
					hostName := "no asset"
					if host != "" {
						hostName = facts.assetName[host]
					}
					switch {
					case got[id] && !want:
						t.Errorf("address %s (on %s) was visible to scope {%s} but its host's "+
							"environments are {%s}", text, hostName, joined(codes), joined(envs))
					case !got[id] && want:
						t.Errorf("address %s (on %s) was NOT visible to scope {%s} although its "+
							"host's environments {%s} are a subset of it",
							text, hostName, joined(codes), joined(envs))
					}
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The fixture's own invariant
// ---------------------------------------------------------------------------

// TestTheFixtureCannotBeFlattenedIntoUniformEnvironments guards the property
// every scope test in this file silently depends on.
//
// WHY THIS EXISTS, because it will look like a test of a test. A uniform
// fixture cannot distinguish a predicate that filters on `code` from one that
// filters on `code AND anything else that happens to be uniform across the
// fixture`. Before commit 32a61a3 the six environments here were built through
// mustEnvironment, which hardcodes in_scope = true and criticality = 2 -- so
// they differed only by code, and a scope predicate weakened with
// `AND e2.in_scope = TRUE` (or any qualifier on criticality or role) passed
// EVERY test in this file, on both engines. In production that same mutation
// means an environment flagged in_scope = false stops counting against a token,
// and AllowsAll degrades toward AllowsAny for precisely the assets that
// straddle a monitored and an unmonitored environment.
//
// So the heterogeneity below is load-bearing, which makes it an invariant, and
// docs/ROADMAP.md §0 is the rule for those: "an invariant with no guard is a
// promise, and this project has already been bitten by the difference."
//
// IF YOU ARE READING THIS BECAUSE YOU SIMPLIFIED THE FIXTURE -- routed it back
// through mustEnvironment, dropped the odd criticality values, made every
// environment in_scope -- the simplification is the bug. It reopens the hole
// this test was written to keep shut, and it does it while looking like a
// tidy-up in review.
//
// One engine is enough: this asserts over the fixture, not over the estate, and
// nothing here is dialect-specific.
func TestTheFixtureCannotBeFlattenedIntoUniformEnvironments(t *testing.T) {
	e := Engines(t)[0]
	f := newAPIFixture(t, e)

	envs, err := f.s.ListEnvironments(f.ctx)
	if err != nil {
		t.Fatalf("loading the fixture environments: %v", err)
	}
	if len(envs) < 2 {
		t.Fatalf("the fixture has %d environments; the scope tests need several", len(envs))
	}

	var outOfScope int
	criticalities := map[int]bool{}
	roles := map[string]bool{}
	for _, env := range envs {
		if !env.InScope {
			outOfScope++
		}
		criticalities[env.Criticality] = true
		roles[env.Role] = true
	}

	const why = "\n\nA uniform fixture cannot tell a predicate filtering on `code` apart from one " +
		"filtering on `code AND <anything uniform across the fixture>`. That is exactly how a " +
		"weakened scope predicate passed every test in this file before commit 32a61a3. If you " +
		"simplified the fixture, the simplification is the bug."

	if outOfScope == 0 {
		t.Error("every fixture environment is in_scope = true, so a predicate qualified with " +
			"`AND e2.in_scope = TRUE` is indistinguishable from the correct one." + why)
	}
	if len(criticalities) < 2 {
		t.Errorf("every fixture environment has criticality %v, so a predicate qualified on "+
			"criticality is indistinguishable from the correct one.%s", criticalities, why)
	}
	// EVERY ROLE THE VOCABULARY DEFINES IS REPRESENTED, read from
	// environment_role rather than from a Go list.
	//
	// Two reasons it is written this way. A bare "more than one role" check
	// could not fire through the regression its neighbours name -- mustEnvironment
	// takes the role as a parameter, so flattening leaves roles varied -- and it
	// would have read as load-bearing while proving nothing. And a hand-kept
	// classification of roles into "utility" and "workload" lived in two places
	// here, so a new role constant would have landed in neither and been
	// silently treated as a workload.
	//
	// AND THE VOCABULARY IS THE TABLE, NOT domain.EnvRoles. That slice
	// documents itself as "NOT the vocabulary" (internal/domain/asset.go) --
	// environment_role is, and a role added by migration without a matching Go
	// constant would fail nothing here while being a role no fixture
	// environment carries. They agree today, six of six; reading the table is
	// what keeps that true without anybody checking.
	var vocabulary []string
	if err := f.s.read(f.ctx, &vocabulary, `SELECT code FROM environment_role ORDER BY code`); err != nil {
		t.Fatalf("reading the environment role vocabulary: %v", err)
	}
	if len(vocabulary) == 0 {
		t.Fatal("the environment_role vocabulary is empty; this check would pass vacuously")
	}
	for _, role := range vocabulary {
		if !roles[role] {
			t.Errorf("no fixture environment has the %q role, so no test can show the scope "+
				"predicate treats it correctly -- a predicate weakened with "+
				"`AND e2.role <> %q` would pass the whole suite.%s", role, role, why)
		}
	}

	// EVERY ENVIRONMENT HAS AN ASSET THAT IS IN IT AND NOTHING ELSE.
	//
	// This replaces the two by-name straddle checks that used to sit here (one
	// for the in_scope boundary, one for utility/workload). The visibility
	// property above subsumes both -- it derives what each scope should see
	// from the estate itself -- but it can only observe an environment that
	// something actually lives in. An environment with no exclusive member
	// changes nothing when a predicate stops counting it, so a mutation keyed
	// on its code, role or criticality survives every test in the file. That is
	// exactly how `AND e2.code <> 'staging'` passed, and why an EMPTY
	// environment is not safe but merely not yet exploited: the disclosure
	// arrives with the first asset somebody puts in it, silently.
	//
	// So this is the fixture-shaped precondition the property needs, and it is
	// stated as a rule over the fixture's own environments rather than as a
	// list of the mutations found so far.
	type membership struct {
		AssetID string `db:"asset_id"`
		Code    string `db:"code"`
	}
	var rows []membership
	if err := f.s.read(f.ctx, &rows, `
		SELECT ae.asset_id, e.code
		FROM asset_environment ae
		JOIN environment e ON e.id = ae.environment_id`); err != nil {
		t.Fatalf("loading memberships: %v", err)
	}
	byAsset := map[string][]string{}
	for _, r := range rows {
		byAsset[r.AssetID] = append(byAsset[r.AssetID], r.Code)
	}
	exclusive := map[string]bool{}
	for _, codes := range byAsset {
		if len(codes) == 1 {
			exclusive[codes[0]] = true
		}
	}
	for _, env := range envs {
		if !exclusive[env.Code] {
			t.Errorf("no fixture asset is in %q and nothing else, so nothing changes when a "+
				"predicate stops counting that environment and a mutation keyed on it survives "+
				"every test.%s", env.Code, why)
		}
	}
}

// ---------------------------------------------------------------------------
// The scope predicate
// ---------------------------------------------------------------------------

// TestABoundaryAssetIsHiddenFromAPartialScope is the central case: AllowsAll,
// not AllowsAny, expressed in SQL.
func TestABoundaryAssetIsHiddenFromAPartialScope(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)

			rows, err := f.s.APIListAssets(f.ctx, APIAssetFilter{Scope: mustScope(t, "dev"), Limit: 500})
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if containsName(rows, "sw-core-1") {
				t.Fatal("sw-core-1 is in {prod, dev}; a {dev} token must not see it, " +
					"or its least sensitive membership decides the disclosure of a production device")
			}
			if !containsName(rows, "dev-box") {
				t.Fatal("a {dev} token must still see what is only in dev; a strict predicate is not an empty one")
			}

			// THE REVERSE, and it does not follow from the case above.
			//
			// A predicate that is asymmetric by sensitivity -- "surely prod may
			// see the dev half of a boundary device" -- passes every {dev}-side
			// assertion in this file. So does any predicate qualified on
			// in_scope, because dev is the out-of-scope environment here and is
			// flagged in_scope = false. AllowsAll is a statement about the SET,
			// not about which side of it is the more sensitive.
			rows, err = f.s.APIListAssets(f.ctx, APIAssetFilter{Scope: mustScope(t, "prod"), Limit: 500})
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if containsName(rows, "sw-core-1") {
				t.Fatal("sw-core-1 is in {prod, dev}; a {prod} token must not see it either. " +
					"The rule is every environment, not every SENSITIVE environment")
			}
			if !containsName(rows, "vm-db-2") {
				t.Fatal("a {prod} token must still see what is only in prod; a strict predicate is not an empty one")
			}
			if _, err := f.s.APIGetAsset(f.ctx, mustScope(t, "prod"), f.assets["sw-core-1"]); !errors.Is(err, ErrOutOfScope) {
				t.Fatalf("fetching the boundary device by id as {prod}: got %v, want ErrOutOfScope", err)
			}

			rows, err = f.s.APIListAssets(f.ctx, APIAssetFilter{Scope: mustScope(t, "dev", "prod"), Limit: 500})
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if !containsName(rows, "sw-core-1") {
				t.Fatal("a {dev, prod} token must see the boundary device it declared")
			}
		})
	}
}

// TestASharedEnvironmentCountsAgainstAScopeLikeAnyOther.
//
// The mutation this kills is `AND e2.role <> 'shared'` on the inner NOT EXISTS,
// and it is not contrived: "a shared or utility environment should not count
// against a token" is a change somebody makes in good faith. It survived the
// whole suite once, and what it actually did was hand a {dev} token the site
// and the rack -- whole rows, id and name and kind and lifecycle and placement,
// for assets in `shared` and nothing else.
//
// A utility environment is an environment. AllowsAll counts it.
func TestASharedEnvironmentCountsAgainstAScopeLikeAnyOther(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)

			// A workload token sees neither the utility-only assets nor the
			// asset that straddles a utility and a workload environment.
			dev, err := f.s.APIListAssets(f.ctx, APIAssetFilter{Scope: mustScope(t, "dev"), Limit: 500})
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			for _, name := range []string{"dc-1", "r14", "mgmt-jump"} {
				if containsName(dev, name) {
					t.Fatalf("a {dev} token read %s, which is in `shared`. A utility environment "+
						"is an environment: it counts against a scope like any other, or a site "+
						"and a rack become readable rows for every token in the estate", name)
				}
			}
			if !containsName(dev, "dev-box") {
				t.Fatal("a {dev} token must still see dev-box; a strict predicate is not an empty one")
			}

			// Nor does the workload half of mgmt-jump's membership admit it.
			prod, err := f.s.APIListAssets(f.ctx, APIAssetFilter{Scope: mustScope(t, "prod"), Limit: 500})
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if containsName(prod, "mgmt-jump") {
				t.Fatal("mgmt-jump is in {shared, prod}; a {prod} token must not see it")
			}
			if _, err := f.s.APIGetAsset(f.ctx, mustScope(t, "prod"), f.assets["mgmt-jump"]); !errors.Is(err, ErrOutOfScope) {
				t.Fatalf("fetching mgmt-jump as {prod}: got %v, want ErrOutOfScope", err)
			}

			// And the positive control, which matters as much: a token that
			// declares both environments gets it.
			both, err := f.s.APIListAssets(f.ctx, APIAssetFilter{Scope: mustScope(t, "prod", "shared"), Limit: 500})
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if !containsName(both, "mgmt-jump") {
				t.Fatal("a {prod, shared} token must see the jump host it declared both halves of")
			}
			if !containsName(both, "dc-1") || !containsName(both, "r14") {
				t.Fatal("a {prod, shared} token must see the utility-only assets too")
			}
			if _, err := f.s.APIGetAsset(f.ctx, mustScope(t, "prod", "shared"), f.assets["mgmt-jump"]); err != nil {
				t.Fatalf("fetching mgmt-jump as {prod, shared}: %v", err)
			}
		})
	}
}

// TestAnAssetInNoEnvironmentIsVisibleToNobody.
//
// "An entity in no environment is covered by nobody, which is a data gap
// surfaced as a denial rather than an implicit allow." The API inherits that
// rule rather than inventing a friendlier one.
func TestAnAssetInNoEnvironmentIsVisibleToNobody(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)
			orphan := f.asset(t, domain.KindServer, "orphan-box", nil)

			for _, codes := range [][]string{{"prod"}, {"dev"}, {"prod", "dev", "staging"}} {
				rows, err := f.s.APIListAssets(f.ctx, APIAssetFilter{
					Scope: mustScope(t, codes...), Limit: 500,
				})
				if err != nil {
					t.Fatalf("listing for %v: %v", codes, err)
				}
				if containsName(rows, "orphan-box") {
					t.Fatalf("scope %v saw an asset that is in no environment", codes)
				}
				// A POSITIVE CONTROL. Every assertion in this test is a
				// negative, so a query that returned nothing at all would pass
				// it clean.
				if len(rows) == 0 {
					t.Fatalf("scope %v returned no assets whatsoever; this test would pass vacuously", codes)
				}
			}
			// Even the scope that names every environment there is.
			rows, err := f.s.APIListAssets(f.ctx, APIAssetFilter{Scope: everyEnvironment(t), Limit: 500})
			if err != nil {
				t.Fatalf("listing for every environment: %v", err)
			}
			if containsName(rows, "orphan-box") {
				t.Fatal("a scope naming every environment still must not reach an asset that is in none")
			}
			if _, err := f.s.APIGetAsset(f.ctx, mustScope(t, "prod"), orphan); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("got %v, want ErrNotFound for an asset in no environment", err)
			}
		})
	}
}

// TestTheScopeIsAppliedInsideThePageNotAfterIt.
//
// The failure this refuses is subtle and only appears for a scoped token
// against an estate large enough to page: filter after fetching and every page
// comes back short, and eventually empty, while rows still remain. The client
// stops early and silently under-reads the estate.
func TestTheScopeIsAppliedInsideThePageNotAfterIt(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)

			// Interleaved so that any post-fetch filter would decimate every
			// page rather than conveniently emptying one.
			const visible = 12
			for i := 0; i < visible; i++ {
				f.asset(t, domain.KindServer, fmt.Sprintf("dev-only-%02d", i), nil, "dev")
				f.asset(t, domain.KindSwitch, fmt.Sprintf("boundary-%02d", i), nil, "dev", "prod")
			}

			scope := mustScope(t, "dev")
			const size = 3
			var seen []string
			after := ""
			for page := 0; page < 50; page++ {
				rows, err := f.s.APIListAssets(f.ctx, APIAssetFilter{
					Scope: scope, After: after, Limit: size,
				})
				if err != nil {
					t.Fatalf("page %d: %v", page, err)
				}
				if len(rows) == 0 {
					break
				}
				// Every page but the last must be full. A short page means the
				// predicate ran after the LIMIT.
				for _, r := range rows {
					seen = append(seen, r.Name)
				}
				if len(rows) < size && len(seen) < visible {
					t.Fatalf("page %d returned %d rows of %d while %d assets remain unseen; "+
						"the scope predicate is being applied to the fetched page, not inside the query",
						page, len(rows), size, visible-len(seen))
				}
				after = rows[len(rows)-1].ID
			}
			// dev-box and dev-api's host plus the twelve created here.
			if len(seen) < visible {
				t.Fatalf("paged through %d assets, want at least %d", len(seen), visible)
			}
			for _, name := range seen {
				if strings.HasPrefix(name, "boundary") {
					t.Fatalf("a {dev} token paged into %s, which is in {dev, prod}", name)
				}
			}
		})
	}
}

// TestTheScopeIsAppliedInsideTheServicePage is the same property for
// APIListServices. Tested rather than reasoned about: the two collections share
// the shape of the bug, not the code that avoids it.
func TestTheScopeIsAppliedInsideTheServicePage(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)

			const visible = 12
			for i := 0; i < visible; i++ {
				f.bareService(t, fmt.Sprintf("dev-svc-%02d", i), "dev")
				f.bareService(t, fmt.Sprintf("prod-svc-%02d", i), "prod")
			}

			seen := f.pageServices(t, mustScope(t, "dev"), 3, visible)
			for _, code := range seen {
				if strings.HasPrefix(code, "prod-") {
					t.Fatalf("a {dev} token paged into %s", code)
				}
			}
		})
	}
}

// pageServices walks every page and returns the codes, failing on a short page
// while rows remain -- which is what filtering after the LIMIT looks like.
func (f *apiFixture) pageServices(t *testing.T, scope domain.EnvironmentScope, size, want int) []string {
	t.Helper()
	var seen []string
	byID := map[string]bool{}
	after := ""
	for page := 0; page < 50; page++ {
		rows, err := f.s.APIListServices(f.ctx, scope, after, size)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			// A cursor of `>=` rather than `>` repeats every page's last row on
			// the next page. Short pages and within-page ordering both stay
			// correct under that mutation, so only this assertion catches it.
			if byID[r.ID] {
				t.Fatalf("service %s appeared on two pages; the keyset cursor is not strict", r.Code)
			}
			byID[r.ID] = true
			seen = append(seen, r.Code)
		}
		for i := 1; i < len(rows); i++ {
			if rows[i-1].ID >= rows[i].ID {
				t.Fatalf("service page is not ascending by id: %s >= %s", rows[i-1].ID, rows[i].ID)
			}
		}
		if len(rows) < size && len(seen) < want {
			t.Fatalf("page %d returned %d rows of %d while %d services remain unseen; "+
				"the scope predicate is being applied to the fetched page, not inside the query",
				page, len(rows), size, want-len(seen))
		}
		after = rows[len(rows)-1].ID
	}
	if len(seen) < want {
		t.Fatalf("paged through %d services, want at least %d", len(seen), want)
	}
	return seen
}

// TestTheScopeIsAppliedInsideTheAddressPage. The likeliest of the three to
// regress: decorating addresses in Go, after the page has been fetched, is the
// tempting refactor and is exactly the bug.
func TestTheScopeIsAppliedInsideTheAddressPage(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)

			const visible = 12
			for i := 0; i < visible; i++ {
				host := fmt.Sprintf("dev-host-%02d", i)
				f.asset(t, domain.KindServer, host, nil, "dev")
				f.address(t, host, "eth0", fmt.Sprintf("10.10.%d.1", i))

				edge := fmt.Sprintf("edge-host-%02d", i)
				f.asset(t, domain.KindSwitch, edge, nil, "dev", "prod")
				f.address(t, edge, "eth0", fmt.Sprintf("10.11.%d.1", i))
			}

			scope := mustScope(t, "dev")
			const size = 3
			var seen []APIAddressRow
			seenID := map[string]bool{}
			after := ""
			for page := 0; page < 50; page++ {
				rows, err := f.s.APIListAddresses(f.ctx, scope, after, size)
				if err != nil {
					t.Fatalf("page %d: %v", page, err)
				}
				if len(rows) == 0 {
					break
				}
				for _, a := range rows {
					// Same reason as the service pager: `>=` on ip.id repeats
					// the last row of every page and nothing else notices.
					if seenID[a.ID] {
						t.Fatalf("address %s appeared on two pages; the keyset cursor is not strict", a.AddrText)
					}
					seenID[a.ID] = true
				}
				seen = append(seen, rows...)
				if len(rows) < size && len(seen) < visible {
					t.Fatalf("page %d returned %d rows of %d while %d addresses remain unseen; "+
						"the scope predicate is being applied to the fetched page, not inside the query",
						page, len(rows), size, visible-len(seen))
				}
				for i := 1; i < len(rows); i++ {
					if rows[i-1].ID >= rows[i].ID {
						t.Fatalf("address page is not ascending by id: %s >= %s", rows[i-1].ID, rows[i].ID)
					}
				}
				after = rows[len(rows)-1].ID
			}
			if len(seen) < visible {
				t.Fatalf("paged through %d addresses, want at least %d", len(seen), visible)
			}
			for _, a := range seen {
				if strings.HasPrefix(a.AddrText, "10.11.") {
					t.Fatalf("a {dev} token paged into %s, which is on a {dev, prod} device", a.AddrText)
				}
			}
		})
	}
}

// TestAnEnvironmentFilterCannotWidenTheScope: `?env=prod` on a {dev} token is
// an empty collection, not an error and certainly not a prod asset. The token's
// scope is not the client's business.
func TestAnEnvironmentFilterCannotWidenTheScope(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)

			rows, err := f.s.APIListAssets(f.ctx, APIAssetFilter{
				Scope: mustScope(t, "dev"), EnvironmentCode: "prod", Limit: 500,
			})
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if len(rows) != 0 {
				t.Fatalf("a {dev} token filtering on env=prod got %d rows; the filter narrows within the scope, never past it", len(rows))
			}

			rows, err = f.s.APIListAssets(f.ctx, APIAssetFilter{
				Scope: everyEnvironment(t), EnvironmentCode: "staging", Limit: 500,
			})
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if !containsName(rows, "staging-box") || containsName(rows, "vm-db-2") {
				t.Fatalf("env=staging returned %d rows and did not select the staging estate", len(rows))
			}
		})
	}
}

// TestAKindFilterNarrowsWithinTheScope.
func TestAKindFilterNarrowsWithinTheScope(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)
			rows, err := f.s.APIListAssets(f.ctx, APIAssetFilter{
				Scope: everyEnvironment(t), Kind: domain.KindSwitch, Limit: 500,
			})
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if len(rows) == 0 {
				t.Fatal("no switches; a zero result means the filter is wrong, not strict")
			}
			for _, r := range rows {
				if r.Kind != domain.KindSwitch {
					t.Errorf("kind=switch returned a %s", r.Kind)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Pagination
// ---------------------------------------------------------------------------

func TestPagesAreOrderedByIDAndDoNotRepeat(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)
			scope := everyEnvironment(t)
			seen := map[string]bool{}
			after := ""
			for pages := 0; pages < 50; pages++ {
				rows, err := f.s.APIListAssets(f.ctx,
					APIAssetFilter{Scope: scope, After: after, Limit: 3})
				if err != nil {
					t.Fatalf("page %d: %v", pages, err)
				}
				if len(rows) == 0 {
					break
				}
				for _, r := range rows {
					if seen[r.ID] {
						t.Fatalf("asset %s appeared on two pages", r.ID)
					}
					seen[r.ID] = true
				}
				for i := 1; i < len(rows); i++ {
					if rows[i-1].ID >= rows[i].ID {
						t.Fatalf("page is not ascending by id: %s >= %s", rows[i-1].ID, rows[i].ID)
					}
				}
				after = rows[len(rows)-1].ID
			}
			if len(seen) == 0 {
				t.Fatal("paged through the estate and saw nothing")
			}
		})
	}
}

// TestAWellFormedButUnknownCursorIsAnEmptyPage.
//
// internal/api validates a cursor as a syntactically valid UUID and nothing
// more -- it cannot ask the store whether the id exists without making the
// cursor an existence oracle. So a well-formed id that names nothing does reach
// here, and it must degrade to "no more rows" rather than to an error or to a
// silent restart at page one.
func TestAWellFormedButUnknownCursorIsAnEmptyPage(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)
			scope := everyEnvironment(t)

			// A v7 id generated now sorts after every fixture row.
			beyond := NewID()
			rows, err := f.s.APIListAssets(f.ctx, APIAssetFilter{Scope: scope, After: beyond, Limit: 500})
			if err != nil {
				t.Fatalf("paging past the end: %v", err)
			}
			if len(rows) != 0 {
				t.Fatalf("a cursor past the end returned %d rows", len(rows))
			}

			// A v4 id, which is not even in the v7 ordering. Still not an error.
			if _, err := f.s.APIListAssets(f.ctx, APIAssetFilter{
				Scope: scope, After: uuid.NewString(), Limit: 500,
			}); err != nil {
				t.Fatalf("a well-formed but nonexistent cursor must not be an error: %v", err)
			}
			for _, listErr := range []error{
				pageErr(f.s.APIListServices(f.ctx, scope, beyond, 500)),
				pageErr(f.s.APIListAddresses(f.ctx, scope, beyond, 500)),
			} {
				if listErr != nil {
					t.Fatalf("a cursor past the end must be an empty page, not an error: %v", listErr)
				}
			}
		})
	}
}

// pageErr discards a page and keeps its error, so the loop above can treat
// three differently typed lists the same way.
func pageErr[T any](_ []T, err error) error { return err }

// TestPageOrderDependsOnUUIDv7.
//
// The single-column cursor is correct ONLY because ids are UUIDv7 and therefore
// time-sortable as text. A future non-v7 id would break page ordering silently,
// which is the worst way for it to break -- so pin the assumption here, where
// the failure names the reason.
func TestPageOrderDependsOnUUIDv7(t *testing.T) {
	a := uuid.Must(uuid.NewV7()).String()
	time.Sleep(2 * time.Millisecond)
	b := uuid.Must(uuid.NewV7()).String()
	if a >= b {
		t.Fatal("ids are no longer time-sortable as text; the API's single-column cursor " +
			"in internal/store/api.go must become a (created_at, id) pair")
	}
	if NewID() == "" {
		t.Fatal("NewID returned nothing")
	}
}

// TestAPageIsBoundedEvenWhenTheCallerAsksForMore. The HTTP layer clamps, and
// the store clamps again: a store method is callable without going through it.
func TestAPageIsBoundedEvenWhenTheCallerAsksForMore(t *testing.T) {
	cases := []struct{ asked, want int }{
		{0, apiDefaultLimit},
		{-1, apiDefaultLimit},
		{1, 1},
		{500, 500},
		{5000, apiMaxLimit},
	}
	for _, tc := range cases {
		if got := apiLimit(tc.asked); got != tc.want {
			t.Errorf("apiLimit(%d) = %d, want %d", tc.asked, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// TestALifecycleFilterNamesAValueAndTheDefaultExcludesRetired.
//
// One control, not a filter plus a boolean. `?lifecycle=retired` returns ONLY
// retired assets, exactly as `?kind=vm` returns only VMs -- a filter names a
// value. Absent means the default exclusion. There is deliberately no way to
// ask for "retired and also everything else", because two callers would read
// that combination two ways.
func TestALifecycleFilterNamesAValueAndTheDefaultExcludesRetired(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)
			scope := mustScope(t, "prod")
			id := f.asset(t, domain.KindServer, "old-box", nil, "prod")
			if err := f.s.RetireAsset(f.ctx, domain.AdministratorPermit(f.actor), id); err != nil {
				t.Fatalf("retiring: %v", err)
			}

			// Default: retired is excluded, everything else is not.
			rows, err := f.s.APIListAssets(f.ctx, APIAssetFilter{Scope: scope, Limit: 500})
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if containsName(rows, "old-box") {
				t.Fatal("a retired asset is kept forever and is not a target; it must be excluded by default")
			}
			if !containsName(rows, "vm-db-2") {
				t.Fatal("the default excludes retired, not everything")
			}

			// lifecycle=retired: ONLY retired.
			rows, err = f.s.APIListAssets(f.ctx, APIAssetFilter{
				Scope: scope, Limit: 500, Lifecycle: domain.LifecycleRetired,
			})
			if err != nil {
				t.Fatalf("listing the retired: %v", err)
			}
			if !containsName(rows, "old-box") {
				t.Fatal("lifecycle=retired must return it; soft delete means the row is still there")
			}
			if containsName(rows, "vm-db-2") {
				t.Fatal("lifecycle=retired returned an active asset; a filter names a value, " +
					"it does not add a set to the default one")
			}

			// lifecycle=active: the complement, and not by accident.
			rows, err = f.s.APIListAssets(f.ctx, APIAssetFilter{
				Scope: scope, Limit: 500, Lifecycle: domain.LifecycleActive,
			})
			if err != nil {
				t.Fatalf("listing the active: %v", err)
			}
			if containsName(rows, "old-box") {
				t.Fatal("lifecycle=active returned a retired asset")
			}
			for _, r := range rows {
				if r.Lifecycle != domain.LifecycleActive {
					t.Errorf("lifecycle=active returned a %s asset", r.Lifecycle)
				}
			}
			if len(rows) == 0 {
				t.Fatal("lifecycle=active returned nothing; a zero result means the filter is wrong, not strict")
			}
		})
	}
}

// TestARetiredHostsAddressesAreExcluded.
//
// Matching /services and the default of /assets. The address of a
// decommissioned host is worse than absent in an Ansible or observability
// join: it may since have been reassigned, so publishing it points a consumer
// at the wrong machine.
func TestARetiredHostsAddressesAreExcluded(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)
			scope := everyEnvironment(t)

			before, err := f.s.APIListAddresses(f.ctx, scope, "", 500)
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if !containsAddr(before, "10.3.0.5") {
				t.Fatal("dev-box's address is missing before it was retired; the test proves nothing")
			}

			if err := f.s.RetireAsset(f.ctx, domain.AdministratorPermit(f.actor), f.assets["dev-box"]); err != nil {
				t.Fatalf("retiring dev-box: %v", err)
			}
			after, err := f.s.APIListAddresses(f.ctx, scope, "", 500)
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if containsAddr(after, "10.3.0.5") {
				t.Fatal("a retired host's address is still published; it may since have been reassigned")
			}
			if !containsAddr(after, "10.2.0.14") {
				t.Fatal("retiring one host removed another host's address")
			}
		})
	}
}

// TestASingleFetchStillReturnsARetiredEntity pins the other half of Ruling T.
//
// The collections exclude retired by default; the single-resource routes do
// NOT. The caller named that id -- it came off an old metric label or an
// Ansible run -- and the DTO carries `lifecycle`, so the honest answer is the
// row plus the word "retired". A 404 there answers a real question with
// silence, and the operator concludes the id was wrong rather than the host
// decommissioned.
//
// Unpinned until now: round 1 changed lifecycle handling across this file and
// every APIGetAsset/APIGetService assertion in it is about scope, so this
// behaviour would have stopped being true without a single test noticing.
func TestASingleFetchStillReturnsARetiredEntity(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)
			scope := mustScope(t, "prod")

			assetID := f.asset(t, domain.KindServer, "doomed-box", nil, "prod")
			svcID := f.bareService(t, "doomed-svc", "prod")
			if err := f.s.RetireAsset(f.ctx, domain.AdministratorPermit(f.actor), assetID); err != nil {
				t.Fatalf("retiring the asset: %v", err)
			}
			if err := f.s.RetireService(f.ctx, f.actor, svcID); err != nil {
				t.Fatalf("retiring the service: %v", err)
			}

			got, err := f.s.APIGetAsset(f.ctx, scope, assetID)
			if err != nil {
				t.Fatalf("fetching a retired asset by id: got %v, want the row; the caller named "+
					"this id and the DTO carries lifecycle", err)
			}
			if got.Lifecycle != domain.LifecycleRetired {
				t.Errorf("lifecycle = %q, want %q -- the caller learns it was decommissioned "+
					"from the field, not from a 404", got.Lifecycle, domain.LifecycleRetired)
			}

			svc, err := f.s.APIGetService(f.ctx, scope, svcID)
			if err != nil {
				t.Fatalf("fetching a retired service by id: got %v, want the row", err)
			}
			if svc.Lifecycle != domain.LifecycleRetired {
				t.Errorf("service lifecycle = %q, want %q", svc.Lifecycle, domain.LifecycleRetired)
			}

			// The collections still exclude both, which is what makes the
			// asymmetry deliberate rather than an accident of one code path.
			rows, err := f.s.APIListAssets(f.ctx, APIAssetFilter{Scope: scope, Limit: 500})
			if err != nil {
				t.Fatalf("listing assets: %v", err)
			}
			if containsName(rows, "doomed-box") {
				t.Error("the collection returned the retired asset")
			}
			svcs, err := f.s.APIListServices(f.ctx, scope, "", 500)
			if err != nil {
				t.Fatalf("listing services: %v", err)
			}
			for _, s := range svcs {
				if s.Code == "doomed-svc" {
					t.Error("the collection returned the retired service")
				}
			}

			// And scope still outranks lifecycle: a retired entity outside the
			// scope is still ErrOutOfScope, not a row.
			if _, err := f.s.APIGetAsset(f.ctx, mustScope(t, "dev"), assetID); !errors.Is(err, ErrOutOfScope) {
				t.Errorf("fetching a retired asset out of scope: got %v, want ErrOutOfScope", err)
			}
		})
	}
}

// TestDecoratedListsExcludeWhatWasRetired.
//
// THE GAP THIS CLOSES. The visibility property asserts row MEMBERSHIP -- which
// entities a scope may see -- and never looks inside Environments, Addresses,
// Services or Assets. Everything the three decoration queries do beyond the
// scope predicate therefore rested on two named tests, both about scope, so
// dropping any of their lifecycle filters changed real behaviour and failed
// nothing: an asset would name a RETIRED service, or name a service through a
// RETIRED placement, and a service would name a host it no longer runs on.
//
// Not a disclosure -- every row involved is already inside the scope -- but the
// same wrong-answer failure that justifies excluding a retired host's addresses:
// a consumer targets something that no longer exists, or worse, the wrong
// machine, because the box may since have been rebuilt as something else.
func TestDecoratedListsExcludeWhatWasRetired(t *testing.T) {
	const (
		host = "probe-host"
		svc  = "probe-svc"
		addr = "10.8.0.1"
	)
	cases := []struct {
		name string
		// what gets retired, and what should survive it
		retire func(t *testing.T, f *apiFixture, hostID, svcID string)
		// after the retirement, does the host still name the service, and does
		// the service still name the host?
		hostNamesService bool
		serviceNamesHost bool
		why              string
	}{
		{
			// THE BASELINE. Without it each case below could pass because the
			// decoration never named anything in the first place; with it, the
			// three retirements are self-evidently the thing that changed.
			name:             "nothing retired",
			retire:           func(t *testing.T, f *apiFixture, hostID, svcID string) {},
			hostNamesService: true,
			serviceNamesHost: true,
			why:              "with nothing retired both sides must name each other, or the cases below prove nothing",
		},
		{
			name: "a retired service",
			retire: func(t *testing.T, f *apiFixture, hostID, svcID string) {
				if err := f.s.RetireService(f.ctx, f.actor, svcID); err != nil {
					t.Fatalf("retiring the service: %v", err)
				}
			},
			hostNamesService: false,
			// The placement and the host are both live; a fetch of the retired
			// service by id still answers where it used to run, which is the
			// same rule as returning the retired entity at all.
			serviceNamesHost: true,
			why:              "a consumer would target a service that no longer exists",
		},
		{
			name: "a retired placement",
			retire: func(t *testing.T, f *apiFixture, hostID, svcID string) {
				if err := f.s.RetireInstance(f.ctx, f.actor, f.placementID(t, svcID)); err != nil {
					t.Fatalf("retiring the placement: %v", err)
				}
			},
			hostNamesService: false,
			serviceNamesHost: false,
			why:              "both sides of a withdrawn placement would point a consumer at the WRONG HOST",
		},
		{
			name: "a retired host",
			retire: func(t *testing.T, f *apiFixture, hostID, svcID string) {
				if err := f.s.RetireAsset(f.ctx, domain.AdministratorPermit(f.actor), hostID); err != nil {
					t.Fatalf("retiring the host: %v", err)
				}
			},
			// The host is retired, so a fetch of it by id still reports what it
			// carries -- but the SERVICE must stop naming a decommissioned
			// machine, for the reason its addresses are already excluded.
			hostNamesService: true,
			serviceNamesHost: false,
			why:              "a service would name a decommissioned machine that may since have been rebuilt",
		},
	}

	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					f := newAPIFixture(t, e)
					scope := mustScope(t, "prod")
					hostID := f.asset(t, domain.KindServer, host, nil, "prod")
					f.address(t, host, "eth0", addr)
					svcID := f.service(t, svc, domain.SvcAPI, "prod", host)

					tc.retire(t, f, hostID, svcID)

					gotAsset, err := f.s.APIGetAsset(f.ctx, scope, hostID)
					if err != nil {
						t.Fatalf("fetching the host: %v", err)
					}
					gotSvc, err := f.s.APIGetService(f.ctx, scope, svcID)
					if err != nil {
						t.Fatalf("fetching the service: %v", err)
					}

					if has := containsString(gotAsset.Services, svc); has != tc.hostNamesService {
						t.Errorf("after %s the host's services = %v (contains %s: %v, want %v): %s",
							tc.name, gotAsset.Services, svc, has, tc.hostNamesService, tc.why)
					}
					if has := containsString(gotSvc.Assets, host); has != tc.serviceNamesHost {
						t.Errorf("after %s the service's assets = %v (contains %s: %v, want %v): %s",
							tc.name, gotSvc.Assets, host, has, tc.serviceNamesHost, tc.why)
					}

					// The other two decorated lists are membership, not
					// lifecycle, and must be undisturbed by any of it.
					if !containsString(gotAsset.Addresses, addr) {
						t.Errorf("after %s the host's addresses = %v, want %s -- an address is a "+
							"fact about the box, not about what runs on it", tc.name, gotAsset.Addresses, addr)
					}
					if len(gotAsset.Environments) != 1 || gotAsset.Environments[0] != "prod" {
						t.Errorf("after %s the host's environments = %v, want [prod] -- membership "+
							"is declared and retirement does not revoke it", tc.name, gotAsset.Environments)
					}
				})
			}
		})
	}
}

// TestAListResponseCarriesTheRightDecorationForEachRow.
//
// THE GAP THIS CLOSES, and it is the one the previous round predicted. Every
// content assertion in this file was on a SINGLE FETCH, where the page holds
// exactly one row -- and with one row, a decoration that mis-keys its batched
// result is a no-op. Four separate defects therefore passed the whole suite on
// both engines:
//
//   - a decoration that returns early unless the page holds one row, so every
//     COLLECTION comes back with empty environments, addresses and services;
//   - a services query keyed so that every asset on the page gets every service
//     found for the page. That one lands on the Ansible view, where groups are
//     formed per service: every host would join every svc_* group, which is "a
//     merged group silently widens the target set of every playbook that uses
//     it" -- the failure docs/api-design.md §4 refuses for name collisions;
//   - an address decoration keyed to the FIRST asset of the page, so every
//     address inherits that asset's environments;
//   - a decoration that publishes one of an asset's addresses rather than all.
//
// None of them is a disclosure: every value here already passed the scope
// predicate. They are wrong answers, which for a machine-facing inventory means
// a consumer acting on them.
//
// So this test asserts EXACT sets, in both directions, for several rows of one
// LIST response. Exact rather than by length, because a length check is what let
// the first-id keying survive -- under the scope it was tested with, the asset it
// checked happened to BE the first id, so it killed by coincidence of ordering
// rather than by design. And the expectations are ordered slices, which is what
// pins the sort: an unsorted list is not guaranteed to agree between the engines,
// and this is a published contract.
func TestAListResponseCarriesTheRightDecorationForEachRow(t *testing.T) {
	// {prod, shared} returns five assets -- rich ones, bare ones, one in two
	// environments -- which is what makes a mis-keyed batch observable.
	want := map[string]struct{ envs, addrs, svcs []string }{
		"vm-db-2":   {envs: []string{"prod"}, addrs: []string{"10.2.0.14", "10.2.0.15"}, svcs: []string{"billing-api"}},
		"hv-01":     {envs: []string{"prod"}},
		"mgmt-jump": {envs: []string{"prod", "shared"}, addrs: []string{"10.5.0.5"}},
		"dc-1":      {envs: []string{"shared"}},
		"r14":       {envs: []string{"shared"}},
	}

	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)
			scope := mustScope(t, "prod", "shared")

			rows, err := f.s.APIListAssets(f.ctx, APIAssetFilter{Scope: scope, Limit: 500})
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if len(rows) < 2 {
				t.Fatalf("the list returned %d rows; with fewer than two, a mis-keyed batch "+
					"decoration is a no-op and this test proves nothing", len(rows))
			}
			got := map[string]APIAssetRow{}
			for _, r := range rows {
				got[r.Name] = r
			}
			for name := range got {
				if _, expected := want[name]; !expected {
					t.Errorf("the list returned %s, which {prod,shared} should not see", name)
				}
			}
			for name, w := range want {
				row, ok := got[name]
				if !ok {
					t.Errorf("%s is missing from a {prod,shared} list", name)
					continue
				}
				assertList(t, name, "environments", row.Environments, w.envs)
				assertList(t, name, "addresses", row.Addresses, w.addrs)
				assertList(t, name, "services", row.Services, w.svcs)
			}

			// The address collection, whose rows inherit their host's
			// environments -- keyed per row, not per page.
			addrs, err := f.s.APIListAddresses(f.ctx, scope, "", 500)
			if err != nil {
				t.Fatalf("listing addresses: %v", err)
			}
			wantAddr := map[string][]string{
				"10.2.0.14": {"prod"},
				"10.2.0.15": {"prod"},
				"10.5.0.5":  {"prod", "shared"},
			}
			if len(addrs) != len(wantAddr) {
				t.Errorf("the address list returned %d rows, want %d", len(addrs), len(wantAddr))
			}
			for _, a := range addrs {
				w, ok := wantAddr[a.AddrText]
				if !ok {
					t.Errorf("the address list returned %s, which {prod,shared} should not see", a.AddrText)
					continue
				}
				assertList(t, a.AddrText, "environments", a.Environments, w)
			}

			// And the service collection's hosts, for the same reason.
			svcs, err := f.s.APIListServices(f.ctx, scope, "", 500)
			if err != nil {
				t.Fatalf("listing services: %v", err)
			}
			wantSvc := map[string][]string{
				"billing-api": {"vm-db-2"},
				"shared-svc":  nil,
			}
			if len(svcs) != len(wantSvc) {
				t.Errorf("the service list returned %d rows, want %d", len(svcs), len(wantSvc))
			}
			for _, sv := range svcs {
				w, ok := wantSvc[sv.Code]
				if !ok {
					t.Errorf("the service list returned %s, which {prod,shared} should not see", sv.Code)
					continue
				}
				assertList(t, sv.Code, "assets", sv.Assets, w)
			}
		})
	}
}

// assertList compares a decorated list against the exact expected one, ORDER
// INCLUDED. Order is part of the contract: an unsorted list is not guaranteed
// to come back the same way from both engines, and a consumer diffing two
// responses would see a change that is not one.
func assertList(t *testing.T, subject, list string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %s = %v, want %v", subject, list, got, want)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s: %s = %v, want %v (differs at position %d; the lists are sorted, "+
				"so this is either the wrong content or the wrong order)", subject, list, got, want, i)
			return
		}
	}
}

func containsString(v []string, want string) bool {
	for _, s := range v {
		if s == want {
			return true
		}
	}
	return false
}

func containsAddr(rows []APIAddressRow, addr string) bool {
	for _, r := range rows {
		if r.AddrText == addr {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Services
// ---------------------------------------------------------------------------

// TestAServiceIsScopedByItsSingleEnvironment.
//
// A service carries one environment_id, not a set, so AllowsAll over a
// one-element slice is the same as Allows. The test exists so that a future
// many-to-many service/environment change cannot quietly widen disclosure.
func TestAServiceIsScopedByItsSingleEnvironment(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)

			prod, err := f.s.APIListServices(f.ctx, mustScope(t, "prod"), "", 500)
			if err != nil {
				t.Fatalf("listing for prod: %v", err)
			}
			dev, err := f.s.APIListServices(f.ctx, mustScope(t, "dev"), "", 500)
			if err != nil {
				t.Fatalf("listing for dev: %v", err)
			}
			for _, svc := range prod {
				if svc.EnvironmentCode != "prod" {
					t.Errorf("a prod-scoped read returned %s, which is in %s", svc.Code, svc.EnvironmentCode)
				}
			}
			for _, svc := range dev {
				if svc.EnvironmentCode != "dev" {
					t.Errorf("a dev-scoped read returned %s, which is in %s", svc.Code, svc.EnvironmentCode)
				}
			}
			if len(prod) == 0 || len(dev) == 0 {
				t.Fatal("the fixture estate has services in both; a zero result means the filter is wrong, not strict")
			}
			if prod[0].Criticality != 2 {
				t.Errorf("criticality = %d, want the service's tier of 2", prod[0].Criticality)
			}
			if len(prod[0].Assets) != 1 || prod[0].Assets[0] != "vm-db-2" {
				t.Errorf("billing-api's hosts = %v, want [vm-db-2]", prod[0].Assets)
			}
		})
	}
}

// TestAServiceDoesNotNameAHostTheTokenCannotSee.
//
// A service's environment and its host's environments are separate facts: an
// in-scope service can be placed on a boundary device. Naming that host in the
// service's DTO would disclose it by another route, so the host list carries
// the asset predicate too.
func TestAServiceDoesNotNameAHostTheTokenCannotSee(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)

			// A dev service placed on the {prod, dev} core switch.
			f.service(t, "edge-agent", domain.SvcAgent, "dev", "sw-core-1")

			rows, err := f.s.APIListServices(f.ctx, mustScope(t, "dev"), "", 500)
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			var found bool
			for _, svc := range rows {
				if svc.Code != "edge-agent" {
					continue
				}
				found = true
				for _, host := range svc.Assets {
					if host == "sw-core-1" {
						t.Fatal("a {dev} token read the name of a {prod, dev} device out of a service's host list")
					}
				}
			}
			if !found {
				t.Fatal("the dev service itself is in scope and must be listed")
			}

			both, err := f.s.APIListServices(f.ctx, mustScope(t, "dev", "prod"), "", 500)
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			for _, svc := range both {
				if svc.Code == "edge-agent" && len(svc.Assets) != 1 {
					t.Errorf("a {dev, prod} token sees edge-agent on %v, want [sw-core-1]", svc.Assets)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Addresses
// ---------------------------------------------------------------------------

func TestAnAddressInheritsItsAssetsEnvironments(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)

			// sw-core-1 is in {prod, dev}, so its addresses are too, and a
			// {dev} token must not see them.
			dev, err := f.s.APIListAddresses(f.ctx, mustScope(t, "dev"), "", 500)
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			var sawDevOnly bool
			for _, a := range dev {
				if a.AssetName != nil && *a.AssetName == "sw-core-1" {
					t.Fatal("an address is scoped by the environments of the asset holding it; " +
						"a {dev} token reading a {prod, dev} switch's address is the same leak by another route")
				}
				if a.AddrText == "10.3.0.5" {
					sawDevOnly = true
				}
			}
			// The positive control: dev-box is in {dev} alone and its address
			// must come back, or the assertion above passes on an empty list.
			if !sawDevOnly {
				t.Fatal("a {dev} token did not see dev-box's 10.3.0.5; a strict predicate is not an empty one")
			}

			// And the reverse, for the same reason as the asset case: prod is
			// not privileged over dev just because it is the more sensitive
			// half of the pair.
			prod, err := f.s.APIListAddresses(f.ctx, mustScope(t, "prod"), "", 500)
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			var sawProdOnly bool
			for _, a := range prod {
				if a.AddrText == "10.9.0.1" {
					t.Fatal("a {prod} token read the boundary switch's address; " +
						"AllowsAll is a statement about the set, not about which half is sensitive")
				}
				if a.AddrText == "10.2.0.14" {
					sawProdOnly = true
				}
			}
			if !sawProdOnly {
				t.Fatal("a {prod} token did not see vm-db-2's 10.2.0.14; the predicate is empty, not strict")
			}

			both, err := f.s.APIListAddresses(f.ctx, mustScope(t, "dev", "prod"), "", 500)
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			var found bool
			for _, a := range both {
				if a.AssetName != nil && *a.AssetName == "sw-core-1" {
					found = true
					if len(a.Environments) != 2 {
						t.Errorf("the address carries %v, want both of its asset's environments", a.Environments)
					}
					if a.AddrFamily != 4 {
						t.Errorf("family = %d, want 4", a.AddrFamily)
					}
				}
			}
			if !found {
				t.Fatal("a {dev, prod} token must see the boundary device's addresses")
			}
		})
	}
}

// TestAnAddressWithNoAssetIsVisibleToNobody.
//
// An FHRP virtual address has fhrp_group_id and no interface_id, so it reaches
// no asset and therefore no environment. Same rule as an asset in no
// environment: a data gap is surfaced as a denial.
func TestAnAddressWithNoAssetIsVisibleToNobody(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)

			rows, err := f.s.APIListAddresses(f.ctx, everyEnvironment(t), "", 500)
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if len(rows) == 0 {
				t.Fatal("no addresses at all; a zero result means the join is wrong, not strict")
			}
			for _, a := range rows {
				if a.AssetID == nil {
					t.Fatalf("address %s has no asset and was returned anyway", a.AddrText)
				}
				if a.AddrText == "10.0.0.254" {
					t.Fatal("the virtual address reaches no asset and must reach no reader")
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Single fetches, and the two sentinels
// ---------------------------------------------------------------------------

// TestOutOfScopeIsAKindOfNotFound.
//
// If this ever stops holding, every caller's errors.Is(err, domain.ErrNotFound)
// silently turns a 404 into a 500.
func TestOutOfScopeIsAKindOfNotFound(t *testing.T) {
	if !errors.Is(ErrOutOfScope, domain.ErrNotFound) {
		t.Fatal("ErrOutOfScope must wrap domain.ErrNotFound: the client must not be able to tell " +
			"an out-of-scope id from an absent one, and every existing errors.Is check depends on it")
	}
}

// TestAnOutOfScopeIDIsDistinguishableOnlyToTheServer.
//
// Both cases are the same 404 to the client -- a 403 would be an existence
// oracle over the estate. The store still separates them, because
// docs/api-design.md §3 promises an operator-side security event as the entire
// mitigation for that deliberately unhelpful 404, and a handler cannot log what
// the store did not tell it.
func TestAnOutOfScopeIDIsDistinguishableOnlyToTheServer(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)
			dev := mustScope(t, "dev")

			_, err := f.s.APIGetAsset(f.ctx, dev, f.assets["sw-core-1"])
			if !errors.Is(err, ErrOutOfScope) {
				t.Fatalf("fetching a boundary device out of scope: got %v, want ErrOutOfScope", err)
			}
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatal("an out-of-scope fetch must still read as ErrNotFound to every existing caller")
			}

			absent, err := f.s.APIGetAsset(f.ctx, dev, NewID())
			if absent != nil {
				t.Fatal("a fabricated id returned a row")
			}
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("fetching a fabricated id: got %v, want ErrNotFound", err)
			}
			if errors.Is(err, ErrOutOfScope) {
				t.Fatal("a fabricated id is not a scope miss; logging it as one would fill the " +
					"security log with noise and hide the real misconfiguration")
			}

			// The same pair for services.
			if _, err := f.s.APIGetService(f.ctx, mustScope(t, "prod"), f.services["dev-api"]); !errors.Is(err, ErrOutOfScope) {
				t.Fatalf("fetching an out-of-scope service: got %v, want ErrOutOfScope", err)
			}
			if _, err := f.s.APIGetService(f.ctx, mustScope(t, "prod"), NewID()); errors.Is(err, ErrOutOfScope) {
				t.Fatal("a fabricated service id was reported as a scope miss")
			}
		})
	}
}

// TestAFetchedAssetCarriesItsPlacementAndItsSets. Placement resolves through
// asset_closure, which is why a VM two hops below the rack still has one.
func TestAFetchedAssetCarriesItsPlacementAndItsSets(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)

			got, err := f.s.APIGetAsset(f.ctx, mustScope(t, "prod"), f.assets["vm-db-2"])
			if err != nil {
				t.Fatalf("fetching: %v", err)
			}
			if got.Site == nil || *got.Site != "dc-1" {
				t.Errorf("site = %v, want dc-1; a VM's site is three hops up and only asset_closure finds it", got.Site)
			}
			if got.Rack == nil || *got.Rack != "r14" {
				t.Errorf("rack = %v, want r14", got.Rack)
			}
			if len(got.Environments) != 1 || got.Environments[0] != "prod" {
				t.Errorf("environments = %v, want [prod]", got.Environments)
			}
			// BOTH of them, and sorted: vm-db-2 carries two addresses so that
			// "publish one" and "publish all" are different answers.
			assertList(t, "vm-db-2", "addresses", got.Addresses, []string{"10.2.0.14", "10.2.0.15"})
			if len(got.Services) != 1 || got.Services[0] != "billing-api" {
				t.Errorf("services = %v, want [billing-api]", got.Services)
			}
		})
	}
}

// TestAnAssetDoesNotNameAServiceTheTokenCannotSee is the mirror of the host
// case: a service carries its own environment_id, which need not be one of its
// host's.
func TestAnAssetDoesNotNameAServiceTheTokenCannotSee(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)
			// A staging service placed on a prod-only host.
			f.service(t, "staging-sidecar", domain.SvcAgent, "staging", "vm-db-2")

			got, err := f.s.APIGetAsset(f.ctx, mustScope(t, "prod"), f.assets["vm-db-2"])
			if err != nil {
				t.Fatalf("fetching: %v", err)
			}
			for _, code := range got.Services {
				if code == "staging-sidecar" {
					t.Fatal("a {prod} token read the name of a staging service from a prod host's service list")
				}
			}

			wide, err := f.s.APIGetAsset(f.ctx, mustScope(t, "prod", "staging"), f.assets["vm-db-2"])
			if err != nil {
				t.Fatalf("fetching: %v", err)
			}
			if len(wide.Services) != 2 {
				t.Errorf("a {prod, staging} token sees %v, want both services", wide.Services)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Environments
// ---------------------------------------------------------------------------

func TestOnlyTheScopedEnvironmentsAreListed(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)

			envs, err := f.s.APIListEnvironments(f.ctx, mustScope(t, "dev", "prod"))
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if len(envs) != 2 {
				t.Fatalf("got %d environments, want 2", len(envs))
			}
			if envs[0].Code != "dev" || envs[1].Code != "prod" {
				t.Errorf("got %s, %s; want dev, prod in code order", envs[0].Code, envs[1].Code)
			}

			// A scope naming something the estate has not built yet is not an
			// error: it is a statement about a credential, not about the world.
			envs, err = f.s.APIListEnvironments(f.ctx, mustScope(t, "nowhere"))
			if err != nil {
				t.Fatalf("listing an unbuilt environment: %v", err)
			}
			if len(envs) != 0 {
				t.Fatalf("got %d environments for a code that does not exist", len(envs))
			}
		})
	}
}

// TestAnEmptyScopePermitsNothing. It cannot arrive through configuration, and
// the guard exists because of what the SQL would do if it did: placeholders(0)
// renders NOT IN (NULL), which makes the NOT EXISTS vacuously false and admits
// the whole estate. A predicate that fails OPEN is worth one explicit test.
func TestAnEmptyScopePermitsNothing(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newAPIFixture(t, e)
			var none domain.EnvironmentScope

			if _, err := f.s.APIListAssets(f.ctx, APIAssetFilter{Scope: none, Limit: 500}); !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("listing assets with an empty scope: got %v, want ErrInvalid", err)
			}
			if _, err := f.s.APIListServices(f.ctx, none, "", 500); !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("listing services with an empty scope: got %v, want ErrInvalid", err)
			}
			if _, err := f.s.APIListAddresses(f.ctx, none, "", 500); !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("listing addresses with an empty scope: got %v, want ErrInvalid", err)
			}
			if _, err := f.s.APIListEnvironments(f.ctx, none); !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("listing environments with an empty scope: got %v, want ErrInvalid", err)
			}
			if _, err := f.s.APIGetAsset(f.ctx, none, f.assets["vm-db-2"]); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("fetching with an empty scope: got %v, want a 404-shaped error", err)
			}
			if _, err := f.s.APIGetService(f.ctx, none, f.services["billing-api"]); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("fetching a service with an empty scope: got %v, want a 404-shaped error", err)
			}
		})
	}
}
