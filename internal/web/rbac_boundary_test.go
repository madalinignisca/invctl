// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/alexedwards/scs/v2"

	"github.com/madalinignisca/invctl/internal/auth"
	"github.com/madalinignisca/invctl/internal/config"
	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/seed"
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web"
	"github.com/madalinignisca/invctl/internal/web/handlers"
	"github.com/madalinignisca/invctl/internal/web/render"
	"github.com/madalinignisca/invctl/internal/web/routescan"
	webassets "github.com/madalinignisca/invctl/web"
)

// The boundary suite -- WP-G1 Task 16.
//
// WHY THIS EXISTS: the WP-A4 auth review found six admin gates that were
// correct but untested, and enumeration was the failure -- a hand-maintained
// route list stops being complete the day somebody adds a route. So this
// file's one enumeration (Step 2/3/4 below) is GENERATED from the router via
// routescan.WriteRoutes, not maintained by hand.
//
// THE TRAP THIS FILE WAS BUILT AROUND, AND WHAT SPRUNG IT: before WP-G1 Task
// 13, auth.CanWrite(RoleProjectOwner) was false, so the project-owner test
// below (Step 4) passed for a reason that was always going to change -- every
// write route refused a project owner at middleware.RequireWrite, before any
// handler-level scope check ran at all. A test that only asserted "403" would
// have kept passing, unchanged, the day Task 13 flipped CanWrite and the
// object gate (auth.Authorizer.Permit / scopedPermit.Covers) became the thing
// actually doing the refusing. It did not keep passing: Step 4 went red the
// moment the flip landed, exactly as designed, because it asserts WHICH LAYER
// refused, not merely that one did. See Step 4's own comment below for the
// post-flip distribution this file now enforces, and the two things reading
// that distribution back out of raw HTTP responses turned out to require
// understanding rather than assuming: a form-driven handler that redirects
// with a flash message on EVERY outcome (success, validation refusal, and an
// authorization refusal all look identical over HTTP -- see
// ManufacturerRetire and JournalCreate/journalRefused for two examples), and
// one handler (respondUserMutation) that deliberately renders a raw
// domain.ErrForbidden message instead of the generic one, for a reason
// unrelated to this flip (the last-active-Administrator guard) that this
// flip made reachable by a project owner for the first time.
//
// REFUSAL LAYERS, DISTINGUISHED BY RESPONSE BODY (verified against the
// source at the call sites, not assumed):
//   - middleware.RequireWrite            -> "You have read-only access."
//   - middleware.RequireAdministrator    -> "This requires an Administrator."
//   - a permit refusal at tx.authorize,
//     surfaced through handleStoreError  -> "You are not allowed to do that."
//   - a permit refusal at tx.authorize, surfaced through respondUserMutation
//     specifically (see that function's own comment) -> the raw wrapped
//     error, "writing change log for app_user <id>: forbidden".

// ---------------------------------------------------------------------------
// Engines. store.Engines lives in an internal/store _test.go file and is not
// importable from here (test files are excluded from a package's importable
// API), so this is a small package-local equivalent -- same shape
// (INV_TEST_POSTGRES_DSN gates the postgres half, sqlite always runs), built
// directly on the exported store.Open/store.Migrate rather than duplicating
// internal/store's own template-caching machinery, which this suite does not
// need: it builds a handful of harnesses, not hundreds.
type boundaryEngine struct {
	name string
	open func(t *testing.T) *store.DB
}

func boundaryEngines(t *testing.T) []boundaryEngine {
	t.Helper()
	engines := []boundaryEngine{{name: "sqlite", open: openBoundarySQLite}}
	if os.Getenv("INV_TEST_POSTGRES_DSN") != "" {
		engines = append(engines, boundaryEngine{name: "postgres", open: openBoundaryPostgres})
	} else {
		t.Log("INV_TEST_POSTGRES_DSN not set: skipping the PostgreSQL half of this test")
	}
	return engines
}

func openBoundarySQLite(t *testing.T) *store.DB {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "rbac-boundary.db")
	db, err := store.Open(store.DriverSQLite, dsn)
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrating sqlite: %v", err)
	}
	return db
}

var (
	boundaryPGExtOnce  sync.Once
	boundaryPGExtErr   error
	boundaryPGSchemaN  int
	boundaryPGSchemaMu sync.Mutex
)

// openBoundaryPostgres gives this test its own schema on the shared
// container, the same isolation internal/store's own postgres helper uses --
// re-derived here rather than imported, for the reason boundaryEngine's
// comment gives.
func openBoundaryPostgres(t *testing.T) *store.DB {
	t.Helper()
	baseDSN := os.Getenv("INV_TEST_POSTGRES_DSN")

	boundaryPGExtOnce.Do(func() {
		admin, err := store.Open(store.DriverPostgres, baseDSN)
		if err != nil {
			boundaryPGExtErr = fmt.Errorf("opening postgres for extension setup: %w", err)
			return
		}
		defer admin.Close()
		if _, err := admin.Writer.Exec(`CREATE EXTENSION IF NOT EXISTS pg_trgm`); err != nil {
			boundaryPGExtErr = fmt.Errorf("creating pg_trgm: %w", err)
		}
	})
	if boundaryPGExtErr != nil {
		t.Fatalf("postgres setup: %v", boundaryPGExtErr)
	}

	boundaryPGSchemaMu.Lock()
	boundaryPGSchemaN++
	n := boundaryPGSchemaN
	boundaryPGSchemaMu.Unlock()
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, strings.ToLower(t.Name()))
	if len(safe) > 40 {
		safe = safe[:40]
	}
	schema := fmt.Sprintf("rbac_%s_%d", safe, n)

	admin, err := store.Open(store.DriverPostgres, baseDSN)
	if err != nil {
		t.Fatalf("opening postgres: %v", err)
	}
	if _, err := admin.Writer.Exec(`CREATE SCHEMA ` + schema); err != nil {
		admin.Close()
		t.Fatalf("creating schema %s: %v", schema, err)
	}
	admin.Close()

	// DROP REGISTERED HERE, immediately after the CREATE succeeds, and not
	// after the store.Open below.
	//
	// The schema name is t.Name() lowercased, mapped and TRUNCATED TO 40
	// CHARACTERS, so two tests whose names agree that far apart share it.
	// That is survivable while every run drops what it made; it stops being
	// survivable the moment one leaks, because the leftover then fails every
	// later run with "schema already exists" -- a failure that looks like a
	// logic bug in whichever test happens to draw the name, reproduces only
	// on Postgres, and survives `git stash`. It cost a diagnosis here: the
	// leak came from an earlier failure between the CREATE and the Cleanup
	// registration, where this function had two more fallible calls.
	t.Cleanup(func() {
		cleanup, err := store.Open(store.DriverPostgres, baseDSN)
		if err != nil {
			return
		}
		defer cleanup.Close()
		_, _ = cleanup.Writer.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
	})

	sep := "?"
	if strings.Contains(baseDSN, "?") {
		sep = "&"
	}
	db, err := store.Open(store.DriverPostgres, baseDSN+sep+"search_path="+schema+",public")
	if err != nil {
		t.Fatalf("opening postgres on schema %s: %v", schema, err)
	}
	t.Cleanup(func() {
		db.Close()
		cleanup, err := store.Open(store.DriverPostgres, baseDSN)
		if err != nil {
			return
		}
		defer cleanup.Close()
		cleanup.Writer.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
	})
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrating postgres: %v", err)
	}
	return db
}

