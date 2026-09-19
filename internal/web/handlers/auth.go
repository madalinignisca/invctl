// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/madalinignisca/invctl/internal/auth"
	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/web/middleware"
	"github.com/madalinignisca/invctl/internal/web/render"
)

// Session keys for one OIDC sign-in attempt. All three are single-use:
// OIDCCallback reads and deletes them together, before any comparison runs,
// so a replayed callback URL finds an empty session and is refused by the
// missing-state check rather than by anything that ran afterwards.
const (
	sessionOIDCStateKey    = "oidc_state"
	sessionOIDCNonceKey    = "oidc_nonce"
	sessionOIDCVerifierKey = "oidc_verifier"
)

type loginPage struct {
	Base
	Error    string
	Username string
	Next     string
	// ShowPassword and ShowOIDC decide what the template renders -- it does
	// not infer this from Base.User or guess from which fields are set, per
	// CLAUDE.md's "Templates ... never guess". Local login can be off with
	// OIDC on (spec D3's default once Keycloak is configured), OIDC can be
	// off with local login on (no Keycloak configured at all), and for a
	// short window during recovery both can be true at once.
	ShowPassword bool
	ShowOIDC     bool
}

// LoginForm renders the sign-in page.
func (a *App) LoginForm(w http.ResponseWriter, r *http.Request) {
	if middleware.UserFrom(r.Context()) != nil {
		render.Redirect(w, r, "/")
		return
	}
	a.Render.Page(w, http.StatusOK, "login", loginPage{
		Base:         a.base(r, "Sign in", ""),
		Next:         safeNext(r.URL.Query().Get("next")),
		ShowPassword: a.Config.AuthLocal,
		ShowOIDC:     a.Config.AuthOIDC,
	})
}

// Login authenticates and starts a session.
func (a *App) Login(w http.ResponseWriter, r *http.Request) {
	username := formValue(r, "username")
	password := r.PostFormValue("password") // never trimmed: spaces are legitimate
	next := safeNext(formValue(r, "next"))

	user, err := a.Auth.Authenticate(r.Context(), username, password)
	if err != nil {
		if !errors.Is(err, auth.ErrInvalidCredentials) {
			// An operational failure (directory unreachable, database down)
			// is logged in full but still shown to the user as a generic
			// failure -- the detail would only help an attacker.
			auth.LogSecurityEvent(r.Context(), slog.LevelError, auth.EventSignInError,
				"username", username, "remote", r.RemoteAddr, "error", err)
		} else {
			auth.LogSecurityEvent(r.Context(), slog.LevelWarn, auth.EventSignInFailed,
				"username", username, "remote", r.RemoteAddr)
		}
		a.Render.Page(w, http.StatusUnauthorized, "login", loginPage{
			Base:         a.base(r, "Sign in", ""),
			Error:        "That username and password combination was not recognised.",
			Username:     username,
			Next:         next,
			ShowPassword: a.Config.AuthLocal,
			ShowOIDC:     a.Config.AuthOIDC,
		})
		return
	}

	// A new session id on privilege change defeats session fixation: an
	// attacker who planted a session id cannot ride it into an authenticated
	// session.
	if err := a.Sessions.RenewToken(r.Context()); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Sessions.Put(r.Context(), middleware.SessionUserIDKey, user.Username)
	auth.LogSecurityEvent(r.Context(), slog.LevelInfo, auth.EventSignInSucceeded,
		"username", user.Username, "source", user.Source,
		"can_write", a.Authz.CanWrite(user), "remote", r.RemoteAddr)

	if next == "" {
		next = "/"
	}
	render.Redirect(w, r, next)
}

// Logout ends the session.
func (a *App) Logout(w http.ResponseWriter, r *http.Request) {
	if err := a.Sessions.Destroy(r.Context()); err != nil {
		a.serverError(w, r, err)
		return
	}
	render.Redirect(w, r, "/login")
}

