// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/madalinignisca/invctl/internal/auth"
	"github.com/madalinignisca/invctl/internal/config"
	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web/middleware"
)

// ---------------------------------------------------------------------------
// Fixture: a small estate plus a real ReaderGuard, so this package's own
// handler tests exercise the real path a request takes -- auth, scope and
// store -- rather than a context built by reaching into middleware's
// unexported readerContextKey.
// ---------------------------------------------------------------------------

type apiHandlerFixture struct {
	t   *testing.T
	api *API
	s   *store.SQLStore
	ctx context.Context

	guard middleware.ReaderGuard

	envs map[string]string // code -> id

	// ids used by the tests.
	assetSwCore  string // in {prod, dev}: the boundary device
	assetDevOnly string // in {dev} only: out of scope for a prod-only token
	assetProdVM  string // in {prod} only
	serviceID    string // billing-api, in prod, placed on assetProdVM
}

// Tokens: "prod-reader" sees only prod, "dev-reader" sees only dev, "both"
// sees prod and dev.
const (
	tokProd = "tok-prod"
	tokDev  = "tok-dev"
	tokBoth = "tok-both"
)

func newAPIHandlerFixture(t *testing.T) *apiHandlerFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api-test.db")
	db, err := store.Open(store.DriverSQLite, "file:"+path)
	if err != nil {
		t.Fatalf("opening test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrating test database: %v", err)
	}
	s := store.New(db)

	registry, err := auth.NewReaderRegistry([]config.ReaderCredential{
		{ID: "prod-reader", Token: tokProd, Environments: []string{"prod"}},
		{ID: "dev-reader", Token: tokDev, Environments: []string{"dev"}},
		{ID: "both-reader", Token: tokBoth, Environments: []string{"prod", "dev"}},
	})
	if err != nil {
		t.Fatalf("building reader registry: %v", err)
	}

	f := &apiHandlerFixture{
		t: t, api: New(s), s: s, ctx: ctx,
		guard: middleware.ReaderGuard{
			Registry:        registry,
			Credentials:     middleware.NewRateLimiter(1000, 1000),
			Unauthenticated: middleware.NewRateLimiter(1000, 1000),
			SessionCookie:   "session",
		},
		envs: map[string]string{},
	}

	f.envs["prod"] = f.mustEnv("prod", domain.EnvRoleProduction)
	f.envs["dev"] = f.mustEnv("dev", domain.EnvRoleDev)

	f.assetSwCore = f.mustAsset(domain.KindSwitch, "sw-core-1", nil, "prod", "dev")
	f.assetDevOnly = f.mustAsset(domain.KindServer, "dev-box", nil, "dev")
	f.assetProdVM = f.mustAsset(domain.KindVM, "vm-db-1", nil, "prod")

	f.mustInterfaceAndAddress(f.assetProdVM, "eth0", "10.2.0.14")

	f.serviceID = f.mustService("billing-api", "prod", f.assetProdVM)

	return f
}

func (f *apiHandlerFixture) mustEnv(code, role string) string {
	f.t.Helper()
	env, err := domain.NewEnvironment(store.NewID(), code, code, role, true, 2, f.s.Now())
	if err != nil {
		f.t.Fatalf("building environment %s: %v", code, err)
	}
	if err := f.s.CreateEnvironment(f.ctx, domain.SystemActor, env); err != nil {
		f.t.Fatalf("creating environment %s: %v", code, err)
	}
	return env.ID
}

func (f *apiHandlerFixture) mustAsset(kind, name string, parentID *string, envCodes ...string) string {
	f.t.Helper()
	a, err := domain.NewAsset(store.NewID(), kind, name, parentID, f.s.Now())
	if err != nil {
		f.t.Fatalf("building asset %s: %v", name, err)
	}
	ids := make([]string, 0, len(envCodes))
	for _, code := range envCodes {
		ids = append(ids, f.envs[code])
	}
	if err := f.s.CreateAsset(f.ctx, domain.SystemActor, a, ids); err != nil {
		f.t.Fatalf("creating asset %s: %v", name, err)
	}
	return a.ID
}

func (f *apiHandlerFixture) mustInterfaceAndAddress(assetID, ifaceName, addr string) {
	f.t.Helper()
	i, err := domain.NewInterface(store.NewID(), assetID, ifaceName, domain.FFRJ45)
	if err != nil {
		f.t.Fatalf("building interface %s: %v", ifaceName, err)
	}
	if err := f.s.CreateInterface(f.ctx, domain.SystemActor, i); err != nil {
		f.t.Fatalf("creating interface %s: %v", ifaceName, err)
	}
	ip, err := domain.NewIPAddress(store.NewID(), addr, &i.ID, domain.IPRolePrimary)
	if err != nil {
		f.t.Fatalf("building address %s: %v", addr, err)
	}
	if err := f.s.CreateIPAddress(f.ctx, domain.SystemActor, ip); err != nil {
		f.t.Fatalf("creating address %s: %v", addr, err)
	}
}

