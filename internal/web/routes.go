// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Package web wires the router.
package web

import (
	"io/fs"
	"net/http"

	"github.com/madalinignisca/invctl/internal/auth"
	"github.com/madalinignisca/invctl/internal/web/handlers"
	"github.com/madalinignisca/invctl/internal/web/middleware"
)

// ObservationsPath is the one route a monitoring credential can reach.
//
// It is a constant because two things must agree about it and drifting apart
// would be silent in the dangerous direction: the route registration below, and
// the CSRF exemption. Deriving both from this identifier means an exemption
// cannot outlive or outgrow the route it was written for (docs/AUDIT.md rule 6).
const ObservationsPath = "/observations"

// AgentSurface is the machine-facing half of the router: one handler and the
// registry that guards it.
//
// It is a parameter rather than a field on handlers.App because the two must
// not share a store. App holds *store.SQLStore -- every declared mutation in
// the package -- and the webhook holds store.ObservedStore, which is two
// methods. Passing them separately is what keeps that difference real.
//
// A nil surface, or one with no configured credentials, mounts no route and
// registers no CSRF exemption: an estate that is not reporting yet should not
// be carrying the attack surface of one that is.
type AgentSurface struct {
	Registry *auth.AgentRegistry
	Handler  *handlers.ObservationAPI
	// SessionCookie is the browser session cookie's name, so the guard can
	// refuse a request carrying one by name.
	SessionCookie string
}

func (a *AgentSurface) enabled() bool {
	return a != nil && a.Handler != nil && a.Registry.Enabled()
}

