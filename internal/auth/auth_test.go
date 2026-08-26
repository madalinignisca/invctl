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
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
)

// fakeUsers is an in-memory UserStore. Authentication logic is pure enough
// that a database adds nothing here -- the store's own behaviour is covered by
// its own suite against both engines.
type fakeUsers struct {
	byName    map[string]*domain.AppUser
	upserted  []string
	logins    []string
	failNext  error
	failLogin error
}

func newFakeUsers() *fakeUsers {
	return &fakeUsers{byName: map[string]*domain.AppUser{}}
}

func (f *fakeUsers) add(t *testing.T, username, password, source string, active bool) *domain.AppUser {
	t.Helper()
	u, err := domain.NewAppUser("id-"+username, username, source, time.Now())
	if err != nil {
		t.Fatalf("building user: %v", err)
	}
	u.IsActive = active
	if password != "" {
		hash, err := HashPassword(password)
		if err != nil {
			t.Fatalf("hashing password: %v", err)
		}
		u.PasswordHash = &hash
	}
	f.byName[u.Username] = u
	return u
}

func (f *fakeUsers) GetUserByUsername(_ context.Context, username string) (*domain.AppUser, error) {
	if f.failNext != nil {
		return nil, f.failNext
	}
	u, ok := f.byName[strings.ToLower(username)]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return u, nil
}

func (f *fakeUsers) UpsertLDAPUser(_ context.Context, username, _, _ string) (*domain.AppUser, error) {
	f.upserted = append(f.upserted, username)
	if u, ok := f.byName[username]; ok {
		return u, nil
	}
	u, err := domain.NewAppUser("id-"+username, username, domain.UserSourceLDAP, time.Now())
	if err != nil {
		return nil, err
	}
	f.byName[username] = u
	return u, nil
}

func (f *fakeUsers) TouchLogin(_ context.Context, userID string) error {
	f.logins = append(f.logins, userID)
	return f.failLogin
}

func TestLocalAuthenticator(t *testing.T) {
	ctx := context.Background()

	users := newFakeUsers()
	users.add(t, "admin", "correct-horse", domain.UserSourceLocal, true)
	users.add(t, "disabled", "correct-horse", domain.UserSourceLocal, false)
	users.add(t, "ldapuser", "", domain.UserSourceLDAP, true)

	a := NewLocalAuthenticator(users)

	tests := []struct {
		name     string
		username string
		password string
		wantOK   bool
	}{
		{name: "correct credentials", username: "admin", password: "correct-horse", wantOK: true},
		{name: "username is case-insensitive", username: "ADMIN", password: "correct-horse", wantOK: true},
		{name: "wrong password", username: "admin", password: "wrong"},
		{name: "unknown user", username: "nobody", password: "correct-horse"},
		{name: "empty password", username: "admin", password: ""},
		// A disabled account must not authenticate even with the right
		// password: deactivation is how access is revoked.
		{name: "disabled account", username: "disabled", password: "correct-horse"},
		// An LDAP user has no local hash; falling through to a nil-hash
		// comparison would be a bypass.
		{name: "ldap user has no local password", username: "ldapuser", password: "anything"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			user, err := a.Authenticate(ctx, tc.username, tc.password)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("Authenticate: %v", err)
				}
				if user == nil || user.Username != "admin" {
					t.Fatalf("got user %+v, want admin", user)
				}
				return
			}
			if !errors.Is(err, ErrInvalidCredentials) {
				t.Errorf("error = %v, want ErrInvalidCredentials", err)
			}
			if user != nil {
				t.Errorf("got a user on a failed authentication: %+v", user)
			}
		})
	}
}

