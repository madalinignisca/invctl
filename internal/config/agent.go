// Monitoring credentials: the configuration half of docs/AUDIT.md rule 6.
//
// A monitoring credential is not an app_user. It has no row in the database, no
// password hash, no session and no place in INV_ADMIN_USERS -- the whole point
// of rule 6 is that it is a *different principal type*, because putting it in
// the user table would hand it every write route in routes.go, including
// POST /assets/{id}/retire and POST /dependencies/{id}/verify.
//
// Everything here is parsed and validated at startup. A malformed pair, a
// credential with no environment scope, a token shared between two credentials
// or an id that collides with an operator's account all refuse to start rather
// than surfacing as a puzzling 401 or, worse, as a silently wider scope.
//
// Nothing in this file logs a token, and AgentCredential deliberately cannot be
// printed with one: see String and LogValue.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
)

// MinAgentTokenLength is the shortest token that will start the process.
//
// 24 characters is a 128-bit random value in any sensible encoding. The check
// exists because the failure mode it prevents is silent: a short token works
// perfectly in a demo and is the entire authentication for a write path.
const MinAgentTokenLength = 24

// scopeSeparator separates environment codes inside one INV_AGENT_SCOPES entry.
// A comma already separates credentials, and an environment code may not
// contain either.
const scopeSeparator = "|"

// AgentCredential is one monitoring credential as configured.
//
// Token is the shared secret. It is compared with subtle.ConstantTimeCompare in
// internal/auth and is never written to the database, never put in an audit
// row, and never logged -- String and LogValue below make the last of those
// structural rather than a rule to remember, because the usual way a secret
// reaches a log is somebody printing the struct that holds it while debugging
// something else.
type AgentCredential struct {
	// ID is the credential id. It becomes the reporter on every reading it
	// writes and, namespaced as monitor:<id>, the actor on anything it changes.
	ID string
	// Token is the bearer secret. Never log this.
	Token string
	// Environments is the explicit scope: the environment codes this credential
	// may write health for. Never empty -- there is no wildcard.
	Environments []string
	// Vocabulary names the per-reporter state mapping (rule 13). Empty means
	// the canonical one; internal/auth resolves and validates the name.
	Vocabulary string
}

// String renders the credential without its token.
//
// fmt verbs reach for String before they reach for the struct's fields, so
// fmt.Sprintf("%v", cred) and fmt.Errorf("...%s", cred) are both safe by
// construction rather than by review.
func (c AgentCredential) String() string {
	return fmt.Sprintf("agent %s (environments=%s, vocabulary=%s)",
		c.ID, strings.Join(c.Environments, scopeSeparator), c.vocabularyName())
}

// LogValue does the same job for log/slog, which does not consult Stringer.
func (c AgentCredential) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("id", c.ID),
		slog.String("environments", strings.Join(c.Environments, scopeSeparator)),
		slog.String("vocabulary", c.vocabularyName()),
	)
}

func (c AgentCredential) vocabularyName() string {
	if c.Vocabulary == "" {
		return "default"
	}
	return c.Vocabulary
}

