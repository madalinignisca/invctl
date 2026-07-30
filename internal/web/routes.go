// Package web wires the router.
package web

import (
	"io/fs"
	"net/http"

	"github.com/gabriel/invctl/internal/auth"
	"github.com/gabriel/invctl/internal/web/handlers"
	"github.com/gabriel/invctl/internal/web/middleware"
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
	read("GET /reports/spanning", app.SpanningReport)

	read("GET /environments", app.EnvironmentList)
	read("GET /assets", app.AssetList)
	read("GET /assets/{id}", app.AssetDetail)
	read("GET /assets/{id}/impact", app.AssetImpact)
	// A view, and only a view: GET, no CSRF, no RequireAdmin, and nothing on
	// the page it renders can change the estate or the model of it.
	read("GET /assets/{id}/neighbourhood", app.AssetNeighbourhood)
	read("GET /services", app.ServiceList)
	read("GET /services/{id}", app.ServiceDetail)
	read("GET /prefixes", app.PrefixList)
	read("GET /network", app.NetworkList)

	// Authenticated writes. Every non-GET route goes through RequireAdmin,
	// and the whole mux is wrapped in CSRF below.
	requireAdmin := middleware.RequireAdmin(authz)
	write := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, middleware.RequireAuth(requireAdmin(h)))
	}
	write("POST /environments", app.EnvironmentCreate)

	write("POST /assets", app.AssetCreate)
	write("POST /assets/{id}", app.AssetUpdate)
	write("POST /assets/{id}/retire", app.AssetRetire)
	write("POST /assets/{id}/parent", app.AssetReparent)

	write("POST /services", app.ServiceCreate)
	write("POST /services/{id}", app.ServiceUpdate)
	write("POST /services/{id}/retire", app.ServiceRetire)
	write("POST /services/{id}/instances", app.InstanceCreate)
	write("POST /services/{id}/endpoints", app.EndpointCreate)
	write("POST /services/{id}/dependencies", app.DependencyCreate)

	write("POST /instances/{id}/retire", app.InstanceRetire)

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
		middleware.SecurityHeaders,
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
