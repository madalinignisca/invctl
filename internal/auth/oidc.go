// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// discoveryTimeout bounds every HTTP call this file makes: the discovery
// document fetch at startup, the JWKS fetch during verification, and the
// authorization-code exchange. Without a deadline, a Keycloak instance that
// accepts the TCP connection but never answers would hang invctl's startup
// (discovery) or a sign-in request (exchange) indefinitely -- an operational
// outage indistinguishable from a hang in this process's own code.
const discoveryTimeout = 10 * time.Second

// OIDCConfig is the four settings needed to talk to one OIDC provider.
// ClientSecret may be empty for a public client -- PKCE is used regardless
// (see AuthCodeURL), so a confidential client gains defence in depth rather
// than relying on the secret alone.
type OIDCConfig struct {
	Issuer, ClientID, ClientSecret, RedirectURL string
}

// OIDCClaims is the slice of an ID token this codebase ever looks at.
// Subject is the only field a security decision is ever made on; the rest
// are display data a person can change at the provider at will.
type OIDCClaims struct {
	Subject, Username, DisplayName, Email string
}

// OIDCProvider owns discovery, the authorization URL and token verification
// for exactly one issuer. It is the only type in this codebase that talks to
// Keycloak -- everything downstream (the handler, the store) sees only
// OIDCClaims or a refusal.
type OIDCProvider struct {
	oauth2Config oauth2.Config
	verifier     *oidc.IDTokenVerifier
	// httpClient is reused for both the JWKS fetch during Verify and the
	// authorization-code exchange, so both calls carry the same timeout.
	// It is never given the code, verifier or a client secret in a header a
	// log line could pick up -- oauth2 sends those in the request body.
	httpClient *http.Client
}

// NewOIDCProvider performs discovery against cfg.Issuer and builds a
// provider ready to start and complete sign-ins.
//
// This is the ONE network call this codebase makes at startup, and it is a
// deliberate exception to "invctl never acts on the estate": the issuer is
// not an inventoried host, it is the identity provider invctl authenticates
// its own users against -- the same argument LDAP's dial already carries.
func NewOIDCProvider(ctx context.Context, cfg OIDCConfig) (*OIDCProvider, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" || cfg.RedirectURL == "" {
		return nil, errors.New("oidc: issuer, client id and redirect url are all required")
	}

	client := &http.Client{Timeout: discoveryTimeout}
	// oidc.ClientContext sets the same context key golang.org/x/oauth2 reads,
	// so this one client is also what Exchange uses later -- see Exchange.
	dctx := oidc.ClientContext(ctx, client)

	provider, err := oidc.NewProvider(dctx, cfg.Issuer)
	if err != nil {
		// Discovery failure is an operational problem (issuer unreachable,
		// misconfigured URL, TLS failure) -- never a credential failure, so
		// it is wrapped and surfaced to the operator rather than mapped to
		// ErrInvalidCredentials.
		return nil, fmt.Errorf("discovering oidc issuer %s: %w", cfg.Issuer, err)
	}

	verifier := provider.Verifier(&oidc.Config{ClientID: cfg.ClientID})

	return &OIDCProvider{
		oauth2Config: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint:     provider.Endpoint(),
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
		verifier:   verifier,
		httpClient: client,
	}, nil
}

// AuthCodeURL returns the URL to redirect the browser to. state, nonce and
// verifier are per-attempt secrets the caller generates and stores in the
// session (internal/web/handlers/auth.go's OIDCStart) -- this function only
// carries them into the request, it does not generate or remember them.
//
// PKCE (the S256 challenge) is sent EVEN THOUGH a confidential client also
// has a client secret. The secret authenticates the client at the token
// endpoint; PKCE binds the authorization code to the party that started this
// specific flow. Without it, a code intercepted in transit (a misconfigured
// redirect, a logging proxy, a malicious app on the same device for a public
// client) could be exchanged by anyone who captured it. It costs one query
// parameter, so there is no case where skipping it is the better trade.
func (p *OIDCProvider) AuthCodeURL(state, nonce, verifier string) string {
	return p.oauth2Config.AuthCodeURL(state,
		// Sends the SHA-256 hash of the verifier, not the verifier. The
		// verifier itself is held back and travels once, later, straight to
		// the token endpoint in Exchange -- which is the whole mechanism:
		// whoever intercepts this URL learns the hash and still cannot redeem
		// the code.
		oauth2.S256ChallengeOption(verifier),
		// SENDS the nonce; it does not check one. Keycloak echoes it into the
		// `nonce` claim of the ID token it mints, and Exchange compares that
		// claim against this same value. Both halves are needed and they are
		// in different functions, so: written here, verified there.
		oidc.Nonce(nonce))
}