// loadAgentCredentials assembles the credentials from the three variables that
// describe them.
//
// Three variables rather than one because INV_AGENT_TOKENS' shape is fixed by
// rule 6 as `id:token`, and overloading it with scope and vocabulary would put
// a secret in the middle of a field an operator edits by hand.
func loadAgentCredentials() ([]AgentCredential, error) {
	tokens, err := splitPairs("INV_AGENT_TOKENS", os.Getenv("INV_AGENT_TOKENS"))
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		// No credentials configured means no machine-facing route is mounted at
		// all. An estate that is not reporting yet should not carry the surface.
		return nil, nil
	}

	scopes, err := splitPairs("INV_AGENT_SCOPES", os.Getenv("INV_AGENT_SCOPES"))
	if err != nil {
		return nil, err
	}
	vocab, err := splitPairs("INV_AGENT_VOCAB", os.Getenv("INV_AGENT_VOCAB"))
	if err != nil {
		return nil, err
	}

	creds := make([]AgentCredential, 0, len(tokens))
	seenID := make(map[string]bool, len(tokens))
	seenToken := make(map[string]string, len(tokens))
	for _, p := range tokens {
		if seenID[p.key] {
			return nil, fmt.Errorf("validating config: INV_AGENT_TOKENS names credential %q twice", p.key)
		}
		seenID[p.key] = true

		if len(p.value) < MinAgentTokenLength {
			// The length is named, the token is not.
			return nil, fmt.Errorf("validating config: the token for credential %q is %d characters; at least %d are required",
				p.key, len(p.value), MinAgentTokenLength)
		}
		if other, clash := seenToken[p.value]; clash {
			// Two credentials sharing a token means the reporter recorded
			// against a reading is whichever one the lookup happened to find --
			// attribution decided by map order.
			return nil, fmt.Errorf("validating config: credentials %q and %q share a token; each credential needs its own",
				other, p.key)
		}
		seenToken[p.value] = p.key

		environments, ok := lookupPair(scopes, p.key)
		if !ok {
			return nil, fmt.Errorf("validating config: credential %q has no entry in INV_AGENT_SCOPES; "+
				"every monitoring credential is scoped to an explicit set of environments and there is no wildcard", p.key)
		}
		codes := splitScope(environments)
		if len(codes) == 0 {
			return nil, fmt.Errorf("validating config: credential %q has an empty environment scope in INV_AGENT_SCOPES", p.key)
		}

		vocabulary, _ := lookupPair(vocab, p.key)
		creds = append(creds, AgentCredential{
			ID:           p.key,
			Token:        p.value,
			Environments: codes,
			Vocabulary:   strings.TrimSpace(vocabulary),
		})
	}

	// A scope or vocabulary naming a credential that does not exist is almost
	// always a typo in the id, and a typo in the id means the real credential
	// silently falls back -- to no scope (caught above) or to the default
	// vocabulary (not caught anywhere). Refuse instead.
	for _, name := range []struct {
		env   string
		pairs []pair
	}{{"INV_AGENT_SCOPES", scopes}, {"INV_AGENT_VOCAB", vocab}} {
		for _, p := range name.pairs {
			if !seenID[p.key] {
				return nil, fmt.Errorf("validating config: %s names credential %q, which is not in INV_AGENT_TOKENS",
					name.env, p.key)
			}
		}
	}

	sort.Slice(creds, func(i, j int) bool { return creds[i].ID < creds[j].ID })
	return creds, nil
}

// pair is one `key:value` entry.
type pair struct {
	key   string
	value string
}

// splitPairs parses a comma-separated list of `key:value` entries.
//
// It follows INV_ADMIN_USERS' shape -- comma separated, trimmed, blanks dropped
// -- with one deliberate difference: only the key is lower-cased. Lower-casing
// the value would mangle a token, and a mangled token fails as an
// authentication error at 03:00 with nothing to point at.
func splitPairs(envName, raw string) ([]pair, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]pair, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, found := strings.Cut(part, ":")
		if !found {
			return nil, fmt.Errorf("validating config: %s entries are id:value; %q has no colon", envName, entryLabel(envName, part))
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if key == "" {
			return nil, fmt.Errorf("validating config: %s has an entry with an empty id", envName)
		}
		if value == "" {
			return nil, fmt.Errorf("validating config: %s entry for %q has an empty value", envName, key)
		}
		if strings.ContainsAny(key, " \t\n") {
			return nil, fmt.Errorf("validating config: %s id %q must not contain whitespace", envName, key)
		}
		out = append(out, pair{key: key, value: value})
	}
	return out, nil
}

// entryLabel keeps a malformed INV_AGENT_TOKENS entry out of the error message,
// because an entry with no colon may well be a bare token somebody pasted
// without its id -- and an error message is the most-copied text there is.
func entryLabel(envName, entry string) string {
	if envName == "INV_AGENT_TOKENS" {
		return "an entry"
	}
	return entry
}

func lookupPair(pairs []pair, key string) (string, bool) {
	for _, p := range pairs {
		if p.key == key {
			return p.value, true
		}
	}
	return "", false
}

// splitScope parses one credential's environment list, normalising codes the
// way domain.NewEnvironment does so a scope written in capitals still matches.
func splitScope(raw string) []string {
	parts := strings.Split(raw, scopeSeparator)
	out := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// validateAgents enforces the rules that involve the rest of the configuration.
//
// The collision check against app_user.username needs the database and so lives
// in cmd/invctl; this is the half that can be answered from the environment
// alone, and it is the half that catches the most likely mistake -- someone
// "granting the collector write access" by adding it to INV_ADMIN_USERS.
func (c *Config) validateAgents() error {
	for _, cred := range c.AgentCredentials {
		if c.IsAdmin(cred.ID) {
			return fmt.Errorf("validating config: credential %q appears in INV_ADMIN_USERS; "+
				"a monitoring credential is not an operator account and INV_ADMIN_USERS grants every write route", cred.ID)
		}
		if strings.EqualFold(strings.TrimSpace(c.AdminUsername), cred.ID) {
			return fmt.Errorf("validating config: credential %q has the same name as INV_ADMIN_USERNAME; "+
				"an audit entry must say unambiguously whether a person or a machine wrote it", cred.ID)
		}
	}
	return nil
}