// TestFailureIsIndistinguishable: an unknown username and a wrong password
// must produce the same error, or the login form becomes a username oracle.
func TestFailureIsIndistinguishable(t *testing.T) {
	ctx := context.Background()
	users := newFakeUsers()
	users.add(t, "admin", "correct-horse", domain.UserSourceLocal, true)
	a := NewLocalAuthenticator(users)

	_, unknownErr := a.Authenticate(ctx, "nobody", "whatever")
	_, wrongErr := a.Authenticate(ctx, "admin", "whatever")

	if unknownErr.Error() != wrongErr.Error() {
		t.Errorf("unknown user gives %q but wrong password gives %q; "+
			"the difference reveals which usernames exist", unknownErr, wrongErr)
	}
}

// TestOperationalErrorIsNotACredentialError: a database outage must not be
// reported as a bad password, or the operator debugs the wrong thing.
func TestOperationalErrorIsNotACredentialError(t *testing.T) {
	ctx := context.Background()
	users := newFakeUsers()
	users.add(t, "admin", "correct-horse", domain.UserSourceLocal, true)
	users.failNext = errors.New("database is on fire")

	a := NewLocalAuthenticator(users)
	_, err := a.Authenticate(ctx, "admin", "correct-horse")

	if err == nil {
		t.Fatal("Authenticate succeeded despite a store failure")
	}
	if errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("a store failure was reported as invalid credentials: %v", err)
	}
}

func TestHashPasswordProducesArgon2id(t *testing.T) {
	hash, err := HashPassword("correct-horse")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	// argon2id only -- never bcrypt, never a bare sha.
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Errorf("hash = %q, want an argon2id hash", hash)
	}
	// The same password must not produce the same hash twice, or the salt is
	// not doing its job.
	second, err := HashPassword("correct-horse")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == second {
		t.Error("two hashes of the same password are identical; the salt is not random")
	}
}

func TestChainTriesEachAuthenticator(t *testing.T) {
	ctx := context.Background()
	users := newFakeUsers()
	users.add(t, "admin", "correct-horse", domain.UserSourceLocal, true)

	rejecting := &stubAuthenticator{name: "rejecting", err: ErrInvalidCredentials}
	chain := NewChain(users, rejecting, NewLocalAuthenticator(users))

	user, err := chain.Authenticate(ctx, "admin", "correct-horse")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if user.Username != "admin" {
		t.Errorf("user = %s, want admin", user.Username)
	}
	if !rejecting.called {
		t.Error("the chain skipped the first authenticator")
	}
	if len(users.logins) != 1 {
		t.Errorf("recorded %d logins, want 1", len(users.logins))
	}
}

// TestChainStopsOnOperationalError: falling through to the next authenticator
// when the first one is broken turns an outage into a confusing authorization
// failure, and could let a stale local password stand in for a directory that
// has since disabled the account.
func TestChainStopsOnOperationalError(t *testing.T) {
	ctx := context.Background()
	users := newFakeUsers()
	users.add(t, "admin", "correct-horse", domain.UserSourceLocal, true)

	broken := &stubAuthenticator{name: "broken", err: errors.New("ldap unreachable")}
	fallback := &stubAuthenticator{name: "fallback"}
	chain := NewChain(users, broken, fallback)

	_, err := chain.Authenticate(ctx, "admin", "correct-horse")
	if err == nil {
		t.Fatal("Authenticate succeeded despite a broken authenticator")
	}
	if errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("an operational failure was reported as invalid credentials: %v", err)
	}
	if fallback.called {
		t.Error("the chain fell through to the next authenticator after an operational failure")
	}
}

func TestChainRejectsEmptyInput(t *testing.T) {
	ctx := context.Background()
	users := newFakeUsers()
	inner := &stubAuthenticator{name: "inner"}
	chain := NewChain(users, inner)

	for _, tc := range []struct{ username, password string }{
		{"", "password"}, {"admin", ""}, {"", ""}, {"   ", "password"},
	} {
		if _, err := chain.Authenticate(ctx, tc.username, tc.password); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("username=%q password=%q gave %v, want ErrInvalidCredentials",
				tc.username, tc.password, err)
		}
	}
	if inner.called {
		t.Error("empty input reached an authenticator")
	}
}

