// The monitoring credential: a principal type that is not an operator account.
//
// docs/AUDIT.md rule 6, first sentence: "A monitoring credential is not an
// app_user (whose `source` CHECK has no room for one anyway), never appears in
// INV_ADMIN_USERS, and never reaches authz.CanWrite -- adding it there is the
// path of least resistance and it grants every write route in routes.go,
// including POST /assets/{id}/retire and POST /dependencies/{id}/verify."
//
// That separation is why nothing in this file returns a *domain.AppUser and why
// Authorizer.CanWrite -- whose parameter is *domain.AppUser -- cannot be handed
// an Agent even by mistake. It does not compile, which is the only kind of
// boundary worth having.

package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"sort"
	"strings"

	"github.com/gabriel/invctl/internal/config"
	"github.com/gabriel/invctl/internal/domain"
)

// Agent is an authenticated monitoring credential.
//
// It carries no token: the registry keeps only a digest, and once a request is
// authenticated the secret has done its job. Handing a handler something that
// still holds the token is how a token ends up in a debug log.
type Agent struct {
	// ID is the credential id, which becomes asset_health.reporter.
	ID string
	// Environments is the explicit scope this credential may write for.
	Environments domain.EnvironmentScope
	// Vocabulary maps this reporter's words onto HealthState (rule 13).
	Vocabulary domain.HealthVocabulary

	// actor is built once, at startup, by domain.AgentActor. Rule 5: never a
	// struct literal, "or a typo becomes a CHECK failure inside a webhook at
	// 03:00". Built here, a typo is a startup failure with the id in the message.
	actor domain.Actor
}

// Actor is the audit-trail identity for this credential: monitor:<id>, kind
// agent. Server-derived from the authenticated credential, never from a request
// body or a header (rule 5).
func (a *Agent) Actor() domain.Actor { return a.actor }

// String renders an agent for a log line. There is no token to leak, and the
// method exists so that the reflex to print the struct produces something
// deliberate.
func (a *Agent) String() string {
	return fmt.Sprintf("agent %s (environments=%s, vocabulary=%s)",
		a.ID, a.Environments, a.Vocabulary.Name())
}

// agentEntry pairs a credential with the digest of its token.
type agentEntry struct {
	agent  Agent
	digest [sha256.Size]byte
}

// AgentRegistry holds the configured monitoring credentials and answers "is
// this bearer token one of them, and which".
//
// A nil or empty registry authenticates nobody, and the router does not mount
// the machine-facing route at all in that case: an estate that is not reporting
// yet should not be carrying the surface.
type AgentRegistry struct {
	entries []agentEntry
}

// NewAgentRegistry validates the configured credentials and builds the lookup.
//
// Every failure here is a startup failure. The alternative -- skipping a
// credential that will not build -- means a collector that authenticates fine
// today stops authenticating after a config edit, with nothing in the logs
// naming the edit.
func NewAgentRegistry(creds []config.AgentCredential) (*AgentRegistry, error) {
	r := &AgentRegistry{entries: make([]agentEntry, 0, len(creds))}
	for _, c := range creds {
		actor, err := domain.AgentActor(c.ID)
		if err != nil {
			return nil, fmt.Errorf("building monitoring credential %q: %w", c.ID, err)
		}
		scope, err := domain.NewEnvironmentScope(c.Environments)
		if err != nil {
			return nil, fmt.Errorf("scoping monitoring credential %q: %w", c.ID, err)
		}
		vocabulary, err := domain.LookupVocabulary(c.Vocabulary)
		if err != nil {
			return nil, fmt.Errorf("configuring monitoring credential %q: %w", c.ID, err)
		}
		r.entries = append(r.entries, agentEntry{
			agent: Agent{
				ID:           c.ID,
				Environments: scope,
				Vocabulary:   vocabulary,
				actor:        actor,
			},
			digest: sha256.Sum256([]byte(c.Token)),
		})
	}
	sort.Slice(r.entries, func(i, j int) bool { return r.entries[i].agent.ID < r.entries[j].agent.ID })
	return r, nil
}

// Enabled reports whether any credential is configured.
func (r *AgentRegistry) Enabled() bool { return r != nil && len(r.entries) > 0 }

// IDs lists the configured credential ids, sorted. Used by the startup check
// that no credential id collides with an app_user.username (rule 5).
func (r *AgentRegistry) IDs() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e.agent.ID)
	}
	return out
}

// Authenticate resolves a bearer token to the credential that owns it.
//
// Two properties are deliberate and both are security-relevant:
//
// The wire carries the token alone, never `id:token`. Identity is *derived from
// the secret* rather than claimed alongside it, so there is no field in which a
// caller can name a credential it does not hold -- "credential A writing as B"
// is not a check that can be forgotten, it is a request that cannot be
// expressed. Two credentials sharing a token would make this ambiguous, so
// config refuses to start on a duplicate.
//
// The comparison is over SHA-256 digests rather than the raw tokens.
// subtle.ConstantTimeCompare is constant-time only for equal-length inputs and
// returns immediately when the lengths differ, which leaks the length of a
// configured token; hashing first makes every comparison the same fixed width.
// The loop does not break on a match either, so the time taken does not reveal
// which credential matched or how many are configured before it.
func (r *AgentRegistry) Authenticate(token string) (*Agent, bool) {
	if !r.Enabled() || token == "" {
		return nil, false
	}
	sum := sha256.Sum256([]byte(token))
	matched := -1
	for i := range r.entries {
		if subtle.ConstantTimeCompare(sum[:], r.entries[i].digest[:]) == 1 {
			matched = i
		}
	}
	if matched < 0 {
		return nil, false
	}
	// A copy: a handler must not be able to widen the scope of a live
	// credential by appending to the slice it was handed.
	agent := r.entries[matched].agent
	agent.Environments = domain.EnvironmentScope(agent.Environments.Codes())
	return &agent, true
}

// BearerToken extracts the token from an Authorization header value.
//
// The scheme match is case-insensitive because RFC 7235 says it is, and getting
// that wrong produces a 401 that no amount of staring at the token explains.
func BearerToken(header string) (string, bool) {
	const prefix = "bearer "
	header = strings.TrimSpace(header)
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}