// ---------------------------------------------------------------------------
// The harness. Builds the same *harness type web_test.go's other files use --
// so csrfToken/post/get/login/logout are all available unchanged -- but over
// an engine-supplied *store.DB instead of the package's SQLite-only template,
// because this suite is explicitly required to run against both engines.
//
// Sessions use scs's default in-memory store rather than the sqlite3-backed
// one newHarnessSecure uses: session storage is independent of which engine
// backs the inventory data, and an in-memory store keeps this constructor
// from acquiring an sqlite-specific dependency it does not need.
func newBoundaryHarness(t *testing.T, adminUsername string, st *store.SQLStore) *harness {
	t.Helper()

	sessions := scs.New()
	sessions.Cookie.Secure = false
	sessions.Cookie.Name = "invctl_session"

	renderer, err := render.New(webassets.FS, false, "EUR")
	if err != nil {
		t.Fatalf("parsing templates: %v", err)
	}

	cfg := &config.Config{AdminUsers: []string{adminUsername}, AuthLocal: true, SecureCookies: false}
	authz := auth.NewAuthorizer(cfg.AdminUsers, st)
	app := &handlers.App{
		Store:    st,
		Render:   renderer,
		Sessions: sessions,
		Auth:     auth.NewChain(st, auth.NewLocalAuthenticator(st)),
		Authz:    authz,
		Config:   cfg,
	}

	// nil, nil: no agent surface, no reader surface. This suite drives the
	// browser-facing write bucket only, per the brief's harness rule --
	// Routes(app, static, authz, nil, nil).
	server := httptest.NewServer(web.Routes(app, staticFS(t), authz, nil, nil))
	t.Cleanup(server.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("building cookie jar: %v", err)
	}
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &harness{t: t, server: server, client: client, store: st}
}

// ---------------------------------------------------------------------------
// Fixtures.

const (
	boundaryAdminUser     = "boundary-admin"
	boundaryAdminPassword = "boundary-admin-password"
	boundaryObserverUser  = "boundary-observer"
	boundaryObserverPass  = "boundary-observer-password"
	boundaryOwnerUser     = "boundary-owner"
	boundaryOwnerPassword = "boundary-owner-password"
)

// boundaryFixtures names the rows this suite drives write routes against.
type boundaryFixtures struct {
	projectAlpha string // the project owner's own project
	projectOther string // a project the owner is NOT assigned to

	assetIn, assetOut     string
	serviceIn, serviceOut string
	circuitIn, circuitOut string

	// Real rows for the path segments a route commonly names, drawn from the
	// seed estate or built directly. Anything not listed here falls back to
	// store.NewID() at substitution time -- see paramsFor's own comment for
	// why that fallback is safe for what this suite measures today.
	teamID, userOtherID                               string
	manufacturerID, deviceTypeID                      string
	powerSourceID, powerPanelID, powerFeedID          string
	vlanID, networkGroupID, endpointID, environmentID string
	certificateID, tagID, customFieldID               string
	// savedViewID names a saved view owned by boundaryAdminUser -- neither
	// the Observer nor the project owner this suite drives, so
	// POST /views/{id}/retire resolves a REAL row and gets refused by
	// authorizeSavedViewOwner (403), not by GetSavedView (404). Without a
	// real id here the {id} placeholder falls back to store.NewID() (see
	// paramsFor), which is a self-gated route's ordinary case: it reaches
	// its handler (self only requires RequireAuth) and 404s at
	// GetSavedView -- exactly the false failure
	// TestNoWriteRouteIsReachableWithoutGoingThroughTheRouter exists to
	// catch, for a route that in fact resolves fine.
	savedViewID string
}

// anyRef returns an arbitrary value from a seed.Refs map, t.Fatal if empty --
// the exact fixture row does not matter to this suite, only that it is real.
func anyRef(t *testing.T, m map[string]string, label string) string {
	t.Helper()
	for _, v := range m {
		return v
	}
	t.Fatalf("seed fixture has no %s to draw a boundary-suite id from", label)
	return ""
}

