// Package middleware holds the cross-cutting HTTP concerns: recovery,
// logging, sessions, authentication and CSRF.
package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"runtime/debug"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/justinas/nosurf"

	"github.com/gabriel/invctl/internal/auth"
	"github.com/gabriel/invctl/internal/domain"
	"github.com/gabriel/invctl/internal/web/render"
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
				slog.Warn("write refused", "user", user.Username, "path", r.URL.Path)
				http.Error(w, "You have read-only access.", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
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
func CSRF(secure bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		handler := nosurf.New(next)
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
			slog.Warn("csrf rejected", "path", r.URL.Path, "reason", nosurf.Reason(r))
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
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
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
		next.ServeHTTP(w, r)
	})
}

// Chain applies middleware in the order given, so the first entry is the
// outermost wrapper.
func Chain(h http.Handler, middlewares ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}
