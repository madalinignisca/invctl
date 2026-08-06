// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Package middleware holds the cross-cutting HTTP concerns: recovery,
// logging, sessions, authentication and CSRF.
package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/justinas/nosurf"

	"github.com/madalinignisca/invctl/internal/auth"
	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/web/render"
)

type contextKey string

const (
	userContextKey  contextKey = "user"
	stateContextKey contextKey = "request_state"
)

// RequestState is scratch space for the lifetime of one request.
//
// It exists because a handler legitimately builds its page data more than once
// -- a list page constructs both the page and the form context, and each wants
// the common fields -- while a flash message must be consumed exactly once. A
// pointer in the context lets those calls agree on what has already been read.
type RequestState struct {
	FlashLoaded bool
	FlashKind   string
	FlashText   string
}

// StateFrom returns the per-request scratch space, or nil when the middleware
// is not installed.
func StateFrom(ctx context.Context) *RequestState {
	state, _ := ctx.Value(stateContextKey).(*RequestState)
	return state
}

// WithRequestState installs the per-request scratch space.
func WithRequestState(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), stateContextKey, &RequestState{})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// SessionUserIDKey is the session field holding the signed-in user's id.
const SessionUserIDKey = "user_id"

// UserFrom returns the signed-in user, or nil.
func UserFrom(ctx context.Context) *domain.AppUser {
	user, _ := ctx.Value(userContextKey).(*domain.AppUser)
	return user
}

// WithUser attaches a user to a context. Exported for tests.
func WithUser(ctx context.Context, user *domain.AppUser) context.Context {
	return context.WithValue(ctx, userContextKey, user)
}

// Recover turns a panic into a 500 instead of a dropped connection.
//
// A panic in one handler must not take the process down: this is an internal
// tool that people leave open in a tab, and a crash loop is far more
// disruptive than one broken page.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				slog.Error("panic in handler",
					"error", err,
					"path", r.URL.Path,
					"stack", string(debug.Stack()))
				w.Header().Set("Connection", "close")
				http.Error(w, "Something went wrong.", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the status code for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Log writes one structured line per request.
func Log(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		level := slog.LevelInfo
		if rec.status >= 500 {
			level = slog.LevelError
		}
		slog.Log(r.Context(), level, "request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start),
			"htmx", render.IsHTMX(r),
			"user", usernameOf(r.Context()))
	})
}

func usernameOf(ctx context.Context) string {
	if user := UserFrom(ctx); user != nil {
		return user.Username
	}
	return "-"
}

// UserStore is the slice of the store this package needs.
type UserStore interface {
	GetUserByUsername(ctx context.Context, username string) (*domain.AppUser, error)
}

// Authenticate loads the signed-in user into the request context.
//
// It does not reject anyone: that is RequireAuth's job. Splitting them means
// a page can render differently for a signed-in user without being closed to
// anonymous ones.
func Authenticate(sessions *scs.SessionManager, users UserStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			username := sessions.GetString(r.Context(), SessionUserIDKey)
			if username == "" {
				next.ServeHTTP(w, r)
				return
			}
			user, err := users.GetUserByUsername(r.Context(), username)
			if err != nil {
				// The account was removed or disabled since the session was
				// issued. Drop the stale session rather than 500.
				sessions.Remove(r.Context(), SessionUserIDKey)
				next.ServeHTTP(w, r)
				return
			}
			if !user.IsActive {
				sessions.Remove(r.Context(), SessionUserIDKey)
				next.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), user)))
		})
	}
}