// OIDCStart begins a Keycloak sign-in: it mints state, a nonce and a PKCE
// verifier, stashes all three in the session, and redirects to the
// provider's authorization endpoint. See spec §3's flow.
//
// state is this flow's CSRF defence -- there is no invctl-issued CSRF token
// to carry through a redirect to an external IdP and back, so state plays
// that role instead (spec §3). nonce defends against ID-token replay, and
// verifier is the PKCE secret that binds the authorization code, once
// Keycloak issues one, to this specific browser having started this
// specific attempt.
func (a *App) OIDCStart(w http.ResponseWriter, r *http.Request) {
	if a.OIDC == nil {
		// Reachable only if this route were mounted with config.AuthOIDC
		// false, which main.go does not do -- see App.OIDC's own comment.
		a.serverError(w, r, errors.New("oidc: /auth/oidc reached with no provider configured"))
		return
	}

	state, err := randomOIDCSecret()
	if err != nil {
		a.serverError(w, r, fmt.Errorf("generating oidc state: %w", err))
		return
	}
	nonce, err := randomOIDCSecret()
	if err != nil {
		a.serverError(w, r, fmt.Errorf("generating oidc nonce: %w", err))
		return
	}
	verifier, err := randomOIDCSecret()
	if err != nil {
		a.serverError(w, r, fmt.Errorf("generating oidc pkce verifier: %w", err))
		return
	}

	a.Sessions.Put(r.Context(), sessionOIDCStateKey, state)
	a.Sessions.Put(r.Context(), sessionOIDCNonceKey, nonce)
	a.Sessions.Put(r.Context(), sessionOIDCVerifierKey, verifier)

	render.Redirect(w, r, a.OIDC.AuthCodeURL(state, nonce, verifier))
}

