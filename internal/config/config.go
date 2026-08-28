// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Package config parses and validates configuration from the environment.
//
// Everything is validated at startup rather than at first use: a service that
// starts happily and fails an hour later because a session key was malformed
// is much harder to diagnose than one that refuses to start.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the whole runtime configuration.
type Config struct {
	DBDriver string
	DBDSN    string
	Listen   string

	// SessionKey is 32 random bytes used to authenticate the session cookie.
	SessionKey     []byte
	SessionTimeout time.Duration

	// AdminUsers is the break-glass override, not the whole model. A named
	// user has full write access regardless of their app_user.role column
	// (docs/rbac-design.md §5, §8) -- it exists so an operator can recover
	// write access when nobody's role column allows it, and to bootstrap the
	// very first Administrator before anyone can grant a role. A deactivated
	// account named here still cannot write; the override restores a role,
	// not a disabled account.
	AdminUsers []string

	// Currency is the symbol every amount is rendered with, estate-wide.
	//
	// ONE CURRENCY, deliberately. Amounts are stored as minor units in an
	// INTEGER and summed; adding two currencies together needs exchange rates
	// with a valuation date, which is a subsystem rather than a column, and is
	// a stated non-goal. A per-row currency field would let mixed values in and
	// produce totals that are wrong without looking wrong.
	Currency string

	// AgentCredentials are the monitoring credentials (docs/AUDIT.md rule 6).
	// They are a different principal type entirely: not app_user rows, never in
	// AdminUsers, and never seen by authz.CanWrite. Empty means no
	// machine-facing route is mounted at all.
	AgentCredentials []AgentCredential

	// Readers are the read-only API credentials for WP-A2's inventory API.
	// Empty means no machine-facing read route is mounted at all.
	Readers []ReaderCredential

	AuthLocal bool
	AuthLDAP  bool

	LDAP LDAPConfig

	// SeedOnStart loads the demo estate when the database is empty. Intended
	// for the demo and for development, off by default.
	SeedOnStart bool
	// SeedObservations additionally stages demo telemetry, through the same
	// recorder the webhook uses rather than by writing asset_health. It exists
	// so a presentation has something in the reporters, override and drift
	// panels; an operator's first real run should show the honest empty state,
	// so it is off unless asked for.
	SeedObservations bool
	// SeedCompany adds the small-company layer to the fixture: a third rack, a
	// rented colo, a firewall per environment, three ISP handoffs and an
	// internal certificate per service. Off by default -- a fresh deployment
	// gets the honest small fixture rather than somebody else's company.
	SeedCompany bool
	// SeedE2EProjectOwner stages a real, loggable-in project owner account
	// (seed.StageE2EProjectOwner) for WP-G1 Task 17's browser suite --
	// tests/e2e's RBAC specs need a project owner they can actually sign in
	// as, and there is no HTTP route by which one could create that
	// assignment itself (internal/store/user_projects.go's AssignProject has
	// no handler in front of it yet). OFF BY DEFAULT AND MUST STAY THAT WAY
	// ON ANY SHARED OR PUBLIC DEPLOYMENT: the account's credentials are
	// fixed and published (see seed.E2EProjectOwnerUsername/Password), which
	// is fine for a throwaway local instance and a live write-capable
	// credential the moment WP-G1 Task 13 flips CanWrite(project owner) to
	// true. Nothing in the Makefile's `make dev`/`make demo` defaults, nor
	// docs/DEMO.md's deployment, sets this -- it exists purely for a local
	// `INV_SEED_E2E_PROJECT_OWNER=true make dev` ahead of `make e2e`.
	SeedE2EProjectOwner bool

	// SeedE2EProjectOwnerPassword is the password the fixture account above
	// is created with. There is NO DEFAULT, deliberately: an empty value
	// makes StageE2EProjectOwner refuse to seed rather than fall back to
	// something guessable.
	//
	// The password used to be a published constant in internal/seed, named
	// in that file, docs/E2E.md and the spec files. An auth review of
	// WP-G1 Task 17 found the real exposure was not the write access that
	// Task 13 will add but the READ access the account already has: an
	// authenticated session over the whole CMDB -- every asset, address,
	// circuit, topology edge, secret_ref PATH and the change log. On a
	// contract deployment the administrator password is strong and that one
	// was in git.
	//
	// Setting INV_SEED_E2E_PROJECT_OWNER alone can therefore no longer
	// produce a working account: an operator must also choose a password,
	// which is a thing nobody does by accident.
	SeedE2EProjectOwnerPassword string
	// DevAdminPassword seeds the initial admin account. Empty means a random
	// password is generated and logged once at startup.
	DevAdminPassword string
	AdminUsername    string

	LogLevel string
	// SecureCookies should be off only when serving the demo over plain HTTP
	// on localhost; a Secure cookie is never sent over http and login would
	// silently fail to persist.
	SecureCookies bool
}