// RequireAuth rejects anonymous requests.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if UserFrom(r.Context()) == nil {
			// HX-Redirect rather than 302: HTMX would otherwise swap the
			// login page into whatever element made the request.
			render.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.Path))
			return
		}
		// A page behind authentication must never be cached by a shared proxy
		// or restored from the back/forward cache after sign-out.
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// RequireAdmin rejects users without write access.
//
// Every non-GET route sits behind this. The check itself is one line in
// authz.CanWrite, which is where LDAP group roles will land later.
func RequireAdmin(authz *auth.Authorizer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := UserFrom(r.Context())
			if user == nil {
				render.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.Path))
				return
			}
			if !authz.CanWrite(user) {
				auth.LogSecurityEvent(r.Context(), slog.LevelWarn, auth.EventWriteDenied,
					"user", user.Username, "path", r.URL.Path, "method", r.Method)
				http.Error(w, "You have read-only access.", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ExactPath is a single URL path exempted from CSRF, and the type is the point.
//
// docs/AUDIT.md rule 6 requires the observed-state webhook's exemption to be
// registered "for that exact path -- never a prefix or glob, or the planned
// /api/inventory inherits it for free". nosurf offers ExemptPath, ExemptPaths,
// ExemptGlob, ExemptRegexp and ExemptFunc; four of those five would grant that
// inheritance, and the one that would not is the one CSRF calls. Requiring this
// named type at the call site means widening the exemption is not a matter of
// passing a different string -- it takes editing this file, where the rule is
// written down.
//
// A value carrying a glob metacharacter is refused rather than exempted, so the
// failure direction is "CSRF stayed on" rather than "CSRF came off more paths
// than intended".
type ExactPath string

// valid reports whether p is a single literal path.
func (p ExactPath) valid() bool {
	s := string(p)
	if s == "" || !strings.HasPrefix(s, "/") {
		return false
	}
	// path.Match's metacharacters, plus the regexp ones that would matter if
	// somebody swapped the call below.
	return !strings.ContainsAny(s, "*?[]\\^$()|+{}")
}

// CSRF wraps the mux with nosurf.
//
// The token reaches HTMX through an hx-headers attribute on <body>, so every
// non-GET request carries it without each form having to remember.
//
// nosurf also enforces a same-origin check, comparing the request's Origin or
// Referer against an origin it derives from r.Host plus a scheme. It assumes
// HTTPS by default, which is the safe assumption but the wrong one when the
// demo is served over plain HTTP: it would compute https://host, compare it
// against the browser's http://host Origin, and reject every form submission.
// So the scheme is told the truth instead.
//
// exempt lists the paths that carry their own authentication and cannot carry a
// token -- in practice exactly one, the observed-state webhook, which a
// monitoring system reaches with a bearer token and no browser. Exempting it is
// safe only because RequireAgent refuses any request on it that carries a
// session; a CSRF-exempt route that accepted a session would be a hole.
func CSRF(secure bool, exempt ...ExactPath) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		handler := nosurf.New(next)
		for _, p := range exempt {
			if !p.valid() {
				// Fail closed and say so. Silently exempting a pattern that
				// nosurf would match loosely is the failure this guards.
				slog.Error("refusing to exempt a path from CSRF: not a single literal path", "path", string(p))
				continue
			}
			// ExemptPath and nothing else. Never ExemptGlob, ExemptRegexp,
			// ExemptFunc or the variadic ExemptPaths -- see ExactPath above.
			handler.ExemptPath(string(p))
		}
		handler.SetIsTLSFunc(func(r *http.Request) bool {
			if r.TLS != nil {
				return true
			}
			// Behind a terminating proxy the connection here is plaintext but
			// the browser's origin is https. Only trust the header when the
			// deployment says it is behind one, which is what secure cookies
			// already imply.
			return secure && r.Header.Get("X-Forwarded-Proto") == "https"
		})
		handler.SetBaseCookie(http.Cookie{
			HttpOnly: true,
			Path:     "/",
			MaxAge:   12 * 60 * 60,
			SameSite: http.SameSiteLaxMode,
			Secure:   secure,
		})
		handler.SetFailureHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth.LogSecurityEvent(r.Context(), slog.LevelWarn, auth.EventCSRFRejected,
				"path", r.URL.Path, "method", r.Method, "reason", nosurf.Reason(r))
			http.Error(w, "Your session expired. Reload the page and try again.", http.StatusBadRequest)
		}))
		return handler
	}
}

// SecurityHeaders sets the defensive headers that cost nothing.
//
// The CSP is strict on script-src because everything is server-rendered:
// htmx and alpine are vendored as files, and the only inline JavaScript is
// Alpine's x-data attributes, which are attribute values rather than inline
// script tags and so are not covered by script-src.
// HSTS IS CONDITIONAL, on the signal the deployment already gives.
//
// Strict-Transport-Security tells a browser never to use plain HTTP for this
// host again, for a year. Sent from a development box on http://localhost it
// is ignored by the browser but it is still a lie, and sent from a host that
// later has to serve HTTP for any reason it is a self-inflicted outage with no
// quick undo. INV_SECURE_COOKIES already means "this deployment is behind
// TLS" — the same fact, so the same switch, rather than a second knob that can
// disagree with the first.
//
// A reverse proxy may well set this too. Both is fine: the header is
// idempotent, and the application asserting its own transport requirement does
// not depend on remembering to configure the edge.
func SecurityHeaders(overHTTPS bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			if overHTTPS {
				h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			// Deny-all: this application uses none of these, and a feature it
			// does not use is a feature an injected script cannot reach for.
			h.Set("Permissions-Policy",
				"accelerometer=(), camera=(), geolocation=(), gyroscope=(), "+
					"magnetometer=(), microphone=(), payment=(), usb=()")
			securityHeaders(h)
			next.ServeHTTP(w, r)
		})
	}
}

func securityHeaders(h http.Header) {
	{
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; "+
				"connect-src 'self'; "+
				"frame-ancestors 'none'; "+
				"base-uri 'none'; "+
				"form-action 'self'")
	}
}

// Chain applies middleware in the order given, so the first entry is the
// outermost wrapper.
func Chain(h http.Handler, middlewares ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}

// MaxRequestBody is the ceiling on a browser-facing request body.
//
// Generous for the forms this application actually has -- the largest is an
// asset with a free-text description -- and far below anything that threatens
// the process. There are no file uploads.
const MaxRequestBody = 1 << 20 // 1 MiB

// LimitBody caps every request body before a handler can read it.
//
// r.ParseForm reads the body to completion into memory, so a handler that calls
// it without a limit will hold whatever was sent. Twelve handlers did, and one
// of them is Login -- reachable with no session, no CSRF token that matters to
// an attacker who is not trying to forge one, and no rate limit on the browser
// surface. A single unauthenticated request could ask the process to buffer as
// much as it liked.
//
// Applied once in the chain rather than in each handler, because the failure
// mode of the per-handler version is a handler added later that forgets. The
// observation route wraps its own tighter 64 KiB limit inside this one; nesting
// MaxBytesReader is well-defined and the inner limit wins.
//
// GET and HEAD are left alone: they have no body worth reading, and wrapping
// them would only add allocation to every static asset request.
func LimitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBody)
		}
		next.ServeHTTP(w, r)
	})
}
