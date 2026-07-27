package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gabriel/invctl/internal/domain"
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
