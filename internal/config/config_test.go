// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

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
		"INV_AGENT_TOKENS", "INV_AGENT_SCOPES", "INV_AGENT_VOCAB",
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

// ---------------------------------------------------------------------------
// Monitoring credentials (docs/AUDIT.md rule 6)
// ---------------------------------------------------------------------------

// TestAgentCredentialsAreParsedAndScoped. INV_AGENT_TOKENS follows the shape
// rule 6 names -- `id:token`, comma-separated -- and the scope and vocabulary
// live beside it rather than inside it, so an operator editing a scope never
// has to handle a secret to do it.
func TestAgentCredentialsAreParsedAndScoped(t *testing.T) {
	pristineEnv(t)
	t.Setenv("INV_ADMIN_USERS", "gabriel")
	t.Setenv("INV_AGENT_TOKENS", "prom-prod:"+longToken("a")+", Prom-Dev :"+longToken("b"))
	t.Setenv("INV_AGENT_SCOPES", "prom-prod:prod|transit,prom-dev:DEV")
	t.Setenv("INV_AGENT_VOCAB", "prom-prod:prometheus")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.AgentCredentials) != 2 {
		t.Fatalf("%d credentials, want 2", len(cfg.AgentCredentials))
	}

	// Sorted by id, so a log line and a test see the same order however the
	// variable was written.
	dev, prod := cfg.AgentCredentials[0], cfg.AgentCredentials[1]
	if dev.ID != "prom-dev" {
		t.Errorf("first credential = %q, want prom-dev (ids are lower-cased and sorted)", dev.ID)
	}
	if got := strings.Join(dev.Environments, ","); got != "dev" {
		t.Errorf("prom-dev scope = %q, want dev (environment codes are lower-cased)", got)
	}
	if dev.Vocabulary != "" {
		t.Errorf("prom-dev vocabulary = %q, want the default", dev.Vocabulary)
	}
	if got := strings.Join(prod.Environments, ","); got != "prod,transit" {
		t.Errorf("prom-prod scope = %q, want prod,transit", got)
	}
	if prod.Vocabulary != "prometheus" {
		t.Errorf("prom-prod vocabulary = %q, want prometheus", prod.Vocabulary)
	}
	// The token is not lower-cased -- doing so would mangle it, and a mangled
	// token fails as an authentication error with nothing to point at.
	if prod.Token != longToken("a") {
		t.Errorf("token was altered during parsing")
	}
}

func TestNoAgentTokensMeansNoCredentials(t *testing.T) {
	pristineEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.AgentCredentials) != 0 {
		t.Errorf("%d credentials configured by default, want none", len(cfg.AgentCredentials))
	}
}

// TestAgentConfigurationRefusesToStart. Every one of these is a way for a
// deployment to end up running an authorization model that differs from the one
// somebody wrote down, and every one of them refuses to start rather than
// surfacing later as a puzzling 401 or a silently wider scope.
func TestAgentConfigurationRefusesToStart(t *testing.T) {
	cases := []struct {
		name    string
		tokens  string
		scopes  string
		vocab   string
		admins  string
		wantErr string
	}{
		{
			name:    "a credential with no scope",
			tokens:  "prom:" + longToken("a"),
			wantErr: "INV_AGENT_SCOPES",
		},
		{
			name:    "an empty scope",
			tokens:  "prom:" + longToken("a"),
			scopes:  "prom: | ",
			wantErr: "empty environment scope",
		},
		{
			name:    "the same id twice",
			tokens:  "prom:" + longToken("a") + ",prom:" + longToken("b"),
			scopes:  "prom:prod",
			wantErr: "twice",
		},
		{
			// Two credentials sharing a token makes the reporter recorded
			// against a reading whichever one the lookup happened to find --
			// attribution decided by map order.
			name:    "two credentials sharing a token",
			tokens:  "prom-a:" + longToken("a") + ",prom-b:" + longToken("a"),
			scopes:  "prom-a:prod,prom-b:dev",
			wantErr: "share a token",
		},
		{
			name:    "a short token",
			tokens:  "prom:short",
			scopes:  "prom:prod",
			wantErr: "characters",
		},
		{
			name:    "an entry with no colon",
			tokens:  longToken("a"),
			scopes:  "prom:prod",
			wantErr: "no colon",
		},
		{
			// Almost always a typo in an id, and its effect is that the real
			// credential silently falls back to no scope or to the default
			// vocabulary.
			name:    "a scope naming a credential that does not exist",
			tokens:  "prom:" + longToken("a"),
			scopes:  "prom:prod,prometheus:dev",
			wantErr: "not in INV_AGENT_TOKENS",
		},
		{
			name:    "a vocabulary naming a credential that does not exist",
			tokens:  "prom:" + longToken("a"),
			scopes:  "prom:prod",
			vocab:   "promm:prometheus",
			wantErr: "not in INV_AGENT_TOKENS",
		},
		{
			// Rule 6's opening sentence: a monitoring credential never appears
			// in INV_ADMIN_USERS, because that list grants every write route.
			name:    "a credential in the admin list",
			tokens:  "prom:" + longToken("a"),
			scopes:  "prom:prod",
			admins:  "gabriel,prom",
			wantErr: "INV_ADMIN_USERS",
		},
		{
			name:    "a credential named after the seeded admin",
			tokens:  "admin:" + longToken("a"),
			scopes:  "admin:prod",
			wantErr: "INV_ADMIN_USERNAME",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pristineEnv(t)
			t.Setenv("INV_ADMIN_USERS", tc.admins)
			t.Setenv("INV_AGENT_TOKENS", tc.tokens)
			t.Setenv("INV_AGENT_SCOPES", tc.scopes)
			t.Setenv("INV_AGENT_VOCAB", tc.vocab)

			_, err := Load()
			if err == nil {
				t.Fatal("Load succeeded; it should have refused to start")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, tc.wantErr)
			}
			if strings.Contains(err.Error(), longToken("a")) {
				t.Errorf("the error message contains a token: %q", err)
			}
		})
	}
}