// Exchange redeems an authorization code for an ID token, verifies it against
// the provider's current key set, and checks nonce, audience, issuer and
// expiry. It is the entire trust boundary between "an HTTP request arrived at
// /auth/oidc/callback" and "a person, identified by an immutable subject,
// signed in".
//
// Every verification failure returns ErrInvalidCredentials so the handler
// cannot accidentally leak WHICH check failed to the browser -- the reason is
// only ever in the log (see the LogSecurityEvent calls below), same rule the
// password path already follows. A failure reaching Keycloak at all (network,
// TLS, Keycloak down) is a different kind of failure and is wrapped instead:
// showing "invalid credentials" for an outage would send an operator looking
// for a compromised account that doesn't exist.
func (p *OIDCProvider) Exchange(ctx context.Context, code, verifier, nonce string) (*OIDCClaims, error) {
	ctx = oidc.ClientContext(ctx, p.httpClient)

	// VerifierOption sends the PKCE verifier to the token endpoint. Keycloak
	// hashes it and compares against the challenge it was given in
	// AuthCodeURL; a mismatch means whoever is redeeming this code is not who
	// started the flow, and the code is refused there rather than here.
	token, err := p.oauth2Config.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		// This is the token endpoint call. A rejected code (wrong PKCE
		// verifier, expired code, replay) surfaces here as a transport-level
		// error from the oauth2 package, not as a parsed ID token -- so it is
		// wrapped, not mapped to ErrInvalidCredentials. It still refuses the
		// sign-in either way; the distinction only changes what the operator
		// sees in the log.
		return nil, fmt.Errorf("exchanging authorization code: %w", err)
	}

	// A provider that omits id_token from the token response is not
	// implementing OIDC for this grant -- a config or connectivity problem
	// on Keycloak's side, but still refused as a sign-in rather than crashed,
	// so it is a security event like every other refusal in this function.
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		LogSecurityEvent(ctx, slog.LevelWarn, EventSignInFailed,
			"authenticator", "oidc", "reason", "no id_token in token response")
		return nil, ErrInvalidCredentials
	}

	// Verify checks signature (against the provider's JWKS, fetched over
	// httpClient), issuer, audience and expiry. It does NOT check nonce --
	// that is this function's job, immediately below.
	idToken, err := p.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		LogSecurityEvent(ctx, slog.LevelWarn, EventSignInFailed,
			"authenticator", "oidc", "reason", classifyVerificationError(err))
		return nil, ErrInvalidCredentials
	}

	// Constant-time comparison: nonce is not a secret an attacker needs to
	// guess character-by-character (it is chosen by us and never reused), but
	// every other comparison against attacker-influenced data in this
	// codebase uses subtle.ConstantTimeCompare on principle, and a token
	// claim is exactly that -- attacker-influenced, in the sense that
	// whoever controls the identity provider's response controls this value.
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(nonce)) != 1 {
		// This is replay defence: a stolen or resent authorization code
		// carries an ID token minted for a DIFFERENT sign-in attempt, and its
		// nonce will not match the one this session generated. Refusing it
		// stops that code from completing this session's sign-in even if
		// signature, issuer, audience and expiry all check out.
		LogSecurityEvent(ctx, slog.LevelWarn, EventSignInFailed,
			"authenticator", "oidc", "reason", "nonce mismatch")
		return nil, ErrInvalidCredentials
	}

	// The whole of the account-matching rule (spec D2): Subject is the only
	// claim UpsertOIDCUser matches an existing account on, and an empty
	// subject would match every OIDC account ever created with no subject at
	// all -- there is no such account today, but "no rows share this value"
	// is a property this hard refusal keeps true rather than assumed.
	if idToken.Subject == "" {
		LogSecurityEvent(ctx, slog.LevelWarn, EventSignInFailed,
			"authenticator", "oidc", "reason", "empty subject claim")
		return nil, ErrInvalidCredentials
	}

	var extra struct {
		PreferredUsername string `json:"preferred_username"`
		Name              string `json:"name"`
		Email             string `json:"email"`
	}
	if err := idToken.Claims(&extra); err != nil {
		// The token verified; this is a malformed claims payload, not a
		// forged token. Still refused -- UpsertOIDCUser needs a username --
		// but it is a shape problem worth telling the operator about, not a
		// credential failure to blame on the user.
		return nil, fmt.Errorf("decoding oidc claims: %w", err)
	}

	// An empty preferred_username is refused for the same reason an empty
	// subject is, one step further out: UpsertOIDCUser has to write SOMETHING
	// to app_user.username. Without this, a realm that lost its username
	// mapper (or a client that lost the `profile` scope) writes `username=''`
	// over an existing row on every sign-in -- after which Authenticate reads
	// the session as anonymous and the account cannot be reached at all,
	// while a second such user collides on the UNIQUE index.
	//
	// It is a realm misconfiguration rather than a bad credential, but it is
	// refused the same way and for the same reason the subject check is: the
	// alternative is persisting corruption driven from the IdP side. The log
	// line is what tells the operator which of the two it was.
	if strings.TrimSpace(extra.PreferredUsername) == "" {
		LogSecurityEvent(ctx, slog.LevelWarn, EventSignInFailed,
			"authenticator", "oidc", "reason", "empty preferred_username claim")
		return nil, ErrInvalidCredentials
	}

	return &OIDCClaims{
		Subject:     idToken.Subject,
		Username:    extra.PreferredUsername,
		DisplayName: extra.Name,
		Email:       extra.Email,
	}, nil
}

// classifyVerificationError names WHICH check inside Verify failed, for the
// log line only -- Exchange always returns the same ErrInvalidCredentials to
// the caller regardless of this value. Keeping the classification here
// rather than inline keeps Verify's actual checks (in the go-oidc library)
// the single source of truth: this only reads the error text and message
// types go-oidc already documents (TokenExpiredError; the rest are
// documented %-formatted strings in coreos/go-oidc's verify.go), it never
// re-implements a check.
func classifyVerificationError(err error) string {
	var expired *oidc.TokenExpiredError
	if errors.As(err, &expired) {
		return "expired"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "audience"):
		return "audience"
	case strings.Contains(msg, "issued by a different provider"):
		return "issuer"
	case strings.Contains(msg, "signature"),
		strings.Contains(msg, "malformed jwt"),
		strings.Contains(msg, "not signed"):
		return "signature"
	default:
		return "verification failed"
	}
}
