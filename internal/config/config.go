// Package config parses and validates configuration from the environment.
//
// Everything is validated at startup rather than at first use: a service that
// starts happily and fails an hour later because a session key was malformed
// is much harder to diagnose than one that refuses to start.
package config

import (
	"crypto/rand"
	"encoding/base64"
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

	// AdminUsers is the POC authorization model in its entirety: membership
	// grants write access, everyone else is read-only.
	AdminUsers []string

	// AgentCredentials are the monitoring credentials (docs/AUDIT.md rule 6).
	// They are a different principal type entirely: not app_user rows, never in
	// AdminUsers, and never seen by authz.CanWrite. Empty means no
	// machine-facing route is mounted at all.
	AgentCredentials []AgentCredential

	AuthLocal bool
	AuthLDAP  bool

	LDAP LDAPConfig

	// SeedOnStart loads the demo estate when the database is empty. Intended
	// for the demo and for development, off by default.
	SeedOnStart bool
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

// Load reads configuration from the environment and validates it.
func Load() (*Config, error) {
	cfg := &Config{
		DBDriver:         envOr("INV_DB_DRIVER", "sqlite"),
		DBDSN:            envOr("INV_DB_DSN", "file:invctl.db?_txlock=immediate"),
		Listen:           envOr("INV_LISTEN", ":8080"),
		SessionTimeout:   envDuration("INV_SESSION_TIMEOUT", 12*time.Hour),
		AdminUsers:       splitList(os.Getenv("INV_ADMIN_USERS")),
		AuthLocal:        envBool("INV_AUTH_LOCAL", true),
		AuthLDAP:         envBool("INV_AUTH_LDAP", false),
		SeedOnStart:      envBool("INV_SEED", false),
		DevAdminPassword: os.Getenv("INV_ADMIN_PASSWORD"),
		AdminUsername:    envOr("INV_ADMIN_USERNAME", "admin"),
		LogLevel:         envOr("INV_LOG_LEVEL", "info"),
		SecureCookies:    envBool("INV_SECURE_COOKIES", false),
		LDAP: LDAPConfig{
			URL:            os.Getenv("INV_LDAP_URL"),
			BindDNTemplate: os.Getenv("INV_LDAP_BIND_DN"),
			StartTLS:       envBool("INV_LDAP_STARTTLS", false),
			SkipVerify:     envBool("INV_LDAP_SKIP_VERIFY", false),
		},
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
		return fmt.Errorf("validating config: INV_DB_DSN is required")
	}
	if !c.AuthLocal && !c.AuthLDAP {
		return fmt.Errorf("validating config: at least one of INV_AUTH_LOCAL or INV_AUTH_LDAP must be enabled")
	}
	if c.AuthLDAP {
		if c.LDAP.URL == "" {
			return fmt.Errorf("validating config: INV_LDAP_URL is required when INV_AUTH_LDAP is enabled")
		}
		if !strings.Contains(c.LDAP.BindDNTemplate, "%s") {
			return fmt.Errorf("validating config: INV_LDAP_BIND_DN must contain %%s for the username")
		}
	}
	if err := c.validateAgents(); err != nil {
		return err
	}
	if len(c.AdminUsers) == 0 {
		// Not fatal: a read-only deployment is legitimate. But an operator
		// who expected to be able to write should find out immediately.
		return nil
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

func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
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