// longToken builds a token that satisfies MinAgentTokenLength.
func longToken(seed string) string {
	return seed + strings.Repeat("x", MinAgentTokenLength)
}

// TestSecurityFlagsFailClosed.
//
// envBool used to swallow a parse error and return the fallback, and every
// boolean here decides a security posture whose fallback is the permissive one.
// "yes" and "no" are exactly the spellings somebody reaches for and exactly the
// ones strconv.ParseBool rejects, so INV_SECURE_COOKIES=yes produced insecure
// cookies with no indication, and INV_LDAP_STARTTLS=yes a plaintext bind
// carrying a real person's password.
func TestSecurityFlagsFailClosed(t *testing.T) {
	t.Run("an unparseable boolean refuses to start", func(t *testing.T) {
		pristineEnv(t)
		t.Setenv("INV_ADMIN_USERS", "gabriel")
		t.Setenv("INV_SECURE_COOKIES", "yes")

		_, err := Load()
		if err == nil {
			t.Fatal("INV_SECURE_COOKIES=yes was accepted; it silently yields INSECURE cookies, " +
				"which is the opposite of what the operator asked for")
		}
		if !strings.Contains(err.Error(), "INV_SECURE_COOKIES") {
			t.Errorf("the error does not name the offending variable: %v", err)
		}
	})

	t.Run("every bad boolean is reported at once", func(t *testing.T) {
		pristineEnv(t)
		t.Setenv("INV_ADMIN_USERS", "gabriel")
		t.Setenv("INV_SECURE_COOKIES", "yes")
		t.Setenv("INV_SEED", "on")

		_, err := Load()
		if err == nil {
			t.Fatal("two unparseable booleans were accepted")
		}
		for _, want := range []string{"INV_SECURE_COOKIES", "INV_SEED"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s is missing from the error; an operator with two typos should learn "+
					"about both on the first start, not one per restart", want)
			}
		}
	})
}

// TestLDAPRefusesAnUntrustworthyChannel.
//
// A simple bind sends an operator's password. It is the only place in this
// application where a human credential crosses the network, and the default
// configuration -- set a URL, set a bind DN, start -- used to be the
// unencrypted one. Both halves are refused: an unencrypted channel, and an
// encrypted one whose peer is never verified.
func TestLDAPRefusesAnUntrustworthyChannel(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		startTLS   string
		skipVerify string
		wantErr    bool
		wantMsg    string
	}{
		{name: "plain ldap:// with no StartTLS", url: "ldap://dir.example.com", wantErr: true},
		{name: "plain ldap:// with StartTLS", url: "ldap://dir.example.com", startTLS: "true"},
		{name: "ldaps:// needs no StartTLS", url: "ldaps://dir.example.com"},
		{name: "LDAPS:// is matched case-insensitively", url: "LDAPS://dir.example.com"},
		// Encryption without verification is not authentication of the peer:
		// anything that can answer the connection presents its own certificate
		// and collects an operator's password, while the login looks normal.
		// This was a startup warning first, which is a thing that scrolls past
		// once and then lives in a systemd unit forever.
		{name: "ldaps:// with verification disabled", url: "ldaps://dir.example.com",
			skipVerify: "true", wantErr: true, wantMsg: "collect operator passwords"},
		{name: "StartTLS with verification disabled", url: "ldap://dir.example.com",
			startTLS: "true", skipVerify: "true", wantErr: true, wantMsg: "collect operator passwords"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pristineEnv(t)
			t.Setenv("INV_ADMIN_USERS", "gabriel")
			t.Setenv("INV_AUTH_LDAP", "true")
			t.Setenv("INV_LDAP_URL", tc.url)
			t.Setenv("INV_LDAP_BIND_DN", "uid=%s,ou=users,dc=example,dc=com")
			if tc.startTLS != "" {
				t.Setenv("INV_LDAP_STARTTLS", tc.startTLS)
			}
			if tc.skipVerify != "" {
				t.Setenv("INV_LDAP_SKIP_VERIFY", tc.skipVerify)
			}

			_, err := Load()
			if tc.wantErr {
				if err == nil {
					t.Fatal("a plaintext LDAP bind was accepted; an operator's password would " +
						"cross the network in clear")
				}
				want := tc.wantMsg
				if want == "" {
					want = "clear"
				}
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the error does not say what is wrong (wanted %q): %v", want, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("an encrypted bind was refused: %v", err)
			}
		})
	}
}
