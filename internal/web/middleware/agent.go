// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// The guard on the one route a machine credential can reach.
//
// docs/AUDIT.md rule 6: the observed-state webhook sits "on one route mounted
// under middleware.RequireAgent only: no session, no RequireWrite". Everything
// here exists to keep those two principal types apart -- an operator's session
// and a collector's bearer token are different things, and a request that looks
// like both is the confusion the rule is written against.
//
// Nothing in this file logs a token. Not on success, not on failure, not
// truncated, not hashed-and-logged-for-correlation. A rejected token is
// reported by what it is not.

package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/madalinignisca/invctl/internal/auth"
	"github.com/madalinignisca/invctl/internal/web/render"
)

const agentContextKey contextKey = "agent"

// AgentFrom returns the authenticated monitoring credential, or nil.
//
// Deliberately a different key and a different type from UserFrom. An agent
// stored under the user key would reach authz.CanWrite, every actor(r) call and
// every template that renders a signed-in user -- which is the single mistake
// rule 6 is written to make impossible.
func AgentFrom(ctx context.Context) *auth.Agent {
	agent, _ := ctx.Value(agentContextKey).(*auth.Agent)
	return agent
}

// WithAgent attaches a credential to a context. Exported for tests.
func WithAgent(ctx context.Context, agent *auth.Agent) context.Context {
	return context.WithValue(ctx, agentContextKey, agent)
}

// Rate limits for the machine-facing route (rule 6: "rate-limits per
// credential").
//
// Constants rather than configuration, for the same reason rule 9 keeps the
// flap thresholds out of the environment: a writer that can raise its own
// ceiling has no ceiling. The numbers are generous for a collector -- a
// 30-second poll cycle batching a whole estate is a request every 30 seconds --
// and tight enough that a stolen token cannot fill the transition ledger faster
// than someone can revoke it.
const (
	// AgentRequestsPerSecond is the sustained per-credential rate.
	AgentRequestsPerSecond = 5.0
	// AgentBurst is how many requests may arrive at once, for a collector that
	// wakes up and flushes several batches.
	AgentBurst = 30
	// UnauthenticatedBurst is the shared allowance for requests that failed to
	// authenticate. Shared rather than per-caller because there is no
	// trustworthy caller identity to key on yet -- and because it deliberately
	// cannot lock out a valid credential, which has its own bucket.
	UnauthenticatedBurst = 20
	// UnauthenticatedPerSecond is the refill for that shared allowance.
	UnauthenticatedPerSecond = 1.0
)

// unauthenticatedKey is the bucket for requests with no established identity.
const unauthenticatedKey = "\x00unauthenticated"

// RateLimiter is a per-key token bucket.
//
// Keys are authenticated credential ids plus one fixed key for failed
// authentication, so the map is bounded by the configured credentials and needs
// no eviction. Keying on something a caller controls -- a claimed id, a header
// -- would make the map itself the denial of service.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64
	burst   float64
	now     func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewRateLimiter builds a limiter refilling at perSecond up to burst.
func NewRateLimiter(perSecond float64, burst int) *RateLimiter {
	return &RateLimiter{
		buckets: make(map[string]*bucket),
		rate:    perSecond,
		burst:   float64(burst),
		now:     time.Now,
	}
}