// TestLoginRecordingIsNotLoadBearing: failing to record a login must not fail
// the login.
func TestLoginRecordingIsNotLoadBearing(t *testing.T) {
	ctx := context.Background()
	users := newFakeUsers()
	users.add(t, "admin", "correct-horse", domain.UserSourceLocal, true)
	users.failLogin = errors.New("write failed")

	chain := NewChain(users, NewLocalAuthenticator(users))
	if _, err := chain.Authenticate(ctx, "admin", "correct-horse"); err != nil {
		t.Errorf("a failed last-login write broke the sign-in: %v", err)
	}
}

func TestAuthorizer(t *testing.T) {
	authz := NewAuthorizer([]string{"gabriel", "Nikolaj", " ingrid "})

	active := func(name string) *domain.AppUser {
		return &domain.AppUser{Username: name, IsActive: true}
	}

	tests := []struct {
		name     string
		user     *domain.AppUser
		wantRead bool
		wantWrit bool
	}{
		{name: "listed admin", user: active("gabriel"), wantRead: true, wantWrit: true},
		{name: "admin list is case-insensitive", user: active("nikolaj"), wantRead: true, wantWrit: true},
		{name: "admin list is trimmed", user: active("ingrid"), wantRead: true, wantWrit: true},
		{name: "unlisted user is read-only", user: active("someone"), wantRead: true},
		{name: "inactive user can do nothing", user: &domain.AppUser{Username: "gabriel"}},
		{name: "nil user can do nothing", user: nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := authz.CanRead(tc.user); got != tc.wantRead {
				t.Errorf("CanRead = %v, want %v", got, tc.wantRead)
			}
			if got := authz.CanWrite(tc.user); got != tc.wantWrit {
				t.Errorf("CanWrite = %v, want %v", got, tc.wantWrit)
			}
		})
	}
}

// TestEmptyAdminListGrantsNothing: a deployment with no admins configured is
// read-only, not open.
func TestEmptyAdminListGrantsNothing(t *testing.T) {
	authz := NewAuthorizer(nil)
	user := &domain.AppUser{Username: "anyone", IsActive: true}

	if authz.CanWrite(user) {
		t.Error("an empty admin list granted write access")
	}
	if !authz.CanRead(user) {
		t.Error("an empty admin list denied read access")
	}
}

// TestAnAdministratorByRoleMayWriteWithoutBeingNamedInTheEnvironment: the
// role column, not INV_ADMIN_USERS, is meant to be the ordinary way someone
// becomes an Administrator (docs/rbac-design.md §5) -- the env list is the
// bootstrap/break-glass, not the everyday mechanism.
func TestAnAdministratorByRoleMayWriteWithoutBeingNamedInTheEnvironment(t *testing.T) {
	authz := NewAuthorizer(nil)
	user := &domain.AppUser{Username: "priya", IsActive: true, Role: domain.RoleAdministrator}

	if !authz.CanWrite(user) {
		t.Error("an Administrator by role, unnamed in INV_ADMIN_USERS, could not write")
	}
}

// TestAUserNamedInTheEnvironmentIsAnAdministratorWhateverTheirRoleColumnSays:
// spec §5 -- INV_ADMIN_USERS OVERRIDES the role column, it does not merely
// seed it. An operator sets this variable BECAUSE the column says otherwise;
// if it only seeded the role, §8's recovery path would not work at the
// moment it is needed.
func TestAUserNamedInTheEnvironmentIsAnAdministratorWhateverTheirRoleColumnSays(t *testing.T) {
	authz := NewAuthorizer([]string{"priya"})
	user := &domain.AppUser{Username: "priya", IsActive: true, Role: domain.RoleObserver}

	if !authz.CanWrite(user) {
		t.Error("a user named in INV_ADMIN_USERS with role=observer could not write")
	}
}