// OIDCCallback completes a Keycloak sign-in.
//
// Every refusal here shows the browser the same generic message and logs the
// specific reason -- the rule Login already follows for a bad password (spec
// §6): the distinction lives in the log, where it helps the operator, not in
// the response, where it would help an attacker decide what to try next. The
// one exception is a username collision (spec D4), which is shown verbatim
// because it names no secret and it is the operator, not an attacker, who
// has to act on it.
func (a *App) OIDCCallback(w http.ResponseWriter, r *http.Request) {
	if a.OIDC == nil {
		a.serverError(w, r, errors.New("oidc: /auth/oidc/callback reached with no provider configured"))
		return
	}

	wantState := a.Sessions.GetString(r.Context(), sessionOIDCStateKey)
	nonce := a.Sessions.GetString(r.Context(), sessionOIDCNonceKey)
	verifier := a.Sessions.GetString(r.Context(), sessionOIDCVerifierKey)
	// Deleted immediately, before the comparison below or any network call:
	// whatever happens next, a second request replaying this exact callback
	// URL finds an empty session and fails the missing-state check, not a
	// state comparison against a value still sitting there to be reused.
	a.Sessions.Remove(r.Context(), sessionOIDCStateKey)
	a.Sessions.Remove(r.Context(), sessionOIDCNonceKey)
	a.Sessions.Remove(r.Context(), sessionOIDCVerifierKey)

	gotState := r.URL.Query().Get("state")
	// Both sides must be non-empty AND match: without the emptiness check, a
	// request arriving with no ?state= at all (nothing to compare) would
	// compare an empty session value to an empty query value and pass.
	if wantState == "" || gotState == "" ||
		subtle.ConstantTimeCompare([]byte(wantState), []byte(gotState)) != 1 {
		auth.LogSecurityEvent(r.Context(), slog.LevelWarn, auth.EventSignInFailed,
			"authenticator", "oidc", "reason", "state missing or mismatched", "remote", r.RemoteAddr)
		a.oidcRefused(w, r)
		return
	}

	code := r.URL.Query().Get("code")
	claims, err := a.OIDC.Exchange(r.Context(), code, verifier, nonce)
	if err != nil {
		if !errors.Is(err, auth.ErrInvalidCredentials) {
			// An operational failure (Keycloak unreachable) is logged in
			// full but still shown generically -- the detail would only
			// help an attacker, exactly as Login already treats it.
			auth.LogSecurityEvent(r.Context(), slog.LevelError, auth.EventSignInError,
				"authenticator", "oidc", "remote", r.RemoteAddr, "error", err)
		} else {
			// oidc.Exchange has already logged which check failed
			// (signature, audience, issuer, expiry, nonce) at its own call
			// site; nothing more to name here without duplicating it.
			auth.LogSecurityEvent(r.Context(), slog.LevelWarn, auth.EventSignInFailed,
				"authenticator", "oidc", "remote", r.RemoteAddr)
		}
		a.oidcRefused(w, r)
		return
	}

	user, err := a.Store.UpsertOIDCUser(r.Context(), claims.Subject, claims.Username, claims.DisplayName, claims.Email)
	if err != nil {
		if errors.Is(err, domain.ErrConflict) {
			auth.LogSecurityEvent(r.Context(), slog.LevelWarn, auth.EventSignInFailed,
				"authenticator", "oidc", "reason", "username collision",
				"username", claims.Username, "remote", r.RemoteAddr)
			a.Render.Page(w, http.StatusConflict, "login", loginPage{
				Base: a.base(r, "Sign in", ""),
				Error: fmt.Sprintf("A local or LDAP account named %q already exists. "+
					"An Administrator can resolve this on /users.", claims.Username),
				ShowPassword: a.Config.AuthLocal,
				ShowOIDC:     a.Config.AuthOIDC,
			})
			return
		}
		a.serverError(w, r, fmt.Errorf("resolving oidc account: %w", err))
		return
	}

	// A new session id on privilege change defeats session fixation, exactly
	// as Login's own comment explains -- the same call, the same reason,
	// reused unchanged for every authenticator (spec §3 step 6).
	if err := a.Sessions.RenewToken(r.Context()); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Sessions.Put(r.Context(), middleware.SessionUserIDKey, user.Username)
	auth.LogSecurityEvent(r.Context(), slog.LevelInfo, auth.EventSignInSucceeded,
		"username", user.Username, "source", user.Source,
		"can_write", a.Authz.CanWrite(user), "remote", r.RemoteAddr)

	render.Redirect(w, r, "/")
}

// oidcRefused renders the login page with the one message every OIDC
// refusal shows a browser. See OIDCCallback's own comment for why the
// specific reason never reaches here.
func (a *App) oidcRefused(w http.ResponseWriter, r *http.Request) {
	a.Render.Page(w, http.StatusUnauthorized, "login", loginPage{
		Base:         a.base(r, "Sign in", ""),
		Error:        "Sign-in with Keycloak did not succeed. Please try again.",
		ShowPassword: a.Config.AuthLocal,
		ShowOIDC:     a.Config.AuthOIDC,
	})
}

// randomOIDCSecret returns a URL-safe random string suitable as OIDC state,
// a nonce, or a PKCE verifier. 32 random bytes base64url-encode to 43
// characters using only unreserved characters (RFC 7636 §4.1's "code-verifier"
// alphabet minus '.', '~' and '-' is a superset of base64url's), which sits
// at the low end of PKCE's 43-128 character range -- the same value serves
// all three purposes because none of them has a narrower requirement than
// this one.
func randomOIDCSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// safeNext restricts post-login redirects to paths inside this application.
//
// Without this, /login?next=https://evil.example becomes an open redirect that
// lends this site's credibility to a phishing page. A protocol-relative URL
// (//evil.example) is the case people forget.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return ""
	}
	if strings.Contains(next, "\\") || strings.Contains(next, "\n") || strings.Contains(next, "\r") {
		return ""
	}
	return next
}