// setupBoundary seeds the demo estate plus this suite's own RBAC fixture:
// an administrator (via the INV_ADMIN_USERS break-glass, matching how every
// other harness in this package grants it), an observer, and a project owner
// assigned to a project ("alpha") that owns one asset, one service and one
// circuit -- with a second asset/service/circuit of each kind left OUTSIDE
// any project the owner holds, per the brief's fixture spec.
func setupBoundary(t *testing.T, eng boundaryEngine) (*harness, *boundaryFixtures) {
	t.Helper()
	ctx := context.Background()
	db := eng.open(t)
	st := store.New(db)

	refs, err := seed.Load(ctx, st)
	if err != nil {
		t.Fatalf("seeding the %s estate: %v", eng.name, err)
	}

	h := newBoundaryHarness(t, boundaryAdminUser, st)
	admin := domain.AdministratorPermit(domain.SystemActor)

	// The three accounts.
	mustBoundaryUser(t, ctx, h, boundaryAdminUser, boundaryAdminPassword, domain.RoleObserver)
	mustBoundaryUser(t, ctx, h, boundaryObserverUser, boundaryObserverPass, domain.RoleObserver)
	other := mustBoundaryUser(t, ctx, h, boundaryOwnerUser+"-other", "irrelevant-password-000", domain.RoleObserver)

	owner := mustBoundaryUser(t, ctx, h, boundaryOwnerUser, boundaryOwnerPassword, domain.RoleProjectOwner)

	alpha, err := domain.NewProject(store.NewID(), domain.ProjectSpec{Code: "t-alpha", Name: "Alpha"}, st.Now())
	if err != nil {
		t.Fatalf("building project alpha: %v", err)
	}
	if err := h.store.CreateProject(ctx, admin, alpha); err != nil {
		t.Fatalf("creating project alpha: %v", err)
	}
	if err := h.store.AssignProject(ctx, admin, owner.ID, alpha.ID); err != nil {
		t.Fatalf("assigning the project owner to alpha: %v", err)
	}
	projectOther := anyRef(t, refs.Projects, "project")

	assetIn := mustBoundaryAsset(t, ctx, h, "t-alpha-asset")
	assetOut := mustBoundaryAsset(t, ctx, h, "t-outside-asset")
	assetLink, err := domain.NewProjectAssetLink(alpha.ID, assetIn, domain.ProjectOwns, nil, st.Now())
	if err != nil {
		t.Fatalf("building the in-scope asset link: %v", err)
	}
	if err := h.store.LinkProjectAsset(ctx, admin, assetLink); err != nil {
		t.Fatalf("linking the in-scope asset to alpha: %v", err)
	}

	env := anyRef(t, refs.Environments, "environment")
	serviceIn := mustBoundaryService(t, ctx, h, "t-alpha-svc", env)
	serviceOut := mustBoundaryService(t, ctx, h, "t-outside-svc", env)
	serviceLink, err := domain.NewProjectServiceLink(alpha.ID, serviceIn, domain.ProjectOwns, nil, st.Now())
	if err != nil {
		t.Fatalf("building the in-scope service link: %v", err)
	}
	if err := h.store.LinkProjectService(ctx, admin, serviceLink); err != nil {
		t.Fatalf("linking the in-scope service to alpha: %v", err)
	}

	provider, err := domain.NewProvider(store.NewID(), "t-provider")
	if err != nil {
		t.Fatalf("building provider: %v", err)
	}
	if err := h.store.CreateProvider(ctx, admin, provider); err != nil {
		t.Fatalf("creating provider: %v", err)
	}
	circuitIn := mustBoundaryCircuit(t, ctx, h, "t-alpha-circuit", provider.ID)
	circuitOut := mustBoundaryCircuit(t, ctx, h, "t-outside-circuit", provider.ID)
	circuitLink, err := domain.NewProjectCircuitLink(alpha.ID, circuitIn, domain.ProjectOwns, nil, st.Now())
	if err != nil {
		t.Fatalf("building the in-scope circuit link: %v", err)
	}
	if err := h.store.LinkProjectCircuit(ctx, admin, circuitLink); err != nil {
		t.Fatalf("linking the in-scope circuit to alpha: %v", err)
	}

	adminRow, err := h.store.GetUserByUsername(ctx, boundaryAdminUser)
	if err != nil {
		t.Fatalf("looking up the boundary admin: %v", err)
	}
	teamID := anyRef(t, refs.Teams, "team")

	tag, err := domain.NewTag(store.NewID(), "t-boundary-tag", "Boundary tag",
		"a fixture tag for the RBAC boundary suite", adminRow.ID, st.Now())
	if err != nil {
		t.Fatalf("building tag: %v", err)
	}
	if err := h.store.CreateTag(ctx, admin, tag); err != nil {
		t.Fatalf("creating tag: %v", err)
	}

	cf, err := domain.NewCustomField(store.NewID(), domain.CustomFieldEntityAsset, "t_boundary_cf",
		"Boundary field", domain.CustomFieldText, "a fixture custom field for the RBAC boundary suite",
		adminRow.ID, teamID, st.Now())
	if err != nil {
		t.Fatalf("building custom field: %v", err)
	}
	if err := h.store.CreateCustomField(ctx, admin, cf); err != nil {
		t.Fatalf("creating custom field: %v", err)
	}

	certificateID := ""
	if certs, err := h.store.ListCertificates(ctx, store.CertificateFilter{}); err == nil && len(certs) > 0 {
		certificateID = certs[0].ID
	}

	view, err := domain.NewSavedView(store.NewID(), adminRow.ID, domain.SavedViewAsset,
		"t-boundary-view", `{"kind":["server"]}`, st.Now())
	if err != nil {
		t.Fatalf("building saved view: %v", err)
	}
	adminUserPermit := domain.ScopedPermit(domain.UserActor(adminRow), nil, nil)
	if err := h.store.CreateSavedView(ctx, adminUserPermit, view); err != nil {
		t.Fatalf("creating saved view: %v", err)
	}

	fx := &boundaryFixtures{
		projectAlpha: alpha.ID, projectOther: projectOther,
		assetIn: assetIn, assetOut: assetOut,
		serviceIn: serviceIn, serviceOut: serviceOut,
		circuitIn: circuitIn, circuitOut: circuitOut,
		teamID: teamID, userOtherID: other.ID,
		manufacturerID: anyRef(t, refs.Manufacturers, "manufacturer"),
		deviceTypeID:   anyRef(t, refs.DeviceTypes, "device type"),
		powerSourceID:  anyRef(t, refs.PowerSources, "power source"),
		powerPanelID:   anyRef(t, refs.PowerPanels, "power panel"),
		powerFeedID:    anyRef(t, refs.PowerFeeds, "power feed"),
		vlanID:         anyRef(t, refs.VLANs, "vlan"),
		networkGroupID: anyRef(t, refs.NetGroups, "network group"),
		endpointID:     anyRef(t, refs.Endpoints, "endpoint"),
		environmentID:  env,
		certificateID:  certificateID,
		tagID:          tag.ID,
		customFieldID:  cf.ID,
		savedViewID:    view.ID,
	}
	return h, fx
}

func mustBoundaryUser(t *testing.T, ctx context.Context, h *harness, username, password, role string) *domain.AppUser {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hashing password for %s: %v", username, err)
	}
	u, err := domain.NewAppUser(store.NewID(), username, domain.UserSourceLocal, h.store.Now())
	if err != nil {
		t.Fatalf("building user %s: %v", username, err)
	}
	u.PasswordHash = &hash
	u.Role = role
	if err := h.store.CreateUser(ctx, domain.AdministratorPermit(domain.SystemActor), u); err != nil {
		t.Fatalf("creating user %s: %v", username, err)
	}
	return u
}

func mustBoundaryAsset(t *testing.T, ctx context.Context, h *harness, name string) string {
	t.Helper()
	a, err := domain.NewAsset(store.NewID(), domain.KindServer, name, nil, h.store.Now())
	if err != nil {
		t.Fatalf("building asset %s: %v", name, err)
	}
	if err := h.store.CreateAsset(ctx, domain.AdministratorPermit(domain.SystemActor), a, nil); err != nil {
		t.Fatalf("creating asset %s: %v", name, err)
	}
	return a.ID
}

func mustBoundaryService(t *testing.T, ctx context.Context, h *harness, code, envID string) string {
	t.Helper()
	svc, err := domain.NewService(store.NewID(), domain.ServiceSpec{
		Code: code, Name: code, Kind: domain.SvcAPI,
		EnvironmentID: envID, Availability: domain.AvailStandalone, Tier: 2,
	}, h.store.Now())
	if err != nil {
		t.Fatalf("building service %s: %v", code, err)
	}
	if err := h.store.CreateService(ctx, domain.AdministratorPermit(domain.SystemActor), svc); err != nil {
		t.Fatalf("creating service %s: %v", code, err)
	}
	return svc.ID
}