// LDAPConfig covers the second authenticator.
type LDAPConfig struct {
	URL string
	// BindDNTemplate turns a username into a bind DN, e.g.
	// "uid=%s,ou=users,dc=example,dc=com". Simple bind only for the POC.
	BindDNTemplate string
	StartTLS       bool
	SkipVerify     bool
}

// Encrypted reports whether the LDAP bind runs over TLS, by either route:
// ldaps:// is TLS from the first byte, and StartTLS upgrades a plain ldap://
// connection before the bind is sent.
func (l LDAPConfig) Encrypted() bool {
	return l.StartTLS || strings.HasPrefix(strings.ToLower(strings.TrimSpace(l.URL)), "ldaps://")
}

// Load reads configuration from the environment and validates it.
func Load() (*Config, error) {
	// Collected rather than returned inline so an operator with two typos
	// learns about both on the first start rather than one per restart.
	var badBools []string
	cfg := &Config{
		DBDriver:                    envOr("INV_DB_DRIVER", "sqlite"),
		DBDSN:                       envOr("INV_DB_DSN", "file:invctl.db?_txlock=immediate"),
		Listen:                      envOr("INV_LISTEN", ":8080"),
		SessionTimeout:              envDuration("INV_SESSION_TIMEOUT", 12*time.Hour),
		AdminUsers:                  splitList(os.Getenv("INV_ADMIN_USERS")),
		Currency:                    envOr("INV_CURRENCY", "EUR"),
		AuthLocal:                   envBool("INV_AUTH_LOCAL", true, &badBools),
		AuthLDAP:                    envBool("INV_AUTH_LDAP", false, &badBools),
		SeedOnStart:                 envBool("INV_SEED", false, &badBools),
		DevAdminPassword:            os.Getenv("INV_ADMIN_PASSWORD"),
		AdminUsername:               envOr("INV_ADMIN_USERNAME", "admin"),
		LogLevel:                    envOr("INV_LOG_LEVEL", "info"),
		SecureCookies:               envBool("INV_SECURE_COOKIES", false, &badBools),
		SeedObservations:            envBool("INV_SEED_OBSERVATIONS", false, &badBools),
		SeedCompany:                 envBool("INV_SEED_COMPANY", false, &badBools),
		SeedE2EProjectOwner:         envBool("INV_SEED_E2E_PROJECT_OWNER", false, &badBools),
		SeedE2EProjectOwnerPassword: os.Getenv("INV_E2E_PROJECT_OWNER_PASSWORD"),
		LDAP: LDAPConfig{
			URL:            os.Getenv("INV_LDAP_URL"),
			BindDNTemplate: os.Getenv("INV_LDAP_BIND_DN"),
			StartTLS:       envBool("INV_LDAP_STARTTLS", false, &badBools),
			SkipVerify:     envBool("INV_LDAP_SKIP_VERIFY", false, &badBools),
		},
	}

	if len(badBools) > 0 {
		return nil, fmt.Errorf("validating config: %s is not a boolean; use true/false, 1/0 or t/f. "+
			"Refusing to start rather than falling back to a default, because every flag here "+
			"decides a security posture and the fallback is the permissive one",
			strings.Join(badBools, ", "))
	}

	key, err := sessionKey()
	if err != nil {
		return nil, err
	}
	cfg.SessionKey = key

	agents, err := loadAgentCredentials()
	if err != nil {
		return nil, err
	}
	cfg.AgentCredentials = agents

	readers, err := loadReaderCredentials()
	if err != nil {
		return nil, err
	}
	cfg.Readers = readers

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	switch c.DBDriver {
	case "sqlite", "postgres":
	default:
		return fmt.Errorf("validating config: INV_DB_DRIVER must be sqlite or postgres, got %q", c.DBDriver)
	}
	if c.DBDSN == "" {
		return errors.New("validating config: INV_DB_DSN is required")
	}
	if !c.AuthLocal && !c.AuthLDAP {
		return errors.New("validating config: at least one of INV_AUTH_LOCAL or INV_AUTH_LDAP must be enabled")
	}
	if c.AuthLDAP {
		if c.LDAP.URL == "" {
			return errors.New("validating config: INV_LDAP_URL is required when INV_AUTH_LDAP is enabled")
		}
		if !strings.Contains(c.LDAP.BindDNTemplate, "%s") {
			return errors.New("validating config: INV_LDAP_BIND_DN must contain %%s for the username")
		}
		// A simple bind sends a real person's password. This is the only place
		// in the application where a human credential crosses the network, and
		// it must not do so in clear.
		//
		// ldaps:// is TLS from the first byte, so StartTLS is redundant there;
		// ldap:// without StartTLS is a plaintext bind. Nothing warned about
		// that, and the default is StartTLS=false, so the obvious
		// configuration -- set INV_LDAP_URL, set INV_LDAP_BIND_DN, start --
		// was the unencrypted one.
		if !c.LDAP.Encrypted() {
			return fmt.Errorf("validating config: INV_LDAP_URL is %q and INV_LDAP_STARTTLS is off, "+
				"so the bind would send an operator's password in clear. Use an ldaps:// URL or "+
				"set INV_LDAP_STARTTLS=true", c.LDAP.URL)
		}
		// Encryption without verification is not authentication of the peer.
		// Anything that can answer the connection -- a DNS answer somebody
		// controls, a host on the path -- presents its own certificate, is
		// accepted, and collects an operator's password on every sign-in. The
		// operator sees a normal login.
		//
		// This was previously a loud startup warning on the grounds that a lab
		// directory with a self-signed certificate is a legitimate thing to
		// develop against. It is, and it is also how the setting reaches
		// production: a warning is a thing that scrolls past once and then
		// lives in a systemd unit forever. A lab that needs this can add its
		// own CA to the host trust store, which is a real step somebody takes
		// deliberately and does not silently follow the config into a
		// deployment carrying real credentials.
		if c.LDAP.SkipVerify {
			return fmt.Errorf("validating config: INV_LDAP_SKIP_VERIFY is set, so any host able to "+
				"answer %q could present its own certificate and collect operator passwords. "+
				"Add the directory's CA to the host trust store instead", c.LDAP.URL)
		}
	}
	if err := c.validateAgents(); err != nil {
		return err
	}
	if err := c.validateCredentialSeparation(); err != nil {
		return err
	}
	if len(c.AdminUsers) == 0 {
		// Not fatal: a read-only deployment is legitimate. But an operator
		// who expected to be able to write should find out immediately.
		return nil
	}
	return nil
}