// TestADeactivatedAdministratorMayNotWriteEvenWhenNamedInTheEnvironment:
// break-glass restores a role, not a disabled account. Otherwise deactivation
// is defeated by a variable an ex-employee's name may still be sitting in --
// see the comment on isAdministrator for why the ordering of the two checks
// is the whole point of this test.
func TestADeactivatedAdministratorMayNotWriteEvenWhenNamedInTheEnvironment(t *testing.T) {
	authz := NewAuthorizer([]string{"priya"})
	user := &domain.AppUser{Username: "priya", IsActive: false, Role: domain.RoleAdministrator}

	if authz.CanWrite(user) {
		t.Error("a deactivated account named in INV_ADMIN_USERS could still write")
	}
}

// TestAnObserverMayReadEverythingAndWriteNothing is the plain baseline the
// rest of this suite's project-owner tests are contrasted against.
func TestAnObserverMayReadEverythingAndWriteNothing(t *testing.T) {
	authz := NewAuthorizer(nil)
	user := &domain.AppUser{Username: "sam", IsActive: true, Role: domain.RoleObserver}

	if !authz.CanRead(user) {
		t.Error("an Observer could not read")
	}
	if authz.CanWrite(user) {
		t.Error("an Observer could write")
	}
}

// TestAProjectOwnerCannotWriteAnythingUntilTheObjectGateIsLive is the
// fail-closed ruling from docs/rbac-design.md §6/§8, asserted directly:
// CanWrite(project owner) == false, exactly, full stop. Object-level scope
// ("may write entities linked to their own project") is decided per-handler
// against the object and does not exist yet -- it is WP-G1 Task 13. Until
// that lands, treating a project owner as writable here would hand them
// unrestricted write over the entire estate, which is worse than refusing
// them outright. THIS IS NOT A BUG. Do not "fix" a red version of this test
// by loosening CanWrite -- land Task 13's object-level check first.
func TestAProjectOwnerCannotWriteAnythingUntilTheObjectGateIsLive(t *testing.T) {
	authz := NewAuthorizer(nil)
	user := &domain.AppUser{Username: "priya", IsActive: true, Role: domain.RoleProjectOwner}

	if authz.CanWrite(user) {
		t.Error("a project owner could write before the object-level gate (Task 13) exists")
	}
}

// TestAProjectOwnerAssignedToAProjectStillCannotWrite is the same ruling,
// with a project assignment recorded, because that is the state an
// Administrator will actually create during WP-G1 Pieces 1-2: a project
// owner is assigned to a project, on the shape of the data long before any
// code consults it for a write decision.
func TestAProjectOwnerAssignedToAProjectStillCannotWrite(t *testing.T) {
	authz := NewAuthorizer(nil)
	user := &domain.AppUser{
		Username: "priya", IsActive: true, Role: domain.RoleProjectOwner,
	}
	// CanWrite takes no project/assignment argument at all today -- there is
	// nothing yet for an assignment to change. That absence is exactly what
	// this test is pinning down: assignment data existing must not, by
	// itself, alter the answer until Task 13 wires an object check in.
	if authz.CanWrite(user) {
		t.Error("an assigned project owner could write before the object-level gate exists")
	}
}

// TestAProjectOwnerSeesCostsOnlyWhenGranted.
func TestAProjectOwnerSeesCostsOnlyWhenGranted(t *testing.T) {
	authz := NewAuthorizer(nil)
	ungranted := &domain.AppUser{Username: "priya", IsActive: true, Role: domain.RoleProjectOwner, CanSeeCosts: false}
	granted := &domain.AppUser{Username: "priya", IsActive: true, Role: domain.RoleProjectOwner, CanSeeCosts: true}

	if authz.CanSeeCosts(ungranted) {
		t.Error("an ungranted project owner could see costs")
	}
	if !authz.CanSeeCosts(granted) {
		t.Error("a granted project owner could not see costs")
	}
}