func mustBoundaryCircuit(t *testing.T, ctx context.Context, h *harness, cid, providerID string) string {
	t.Helper()
	c, err := domain.NewCircuit(store.NewID(), cid, providerID)
	if err != nil {
		t.Fatalf("building circuit %s: %v", cid, err)
	}
	if err := h.store.CreateCircuit(ctx, domain.AdministratorPermit(domain.SystemActor), c); err != nil {
		t.Fatalf("creating circuit %s: %v", cid, err)
	}
	return c.ID
}

// ---------------------------------------------------------------------------
// Driving a route through the real router.

// boundaryCSRFPattern reads the token every authenticated page carries in
// base.html's hx-headers attribute -- present on EVERY full-page render,
// unlike the hidden csrf_token form input the package's other csrfToken
// helper looks for, which only exists on pages that happen to render a form.
// The dashboard this suite fetches it from has none.
var boundaryCSRFPattern = regexp.MustCompile(`hx-headers='\{"X-CSRF-Token":\s*"([^"]+)"\}'`)

func boundaryCSRFToken(t *testing.T, h *harness) string {
	t.Helper()
	resp := h.get("/", false)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the dashboard for a CSRF token: %v", err)
	}
	m := boundaryCSRFPattern.FindSubmatch(b)
	if m == nil {
		t.Fatalf("no CSRF token found on the dashboard")
	}
	return html.UnescapeString(string(m[1]))
}

// paramsFor resolves every {name} placeholder in pattern to a real fixture
// row where one is cheap to have, and to a syntactically valid but
// nonexistent id (store.NewID()) otherwise.
//
// THE FALLBACK IS SAFE FOR WHAT THIS SUITE MEASURES TODAY, and that claim is
// checked, not assumed: net/http.ServeMux dispatches on path SHAPE alone --
// it does not consult the database, so a fallback id can never itself
// produce a router-level 404 (TestNoWriteRouteIsReachableWithoutGoingThrough-
// TheRouter's own mutation, Step 6 Row 4, proves this file's 404 assertion
// catches a route that genuinely stops resolving). And for every persona this
// file drives -- Observer and a project owner, with auth.CanWrite(Project-
// Owner) still false (see this file's package comment) -- the request is
// refused at middleware.RequireWrite/RequireAdministrator BEFORE the mux's
// chosen handler ever reads a path parameter, so no fallback id is ever
// dereferenced against the store during this suite's runs. The day Task 13
// lands and a project owner starts reaching handlers for real, the routes
// that matters for (the ~40 asset/service/circuit ones) already resolve to
// REAL rows below; the long tail of narrow sub-resources that still falls
// back (cost lines, journal notes, patch-throughs, terminations, and
// similar) are Administrator-only both today and after Task 13 -- see
// docs/rbac-design.md §4, "estate-wide configuration" and the routes this
// task's report lists as writeAdminOnly -- so a project owner never reaches
// them regardless.
func paramsFor(pattern string, fx *boundaryFixtures) string {
	parts := strings.SplitN(pattern, " ", 2)
	path := parts[len(parts)-1]
	segments := strings.Split(strings.Trim(path, "/"), "/")

	resolveByPreceding := map[string]func() string{
		"environments":  func() string { return fx.environmentID },
		"users":         func() string { return fx.userOtherID },
		"assets":        func() string { return fx.assetOut },
		"certificates":  func() string { return fx.certificateID },
		"sources":       func() string { return fx.powerSourceID },
		"panels":        func() string { return fx.powerPanelID },
		"feeds":         func() string { return fx.powerFeedID },
		"manufacturers": func() string { return fx.manufacturerID },
		"types":         func() string { return fx.deviceTypeID },
		"teams":         func() string { return fx.teamID },
		"projects":      func() string { return fx.projectOther },
		"custom-fields": func() string { return fx.customFieldID },
		"tags":          func() string { return fx.tagID },
		"services":      func() string { return fx.serviceOut },
		"circuits":      func() string { return fx.circuitOut },
		"vlans":         func() string { return fx.vlanID },
		"groups":        func() string { return fx.networkGroupID },
		"views":         func() string { return fx.savedViewID },
	}
	resolveByName := map[string]func() string{
		"assetID":   func() string { return fx.assetOut },
		"serviceID": func() string { return fx.serviceOut },
		"circuitID": func() string { return fx.circuitOut },
		"projectID": func() string { return fx.projectAlpha },
	}

	for i, seg := range segments {
		if !strings.HasPrefix(seg, "{") || !strings.HasSuffix(seg, "}") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(seg, "{"), "}")
		var value string
		if name == "id" && i > 0 {
			if resolve, ok := resolveByPreceding[segments[i-1]]; ok {
				value = resolve()
			}
		} else if resolve, ok := resolveByName[name]; ok {
			value = resolve()
		}
		if value == "" {
			value = store.NewID()
		}
		segments[i] = value
	}
	return "/" + strings.Join(segments, "/")
}