// validateCredentialSeparation refuses one secret that carries two
// capabilities.
//
// INV_AGENT_TOKENS and INV_API_TOKENS are two registries because they are two
// principal types: an agent writes observed state, a reader reads the whole
// scoped inventory, and neither is meant to be able to do the other's job.
// Each list already refuses a token used twice WITHIN it, for the narrower
// version of this reason -- but a token pasted into both started cleanly and
// authenticated on both surfaces, which is exactly what the second registry
// exists to prevent. This is the only place both lists are in scope at once,
// so it is the only place the check can live.
//
// Neither token is named, only the two credential ids. The error is a startup
// failure for the same reason every other one in this file is: a deployment
// whose authorization posture differs from the one somebody wrote down.
func (c *Config) validateCredentialSeparation() error {
	if len(c.AgentCredentials) == 0 || len(c.Readers) == 0 {
		return nil
	}
	byToken := make(map[string]string, len(c.AgentCredentials))
	for _, a := range c.AgentCredentials {
		byToken[a.Token] = a.ID
	}
	// Readers is sorted by id (loadReaderCredentials), so two clashes report
	// the same one on every run.
	for _, r := range c.Readers {
		if agentID, clash := byToken[r.Token]; clash {
			return fmt.Errorf(
				"validating config: monitoring credential %q (INV_AGENT_TOKENS) and read credential %q "+
					"(INV_API_TOKENS) share a token; they are separate principal types and one secret "+
					"must not carry both capabilities", agentID, r.ID)
		}
	}
	return nil
}

// IsAdmin implements the POC authorization model. It is deliberately trivial,
// but it is a function so that LDAP group membership can land here later
// without touching a single handler.
func (c *Config) IsAdmin(username string) bool {
	username = strings.ToLower(strings.TrimSpace(username))
	for _, admin := range c.AdminUsers {
		if admin == username {
			return true
		}
	}
	return false
}

// sessionKey reads INV_SESSION_KEY, or generates one.
//
// A generated key is fine for a demo -- it only means sessions do not survive
// a restart -- but it must not happen silently in production, so the caller
// logs a warning when Generated is true.
func sessionKey() ([]byte, error) {
	raw := os.Getenv("INV_SESSION_KEY")
	if raw == "" {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("generating session key: %w", err)
		}
		return key, nil
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("decoding INV_SESSION_KEY: expected base64: %w", err)
	}
	if len(key) < 32 {
		return nil, fmt.Errorf("decoding INV_SESSION_KEY: need at least 32 bytes, got %d", len(key))
	}
	return key, nil
}

// SessionKeyGenerated reports whether the key came from the environment.
func SessionKeyGenerated() bool { return os.Getenv("INV_SESSION_KEY") == "" }

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envBool parses a boolean, recording anything it could not parse.
//
// It used to swallow the error and return the fallback, which fails OPEN for
// every security-relevant flag here: INV_SECURE_COOKIES=yes is not a value
// strconv.ParseBool accepts, so an operator who wrote it got insecure cookies
// and no indication; INV_LDAP_STARTTLS=yes silently gave a plaintext bind
// carrying a real person's password. "yes"/"no"/"on"/"off" are exactly the
// spellings somebody reaches for, and the wrong ones.
//
// Refusing to start is the right response. A deployment running with an
// authorization or transport posture that differs from the one somebody wrote
// down is the state this whole file exists to prevent.
func envBool(key string, fallback bool, bad *[]string) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		*bad = append(*bad, fmt.Sprintf("%s=%q", key, v))
		return fallback
	}
	return parsed
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}

func splitList(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
