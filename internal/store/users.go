package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/gabriel/invctl/internal/domain"
)

// GetUserByUsername loads an account. Returns domain.ErrNotFound if there is
// no such user -- callers must not distinguish that from a bad password in
// anything they show the client.
func (s *SQLStore) GetUserByUsername(ctx context.Context, username string) (*domain.AppUser, error) {
	var u domain.AppUser
	if err := s.readOne(ctx, &u, `SELECT * FROM app_user WHERE username = ?`, lower(username)); err != nil {
		return nil, fmt.Errorf("getting user %s: %w", username, err)
	}
	return &u, nil
}

// CountUsers reports how many accounts exist, used to decide whether the
// seeded admin needs creating on first run.
func (s *SQLStore) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := s.readOne(ctx, &n, `SELECT COUNT(*) FROM app_user`); err != nil {
		return 0, fmt.Errorf("counting users: %w", err)
	}
	return n, nil
}

// CreateUser inserts an account.
//
// The change log records the account, never the hash: an audit trail is read
// by more people than the user table is.
func (s *SQLStore) CreateUser(ctx context.Context, actor domain.Actor, u *domain.AppUser) error {
	return s.write(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO app_user (id, username, display_name, email, source,
			                      password_hash, is_active, last_login_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			u.ID, u.Username, u.DisplayName, u.Email, u.Source,
			u.PasswordHash, u.IsActive, u.LastLoginAt, u.CreatedAt)
		if err != nil {
			return translateWriteErr(err, "creating user")
		}
		redacted := *u
		redacted.PasswordHash = nil
		return t.logCreate(ctx, "app_user", u.ID, &redacted)
	})
}

// UpsertLDAPUser records an account after a successful LDAP bind.
//
// LDAP users never get a password hash: the credential belongs to the
// directory and we have no business storing anything derived from it.
func (s *SQLStore) UpsertLDAPUser(ctx context.Context, username, displayName, email string) (*domain.AppUser, error) {
	existing, err := s.GetUserByUsername(ctx, username)
	switch {
	case err == nil:
		// Two directories of truth can produce the same username. Handing an
		// LDAP principal the local account row would merge the identities
		// outright: the directory user would inherit the local account's id,
		// and every change_log row it wrote would be attributed to the local
		// operator. Refuse instead -- an admin has to resolve the collision.
		if existing.Source != domain.UserSourceLDAP {
			return nil, fmt.Errorf(
				"recording ldap user %s: a %s account already owns that username: %w",
				username, existing.Source, domain.ErrConflict)
		}
		return existing, nil

	case errors.Is(err, domain.ErrNotFound):
		// Expected: first sign-in for this directory user.

	default:
		// A transient failure must not be mistaken for "no such user", or the
		// insert below turns a database blip into a confusing conflict.
		return nil, fmt.Errorf("looking up ldap user %s: %w", username, err)
	}
	u, err := domain.NewAppUser(NewID(), username, domain.UserSourceLDAP, s.now())
	if err != nil {
		return nil, err
	}
	if displayName != "" {
		u.DisplayName = &displayName
	}
	if email != "" {
		u.Email = &email
	}
	// The directory is the actor here, not the person logging in: the account
	// row exists because LDAP said so.
	if err := s.CreateUser(ctx, domain.Actor{Name: "ldap", Kind: "system"}, u); err != nil {
		return nil, err
	}
	return u, nil
}

// TouchLogin records a successful sign-in. Deliberately not audited: a
// change_log row per login would bury the changes that matter.
func (s *SQLStore) TouchLogin(ctx context.Context, userID string) error {
	at := domain.FormatTime(s.now())
	_, err := s.db.Writer.ExecContext(ctx,
		s.db.Rebind(`UPDATE app_user SET last_login_at = ? WHERE id = ?`), at, userID)
	if err != nil {
		return fmt.Errorf("recording login for %s: %w", userID, err)
	}
	return nil
}
