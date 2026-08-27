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

// ProjectResolver is the narrow slice of the store that Authorizer.Permit
// needs (WP-G1 Task 12) to turn "this person is a project owner" into "these
// are the rows they may write": the projects a user is assigned to, and the
// entities each of the three project-linkable types (asset, service,
// circuit -- docs/rbac-design.md §4) currently links to those projects.
//
// A NARROW INTERFACE, not *store.SQLStore, for the same reason UserStore
// above is narrow: this package must not need to know the store's full
// surface to authorize a request, and a fake satisfying four methods is what
// keeps permit_test.go from needing a real database.
type ProjectResolver interface {
	// ProjectsForUser returns the ids of projects userID currently holds an
	// active assignment to, in a project that is itself not retired --
	// internal/store/user_projects.go's ProjectsForUser.
	ProjectsForUser(ctx context.Context, userID string) ([]string, error)
	// AssetIDsForProjects, ServiceIDsForProjects and CircuitIDsForProjects
	// each return the ids of every entity of that type currently linked
	// (owned or used) to any of projectIDs. Not filtered to `owns`:
	// docs/rbac-design.md §4 scopes a project owner's write to entities
	// LINKED to their projects, not only the ones they own outright.
	AssetIDsForProjects(ctx context.Context, projectIDs []string) ([]string, error)
	ServiceIDsForProjects(ctx context.Context, projectIDs []string) ([]string, error)
	CircuitIDsForProjects(ctx context.Context, projectIDs []string) ([]string, error)
}

// Authorizer decides what a user may do.
type Authorizer struct {
	admins   map[string]bool
	projects ProjectResolver
}

// NewAuthorizer builds the POC authorization model from a username list and
// the store slice Permit needs to derive a project owner's scope.
//
// projects may be nil for a caller that never mints a Permit for a project
// owner -- every existing call site before Task 12 only ever asked CanWrite/
// CanSeeCosts/IsAdministrator, none of which touch it -- but a real deployment
// wires the actual store here (cmd/invctl/main.go), because Permit calls
// straight through to it with no caching (see that method's own comment for
// why: a cached permission set is a stale permission set).
func NewAuthorizer(adminUsers []string, projects ProjectResolver) *Authorizer {
	admins := make(map[string]bool, len(adminUsers))
	for _, u := range adminUsers {
		admins[strings.ToLower(strings.TrimSpace(u))] = true
	}
	return &Authorizer{admins: admins, projects: projects}
}

// isAdministrator reports whether user has full Administrator authority --
// either by role, or because INV_ADMIN_USERS names them (docs/rbac-design.md
// §5, §8). The env list OVERRIDES the role column rather than merely seeding
// it: an operator setting it is recovering BECAUSE the column says otherwise,
// so §8's break-glass path would not work if this only applied at bootstrap.
//
// Callers must check user.IsActive themselves, and must do so BEFORE calling
// this -- break-glass restores a role, not a disabled account. If the
// activity check moved below this lookup, a deactivated account named in
// INV_ADMIN_USERS (an ex-employee's name can sit there long after they left)
// would write again the moment the variable is set for someone else's
// recovery. See TestADeactivatedAdministratorMayNotWriteEvenWhenNamedInTheEnvironment.
func (a *Authorizer) isAdministrator(user *domain.AppUser) bool {
	if a.admins[strings.ToLower(user.Username)] {
		return true
	}
	return user.Role == domain.RoleAdministrator
}

// IsAdministrator reports whether user holds full Administrator authority --
// by role, or by the INV_ADMIN_USERS break-glass override -- and is active.
//
// DELIBERATELY SEPARATE FROM CanWrite, even though the two agree on every
// input today: CanWrite is "may mutate anything", which happens to equal "is
// an Administrator" only because RoleProjectOwner is fail-closed until Task
// 13 (see CanWrite's own comment). A caller asking "is this specifically an
// Administrator" -- the secret-reference redaction on the service page is
// exactly that caller -- must not borrow CanWrite for it: the day a project
// owner's scoped write lands, CanWrite(project owner) starts returning true
// for their own objects, and a check that meant "administrator" would start
// handing them every other identity's credential path along with it.
func (a *Authorizer) IsAdministrator(user *domain.AppUser) bool {
	if user == nil || !user.IsActive {
		return false
	}
	return a.isAdministrator(user)
}

// EnvOverride reports whether user's write access, if any, is granted
// specifically by the INV_ADMIN_USERS break-glass list -- independent of what
// app_user.role says, and independent of IsActive.
//
// Exported for exactly one caller: the user-administration roster (WP-G1 Task
// 5), which has to say WHERE effective access comes from, not just what
// CanWrite/IsAdministrator currently answers. Every existing estate upgrading
// into role-based access starts with every row at role='observer' (migration
// 00058's default) while INV_ADMIN_USERS keeps naming the real operators --
// so the roster showing the stored column alone would name its only writer a
// reader, on the one screen whose entire job is answering "who can write
// here". See docs/RECOVERY.md for what this environment variable is and why
// it exists.
func (a *Authorizer) EnvOverride(user *domain.AppUser) bool {
	if user == nil {
		return false
	}
	return a.admins[strings.ToLower(user.Username)]
}

// CanWrite reports whether a user may mutate anything.
//
// Administrator (by role, or by the INV_ADMIN_USERS override) may write
// everything. Observer may write nothing.
//
// RoleProjectOwner deliberately returns false here, unconditionally, even
// once they are assigned to a project. Object-level scope -- "may write
// entities linked to their own project" -- is a per-handler check against the
// object, decided by WP-G1 Task 13, and does not exist yet. Treating a
// project owner as writable before that check lands would grant them
// unrestricted write over the whole estate, which is worse than today's
// model. This is the deliberate fail-closed state, not a bug -- see
// TestAProjectOwnerCannotWriteAnythingUntilTheObjectGateIsLive. Do not "fix"
// it without Task 13 also landing.
func (a *Authorizer) CanWrite(user *domain.AppUser) bool {
	if user == nil || !user.IsActive {
		return false
	}
	return a.isAdministrator(user)
}

// CanRead reports whether a user may see anything. Every authenticated user
// can read -- see docs/rbac-design.md §2, a deliberate decision to avoid the
// disclosure risk and impact-engine correctness cost of read scoping.
func (a *Authorizer) CanRead(user *domain.AppUser) bool {
	return user != nil && user.IsActive
}

// CanSeeCosts reports whether a user may see money: acquisition prices,
// support contract values, project totals. See docs/rbac-design.md §3.
//
// Administrator sees costs implicitly and never consults the grant column --
// withholding money from someone who can already see and change everything
// else would be theatre.
//
// Observer AND ProjectOwner see costs only if app_user.can_see_costs is set,
// and BOTH consult the same column the SAME way. This is deliberate and is
// the fix for a real defect an earlier draft shipped: giving Observers costs
// implicitly made the permission non-monotonic, because demoting a project
// owner (grant=false) to Observer would have taken them from one project's
// costs to the whole estate's -- exactly backwards for the case this exists
// to serve, a newly hired product owner who must not see costs for a
// contractual period. See
// TestDemotingAProjectOwnerToObserverNeverWidensTheirCostVisibility.
//
// This narrows behaviour for every existing deployment: CanSeeCosts used to
// return CanRead(user) verbatim, so every reader saw costs. That is a
// documented behaviour change (CHANGELOG.md), not a silent regression.
func (a *Authorizer) CanSeeCosts(user *domain.AppUser) bool {
	if user == nil || !user.IsActive {
		return false
	}
	if a.isAdministrator(user) {
		return true
	}
	return user.CanSeeCosts
}
