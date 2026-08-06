// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Package auth holds the authenticators, session plumbing and the
// authorization check.
package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/alexedwards/argon2id"

	"github.com/madalinignisca/invctl/internal/domain"
)

// ErrInvalidCredentials is returned for every authentication failure.
//
// One error for "no such user", "wrong password" and "account disabled" is
// deliberate: distinguishing them tells an attacker which usernames exist.
var ErrInvalidCredentials = errors.New("invalid credentials")

// Authenticator verifies a username and password.
//
// Both authenticators sit behind this interface so that adding a third (or
// removing LDAP) is a wiring change, not a handler change.
type Authenticator interface {
	// Name identifies the authenticator in logs.
	Name() string
	// Authenticate returns the user on success, or ErrInvalidCredentials.
	Authenticate(ctx context.Context, username, password string) (*domain.AppUser, error)
}

// UserStore is the slice of the store that authentication needs.
type UserStore interface {
	GetUserByUsername(ctx context.Context, username string) (*domain.AppUser, error)
	UpsertLDAPUser(ctx context.Context, username, displayName, email string) (*domain.AppUser, error)
	TouchLogin(ctx context.Context, userID string) error
}

// LocalAuthenticator checks an argon2id hash held in app_user.
type LocalAuthenticator struct {
	users UserStore
}

// NewLocalAuthenticator builds the local authenticator.
func NewLocalAuthenticator(users UserStore) *LocalAuthenticator {
	return &LocalAuthenticator{users: users}
}

// Name identifies this authenticator.
func (a *LocalAuthenticator) Name() string { return "local" }

// Authenticate verifies a password against the stored argon2id hash.
func (a *LocalAuthenticator) Authenticate(ctx context.Context, username, password string) (*domain.AppUser, error) {
	user, err := a.users.GetUserByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			// Still run a hash comparison so that a missing user and a wrong
			// password take comparable time. Without this the response time
			// is a username oracle.
			_, _ = argon2id.ComparePasswordAndHash(password, dummyHash)
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("looking up user: %w", err)
	}
	if !user.IsActive || user.Source != domain.UserSourceLocal || user.PasswordHash == nil {
		_, _ = argon2id.ComparePasswordAndHash(password, dummyHash)
		return nil, ErrInvalidCredentials
	}

	match, err := argon2id.ComparePasswordAndHash(password, *user.PasswordHash)
	if err != nil {
		return nil, fmt.Errorf("comparing password hash: %w", err)
	}
	if !match {
		return nil, ErrInvalidCredentials
	}
	return user, nil
}

// dummyHash is a real argon2id hash of a value nobody knows, used to keep the
// timing of a failed lookup close to that of a failed comparison.
const dummyHash = "$argon2id$v=19$m=65536,t=1,p=2$YWJjZGVmZ2hpamtsbW5vcA$c2VtaWNvbnN0YW50ZHVtbXloYXNodmFsdWU"

// HashPassword produces an argon2id hash. argon2id only -- never bcrypt,
// never a bare sha.
func HashPassword(password string) (string, error) {
	hash, err := argon2id.CreateHash(password, argon2id.DefaultParams)
	if err != nil {
		return "", fmt.Errorf("hashing password: %w", err)
	}
	return hash, nil
}

// Chain tries each authenticator in order and returns the first success.
type Chain struct {
	authenticators []Authenticator
	users          UserStore
}

// NewChain builds an authenticator chain.
func NewChain(users UserStore, authenticators ...Authenticator) *Chain {
	return &Chain{authenticators: authenticators, users: users}
}

// Name identifies the chain.
func (c *Chain) Name() string { return "chain" }

// Authenticate tries each configured authenticator in turn.
//
// A non-credential error (LDAP unreachable, database down) stops the chain
// rather than falling through: silently degrading to the next authenticator
// would turn an outage into a confusing authorization failure.
func (c *Chain) Authenticate(ctx context.Context, username, password string) (*domain.AppUser, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if username == "" || password == "" {
		return nil, ErrInvalidCredentials
	}

	var lastCredentialErr error
	for _, a := range c.authenticators {
		user, err := a.Authenticate(ctx, username, password)
		if err == nil {
			// Recording the login is useful, not load-bearing: a failure here
			// must not deny access. It must still be visible, though --
			// silently discarding it means last_login_at quietly stops
			// updating and nobody finds out.
			if touchErr := c.users.TouchLogin(ctx, user.ID); touchErr != nil {
				slog.Warn("could not record last login",
					"username", user.Username, "error", touchErr)
			}
			return user, nil
		}
		if errors.Is(err, ErrInvalidCredentials) {
			lastCredentialErr = err
			continue
		}
		return nil, fmt.Errorf("authenticating with %s: %w", a.Name(), err)
	}
	if lastCredentialErr == nil {
		lastCredentialErr = ErrInvalidCredentials
	}
	return nil, lastCredentialErr
}

// Authorizer decides what a user may do.
type Authorizer struct {
	admins map[string]bool
}

// NewAuthorizer builds the POC authorization model from a username list.
func NewAuthorizer(adminUsers []string) *Authorizer {
	admins := make(map[string]bool, len(adminUsers))
	for _, u := range adminUsers {
		admins[strings.ToLower(strings.TrimSpace(u))] = true
	}
	return &Authorizer{admins: admins}
}

// CanWrite reports whether a user may mutate anything.
//
// This is a one-liner today by design. LDAP group-based roles land post-POC,
// and the point of routing every check through this function is that they
// should only require changing its body -- not touching every handler.
func (a *Authorizer) CanWrite(user *domain.AppUser) bool {
	if user == nil || !user.IsActive {
		return false
	}
	return a.admins[strings.ToLower(user.Username)]
}

// CanRead reports whether a user may see anything. Every authenticated user
// can read in the POC.
func (a *Authorizer) CanRead(user *domain.AppUser) bool {
	return user != nil && user.IsActive
}

// CanSeeCosts reports whether a user may see money: acquisition prices, support
// contract values, project totals.
//
// It returns exactly what CanRead does, and that is a DECISION rather than an
// oversight. This application is not yet aimed at an audience some of whom must
// be kept away from commercial figures, so gating them now would be inventing a
// requirement -- and a permission nobody has thought through is worse than none,
// because it looks like protection.
//
// It exists as its own function anyway, for the same reason CanWrite is a
// one-liner: cost visibility is the most likely FIRST thing a real deployment
// wants to separate from ordinary read access, and when that day comes the whole
// change should be this function's body rather than every handler and template
// that renders an amount.
func (a *Authorizer) CanSeeCosts(user *domain.AppUser) bool {
	return a.CanRead(user)
}