func (f *apiHandlerFixture) mustService(code, envCode, hostAssetID string) string {
	f.t.Helper()
	svc, err := domain.NewService(store.NewID(), domain.ServiceSpec{
		Code: code, Name: code, Kind: domain.SvcAPI,
		EnvironmentID: f.envs[envCode], Availability: domain.AvailStandalone, Tier: 1,
	}, f.s.Now())
	if err != nil {
		f.t.Fatalf("building service %s: %v", code, err)
	}
	if err := f.s.CreateService(f.ctx, domain.SystemActor, svc); err != nil {
		f.t.Fatalf("creating service %s: %v", code, err)
	}
	si, err := domain.NewServiceInstance(store.NewID(), svc.ID, hostAssetID, domain.RuntimeSystemd, 0, f.s.Now())
	if err != nil {
		f.t.Fatalf("building the placement of %s: %v", code, err)
	}
	if err := f.s.CreateInstance(f.ctx, domain.SystemActor, si); err != nil {
		f.t.Fatalf("placing %s: %v", code, err)
	}
	return svc.ID
}

// serve runs a handler behind the real RequireReader middleware, so the
// context carries a genuinely authenticated reader rather than one poked in
// by the test.
func (f *apiHandlerFixture) serve(handler http.HandlerFunc, method, target, bearer string) *httptest.ResponseRecorder {
	f.t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	middleware.RequireReader(f.guard)(handler).ServeHTTP(rec, req)
	return rec
}

// serveByID is serve for a single-resource route ({id} in the pattern).
// Routes are mounted through http.ServeMux in Task 9 with a "{id}" pattern
// segment, which is what populates r.PathValue("id") on a real request; a
// direct ServeHTTP call bypasses the mux entirely, so the test sets the path
// value the same way the mux would.
func (f *apiHandlerFixture) serveByID(handler http.HandlerFunc, id, bearer string) *httptest.ResponseRecorder {
	f.t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/x/"+id, nil)
	req.SetPathValue("id", id)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	middleware.RequireReader(f.guard)(handler).ServeHTTP(rec, req)
	return rec
}

// ---------------------------------------------------------------------------
// Item 1: an out-of-scope id and a fabricated id are indistinguishable.
// ---------------------------------------------------------------------------

func TestAnOutOfScopeAssetIsIndistinguishableFromAnAbsentOne(t *testing.T) {
	f := newAPIHandlerFixture(t)

	// assetDevOnly is real but carries no `prod` membership, so a prod-only
	// token cannot see it: out of scope, not absent.
	outOfScope := f.serveByID(f.api.GetAsset, f.assetDevOnly, tokProd)
	fabricated := f.serveByID(f.api.GetAsset, store.NewID(), tokProd)

	if outOfScope.Code != fabricated.Code {
		t.Fatalf("status differs: out-of-scope %d, fabricated %d", outOfScope.Code, fabricated.Code)
	}
	if outOfScope.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", outOfScope.Code)
	}
	if outOfScope.Body.String() != fabricated.Body.String() {
		t.Fatalf("body differs: out-of-scope %q, fabricated %q", outOfScope.Body.String(), fabricated.Body.String())
	}
}

func TestAnOutOfScopeServiceIsIndistinguishableFromAnAbsentOne(t *testing.T) {
	f := newAPIHandlerFixture(t)

	// billing-api is in prod only, so a dev-only token cannot see it.
	outOfScope := f.serveByID(f.api.GetService, f.serviceID, tokDev)
	fabricated := f.serveByID(f.api.GetService, store.NewID(), tokDev)

	if outOfScope.Code != fabricated.Code || outOfScope.Body.String() != fabricated.Body.String() {
		t.Fatalf("out-of-scope and fabricated ids diverge: %d %q vs %d %q",
			outOfScope.Code, outOfScope.Body.String(), fabricated.Code, fabricated.Body.String())
	}
}

