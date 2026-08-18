// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// The guard on the read-only, token-scoped inventory API (WP-A2).
//
// RequireReader is RequireAgent's sibling: a different principal type, a
// different registry, but the same order of risks, because the reasoning in
// agent.go's long comment applies unchanged -- a browser session and a bearer
// token are different things, and a request that looks like both is refused
// rather than arbitrated.
//
// Nothing here logs a token. A rejected token is reported by what it is not.

package middleware

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/madalinignisca/invctl/internal/auth"
	"github.com/madalinignisca/invctl/internal/web/render"
)

// Rate limits for the read-only API route.
//
// Generous relative to the monitoring surface's: a reader is a person's
// tooling (Ansible, a script, a dashboard) polling on demand rather than a
// collector on a fixed cycle, and the read-only API cannot mutate anything --
// so the ceiling exists to bound abuse, not to protect declared state.
const (
	// ReaderRequestsPerSecond is the sustained per-credential rate.
	ReaderRequestsPerSecond = 10.0
	// ReaderBurst is how many requests may arrive at once.
	ReaderBurst = 60
)

// readerContextKey is the context key for the authenticated reader.
//
// An unexported struct type, not the string-based contextKey agent.go uses,
// per this task's brief -- a distinct type so a reader can never collide with
// or be mistaken for an agent or a user in the same context.
type readerContextKey struct{}

// ReaderFrom returns the authenticated read credential, or nil.
func ReaderFrom(ctx context.Context) (*auth.Reader, bool) {
	reader, ok := ctx.Value(readerContextKey{}).(*auth.Reader)
	return reader, ok
}

// withReader attaches a credential to a context.
func withReader(ctx context.Context, reader *auth.Reader) context.Context {
	return context.WithValue(ctx, readerContextKey{}, reader)
}

// ReaderGuard is everything RequireReader needs.
type ReaderGuard struct {
	// Registry holds the configured read credentials. A nil or empty registry
	// authenticates nobody.
	Registry *auth.ReaderRegistry
	// Credentials limits each authenticated credential.
	Credentials *RateLimiter
	// Unauthenticated limits requests that failed to authenticate.
	Unauthenticated *RateLimiter
	// SessionCookie is the name of the browser session cookie, so a request
	// carrying one can be refused by name.
	SessionCookie string
}

// RequireReader authenticates a read-only credential and refuses everything
// else.
//
// The order of the checks mirrors RequireAgent exactly, because it is the
// order of the risks and not a detail specific to the write-side guard:
//
//  1. A session. A request carrying a browser session on this route is either
//     a confused client or an attempt to have one principal type ride the
//     other's credentials, and the rule is to refuse it outright rather than
//     to pick a winner.
//  2. A bearer token, or the absence of one.
//  3. The rate limit, applied after identity so a flood by one credential
//     cannot deny the others. The unauthenticated bucket is consumed on
//     failure only, so a working client never touches it and a brute-force
//     attempt cannot lock one out.
//
// Every refusal is a security event, never a silent drop. A scope miss --
// this credential is valid but scoped away from the entity it asked for -- is
// deliberately not checked here: it is discovered deep in the store when a
// scoped query finds nothing, and is the API handler's job in a later task.
func RequireReader(g ReaderGuard) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			w.Header().Set("Cache-Control", "no-store")

			if reason, confused := sessionPresent(r, g.SessionCookie); confused {
				auth.LogSecurityEvent(ctx, slog.LevelWarn, auth.EventReaderSessionConfusion,
					"path", r.URL.Path, "remote", r.RemoteAddr, "reason", reason)
				render.JSONError(w, http.StatusUnauthorized,
					"this route authenticates a read-only credential only; it does not accept a browser session")
				return
			}

			token, ok := auth.BearerToken(r.Header.Get("Authorization"))
			if !ok {
				auth.LogSecurityEvent(ctx, slog.LevelWarn, auth.EventReaderRejected,
					"path", r.URL.Path, "remote", r.RemoteAddr, "reason", "no bearer token")
				readerUnauthorised(w)
				return
			}

			reader, ok := g.Registry.Authenticate(token)
			if !ok {
				// The bucket is consumed on failure only, so a working client
				// never touches it and a brute-force attempt cannot lock one out.
				if !g.Unauthenticated.Allow(unauthenticatedKey) {
					auth.LogSecurityEvent(ctx, slog.LevelWarn, auth.EventReaderThrottled,
						"path", r.URL.Path, "remote", r.RemoteAddr, "reason", "repeated authentication failure")
					tooManyRequests(w)
					return
				}
				auth.LogSecurityEvent(ctx, slog.LevelWarn, auth.EventReaderRejected,
					"path", r.URL.Path, "remote", r.RemoteAddr, "reason", "unrecognised token")
				readerUnauthorised(w)
				return
			}

			if !g.Credentials.Allow(reader.ID) {
				auth.LogSecurityEvent(ctx, slog.LevelWarn, auth.EventReaderThrottled,
					"credential", reader.ID, "path", r.URL.Path)
				tooManyRequests(w)
				return
			}

			next.ServeHTTP(w, r.WithContext(withReader(ctx, reader)))
		})
	}
}

// readerUnauthorised is agent.go's unauthorised with this route's own noun.
//
// It exists because the shared helper leaked across the seam: /api/v1
// answered "a valid monitoring credential is required", which sends an
// Ansible integrator debugging a 401 to INV_AGENT_TOKENS -- the wrong
// variable, on the wrong surface, for a credential that would not have worked
// here anyway. Same status, same header, same single message for "no token",
// "malformed token" and "wrong token", because that difference is only useful
// to somebody guessing.
func readerUnauthorised(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="invctl"`)
	render.JSONError(w, http.StatusUnauthorized,
		"a valid read credential is required; see INV_API_TOKENS")
}
