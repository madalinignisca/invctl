package auth

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"

	"github.com/go-ldap/ldap/v3"

	"github.com/gabriel/invctl/internal/config"
	"github.com/gabriel/invctl/internal/domain"
)

// LDAPAuthenticator authenticates by simple bind against a directory.
//
// It never stores anything derived from the password: on a successful bind it
// upserts an app_user row with source='ldap' and no hash. The credential
// belongs to the directory.
type LDAPAuthenticator struct {
	cfg   config.LDAPConfig
	users UserStore
}

// NewLDAPAuthenticator builds the LDAP authenticator.
func NewLDAPAuthenticator(cfg config.LDAPConfig, users UserStore) *LDAPAuthenticator {
	// No SkipVerify check here: config.validate refuses to start on it, so an
	// authenticator can never be built with it set. The tls.Config below still
	// reads the field rather than hardcoding false, because a struct that
	// silently ignores its own configuration is worse than one that cannot be
	// given a bad value in the first place.
	return &LDAPAuthenticator{cfg: cfg, users: users}
}

// Name identifies this authenticator.
func (a *LDAPAuthenticator) Name() string { return "ldap" }

// Authenticate binds as the user and, on success, ensures a local account row
// exists to hang sessions and audit entries off.
func (a *LDAPAuthenticator) Authenticate(ctx context.Context, username, password string) (*domain.AppUser, error) {
	// A DN is assembled by substitution, so a username containing DN
	// metacharacters could otherwise change the DN's meaning.
	if !safeUsername(username) {
		return nil, ErrInvalidCredentials
	}
	// An empty password against most directories is an *anonymous* bind,
	// which succeeds and would authenticate anybody.
	if password == "" {
		return nil, ErrInvalidCredentials
	}

	conn, err := ldap.DialURL(a.cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("dialling ldap: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if a.cfg.StartTLS {
		if err := conn.StartTLS(&tls.Config{InsecureSkipVerify: a.cfg.SkipVerify}); err != nil {
			return nil, fmt.Errorf("starting tls: %w", err)
		}
	}

	dn := fmt.Sprintf(a.cfg.BindDNTemplate, username)
	if err := conn.Bind(dn, password); err != nil {
		if ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials) {
			return nil, ErrInvalidCredentials
		}
		// Anything else -- server down, DN not found, TLS failure -- is an
		// operational problem, and reporting it as a bad password would send
		// the operator looking in entirely the wrong place. The message is
		// logged, never shown to the client.
		return nil, fmt.Errorf("binding as %s: %w", dn, err)
	}

	user, err := a.users.UpsertLDAPUser(ctx, username, "", "")
	if err != nil {
		return nil, fmt.Errorf("recording ldap user: %w", err)
	}
	if !user.IsActive {
		return nil, ErrInvalidCredentials
	}
	return user, nil
}

// safeUsername rejects anything that could alter the meaning of the assembled
// DN. The allowed set is deliberately narrow -- real usernames fit inside it,
// and injection attempts do not.
func safeUsername(username string) bool {
	if username == "" || len(username) > 128 {
		return false
	}
	for _, r := range username {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9':
		case strings.ContainsRune(".-_@", r):
		default:
			return false
		}
	}
	return true
}