// TestAnObserverSeesCostsOnlyWhenGranted is the corrected rule (spec §3): an
// earlier draft gave Observers costs implicitly, which this test forbids.
func TestAnObserverSeesCostsOnlyWhenGranted(t *testing.T) {
	authz := NewAuthorizer(nil)
	ungranted := &domain.AppUser{Username: "sam", IsActive: true, Role: domain.RoleObserver, CanSeeCosts: false}
	granted := &domain.AppUser{Username: "sam", IsActive: true, Role: domain.RoleObserver, CanSeeCosts: true}

	if authz.CanSeeCosts(ungranted) {
		t.Error("an ungranted Observer could see costs")
	}
	if !authz.CanSeeCosts(granted) {
		t.Error("a granted Observer could not see costs")
	}
}

// TestDemotingAProjectOwnerToObserverNeverWidensTheirCostVisibility is the
// monotonicity defect from spec §3, written as a test. Under the earlier
// (wrong) rule, demoting a project owner to Observer took them from one
// project's costs to the whole estate's, defeating exactly the case this
// exists to serve: a newly hired product owner who must not see costs for a
// contractual period. A narrower role must never widen what someone can see.
func TestDemotingAProjectOwnerToObserverNeverWidensTheirCostVisibility(t *testing.T) {
	authz := NewAuthorizer(nil)
	asProjectOwner := &domain.AppUser{Username: "priya", IsActive: true, Role: domain.RoleProjectOwner, CanSeeCosts: false}
	asObserver := &domain.AppUser{Username: "priya", IsActive: true, Role: domain.RoleObserver, CanSeeCosts: false}

	if authz.CanSeeCosts(asProjectOwner) != authz.CanSeeCosts(asObserver) {
		t.Error("demoting a project owner to Observer changed their cost visibility with the grant unchanged")
	}
	if authz.CanSeeCosts(asObserver) {
		t.Error("demotion to Observer granted cost visibility that was never given")
	}
}

// TestAnAdministratorSeesCostsWithoutTheGrant: Administrator is the one role
// that does not consult app_user.can_see_costs at all.
func TestAnAdministratorSeesCostsWithoutTheGrant(t *testing.T) {
	authz := NewAuthorizer(nil)
	user := &domain.AppUser{Username: "gabriel", IsActive: true, Role: domain.RoleAdministrator, CanSeeCosts: false}

	if !authz.CanSeeCosts(user) {
		t.Error("an Administrator without the can_see_costs grant could not see costs")
	}
}

// TestSafeUsernameRejectsDNInjection: the bind DN is assembled by
// substitution, so a username containing DN metacharacters could otherwise
// change which entry is bound.
func TestSafeUsernameRejectsDNInjection(t *testing.T) {
	allowed := []string{"gabriel", "first.last", "user-name", "user_name", "a@b.com", "abc123"}
	for _, u := range allowed {
		if !safeUsername(u) {
			t.Errorf("safeUsername(%q) = false, want true", u)
		}
	}

	hostile := []string{
		"", "admin,ou=admins", "admin)(uid=*", "admin*", `admin\`, "admin=x",
		"admin+ou=y", `admin"`, "admin;dc=x", "admin<x", "admin>x", "admin#x",
		"admin\x00", "admin ", strings.Repeat("a", 129),
	}
	for _, u := range hostile {
		if safeUsername(u) {
			t.Errorf("safeUsername(%q) = true, want false", u)
		}
	}
}

// stubAuthenticator records whether it was consulted.
type stubAuthenticator struct {
	name   string
	err    error
	called bool
}

func (s *stubAuthenticator) Name() string { return s.name }

func (s *stubAuthenticator) Authenticate(context.Context, string, string) (*domain.AppUser, error) {
	s.called = true
	if s.err != nil {
		return nil, s.err
	}
	return &domain.AppUser{Username: "stub", IsActive: true}, nil
}