// driveRoute drives one write-bucket route through the real router as
// whichever persona h is currently logged in as, substituting real fixture
// ids for its path parameters and a valid CSRF token for a non-GET request.
// A fresh CSRF token is fetched per call, matching this package's existing
// convention (see e.g. projects_test.go's makeProject) rather than reusing
// one across many requests.
func driveRoute(t *testing.T, h *harness, route routescan.Route, fx *boundaryFixtures) *http.Response {
	t.Helper()
	parts := strings.SplitN(route.Pattern, " ", 2)
	method, path := parts[0], paramsFor(route.Pattern, fx)

	if method == http.MethodGet {
		return h.do(h.request(http.MethodGet, path, nil))
	}

	token := boundaryCSRFToken(t, h)
	req := h.request(method, path, strings.NewReader(url.Values{}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", h.server.URL)
	req.Header.Set("X-CSRF-Token", token)
	return h.do(req)
}

// drainedBody reads and closes resp's body, for a caller that only wants the
// text (to distinguish which layer refused a request).
func drainedBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response body: %v", err)
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// Step 2: every write route refuses an Observer.

func TestEveryRegisteredWriteRouteRefusesAnObserver(t *testing.T) {
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)
			h.login(boundaryObserverUser, boundaryObserverPass)

			routes := routescan.WriteRoutes(t)
			if len(routes) == 0 {
				t.Fatal("routescan.WriteRoutes returned no routes -- this suite would pass vacuously")
			}
			driven := 0
			for _, route := range routes {
				if route.Gate == "self" {
					// self routes are deliberately reachable by an Observer
					// -- their subject is the signed-in person, not the
					// estate (routes.go's own comment on the self
					// registrar) -- so "refuses an Observer" is the wrong
					// claim to make about them. Their own coverage lives in
					// savedviews_observer_test.go: an Observer CAN manage
					// their own saved view and CANNOT reach another
					// person's, which is the actual boundary a self route
					// has to hold.
					continue
				}
				driven++
				resp := driveRoute(t, h, route, fx)
				body := drainedBody(t, resp)
				if resp.StatusCode != http.StatusForbidden {
					t.Errorf("%s as an observer returned %d (body %q), want 403",
						route.Pattern, resp.StatusCode, truncate(body))
				}
			}
			if driven == 0 {
				t.Fatal("every route was excluded as self-gated -- this suite would pass vacuously")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Step 3: no write route is reachable without going through the router.
//
// A release once shipped with four green checks and a 404 on a button
// because handler tests injected router params by hand and never consulted
// the mux. Every response here comes from the real router (driveRoute always
// goes through h.client -> httptest.Server -> web.Routes), so a 404 here
// means the PATTERN itself does not resolve -- which would make every 403
// assertion elsewhere in this file vacuous, since a 404 also satisfies
// "not 200".

func TestNoWriteRouteIsReachableWithoutGoingThroughTheRouter(t *testing.T) {
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)
			h.login(boundaryObserverUser, boundaryObserverPass)

			routes := routescan.WriteRoutes(t)
			if len(routes) == 0 {
				t.Fatal("routescan.WriteRoutes returned no routes -- this suite would pass vacuously")
			}
			for _, route := range routes {
				resp := driveRoute(t, h, route, fx)
				body := drainedBody(t, resp)
				if resp.StatusCode == http.StatusNotFound {
					t.Errorf("%s returned 404 (body %q) -- the route does not resolve",
						route.Pattern, truncate(body))
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Step 4: a project owner reaches every non-project-linkable write handler
// (WP-G1 Task 13 flipped auth.CanWrite(RoleProjectOwner) to true), and this
// asserts WHICH LAYER, if any, refuses each one -- not merely a status code.
//
// WHY THIS CANNOT BE "every driven route is refused at 403", THE WAY IT WAS
// BEFORE TASK 13: driveRoute submits an empty form body (this suite's own
// design -- see driveRoute's comment -- because building valid payloads for
// ~130 distinct handlers is Tasks 14-17's job, done with real fixtures,
// against real scope logic, not this enumeration's). Reaching a real handler
// with no form data hits THREE different outcomes ahead of the permit check,
// all legitimate and none of them a security gap:
//   - Required-field validation refuses first (422), the object's scope is
//     never consulted.
//   - A handful of GETs in the write bucket are render-only forms/listings
//     (routescan's own doc comment names them) -- CanRead is unscoped for
//     every authenticated user (rbac-design.md §2), so these now render 200,
//     which is not a new disclosure.
//   - Several handlers (ManufacturerRetire and friends, JournalCreate via
//     journalRefused) render the SAME redirect-with-flash-message on every
//     outcome -- success, a business conflict, AND a permit refusal are all
//     a 303 with an empty body over HTTP, because the reason lives in a
//     flash cookie this suite does not decode. That looked, on first read of
//     this task, like an authorization bypass; it is not. Every write in
//     this codebase runs inside writeTx (store.go), which rolls back the
//     WHOLE transaction -- including an already-executed UPDATE -- the
//     moment t.log's authorize() call returns an error (verified by reading
//     RetireManufacturer: the exec runs before the logUpdate that can still
//     refuse and unwind it). A misleading flash message is a UX defect
//     worth a separate ticket, not a write that reached the database.
//
// So Step 4 pins the two numbers that ARE a clean, falsifiable claim about
// the layer shift, plus the number of routes that DO reach a distinguishable
// permit refusal despite the empty body:
//   - adminGate == 0: no "write"-gated route refuses a project owner at
//     middleware.RequireWrite anymore. This is the headline of the whole
//     task -- if this is ever nonzero again, CanWrite regressed.
//   - administratorGate == 6: the writeAdminOnly import surface is
//     untouched by this flip, because it gates on IsAdministrator, not
//     CanWrite, and a project owner is never an Administrator.
//   - permitGate == 10: routes whose handler's error path preserves
//     handleStoreError's generic "You are not allowed to do that." text
//     for a plain domain.ErrForbidden, reached with an empty body. 9, not
//     8: WP-G4b Wave B moved POST /views/{id}/retire off `write` onto
//     `self`, and gave this suite a real saved-view fixture row to drive it
//     against (savedViewID, owned by boundaryAdminUser) instead of a
//     random fallback id -- so it now resolves to a real row a project
//     owner does not own and is refused at authorizeSavedViewOwner, one
//     more permit-layer 403 than before. Previously it fell back to
//     store.NewID() and 404'd at GetSavedView instead, landing in "other"
//     -- a real row was never in scope to prove the refusal, so the old 8
//     undercounted what this route actually does. 10, not 9: Task 5 added
//     POST /views/{id}/rename to the same `self` registrar, on the same
//     "views" -> savedViewID fixture resolution, so an empty-body project-
//     owner request also resolves to a real row it does not own and is
//     refused at authorizeSavedViewOwner before UpdateSavedView's own
//     Validate() ever runs (the store authorizes before validating -- see
//     UpdateSavedView's comment) -- another clean permit-layer 403.
//   - userForbiddenGate == 2: the two /users/* mutation routes, which go
//     through respondUserMutation's own deliberate exception (see that
//     function's comment) and therefore surface the raw wrapped error
//     instead of the generic text.
//   - every other driven route must return a status under 500 -- this suite
//     cannot know the semantic outcome of each one without real payloads,
//     but a project owner request must never crash a handler.
//
// If a future change makes any of these four counts drift, THAT IS THE
// SIGNAL to look, the same way the original three-body switch was -- a
// route quietly moving from admin-only to a wide-open write, or a new
// handler adopting respondUserMutation's raw-error pattern for a route
// this test does not yet know about.
func TestAProjectOwnerIsRefusedOnEveryNonProjectLinkableWriteRoute(t *testing.T) {
	const (
		bodyRequireWrite         = "You have read-only access.\n"
		bodyRequireAdministrator = "This requires an Administrator.\n"
		bodyPermitRefused        = "You are not allowed to do that.\n"
		// Task 4a: middleware.RequireCostVisibility's refusal text, checked
		// on the same "which layer refused this" basis as the three above.
		// boundaryOwnerUser is a project owner with app_user.can_see_costs
		// left at its zero value (false), so a writeCost route it reaches
		// through RequireWrite is refused here, not at the permit layer.
		bodyCostVisibilityRefused = "You may not view or change costs.\n"
	)
	isUserForbiddenBody := func(body string) bool {
		return strings.HasPrefix(body, "writing change log for app_user ") &&
			strings.HasSuffix(body, ": forbidden\n")
	}

	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)
			h.login(boundaryOwnerUser, boundaryOwnerPassword)

			routes := routescan.WriteRoutes(t)
			linkable := routeScopedTypes(t, routes)
			excluded := excludedFromStep4(routes, linkable)

			// THE ASSERTION THAT CARRIES THE SECURITY CLAIM. Every route
			// driven below is, by construction of excludedFromStep4, one a
			// project owner may NOT write -- so whatever status each one
			// returns, the estate must be byte-identical afterwards.
			//
			// This exists because the status-code buckets stopped being
			// sufficient at Task 13. Before the flip, RequireWrite refused
			// every route before it parsed a body, so an empty body was
			// enough to prove refusal. After the flip a project owner
			// passes the middleware and most handlers now fail on form
			// validation, a no-op redirect or a 404 BEFORE the permit is
			// ever consulted -- 119 of 135 routes land in "other", and a
			// 3xx from a handler that redirects on both success and no-op
			// is indistinguishable from a write that worked.
			//
			// Counting change_log closes that hole without needing a valid
			// payload for 135 routes: tx.log is the only INSERT INTO
			// change_log in this codebase, so a declared-state write that
			// committed leaves a row here and one that was refused or
			// rolled back does not. It turns "119 routes returned
			// something" into "119 routes wrote nothing", which is the
			// property the test's name claims.
			changeLogBefore := h.count(`SELECT COUNT(*) FROM change_log`)

			var driven, adminGate, administratorGate, permitGate, userForbiddenGate, costVisibilityGate, other int
			for _, route := range routes {
				if excluded[route.Pattern] {
					continue
				}
				driven++
				resp := driveRoute(t, h, route, fx)
				body := drainedBody(t, resp)

				if resp.StatusCode >= http.StatusInternalServerError {
					t.Errorf("%s as a project owner returned %d (body %q) -- a project owner's request must never crash a handler",
						route.Pattern, resp.StatusCode, truncate(body))
					continue
				}
				if resp.StatusCode != http.StatusForbidden {
					other++
					continue
				}
				switch {
				case body == bodyRequireWrite:
					adminGate++
					if route.Gate != "write" {
						t.Errorf("%s refused by RequireWrite's text but registered gate is %q", route.Pattern, route.Gate)
					}
				case body == bodyRequireAdministrator:
					administratorGate++
					if route.Gate != "writeAdminOnly" {
						t.Errorf("%s refused by RequireAdministrator's text but registered gate is %q", route.Pattern, route.Gate)
					}
				case body == bodyPermitRefused:
					permitGate++
				case body == bodyCostVisibilityRefused:
					costVisibilityGate++
					if route.Gate != "writeCost" {
						t.Errorf("%s refused by RequireCostVisibility's text but registered gate is %q", route.Pattern, route.Gate)
					}
				case isUserForbiddenBody(body):
					userForbiddenGate++
					if !strings.HasPrefix(route.Pattern, "POST /users/") {
						t.Errorf("%s refused with the app_user-forbidden text, but is not a /users/ route -- "+
							"respondUserMutation's raw-error exception has spread somewhere this test does not expect",
							route.Pattern)
					}
				default:
					t.Errorf("%s refused with an unrecognised body %q -- update this test's known refusal texts",
						route.Pattern, truncate(body))
				}
			}

			if driven == 0 {
				t.Fatal("no route was driven -- the exclusion filter ate the whole list")
			}
			if after := h.count(`SELECT COUNT(*) FROM change_log`); after != changeLogBefore {
				t.Errorf("change_log grew from %d to %d while driving %d routes a project owner "+
					"may not write. Whatever status those routes returned, at least one write "+
					"COMMITTED -- that is the escalation this suite exists to catch, and a "+
					"status-code bucket would not have shown it.",
					changeLogBefore, after, driven)
			}
			if adminGate != 0 {
				t.Errorf("RequireWrite refusals = %d, want 0 -- a project owner is being refused at "+
					"middleware on a route that should reach its handler now that CanWrite(project owner) is true",
					adminGate)
			}
			// 14, not 6: the six import routes plus the eight /users routes.
			// A whole-branch review found GET /users reachable by a project
			// owner -- registered through write(), which stopped meaning
			// "Administrator" at Task 13, and writing nothing, so the permit
			// layer had nothing to refuse. The roster renders every
			// username, role, cost grant and the note naming which accounts
			// hold INV_ADMIN_USERS break-glass access. Only the nav rail hid
			// it, which is not enforcement. All six moved to writeAdminOnly.
			// Task 19 added the two project-assignment routes to the same
			// bucket, for the same reason -- user_project is the table that
			// decides a project owner's own scope, so it stays
			// Administrator-only alongside the rest of user administration.
			if administratorGate != 14 {
				t.Errorf("RequireAdministrator refusals = %d, want 14 (six import routes "+
					"and eight /users routes)", administratorGate)
			}
			if permitGate != 10 {
				t.Errorf("permit-layer refusals (generic body) = %d, want 10 -- see this test's own "+
					"comment for the routes this pins", permitGate)
			}
			// 4: the four /projects/{id}/costs* routes (Task 4a moved all
			// cost-line write routes onto writeCost). Before this task these
			// four were NOT in permitGate at all -- they landed in "other",
			// because CostAddToProject/CostEditOnProject/CostRetireOnProject/
			// CostRepriceOnProject either redirect on an empty form (303) or
			// 404 against the random costID fallback BEFORE any permit check
			// runs, the same way most of this suite's driven routes do. Now
			// RequireCostVisibility refuses boundaryOwnerUser (can_see_costs
			// left false) before the handler is reached at all, so all four
			// become a clean 403 instead of a redirect or a 404.
			//
			// The other 13 cost routes (5 under /assets/, 4 under
			// /services/, 4 under /circuits/) never appear here: they are
			// already excluded by excludedFromStep4, because their first
			// path segment pluralises a project-linkable type, the same
			// exclusion that keeps the ~40 asset/service/circuit routes out
			// of this project-owner suite entirely.
			if costVisibilityGate != 4 {
				t.Errorf("RequireCostVisibility refusals = %d, want 4 (the four /projects/{id}/costs* "+
					"routes -- see this test's own comment)", costVisibilityGate)
			}
			// 0, and the counter stays. It was 2 while /users/{id}/active and
			// /users/{id}/scrub reached their handlers and were refused deep
			// at tx.log, surfacing respondUserMutation's raw wrapped error.
			// Those routes now refuse at the middleware instead, which is
			// strictly earlier and better. The bucket is kept so that if any
			// route ever surfaces that raw-error shape again -- the pattern
			// spreading to a handler this test does not know about -- it is
			// counted and fails here rather than landing silently in "other".
			if userForbiddenGate != 0 {
				t.Errorf("permit-layer refusals through respondUserMutation's raw-error path = %d, want 0 "+
					"-- the /users routes moved behind RequireAdministrator, so nothing should now "+
					"reach tx.log by that path; a nonzero count means the raw-error pattern has spread",
					userForbiddenGate)
			}
			t.Logf("%d driven, %d refused with a status other than 403 (validation, no-op-looking "+
				"redirects, 404s against random fallback ids, and similar -- see this test's own comment)",
				driven, other)
		})
	}
}

// excludedFromStep4 returns the set of patterns whose first path segment
// pluralises one of linkable's entity types -- the "~40 asset/service/
// circuit patterns" the brief names, derived structurally from the route
// table rather than hand-listed.
func excludedFromStep4(routes []routescan.Route, linkable []string) map[string]bool {
	plural := map[string]bool{}
	for _, ty := range linkable {
		plural[ty+"s"] = true
	}
	excluded := map[string]bool{}
	for _, r := range routes {
		if plural[firstPathSegment(r.Pattern)] {
			excluded[r.Pattern] = true
		}
	}
	return excluded
}

func firstPathSegment(pattern string) string {
	parts := strings.SplitN(pattern, " ", 2)
	path := strings.Trim(parts[len(parts)-1], "/")
	if i := strings.IndexByte(path, '/'); i >= 0 {
		return path[:i]
	}
	return path
}

// routeScopedTypes returns the entity types the ROUTER treats as
// project-linkable: the singular of every <segment> in a registered
// "POST /projects/{projectID}/<segment>/new" pattern (docs/rbac-design.md
// §4's create-and-link routes). Purely structural, from routes.go's own
// text via routescan -- see TestTheScopedRouteListMatchesTheClassification
// for the independently-derived other half of this comparison.
func routeScopedTypes(t *testing.T, routes []routescan.Route) []string {
	t.Helper()
	const prefix, suffix = "POST /projects/{projectID}/", "/new"
	seen := map[string]bool{}
	for _, r := range routes {
		if !strings.HasPrefix(r.Pattern, prefix) || !strings.HasSuffix(r.Pattern, suffix) {
			continue
		}
		segment := strings.TrimSuffix(strings.TrimPrefix(r.Pattern, prefix), suffix)
		seen[strings.TrimSuffix(segment, "s")] = true
	}
	types := make([]string, 0, len(seen))
	for ty := range seen {
		types = append(types, ty)
	}
	sort.Strings(types)
	if len(types) == 0 {
		t.Fatal("no POST /projects/{projectID}/<segment>/new route found -- this walker needs updating")
	}
	return types
}

// ---------------------------------------------------------------------------
// Step 5: the scoped route list matches the domain classification.
//
// TWO LISTS, DERIVED FROM TWO DIFFERENT FILES, THAT MUST AGREE:
//   - routeScopedTypes (above) reads routes.go's own text: which entity
//     types the ROUTER lets a project owner create-and-link.
//   - domainScopedEntityTypes (below) reads internal/auth/permit.go's own
//     text: which entity types auth.Authorizer.Permit actually builds a
//     domain.ScopedEntities scope for.
//
// Neither is derived from the other, so classifying a new type without a
// matching route (or registering a route without classifying the type) makes
// them disagree -- see Step 6's mutation for "team" moved into the
// classification with no matching route.
func TestTheScopedRouteListMatchesTheClassification(t *testing.T) {
	routes := routescan.WriteRoutes(t)
	fromRoutes := routeScopedTypes(t, routes)
	fromDomain := domainScopedEntityTypes(t)

	if !reflect.DeepEqual(fromRoutes, fromDomain) {
		t.Errorf("project-linkable route types = %v, domain.ScopedEntities classifies = %v -- "+
			"these must name exactly the same set (see this test's own doc comment)", fromRoutes, fromDomain)
	}
}

// domainScopedEntityTypes parses internal/auth/permit.go and returns the
// literal string keys of the domain.ScopedEntities composite literal built
// inside Authorizer.Permit -- the domain package's own definition of which
// entity types a project owner's scope can ever cover.
func domainScopedEntityTypes(t *testing.T) []string {
	t.Helper()
	root := repoRoot(t)
	fset := token.NewFileSet()
	path := filepath.Join(root, "internal/auth/permit.go")
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	var types []string
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "ScopedEntities" {
			return true
		}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			bl, ok := kv.Key.(*ast.BasicLit)
			if !ok || bl.Kind != token.STRING {
				continue
			}
			s, err := strconv.Unquote(bl.Value)
			if err != nil {
				continue
			}
			types = append(types, s)
		}
		return true
	})
	if len(types) == 0 {
		t.Fatalf("found no domain.ScopedEntities{} literal in %s -- this walker needs updating", path)
	}
	sort.Strings(types)
	return types
}