// Routes builds the HTTP handler.
//
// Routing uses net/http.ServeMux with Go 1.22 method and wildcard patterns --
// no third-party router. The three groups below differ only in the middleware
// they carry: public, authenticated read, and authenticated write. A fourth
// group, the machine-facing route, carries none of them and is described where
// it is registered.
func Routes(app *handlers.App, static fs.FS, authz *auth.Authorizer, agents *AgentSurface) http.Handler {
	mux := http.NewServeMux()

	// Static assets are served without session or CSRF machinery; they are
	// embedded in the binary and identical for everyone.
	//
	// StripPrefix matters: the filesystem passed in is already rooted at the
	// static directory, so without it every request would look for
	// static/static/app.css and 404 -- which shows up as an unstyled page
	// rather than as an error, because a stylesheet that fails to load is not
	// a page failure.
	fileServer := http.StripPrefix("/static/", http.FileServer(http.FS(static)))
	mux.Handle("GET /static/", cacheStatic(fileServer))

	// Public.
	mux.HandleFunc("GET /healthz", app.Health)
	mux.HandleFunc("GET /login", app.LoginForm)
	mux.HandleFunc("POST /login", app.Login)
	mux.HandleFunc("POST /logout", app.Logout)

	// Authenticated reads.
	read := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, middleware.RequireAuth(h))
	}
	read("GET /{$}", app.Dashboard)
	read("GET /search", app.Search)
	read("GET /changes", app.ChangeLog)
	read("GET /changes/{id}", app.ChangeEntry)
	read("GET /reports/spanning", app.SpanningReport)
	read("GET /reports/expiry", app.ExpiryReport)

	read("GET /environments", app.EnvironmentList)
	read("GET /environments/{id}/map", app.EnvironmentMap)
	read("GET /assets", app.AssetList)
	read("GET /assets/{id}", app.AssetDetail)
	read("GET /assets/{id}/impact", app.AssetImpact)
	// A view, and only a view: GET, no CSRF, no RequireAdmin, and nothing on
	// the page it renders can change the estate or the model of it.
	read("GET /assets/{id}/neighbourhood", app.AssetNeighbourhood)
	read("GET /certificates", app.CertificateList)
	read("GET /certificates/{id}", app.CertificateDetail)
	read("GET /catalogue", app.Catalogue)
	read("GET /interfaces/{id}/trace", app.TracePort)
	read("GET /power", app.Power)
	read("GET /power/feeds/{id}/impact", app.PowerFeedImpact)
	read("GET /power/sources/{id}/impact", app.PowerSourceImpact)
	read("GET /reports/power", app.PowerReport)
	read("GET /teams", app.TeamList)
	read("GET /teams/{id}", app.TeamDetail)
	read("GET /projects", app.ProjectList)
	read("GET /projects/{id}", app.ProjectOverview)
	read("GET /projects/{id}/map", app.ProjectMap)
	read("GET /vocabularies", app.VocabularyList)
	read("GET /help", app.Help)
	read("GET /help/{topic}", app.Help)
	read("GET /paths", app.ServicePath)
	read("GET /services", app.ServiceList)
	read("GET /services/{id}", app.ServiceDetail)
	read("GET /prefixes", app.PrefixList)
	read("GET /vlans", app.VLANList)
	read("GET /clusters", app.ClusterList)
	read("GET /clusters/{id}", app.ClusterDetail)
	read("GET /circuits", app.CircuitList)
	read("GET /circuits/{id}", app.CircuitDetail)
	// "The fibre is cut, what goes dark" (WP-E1b). A circuit is not an asset,
	// so it cannot ride the asset simulator -- see circuit_impact.go.
	read("GET /circuits/{id}/impact", app.CircuitImpact)
	read("GET /allocations", app.RegistryList)
	read("GET /overlays", app.L2VPNList)
	read("GET /overlays/{id}", app.L2VPNDetail)
	read("GET /redundancy", app.FHRPList)
	read("GET /redundancy/{id}", app.FHRPDetail)
	read("GET /vlans/{id}", app.VLANDetail)
	read("GET /network", app.NetworkList)

	// Authenticated writes. Every non-GET route goes through RequireAdmin,
	// and the whole mux is wrapped in CSRF below.
	requireAdmin := middleware.RequireAdmin(authz)
	write := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, middleware.RequireAuth(requireAdmin(h)))
	}
	// The import page is admin-only on the GET as well as the POST. It is
	// purely a write tool: rendering it to a read-only user offers a form whose
	// only outcome is a 403.
	write("GET /imports", app.ImportJobList)
	write("GET /imports/{id}", app.ImportJobPage)
	write("GET /import/assets", app.AssetImportForm)
	write("POST /import/assets", app.AssetImportRun)
	write("GET /import/device-types", app.DeviceTypeImportForm)
	write("POST /import/device-types", app.DeviceTypeImportRun)

	write("POST /environments", app.EnvironmentCreate)

	write("POST /assets", app.AssetCreate)
	write("POST /assets/{id}", app.AssetUpdate)
	write("POST /assets/{id}/retire", app.AssetRetire)
	write("POST /assets/{id}/parent", app.AssetReparent)

	write("POST /services", app.ServiceCreate)
	// One route per surface, as with costs: an entity type arriving in a URL
	// is an entity type arriving from a request, and it would select a table.
	write("POST /certificates", app.CertificateCreate)
	write("POST /certificates/{id}", app.CertificateUpdate)
	write("POST /certificates/{id}/retire", app.CertificateRetire)
	write("POST /certificates/{id}/assets", app.CertificateDeployAsset)
	write("POST /certificates/{id}/assets/{assetID}/retire", app.CertificateUndeployAsset)
	write("POST /certificates/{id}/services", app.CertificateDeployService)
	write("POST /certificates/{id}/services/{serviceID}/retire", app.CertificateUndeployService)
	write("POST /assets/{id}/patch", app.PassThroughCreate)
	write("POST /assets/{id}/patch/{patchID}/retire", app.PassThroughRetire)
	write("POST /assets/{id}/power", app.PowerInputCreate)
	write("POST /assets/{id}/power/{inputID}/retire", app.PowerInputRetire)

	write("POST /power/sources", app.PowerSourceCreate)
	write("POST /power/sources/{id}/retire", app.PowerSourceRetire)
	write("POST /power/panels", app.PowerPanelCreate)
	write("POST /power/panels/{id}", app.PowerPanelUpdate)
	write("POST /power/panels/{id}/retire", app.PowerPanelRetire)
	write("POST /power/feeds", app.PowerFeedCreate)
	write("POST /power/feeds/{id}/retire", app.PowerFeedRetire)

	write("POST /catalogue/manufacturers", app.ManufacturerCreate)
	write("POST /catalogue/manufacturers/{id}", app.ManufacturerUpdate)
	write("POST /catalogue/manufacturers/{id}/retire", app.ManufacturerRetire)
	write("POST /catalogue/types", app.DeviceTypeCreate)
	write("POST /catalogue/types/{id}", app.DeviceTypeUpdate)
	write("POST /catalogue/types/{id}/retire", app.DeviceTypeRetire)

	write("POST /teams", app.TeamCreate)
	write("POST /teams/{id}", app.TeamUpdate)
	write("POST /teams/{id}/retire", app.TeamRetire)
	write("POST /projects", app.ProjectCreate)
	write("POST /projects/{id}", app.ProjectUpdate)
	write("POST /projects/{id}/retire", app.ProjectRetire)
	write("POST /projects/{id}/assets", app.ProjectAssetLink)
	write("POST /projects/{id}/assets/{assetID}/retire", app.ProjectAssetRetire)
	write("POST /projects/{id}/services", app.ProjectServiceLink)
	write("POST /projects/{id}/services/{serviceID}/retire", app.ProjectServiceRetire)
	// Circuits belong to projects too (WP-I2). Without this the project cost
	// rollup gathered assets and services and quietly understated every project
	// that depends on connectivity.
	write("POST /projects/{id}/circuits", app.ProjectCircuitLink)
	write("POST /projects/{id}/circuits/{circuitID}/retire", app.ProjectCircuitRetire)

	// Cost lines. One route per surface rather than a generic
	// /costs/{type}/{id}: an entity type arriving in a URL is an entity type
	// arriving from a request, and it would select a table name.
	write("POST /assets/{id}/costs", app.CostAddToAsset)
	write("POST /assets/{id}/costs/{costID}", app.CostEditOnAsset)
	write("POST /assets/{id}/costs/{costID}/retire", app.CostRetireOnAsset)
	write("POST /services/{id}/costs", app.CostAddToService)
	write("POST /services/{id}/costs/{costID}", app.CostEditOnService)
	write("POST /services/{id}/costs/{costID}/retire", app.CostRetireOnService)
	write("POST /projects/{id}/costs", app.CostAddToProject)
	write("POST /projects/{id}/costs/{costID}", app.CostEditOnProject)
	write("POST /projects/{id}/costs/{costID}/retire", app.CostRetireOnProject)
	write("POST /vocabularies", app.VocabularyUpsert)
	write("POST /services/{id}", app.ServiceUpdate)
	write("POST /services/{id}/retire", app.ServiceRetire)
	write("POST /services/{id}/instances", app.InstanceCreate)
	write("POST /services/{id}/endpoints", app.EndpointCreate)
	write("POST /services/{id}/dependencies", app.DependencyCreate)

	write("POST /instances/{id}/retire", app.InstanceRetire)
	// Correcting what a thing IS. Nothing here moves a thing: an endpoint stays
	// on its service, a placement stays on its host. Those re-point the graph
	// and have their own flows.
	write("POST /endpoints/{id}", app.EndpointUpdate)
	write("POST /endpoints/{id}/retire", app.EndpointRetire)
	write("POST /instances/{id}", app.InstanceUpdate)
	write("POST /environments/{id}", app.EnvironmentUpdate)
	write("POST /interfaces/{id}", app.InterfaceUpdate)
	write("POST /addresses/{id}", app.IPAddressUpdate)
	write("POST /prefixes/{id}", app.PrefixUpdate)

	// Operator overrides of an observation (docs/AUDIT.md rule 14). These are
	// DECLARED mutations -- a person decided that a reading is wrong -- so they
	// sit in the write group with everything else: CSRF, RequireAdmin, and a
	// change_log row in the same transaction. They are deliberately nowhere
	// near the machine-facing route below: a monitoring credential that could
	// write one could silence the estate it is reporting on.
	write("POST /overrides", app.HealthOverrideCreate)
	write("POST /overrides/{id}", app.HealthOverrideAmend)
	write("POST /overrides/{id}/clear", app.HealthOverrideClear)
	write("POST /dependencies/{id}/retire", app.DependencyRetire)
	write("POST /dependencies/{id}/verify", app.DependencyVerify)

	write("POST /assets/{id}/interfaces", app.InterfaceCreate)
	write("POST /addresses", app.IPAddressCreate)
	write("POST /links", app.LinkCreate)
	write("POST /links/{id}/retire", app.LinkRetire)
	write("POST /prefixes", app.PrefixCreate)
	// Reservations live on the prefixes page rather than a page of their own:
	// a span of addresses only means anything beside the network it falls in.
	write("POST /clusters", app.ClusterCreate)
	write("POST /clusters/{id}", app.ClusterUpdate)
	write("POST /clusters/{id}/hosts", app.ClusterSetHosts)
	write("POST /clusters/{id}/retire", app.ClusterRetire)
	write("POST /circuits", app.CircuitCreate)
	write("POST /circuits/{id}/retire", app.CircuitRetire)

	// Journal entries, on whatever page somebody is standing on. One route set
	// for every entity: {resource} is allow-listed in the handler, because the
	// table has no foreign key and would otherwise accept a note against
	// anything at all.
	for _, res := range handlers.JournalResources() {
		write("POST /"+res+"/{id}/journal", app.JournalCreate)
		write("POST /"+res+"/{id}/journal/{noteID}", app.JournalUpdate)
		write("POST /"+res+"/{id}/journal/{noteID}/retire", app.JournalRetire)
	}

	write("POST /circuits/{id}/terminations", app.CircuitLand)
	write("POST /circuits/{id}/terminations/{termID}/retire", app.CircuitLift)
	write("POST /circuits/{id}/costs", app.CostAddToCircuit)
	write("POST /circuits/{id}/costs/{costID}", app.CostEditOnCircuit)
	write("POST /circuits/{id}/costs/{costID}/retire", app.CostRetireOnCircuit)
	write("POST /providers", app.ProviderCreate)
	write("POST /overlays", app.L2VPNCreate)
	write("POST /overlays/{id}/retire", app.L2VPNRetire)
	write("POST /overlays/{id}/terminations", app.L2VPNAttach)
	write("POST /overlays/{id}/terminations/{termID}/retire", app.L2VPNDetach)
	write("POST /allocations", app.AggregateCreate)
	write("POST /allocations/{id}/retire", app.AggregateRetire)
	write("POST /asn", app.ASNCreate)
	write("POST /asn/{id}/retire", app.ASNRetire)
	write("POST /redundancy", app.FHRPCreate)
	write("POST /redundancy/{id}/retire", app.FHRPRetire)
	write("POST /redundancy/{id}/members", app.FHRPMemberAdd)
	write("POST /redundancy/{id}/members/{ifaceID}/remove", app.FHRPMemberRemove)
	write("POST /vlans", app.VLANCreate)
	write("POST /vlans/{id}/retire", app.VLANRetire)
	write("POST /vlans/{id}/ports", app.VLANPortAdd)
	write("POST /vlans/{id}/ports/{ifaceID}/remove", app.VLANPortRemove)
	write("POST /ip-ranges", app.IPRangeCreate)
	write("POST /ip-ranges/{id}/retire", app.IPRangeRetire)

	write("POST /network/groups", app.NetworkGroupCreate)
	write("POST /network/groups/{id}/members", app.NetworkGroupMemberCreate)
	write("POST /network/groups/{id}/uplinks", app.NetworkUplinkCreate)
	write("POST /network/attachments", app.NetworkAttachmentCreate)
	write("POST /network/anchors", app.NetworkAnchorCreate)
	write("POST /network/derive", app.NetworkDerive)

	// The machine-facing route. One route, and this is it.
	//
	// It carries RequireAgent and nothing else: no RequireAuth, no
	// RequireAdmin, and no session. A monitoring credential is a different
	// principal type from an operator (rule 6) -- it is not an app_user, it
	// never appears in INV_ADMIN_USERS, and authz.CanWrite's signature takes a
	// *domain.AppUser, so it could not reach it even if this line said so.
	//
	// The CSRF exemption is registered for this exact path and no other. Both
	// the pattern and the exemption are built from ObservationsPath, and the
	// exemption's parameter type is middleware.ExactPath, whose implementation
	// calls nosurf's ExemptPath -- never ExemptGlob or ExemptRegexp. That is
	// what stops the planned /api/inventory from inheriting it for free.
	var csrfExempt []middleware.ExactPath
	if agents.enabled() {
		guard := middleware.AgentGuard{
			Registry:        agents.Registry,
			Credentials:     middleware.NewRateLimiter(middleware.AgentRequestsPerSecond, middleware.AgentBurst),
			Unauthenticated: middleware.NewRateLimiter(middleware.UnauthenticatedPerSecond, middleware.UnauthenticatedBurst),
			SessionCookie:   agents.SessionCookie,
		}
		mux.Handle("POST "+ObservationsPath,
			middleware.RequireAgent(guard)(http.HandlerFunc(agents.Handler.Record)))
		csrfExempt = append(csrfExempt, middleware.ExactPath(ObservationsPath))
	}

	// Outermost first. Recovery wraps everything so a panic anywhere still
	// produces a response; the session manager has to wrap CSRF because
	// nosurf's token is stored per-request but the failure handler renders
	// through the session.
	//
	// Log sits *inside* Authenticate on purpose. Authenticate attaches the
	// user to a derived context, so anything wrapping it only ever sees the
	// original -- with Log on the outside every access-log line recorded
	// user=-, including authenticated writes, which is precisely the case the
	// log exists for.
	return middleware.Chain(mux,
		middleware.Recover,
		middleware.SecurityHeaders(app.Config.SecureCookies),
		// Before anything reads a body. CSRF parses the form to find its token,
		// so a limit applied after it would already have lost the argument.
		middleware.LimitBody,
		app.Sessions.LoadAndSave,
		middleware.WithRequestState,
		middleware.CSRF(app.Config.SecureCookies, csrfExempt...),
		middleware.Authenticate(app.Sessions, app.Store),
		middleware.Log,
	)
}

// cacheStatic lets the browser hold on to vendored assets. They only change
// when the binary does.
func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		next.ServeHTTP(w, r)
	})
}
