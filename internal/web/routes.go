// Package web wires the router.
package web

import (
	"io/fs"
	"net/http"

	"github.com/gabriel/invctl/internal/auth"
	"github.com/gabriel/invctl/internal/web/handlers"
	"github.com/gabriel/invctl/internal/web/middleware"
)

// Routes builds the HTTP handler.
//
// Routing uses net/http.ServeMux with Go 1.22 method and wildcard patterns --
// no third-party router. The three groups below differ only in the middleware
// they carry: public, authenticated read, and authenticated write.
func Routes(app *handlers.App, static fs.FS, authz *auth.Authorizer) http.Handler {
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
	read("GET /services", app.ServiceList)
	read("GET /services/{id}", app.ServiceDetail)
	read("GET /prefixes", app.PrefixList)

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

	write("POST /instances/{id}/disable", app.InstanceDisable)
	write("POST /dependencies/{id}/retire", app.DependencyRetire)
	write("POST /dependencies/{id}/verify", app.DependencyVerify)

	write("POST /assets/{id}/interfaces", app.InterfaceCreate)
	write("POST /addresses", app.IPAddressCreate)
	write("POST /links", app.LinkCreate)
	write("POST /links/{id}/retire", app.LinkRetire)
	write("POST /prefixes", app.PrefixCreate)

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
		app.Sessions.LoadAndSave,
		middleware.WithRequestState,
		middleware.CSRF(app.Config.SecureCookies),
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
