package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

// Configuration is validated at startup rather than at first use, so these
// tests are about refusing to start rather than about behaviour later.

// pristineEnv clears every variable Load reads, so a test asserting default
// behaviour asserts it regardless of who invoked the test binary.
//
// This is not defensive tidiness. `make test` exports INV_LISTEN=0.0.0.0:8088
// for the demo server, which meant TestLoadDefaults asserted the default port
// while running with a non-default port in its environment -- it failed under
// `make test` and passed under a bare `go test`, so the suite's verdict
// depended on how it was invoked. A test named "defaults" must own its
// environment rather than inherit one.
//
// t.Setenv is enough to clear: envOr treats empty as unset, and the testing
// package restores the previous values when the test ends.
func pristineEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"INV_ADMIN_PASSWORD", "INV_ADMIN_USERNAME", "INV_ADMIN_USERS",
		"INV_AUTH_LDAP", "INV_AUTH_LOCAL", "INV_DB_DRIVER", "INV_DB_DSN",
		"INV_LDAP_BIND_DN", "INV_LDAP_SKIP_VERIFY", "INV_LDAP_STARTTLS",
		"INV_LDAP_URL", "INV_LISTEN", "INV_LOG_LEVEL", "INV_SECURE_COOKIES",
		"INV_SEED", "INV_SESSION_KEY", "INV_SESSION_TIMEOUT",
	} {
		t.Setenv(key, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	pristineEnv(t)
	t.Setenv("INV_ADMIN_USERS", "gabriel,Nikolaj")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.DBDriver != "sqlite" {
		t.Errorf("driver = %q, want sqlite", cfg.DBDriver)
	}
	if cfg.Listen != ":8080" {
		t.Errorf("listen = %q, want :8080", cfg.Listen)
	}
	if !cfg.AuthLocal {
		t.Error("local authentication is off by default")
	}
	if cfg.AuthLDAP {
		t.Error("LDAP authentication is on by default")
	}
	// A generated key is fine for a demo; it must still be long enough to be
	// a real key rather than a placeholder.
	if len(cfg.SessionKey) < 32 {
		t.Errorf("generated session key is %d bytes, want at least 32", len(cfg.SessionKey))
	}
	// The admin list is normalised so the check does not depend on how the
	// operator typed it.
	if len(cfg.AdminUsers) != 2 || cfg.AdminUsers[1] != "nikolaj" {
		t.Errorf("admins = %v, want them lowercased", cfg.AdminUsers)
	}
}

func TestIsAdmin(t *testing.T) {
	cfg := &Config{AdminUsers: []string{"gabriel", "nikolaj"}}

	tests := []struct {
		username string
		want     bool
	}{
		{"gabriel", true},
		{"GABRIEL", true},
		{"  gabriel  ", true},
		{"nikolaj", true},
		{"someone", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := cfg.IsAdmin(tc.username); got != tc.want {
			t.Errorf("IsAdmin(%q) = %v, want %v", tc.username, got, tc.want)
		}
	}
}

func TestValidationRefusesToStart(t *testing.T) {
	pristineEnv(t)
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{
			name:    "unknown driver",
			env:     map[string]string{"INV_DB_DRIVER": "mysql"},
			wantErr: "sqlite or postgres",
		},
		{
			// A process with no way to sign in is not useful, and finding out
			// at the login page is worse than finding out at startup.
			name: "no authenticator enabled",
			env: map[string]string{
				"INV_AUTH_LOCAL": "false",
				"INV_AUTH_LDAP":  "false",
			},
			wantErr: "at least one of",
		},
		{
			name: "ldap enabled without a url",
			env: map[string]string{
				"INV_AUTH_LDAP": "true",
			},
			wantErr: "INV_LDAP_URL",
		},
		{
			name: "ldap bind template without a placeholder",
			env: map[string]string{
				"INV_AUTH_LDAP":    "true",
				"INV_LDAP_URL":     "ldap://127.0.0.1:389",
				"INV_LDAP_BIND_DN": "uid=fixed,ou=users,dc=example,dc=com",
			},
			wantErr: "must contain",
		},
		{
			name:    "session key is not base64",
			env:     map[string]string{"INV_SESSION_KEY": "not base64!!"},
			wantErr: "expected base64",
		},
		{
			name: "session key is too short",
			env: map[string]string{
				"INV_SESSION_KEY": base64.StdEncoding.EncodeToString([]byte("too-short")),
			},
			wantErr: "at least 32 bytes",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, err := Load()
			if err == nil {
				t.Fatalf("Load succeeded, want an error mentioning %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidSessionKeyIsAccepted(t *testing.T) {
	pristineEnv(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	t.Setenv("INV_SESSION_KEY", base64.StdEncoding.EncodeToString(key))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.SessionKey) != 32 {
		t.Errorf("key length = %d, want 32", len(cfg.SessionKey))
	}
	if SessionKeyGenerated() {
		t.Error("SessionKeyGenerated reports true for a configured key")
	}
}