// Allow consumes one token for key, reporting whether there was one.
//
// A nil limiter allows everything, so a caller that has not configured one is
// unlimited rather than silently blocked -- failing open here is right because
// the limiter is a volume control, not the authorization check.
func (l *RateLimiter) Allow(key string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// AgentGuard is everything RequireAgent needs.
type AgentGuard struct {
	// Registry holds the configured credentials. A nil or empty registry
	// authenticates nobody.
	Registry *auth.AgentRegistry
	// Credentials limits each authenticated credential.
	Credentials *RateLimiter
	// Unauthenticated limits requests that failed to authenticate.
	Unauthenticated *RateLimiter
	// SessionCookie is the name of the browser session cookie, so a request
	// carrying one can be refused by name.
	SessionCookie string
}

// RequireAgent authenticates a monitoring credential and refuses everything
// else.
//
// The order of the checks is the order of the risks:
//
//  1. A session. A request carrying a browser session on this route is either a
//     confused client or an attempt to have one principal type ride the other's
//     credentials, and rule 6 says to refuse it outright rather than to pick a
//     winner. Note this route is CSRF-exempt by design, so if it also accepted
//     a session it would be a CSRF-exempt authenticated write -- exactly the
//     hole the exemption is otherwise safe because of.
//  2. A bearer token, or the absence of one.
//  3. The rate limit, which is applied after identity so a flood by one
//     credential cannot deny the others.
//
// Every refusal is a security event (rule 11), never a silent drop.
func RequireAgent(g AgentGuard) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			w.Header().Set("Cache-Control", "no-store")

			if reason, confused := sessionPresent(r, g.SessionCookie); confused {
				auth.LogSecurityEvent(ctx, slog.LevelWarn, auth.EventAgentSessionConfusion,
					"path", r.URL.Path, "remote", r.RemoteAddr, "reason", reason)
				render.JSONError(w, http.StatusUnauthorized,
					"this route authenticates a monitoring credential only; it does not accept a browser session")
				return
			}

			token, ok := auth.BearerToken(r.Header.Get("Authorization"))
			if !ok {
				auth.LogSecurityEvent(ctx, slog.LevelWarn, auth.EventAgentRejected,
					"path", r.URL.Path, "remote", r.RemoteAddr, "reason", "no bearer token")
				unauthorised(w)
				return
			}

			agent, ok := g.Registry.Authenticate(token)
			if !ok {
				// The bucket is consumed on failure only, so a working collector
				// never touches it and a brute-force attempt cannot lock one out.
				if !g.Unauthenticated.Allow(unauthenticatedKey) {
					auth.LogSecurityEvent(ctx, slog.LevelWarn, auth.EventAgentThrottled,
						"path", r.URL.Path, "remote", r.RemoteAddr, "reason", "repeated authentication failure")
					tooManyRequests(w)
					return
				}
				auth.LogSecurityEvent(ctx, slog.LevelWarn, auth.EventAgentRejected,
					"path", r.URL.Path, "remote", r.RemoteAddr, "reason", "unrecognised token")
				unauthorised(w)
				return
			}

			if !g.Credentials.Allow(agent.ID) {
				auth.LogSecurityEvent(ctx, slog.LevelWarn, auth.EventAgentThrottled,
					"credential", agent.ID, "path", r.URL.Path)
				tooManyRequests(w)
				return
			}

			next.ServeHTTP(w, r.WithContext(WithAgent(ctx, agent)))
		})
	}
}

// sessionPresent reports whether this request looks like it came from a signed
// in browser.
//
// Both halves matter. An established user in the context is the obvious case.
// The cookie check catches the one the context misses: a session that has
// expired or whose account was disabled populates no user, but a request still
// carrying the cookie is still a browser, and a browser has no business here.
//
// Only the session cookie is refused, not every cookie: nosurf regenerates its
// own CSRF cookie on every request that passes through the chain, including
// this one, so a client with a cookie jar echoes that back through no fault of
// its own.
func sessionPresent(r *http.Request, sessionCookie string) (string, bool) {
	if UserFrom(r.Context()) != nil {
		return "signed-in session", true
	}
	if sessionCookie == "" {
		return "", false
	}
	if _, err := r.Cookie(sessionCookie); err == nil {
		return "session cookie", true
	}
	return "", false
}

func unauthorised(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="invctl"`)
	// One message for "no token", "malformed token" and "wrong token", for the
	// same reason sign-in has one message for "no such user" and "wrong
	// password": the difference is only useful to somebody guessing.
	render.JSONError(w, http.StatusUnauthorized, "a valid monitoring credential is required")
}

func tooManyRequests(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	render.JSONError(w, http.StatusTooManyRequests, "too many requests")
}