// truncate keeps a failure message readable when a handler's error page body
// is long.
func truncate(s string) string {
	const max = 120
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// ---------------------------------------------------------------------------
// Step 7: no write route is reachable with no session at all.
//
// Steps 2-5 all drive an AUTHENTICATED caller -- Observer or project owner --
// and prove object-level permission holds once someone is signed in. None of
// them prove middleware.RequireAuth is actually in the chain; that was
// verified only by reading routes.go. It became worth writing down at WP-G4b,
// which added a fourth registrar, self, whose ENTIRE gate is RequireAuth: for
// POST /views, POST /views/{id}/rename and POST /views/{id}/retire, an
// unnoticed break in that middleware is the whole authorization story, not
// one layer of it.
//
// Every non-GET route from the write, writeAdminOnly and self registrars is
// driven here -- the full census routescan.WriteRoutes returns, not a
// hand-picked subset, so a route registered under a new gate this file has
// never heard of is swept automatically rather than requiring this test to
// be told about it.
//
// WHY "REDIRECT TO /login OR A 4xx", NOT A SINGLE STATUS: CSRF wraps the
// WHOLE mux, outside every registrar (routes.go's own comment on Routes says
// so), so for a POST with no CSRF cookie at all nosurf can refuse first,
// before RequireAuth is ever reached. This sweep does not let that hide the
// gap: it fetches a genuine CSRF token from the public /login page before
// driving each route (driveUnauthenticated below), the same way a browser
// would carry one from any page it has loaded, so the request that reaches
// the mux is refused by RequireAuth specifically wherever CSRF was not the
// reason -- and the "or a 4xx" branch stays only for whatever this walk has
// not anticipated, rather than being the only thing distinguishing a pass.
func TestNoWriteRouteIsReachableWithNoSessionAtAll(t *testing.T) {
	// pinnedNoSessionRouteCount is every non-GET route the write,
	// writeAdminOnly and self registrars register today. Pinned rather than
	// merely asserted nonzero, the same way Step 4 pins permitGate == 10 -- a
	// sweep that drives zero routes passes trivially, and asserting only
	// "> 0" would not catch the census quietly shrinking by one route that
	// stopped being walked. Update this deliberately if the route count
	// genuinely changes; do not let it drift unnoticed. 181 -> 182: Task 5
	// added POST /views/{id}/rename to the self registrar.
	const pinnedNoSessionRouteCount = 182

	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)
			// Deliberately no h.login call anywhere in this test: the whole
			// point is a caller with no session at all.

			routes := routescan.WriteRoutes(t)

			// The change_log-unchanged invariant Step 4 established: tx.log
			// is the only INSERT INTO change_log in this codebase, so a
			// declared-state write that committed leaves a row here and one
			// that was refused does not, regardless of which status code the
			// handler happened to return.
			changeLogBefore := h.count(`SELECT COUNT(*) FROM change_log`)

			driven := 0
			for _, route := range routes {
				method := strings.SplitN(route.Pattern, " ", 2)[0]
				if method == http.MethodGet {
					continue
				}
				driven++

				resp := driveUnauthenticated(t, h, route, fx)
				body := drainedBody(t, resp)

				loginRedirect := resp.StatusCode == http.StatusSeeOther &&
					strings.Contains(resp.Header.Get("Location"), "/login")
				refusalStatus := resp.StatusCode >= 400 && resp.StatusCode < 500
				if !loginRedirect && !refusalStatus {
					t.Errorf("%s with no session returned %d (body %q), want a redirect to "+
						"/login (middleware.RequireAuth) or a 4xx refusal", route.Pattern, resp.StatusCode, truncate(body))
				}
			}

			if driven == 0 {
				t.Fatal("no non-GET route was driven -- this suite would pass vacuously")
			}
			if driven != pinnedNoSessionRouteCount {
				t.Errorf("drove %d non-GET write-bucket routes, want %d -- either the pin is stale "+
					"(update it deliberately) or the census just silently lost a route", driven, pinnedNoSessionRouteCount)
			}

			if after := h.count(`SELECT COUNT(*) FROM change_log`); after != changeLogBefore {
				t.Errorf("change_log grew from %d to %d while driving %d routes with no session "+
					"at all -- an unauthenticated caller wrote something",
					changeLogBefore, after, driven)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Task 4a: RequireCostVisibility is the layer that refuses, or lets through,
// a project owner writing a cost line -- proven end to end, through the real
// router, not just at the middleware unit level.
//
// Two claims, each needing its own case per this task's brief: a case where
// the grant is the ONLY thing standing between the caller and success, and a
// case where its absence is what refuses -- on a route and an asset where
// every OTHER check (RequireWrite, the object-scope permit) would have
// passed. fx.assetIn is owned by boundaryOwnerUser's own project (alpha), so
// a refusal here cannot be the permit layer running out of scope; it can
// only be RequireCostVisibility.
func TestAProjectOwnersCostWriteTurnsOnTheGrantAlone(t *testing.T) {
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)
			ctx := context.Background()
			admin := domain.AdministratorPermit(domain.SystemActor)

			ownerRow, err := h.store.GetUserByUsername(ctx, boundaryOwnerUser)
			if err != nil {
				t.Fatalf("looking up %s: %v", boundaryOwnerUser, err)
			}
			if ownerRow.CanSeeCosts {
				t.Fatalf("%s already has can_see_costs -- fixture assumption broken, this test proves nothing", boundaryOwnerUser)
			}

			// Case 1: no grant, in-scope asset. Refused, and refused BY
			// REQUIRECOSTVISIBILITY specifically -- not the permit layer,
			// which would have let this through (the asset is fx.assetIn,
			// alpha's own).
			h.login(boundaryOwnerUser, boundaryOwnerPassword)
			changeLogBefore := h.count(`SELECT COUNT(*) FROM change_log`)
			resp := h.post("/assets/"+fx.assetIn+"/costs", url.Values{
				"csrf_token": {h.csrfToken("/assets/" + fx.assetIn)},
				"kind":       {"operating"}, "period": {"monthly"}, "amount": {"10"},
			}, false)
			body := drainedBody(t, resp)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("ungranted owner writing an in-scope cost = %d (body %q), want 403", resp.StatusCode, truncate(body))
			}
			if body != "You may not view or change costs.\n" {
				t.Errorf("ungranted owner refused with body %q, want RequireCostVisibility's own text -- "+
					"a different refusal here means some OTHER layer caught this first, "+
					"which is not the claim this test makes", body)
			}
			if after := h.count(`SELECT COUNT(*) FROM change_log`); after != changeLogBefore {
				t.Errorf("change_log grew from %d to %d on a refused write", changeLogBefore, after)
			}

			// Case 2: grant it, same asset, same route. Now let through --
			// reaches the handler, writes, and change_log gets a row. If
			// this fails, the middleware refuses everyone regardless of the
			// grant, and Case 1 alone would not have caught that.
			if err := h.store.SetUserCostVisibility(ctx, admin, ownerRow.ID, true); err != nil {
				t.Fatalf("granting can_see_costs: %v", err)
			}
			// Re-baselined here: SetUserCostVisibility itself writes its own
			// change_log row (an app_user grant is declared state), which
			// would otherwise be mistaken for the cost write this case is
			// actually proving.
			changeLogBeforeGrantedWrite := h.count(`SELECT COUNT(*) FROM change_log`)
			resp2 := h.post("/assets/"+fx.assetIn+"/costs", url.Values{
				"csrf_token": {h.csrfToken("/assets/" + fx.assetIn)},
				"kind":       {"operating"}, "period": {"monthly"}, "amount": {"10"},
			}, false)
			body2 := drainedBody(t, resp2)
			if resp2.StatusCode != http.StatusSeeOther {
				t.Fatalf("granted owner writing an in-scope cost = %d (body %q), want a redirect", resp2.StatusCode, truncate(body2))
			}
			if after := h.count(`SELECT COUNT(*) FROM change_log`); after != changeLogBeforeGrantedWrite+1 {
				t.Errorf("change_log = %d after a granted owner's write, want %d (exactly one committed write)",
					after, changeLogBeforeGrantedWrite+1)
			}
		})
	}
}

// driveUnauthenticated drives one write-bucket route through the real
// router as a caller with no session cookie whatsoever, carrying a genuine
// CSRF token fetched from the public /login page -- the same one a browser
// that had merely loaded that page would carry -- so that a refusal is
// attributable to RequireAuth rather than to the CSRF layer running first.
func driveUnauthenticated(t *testing.T, h *harness, route routescan.Route, fx *boundaryFixtures) *http.Response {
	t.Helper()
	parts := strings.SplitN(route.Pattern, " ", 2)
	method, path := parts[0], paramsFor(route.Pattern, fx)

	token := h.csrfToken("/login")
	form := url.Values{"csrf_token": {token}}
	req := h.request(method, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", h.server.URL)
	return h.do(req)
}
