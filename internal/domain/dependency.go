package domain

import (
	"strings"
	"time"
)

// Dependency natures encode failure semantics (HANDOVER §3.4).
const (
	// NatureHard: the consumer cannot serve without the provider.
	NatureHard = "hard"
	// NatureSoft: the consumer degrades but keeps serving.
	NatureSoft = "soft"
	// NatureStartup: only needed at boot. The consumer is fine right now and
	// will not come back after a restart. This is the highest-value nature and
	// the one people forget — the outage lands weeks later when something
	// unrelated triggers a restart.
	NatureStartup = "startup"
	// NatureAsync: buffered or retried. Survives an outage shorter than
	// tolerance_seconds.
	NatureAsync = "async"
	// NatureOptional: no functional effect (metrics scrape, telemetry).
	NatureOptional = "optional"
)

// Natures is the Go side of the dependency.nature CHECK constraint.
var Natures = []string{NatureHard, NatureSoft, NatureStartup, NatureAsync, NatureOptional}

// Data classes carried over a dependency. chd/sad are PCI terms (cardholder
// data / sensitive authentication data); pii drives GDPR scope. Tracking them
// on the edge is what lets "which flows carry personal data across a
// segmentation boundary" be a query rather than an interview.
var DataClasses = []string{"chd", "sad", "pii", "credential", "telemetry", "config", "none"}

// Dependency is a directed edge from a consumer service to a provider endpoint
// or route. Exactly one of the two provider columns is set.
type Dependency struct {
	ID                 string   `db:"id"`
	ConsumerServiceID  string   `db:"consumer_service_id"`
	ConsumerInstanceID *string  `db:"consumer_instance_id"`
	ProviderEndpointID *string  `db:"provider_endpoint_id"`
	ProviderRouteID    *string  `db:"provider_route_id"`
	Nature             string   `db:"nature"`
	ToleranceSeconds   *int     `db:"tolerance_seconds"`
	FailureMode        string   `db:"failure_mode"`
	IdentityID         *string  `db:"identity_id"`
	AuthMethod         *string  `db:"auth_method"`
	FirewallRuleRef    *string  `db:"firewall_rule_ref"`
	Source             string   `db:"source"`
	Confidence         *float64 `db:"confidence"`
	FirstSeen          *string  `db:"first_seen"`
	LastSeen           *string  `db:"last_seen"`
	VerifiedBy         *string  `db:"verified_by"`
	VerifiedAt         *string  `db:"verified_at"`
	Lifecycle          string   `db:"lifecycle"`
	CreatedAt          string   `db:"created_at"`
	UpdatedAt          string   `db:"updated_at"`
}

// DependencySpec is everything needed to declare an edge.
//
// Like ServiceSpec, it exists because nature and the fields it requires must
// be validated together: tolerance_seconds is mandatory for an async edge and
// meaningless for the others.
type DependencySpec struct {
	ConsumerServiceID  string
	ConsumerInstanceID *string
	ProviderEndpointID *string
	ProviderRouteID    *string
	Nature             string
	ToleranceSeconds   *int
	FailureMode        string
	IdentityID         *string
	AuthMethod         *string
	FirewallRuleRef    *string
	// Source defaults to declared when empty. A reconciler sets it explicitly.
	Source string
}