// TestABoundaryAssetIsHiddenFromAPartialScope pins the AllowsAll rule at the
// handler level: sw-core-1 sits in {prod, dev}, so a token that can see only
// one of those must not be able to read it, while a token scoped to both can.
func TestABoundaryAssetIsHiddenFromAPartialScope(t *testing.T) {
	f := newAPIHandlerFixture(t)

	if rec := f.serveByID(f.api.GetAsset, f.assetSwCore, tokDev); rec.Code != http.StatusNotFound {
		t.Fatalf("a {dev} token read the boundary device: got %d, want 404", rec.Code)
	}
	if rec := f.serveByID(f.api.GetAsset, f.assetSwCore, tokBoth); rec.Code != http.StatusOK {
		t.Fatalf("a {prod, dev} token could not read the boundary device: got %d, want 200", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Item 2: an unknown filter value is 400, not an empty collection.
// ---------------------------------------------------------------------------

func TestAnUnrecognisedAssetKindFilterValueIsRefused(t *testing.T) {
	f := newAPIHandlerFixture(t)
	rec := f.serve(f.api.ListAssets, "GET", "/api/v1/assets?kind=spaceship", tokProd)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}

func TestAnUnrecognisedLifecycleFilterValueIsRefused(t *testing.T) {
	f := newAPIHandlerFixture(t)
	rec := f.serve(f.api.ListAssets, "GET", "/api/v1/assets?lifecycle=zombie", tokProd)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}

func TestAnUnrecognisedEnvironmentFilterValueIsRefused(t *testing.T) {
	f := newAPIHandlerFixture(t)
	rec := f.serve(f.api.ListAssets, "GET", "/api/v1/assets?env=nonexistent", tokProd)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Item 8: filters narrow within scope; a valid value outside scope is an
// empty collection, not an error.
// ---------------------------------------------------------------------------

func TestAnEnvironmentFilterOutsideScopeReturnsAnEmptyCollectionNotAnError(t *testing.T) {
	f := newAPIHandlerFixture(t)
	// "prod" is a real environment, just not one the dev-only token may see.
	rec := f.serve(f.api.ListAssets, "GET", "/api/v1/assets?env=prod", tokDev)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: a valid but out-of-scope filter value is an empty answer, not an error", rec.Code)
	}
	var page Page[Asset]
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(page.Data) != 0 {
		t.Fatalf("got %d assets, want 0: the token's scope is not the client's business, but it still bounds the answer", len(page.Data))
	}
	if page.Data == nil {
		t.Fatal("Data must marshal as [], never null, even for a scope-narrowed empty answer")
	}
}

// TestAnEmptyCollectionMarshalsAsAnEmptyArray is the handler-level companion
// to TestPageDataMarshalsAsEmptyArrayNotNull in api_test.go: it exercises the
// real ListAssets path end to end rather than constructing a Page by hand.
func TestAnEmptyCollectionMarshalsAsAnEmptyArray(t *testing.T) {
	f := newAPIHandlerFixture(t)
	rec := f.serve(f.api.ListAssets, "GET", "/api/v1/assets?env=prod", tokDev)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	const want = `{"data":[],"next":null}`
	if rec.Body.String() != want {
		t.Fatalf("got %s, want %s", rec.Body.String(), want)
	}
}

// ---------------------------------------------------------------------------
// A handler with no reader in context is a 500, never an invented scope.
// ---------------------------------------------------------------------------

func TestAHandlerWithNoReaderInContextIsA500NotAnEmptyScope(t *testing.T) {
	f := newAPIHandlerFixture(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/assets", nil)
	// No middleware in front: this simulates a route mounted without the
	// guard, which must fail closed rather than publish the estate.
	f.api.ListAssets(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500: a missing reader must never fall back to an empty (or any) scope", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Sanity: a well-scoped, well-formed request returns what it should. Not
// exhaustive coverage of the store layer (that lives in internal/store), but
// enough to know the handler wiring itself is correct.
// ---------------------------------------------------------------------------

func TestListAssetsReturnsWhatTheTokenMaySee(t *testing.T) {
	f := newAPIHandlerFixture(t)
	rec := f.serve(f.api.ListAssets, "GET", "/api/v1/assets", tokProd)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var page Page[Asset]
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	// vm-db-1 (prod only) is visible. sw-core-1 straddles prod AND dev, so a
	// prod-only token -- AllowsAll, not AllowsAny -- must not see it either;
	// dev-box (dev only) is out of scope entirely.
	names := map[string]bool{}
	for _, a := range page.Data {
		names[a.Name] = true
	}
	if !names["vm-db-1"] {
		t.Fatalf("missing an in-scope asset: got %+v", names)
	}
	if names["sw-core-1"] {
		t.Fatalf("a prod-only token saw sw-core-1, which also requires dev (AllowsAll): got %+v", names)
	}
	if names["dev-box"] {
		t.Fatalf("a prod-only token saw dev-box, which is out of its scope: got %+v", names)
	}
}

func TestGetServiceReturnsTheOneElementEnvironmentsSlice(t *testing.T) {
	f := newAPIHandlerFixture(t)
	rec := f.serveByID(f.api.GetService, f.serviceID, tokProd)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var svc Service
	if err := json.Unmarshal(rec.Body.Bytes(), &svc); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(svc.Environments) != 1 || svc.Environments[0] != "prod" {
		t.Fatalf("got Environments %+v, want [\"prod\"]", svc.Environments)
	}
}

func TestListEnvironmentsReturnsOnlyWhatTheTokenMaySee(t *testing.T) {
	f := newAPIHandlerFixture(t)
	rec := f.serve(f.api.ListEnvironments, "GET", "/api/v1/environments", tokProd)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	var page Page[Environment]
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(page.Data) != 1 || page.Data[0].Code != "prod" {
		t.Fatalf("got %+v, want exactly [prod]", page.Data)
	}
}
