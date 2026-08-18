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

	f.service(t, "billing-api", domain.SvcAPI, "prod", "vm-db-2")
	f.service(t, "dev-api", domain.SvcAPI, "dev", "dev-box")

	f.address(t, "sw-core-1", "eth0", "10.9.0.1")
	f.address(t, "vm-db-2", "eth0", "10.2.0.14")
	f.address(t, "dev-box", "eth0", "10.3.0.5")

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
	if err := f.s.CreateEnvironment(f.ctx, testActor, env); err != nil {
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
			seen = append(seen, r.Code)
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
			after := ""
			for page := 0; page < 50; page++ {
				rows, err := f.s.APIListAddresses(f.ctx, scope, after, size)
				if err != nil {
					t.Fatalf("page %d: %v", page, err)
				}
				if len(rows) == 0 {
					break
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
	if !(a < b) {
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
			if err := f.s.RetireAsset(f.ctx, f.actor, id); err != nil {
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

			if err := f.s.RetireAsset(f.ctx, f.actor, f.assets["dev-box"]); err != nil {
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
			if len(got.Addresses) != 1 || got.Addresses[0] != "10.2.0.14" {
				t.Errorf("addresses = %v, want [10.2.0.14]", got.Addresses)
			}
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