// NewDependency validates and constructs an edge.
//
// failure_mode is mandatory and free text on purpose: forcing the author to
// write down what actually breaks is most of the value of recording the edge
// at all.
func NewDependency(id string, spec DependencySpec, now time.Time) (*Dependency, error) {
	source := spec.Source
	if source == "" {
		source = SourceDeclared
	}
	d := &Dependency{
		ID:                 id,
		ConsumerServiceID:  spec.ConsumerServiceID,
		ConsumerInstanceID: spec.ConsumerInstanceID,
		ProviderEndpointID: spec.ProviderEndpointID,
		ProviderRouteID:    spec.ProviderRouteID,
		Nature:             spec.Nature,
		ToleranceSeconds:   spec.ToleranceSeconds,
		FailureMode:        spec.FailureMode,
		IdentityID:         spec.IdentityID,
		AuthMethod:         spec.AuthMethod,
		FirewallRuleRef:    spec.FirewallRuleRef,
		Source:             source,
		Lifecycle:          LifecycleActive,
		CreatedAt:          FormatTime(now),
		UpdatedAt:          FormatTime(now),
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return d, nil
}

// Validate checks a dependency against its business rules.
func (d *Dependency) Validate() error {
	ve := &ValidationError{}
	checkRequired(ve, "consumer_service_id", d.ConsumerServiceID)
	checkEnum(ve, "nature", d.Nature, Natures)
	checkEnum(ve, "source", d.Source, DependencySources)
	d.FailureMode = checkRequired(ve, "failure_mode", d.FailureMode)

	hasEndpoint := d.ProviderEndpointID != nil && *d.ProviderEndpointID != ""
	hasRoute := d.ProviderRouteID != nil && *d.ProviderRouteID != ""
	switch {
	case hasEndpoint && hasRoute:
		ve.Add("provider", "set either a provider endpoint or a route, not both")
	case !hasEndpoint && !hasRoute:
		ve.Add("provider", "a provider endpoint or route is required")
	}
	// Normalize the unset side to NULL so the table CHECK is satisfied by an
	// empty-string form value.
	if !hasEndpoint {
		d.ProviderEndpointID = nil
	}
	if !hasRoute {
		d.ProviderRouteID = nil
	}

	if d.Nature == NatureAsync {
		if d.ToleranceSeconds == nil {
			ve.Add("tolerance_seconds", "is required for an async dependency")
		} else if *d.ToleranceSeconds < 0 {
			ve.Add("tolerance_seconds", "must not be negative")
		}
	}
	if d.Confidence != nil && (*d.Confidence < 0 || *d.Confidence > 1) {
		ve.Add("confidence", "must be between 0 and 1")
	}
	return ve.OrNil()
}

// IsDeclared reports whether this edge was entered by an operator. Declared
// data is authoritative and is never silently overwritten by a reconciler.
func (d *Dependency) IsDeclared() bool { return d.Source == SourceDeclared }

// Propagation is the effect of a provider's status on its consumer.
type Propagation struct {
	// Status is the status to merge into the consumer. StatusOK means the
	// consumer is unaffected by this edge.
	Status Status
	// WontRestart marks an unmet startup dependency: the consumer is serving
	// now but will not come back if it restarts.
	WontRestart bool
}

// Propagate applies the nature table from HANDOVER §6 phase 3.
//
// windowSeconds matters only for async edges — a 3-minute reboot and a
// 45-minute one genuinely differ for a queue consumer with a buffer.
func (d *Dependency) Propagate(provider Status, windowSeconds int) Propagation {
	if provider == StatusOK {
		return Propagation{Status: StatusOK}
	}
	switch d.Nature {
	case NatureHard:
		// The only nature that transmits an outage at full strength.
		if provider == StatusDown {
			return Propagation{Status: StatusDown}
		}
		return Propagation{Status: StatusDegraded}

	case NatureSoft:
		if provider == StatusDown {
			return Propagation{Status: StatusDegraded}
		}
		return Propagation{Status: StatusOK}

	case NatureAsync:
		// Buffered: the consumer only notices once the outage outlasts its
		// tolerance. A degraded provider is still answering, so nothing yet.
		if provider == StatusDown && d.ToleranceSeconds != nil && *d.ToleranceSeconds < windowSeconds {
			return Propagation{Status: StatusDegraded}
		}
		return Propagation{Status: StatusOK}

	case NatureStartup:
		// Deliberately no status change. Reporting this as an outage would be
		// wrong — the consumer is serving. Reporting nothing at all would hide
		// a landmine, so it goes on a separate list.
		if provider == StatusDown {
			return Propagation{Status: StatusOK, WontRestart: true}
		}
		return Propagation{Status: StatusOK}

	default: // NatureOptional
		return Propagation{Status: StatusOK}
	}
}

// NatureDescription renders the failure semantics for the UI, so an operator
// filling in the form does not have to remember the table.
func NatureDescription(nature string) string {
	switch nature {
	case NatureHard:
		return "Consumer goes down immediately"
	case NatureSoft:
		return "Consumer degrades but keeps serving"
	case NatureStartup:
		return "Fine now, but will not restart"
	case NatureAsync:
		return "Fine until the tolerance window elapses"
	case NatureOptional:
		return "No effect"
	default:
		return ""
	}
}

// Change log actions.
const (
	ActionCreate = "create"
	ActionUpdate = "update"
	ActionDelete = "delete"
	ActionRetire = "retire"
)

// ChangeActions is the Go side of the change_log.action CHECK constraint.
var ChangeActions = []string{ActionCreate, ActionUpdate, ActionDelete, ActionRetire}

// Actor kinds. "was this a person or a machine" is the thing a reader needs at
// a glance, and it is not personal data, so every view that renders `actor`
// renders this beside it.
const (
	// ActorKindUser is a signed-in operator.
	ActorKindUser = "user"
	// ActorKindAgent is a monitoring or discovery credential -- a machine, and
	// never an app_user.
	ActorKindAgent = "agent"
	// ActorKindSystem is this process itself: the seeder, a migration, the LDAP
	// upsert. Not an external writer.
	ActorKindSystem = "system"
)

// ActorKinds is the Go side of the change_log.actor_kind CHECK constraint.
var ActorKinds = []string{ActorKindUser, ActorKindAgent, ActorKindSystem}

// ChangeLog is the audit trail. Every mutation writes one of these in the same
// transaction as the mutation itself — a handler that mutates without logging
// is incomplete, not merely untidy.
//
// Diff holds field-level changes as JSON (see docs/DECISIONS.md Q3):
// {"field": {"old": ..., "new": ...}} for updates, a full snapshot under "new"
// for creates.
//
// Actor holds an opaque identifier (docs/DECISIONS.md, 2026-07-28 decisions):
// app_user.id for a person, never a username or email, so the audit trail
// carries no personal data and can be kept forever with no retention
// argument. ActorName is resolved at read time by a LEFT JOIN to app_user and
// is nil when it does not resolve — either because the actor was never a real
// account (system, seed, ldap) or because the account was scrubbed to satisfy
// an erasure request. DisplayActor is what a template should render.
type ChangeLog struct {
	ID         string  `db:"id"`
	EntityType string  `db:"entity_type"`
	EntityID   string  `db:"entity_id"`
	Action     string  `db:"action"`
	Actor      string  `db:"actor"`
	ActorKind  string  `db:"actor_kind"`
	ActorName  *string `db:"actor_name"`
	Diff       string  `db:"diff"`
	TicketRef  *string `db:"ticket_ref"`
	At         string  `db:"at"`
}

// DisplayActor returns a human-readable name for the actor, falling back to
// the raw stored value -- an opaque id, or a system/seed/ldap literal -- when
// it does not resolve to an app_user row.
func (c ChangeLog) DisplayActor() string {
	if c.ActorName != nil && *c.ActorName != "" {
		return *c.ActorName
	}
	return c.Actor
}

// Actor identifies who is making a change, for the audit trail.
//
// ID is what is written to change_log.actor: an app_user.id for a person, or
// a fixed non-personal literal ("system", "seed", "ldap") for a writer that
// is not a person at all. Name is a human-readable label kept for the few
// declared columns that name an actor directly today (dependency.verified_by)
// and is never written to change_log.
type Actor struct {
	ID   string
	Name string
	Kind string
}

// SystemActor is used by the seeder and migrations. "system" is not a
// person's name, so it needs no opaque id of its own.
var SystemActor = Actor{ID: "system", Name: "system", Kind: ActorKindSystem}

// UserActor builds an actor for a logged-in operator. Only the account's
// opaque id reaches the audit trail; the username never does.
func UserActor(user *AppUser) Actor {
	return Actor{ID: user.ID, Name: user.Username, Kind: ActorKindUser}
}

// AgentActorPrefix namespaces a monitoring or discovery credential inside
// change_log.actor.
//
// The column is free TEXT shared with app_user ids, so without a namespace a
// credential called after a person's account id would write entries
// indistinguishable from that person's. The prefix makes the two spaces
// disjoint by construction rather than by hoping the ids never collide;
// startup additionally refuses to run if a credential id matches an
// app_user.username (docs/AUDIT.md rule 5).
const AgentActorPrefix = "monitor:"

// AgentActor builds the actor for a monitoring credential.
//
// Call this; never write Actor{Kind: "agent", ...} by hand. A struct literal
// puts the namespace and the kind spelling in the caller's hands, and a typo in
// either surfaces as a CHECK constraint failure inside a webhook at 03:00 --
// the worst possible time and place to learn it. Here it is an error returned
// where the credential is loaded, at startup.
//
// credentialID is the id half of an INV_AGENT_TOKENS pair. Name carries the
// namespaced id too: an agent has no person's name to leak.
func AgentActor(credentialID string) (Actor, error) {
	ve := &ValidationError{}
	id := checkRequired(ve, "credential_id", credentialID)
	switch {
	case strings.HasPrefix(id, AgentActorPrefix):
		ve.Add("credential_id", "is already namespaced; pass the bare credential id, not %q", id)
	case strings.ContainsAny(id, ": \t\n"):
		ve.Add("credential_id", "must not contain a colon or whitespace")
	case id != strings.ToLower(id):
		ve.Add("credential_id", "must be lower case, like every other identifier read from the environment")
	}
	if err := ve.OrNil(); err != nil {
		return Actor{}, err
	}
	namespaced := AgentActorPrefix + id
	return Actor{ID: namespaced, Name: namespaced, Kind: ActorKindAgent}, nil
}

// IsAgent reports whether this actor is a machine credential.
func (a Actor) IsAgent() bool { return a.Kind == ActorKindAgent }

// CredentialID undoes AgentActor's namespacing, for a reporter column or a log
// line. ok is false for any actor that is not an agent.
func (a Actor) CredentialID() (string, bool) {
	if !a.IsAgent() || !strings.HasPrefix(a.ID, AgentActorPrefix) {
		return "", false
	}
	return strings.TrimPrefix(a.ID, AgentActorPrefix), true
}

// Validate rejects an actor that could not have come from one of the
// constructors above.
//
// This is what makes the struct-literal path *clearly wrong* rather than merely
// discouraged: a hand-built agent actor fails here, in Go, with a message
// naming the constructor -- instead of reaching the database and failing a
// CHECK with no indication of what to do about it.
func (a Actor) Validate() error {
	ve := &ValidationError{}
	checkRequired(ve, "actor", a.ID)
	checkEnum(ve, "actor_kind", a.Kind, ActorKinds)
	switch a.Kind {
	case ActorKindAgent:
		if !strings.HasPrefix(a.ID, AgentActorPrefix) {
			ve.Add("actor", "an agent actor must be built with domain.AgentActor: %q is missing the %q namespace",
				a.ID, AgentActorPrefix)
		}
	case ActorKindUser, ActorKindSystem:
		if strings.HasPrefix(a.ID, AgentActorPrefix) {
			ve.Add("actor", "a %s actor must not carry the %q namespace", a.Kind, AgentActorPrefix)
		}
	}
	return ve.OrNil()
}

// CheckProvenanceWrite enforces rule 7 for a fact arriving from outside this
// process -- a webhook, a discovery reconciler, a form post.
//
// No machine may assert that a fact was hand-declared. That laundering is how a
// fabricated workload inside an in_scope environment renders to an operator as
// hand-asserted fact and never reaches the conflict queue; a machine credential
// may set only discovered-subset values.
//
// It denies AGENT actors specifically rather than everything that is not a
// user, which is a deliberate reading of rule 7 against rule 10. The seeder,
// the LDAP upsert and migrations write declared state as SystemActor by design
// -- rule 10 names all three -- and they are this process, not an external
// writer holding a narrow token. Denying them would break the fixture without
// closing anything: laundering is a threat because a credential arrives from
// outside and asserts more authority than it was issued, and SystemActor is not
// reachable from outside at all. An agent is the only principal that both
// authenticates over the network and could benefit from the lie.
//
// This is enforced at the store entry points, not left to callers, because a
// guard every caller must remember to invoke is the shape rule 1 rejects.
func CheckProvenanceWrite(actor Actor, source string) error {
	if source != SourceDeclared || actor.Kind != ActorKindAgent {
		return nil
	}
	ve := &ValidationError{}
	ve.Add("source", "only a signed-in operator may assert source = %q; a %s actor may set only a discovered_* value",
		SourceDeclared, actor.Kind)
	return ve
}

// CheckAttestationWrite enforces the classification table's ruling on
// dependency.verified_by / .verified_at: they are "a *person's* attestation
// that an edge is legitimate. A machine credential may never write these --
// that is a rubber stamp on an undocumented chd edge and the firewall rule
// justified by it."
//
// Separate from CheckProvenanceWrite because it is a different claim. Rule 7 is
// about where a fact came from; this is about somebody putting their name to
// it. A machine may legitimately report a discovered edge and may never sign it
// off, so the two checks deny different sets and neither implies the other.
func CheckAttestationWrite(actor Actor) error {
	if actor.Kind == ActorKindUser {
		return nil
	}
	ve := &ValidationError{}
	ve.Add("verified_by", "only a signed-in operator may verify an edge; a %s actor may not attest to one", actor.Kind)
	return ve
}

// ConfidenceFor decides the confidence stored for a write.
//
// Rule 7: "confidence is likewise set by the store from the credential, never
// from the payload: self-attested confidence is not a control." A machine that
// grades its own certainty can claim 1.0 for a guess, and every downstream
// report that filters on confidence then treats the guess as fact. An
// operator's own figure is their judgement and is kept.
func ConfidenceFor(actor Actor, requested *float64) *float64 {
	if actor.Kind != ActorKindAgent {
		return requested
	}
	c := AgentConfidence
	return &c
}

// AgentConfidence is what a machine-reported fact is worth until a person looks
// at it. Not 1.0, and not writable by the reporter.
const AgentConfidence = 0.5

// AppUser is an operator account. Local users carry an argon2id hash; LDAP
// users carry none and are upserted on successful bind.
type AppUser struct {
	ID           string  `db:"id"`
	Username     string  `db:"username"`
	DisplayName  *string `db:"display_name"`
	Email        *string `db:"email"`
	Source       string  `db:"source"`
	PasswordHash *string `db:"password_hash"`
	IsActive     bool    `db:"is_active"`
	LastLoginAt  *string `db:"last_login_at"`
	CreatedAt    string  `db:"created_at"`
}

// User sources.
const (
	UserSourceLocal = "local"
	UserSourceLDAP  = "ldap"
)

// UserSources is the Go side of the app_user.source CHECK constraint.
var UserSources = []string{UserSourceLocal, UserSourceLDAP}

// NewAppUser validates and constructs an account.
func NewAppUser(id, username, source string, now time.Time) (*AppUser, error) {
	ve := &ValidationError{}
	username = strings.ToLower(checkRequired(ve, "username", username))
	checkEnum(ve, "source", source, UserSources)
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	return &AppUser{
		ID: id, Username: username, Source: source,
		IsActive: true, CreatedAt: FormatTime(now),
	}, nil
}

// Display returns the friendliest available name.
func (u *AppUser) Display() string {
	if u.DisplayName != nil && *u.DisplayName != "" {
		return *u.DisplayName
	}
	return u.Username
}
