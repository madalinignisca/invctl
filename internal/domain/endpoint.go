// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import "time"

// L4 protocols.
const (
	ProtoTCP  = "tcp"
	ProtoUDP  = "udp"
	ProtoSCTP = "sctp"
	ProtoUnix = "unix"
)

// L4Protos is the Go side of the endpoint.l4_proto CHECK constraint.
var L4Protos = []string{ProtoTCP, ProtoUDP, ProtoSCTP, ProtoUnix}

// Bind scopes describe how reachable a listening socket is. This is what makes
// "who can actually talk to this?" answerable without reading firewall rules.
const (
	BindLoopback  = "loopback"
	BindHost      = "host"
	BindVIP       = "vip"
	BindClusterIP = "cluster_ip"
	BindNodePort  = "node_port"
	BindIngress   = "ingress"
	BindUnix      = "unix"
)

// BindScopes is the Go side of the endpoint.bind_scope CHECK constraint.
var BindScopes = []string{
	BindLoopback, BindHost, BindVIP, BindClusterIP, BindNodePort, BindIngress, BindUnix,
}

// IsLocalBind reports whether a socket's traffic never leaves the host it is on.
//
// It lives here rather than in one consumer because two of them now depend on
// it agreeing with itself. The impact engine exempts these binds from network
// reasoning entirely -- a loopback listener cannot be partitioned from anything
// -- and the neighbourhood diagram must not draw a line implying traversal for
// the same socket. A second copy of this rule anywhere would eventually let the
// picture contradict the engine, which is the worst failure either can have.
func IsLocalBind(scope string) bool {
	return scope == BindLoopback || scope == BindUnix
}

// TLSModes is the Go side of the endpoint.tls_mode CHECK constraint.
var TLSModes = []string{"none", "tls", "mtls", "starttls"}

// Exposures is the Go side of the endpoint.exposure CHECK constraint.
// cross_env is the interesting value: it marks traffic that must traverse a
// segmentation boundary and therefore needs a transit path and a firewall rule.
var Exposures = []string{"internal", "environment", "cross_env", "external"}

// Endpoint is a listening socket belonging to a service.
type Endpoint struct {
	ID            string  `db:"id"`
	ServiceID     string  `db:"service_id"`
	Name          string  `db:"name"`
	L4Proto       string  `db:"l4_proto"`
	Port          *int    `db:"port"`
	UnixPath      *string `db:"unix_path"`
	BindScope     string  `db:"bind_scope"`
	IPAddressID   *string `db:"ip_address_id"`
	L7Proto       *string `db:"l7_proto"`
	TLSMode       string  `db:"tls_mode"`
	CertificateID *string `db:"certificate_id"`
	Exposure      string  `db:"exposure"`
	Lifecycle     string  `db:"lifecycle"`
	CreatedAt     *string `db:"created_at"`
	UpdatedAt     *string `db:"updated_at"`
	RowVersion    int     `db:"row_version"`
}

// EndpointLifecycles is the Go side of the endpoint.lifecycle CHECK.
//
// Deliberately narrower than ServiceLifecycles, for the reason placements are:
// a socket either exists or it does not. 'planned' and 'deprecated' describe
// the SERVICE, and offering them here would invite writing them where nothing
// reads them.
var EndpointLifecycles = []string{LifecycleActive, LifecycleRetired}

// Retired reports whether this socket has been withdrawn.
func (e *Endpoint) Retired() bool { return e.Lifecycle == LifecycleRetired }

// NewEndpoint validates and constructs a listening socket.
//
// The port/unix_path exclusivity mirrors the table CHECK: a unix socket has a
// path and no port, everything else has a port and no path.
func NewEndpoint(id, serviceID, name, proto string, port *int, bindScope string) (*Endpoint, error) {
	e := &Endpoint{
		ID: id, ServiceID: serviceID, Name: name, L4Proto: proto,
		Port: port, BindScope: bindScope, TLSMode: "none", Exposure: "internal",
		Lifecycle: LifecycleActive,
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return e, nil
}

// Validate checks an endpoint against its business rules.
func (e *Endpoint) Validate() error {
	ve := &ValidationError{}
	e.Name = checkRequired(ve, "name", e.Name)
	checkRequired(ve, "service_id", e.ServiceID)
	checkEnum(ve, "l4_proto", e.L4Proto, L4Protos)
	checkEnum(ve, "bind_scope", e.BindScope, BindScopes)
	checkEnum(ve, "tls_mode", e.TLSMode, TLSModes)
	checkEnum(ve, "exposure", e.Exposure, Exposures)
	// Defaulted rather than rejected: every endpoint built before this column
	// existed, and every literal in the seed, omits it.
	if e.Lifecycle == "" {
		e.Lifecycle = LifecycleActive
	}
	checkEnum(ve, "lifecycle", e.Lifecycle, EndpointLifecycles)

	if e.L4Proto == ProtoUnix {
		if e.UnixPath == nil || *e.UnixPath == "" {
			ve.Add("unix_path", "is required for a unix socket")
		}
		if e.Port != nil {
			ve.Add("port", "must be empty for a unix socket")
		}
	} else {
		if e.Port == nil {
			ve.Add("port", "is required for %s", e.L4Proto)
		} else if *e.Port < 1 || *e.Port > 65535 {
			ve.Add("port", "must be between 1 and 65535")
		}
		if e.UnixPath != nil && *e.UnixPath != "" {
			ve.Add("unix_path", "must be empty for %s", e.L4Proto)
		}
	}
	return ve.OrNil()
}

// Addr renders the endpoint for display.
func (e *Endpoint) Addr() string {
	if e.L4Proto == ProtoUnix && e.UnixPath != nil {
		return *e.UnixPath
	}
	if e.Port == nil {
		return e.L4Proto
	}
	return e.L4Proto + "/" + itoa(*e.Port)
}

// BackendPool is a set of endpoints behind a proxy.
type BackendPool struct {
	ID          string  `db:"id"`
	ServiceID   string  `db:"service_id"`
	Name        string  `db:"name"`
	LBAlgorithm *string `db:"lb_algorithm"`
}

// BackendMember places an endpoint into a pool.
type BackendMember struct {
	PoolID     string `db:"pool_id"`
	EndpointID string `db:"endpoint_id"`
	Weight     int    `db:"weight"`
	IsBackup   bool   `db:"is_backup"`
}

// MatchTypes is the Go side of the route.match_type CHECK constraint.
var MatchTypes = []string{"sni", "host_header", "path_prefix", "default"}

// TLSTerminations is the Go side of the route.tls_termination CHECK constraint.
var TLSTerminations = []string{"passthrough", "terminate", "reencrypt"}

// Route is an L7 matching rule mapping a frontend endpoint to a backend pool.
//
// Routes are nodes in the dependency graph, not passthroughs (HANDOVER §6): a
// dependency on a route resolves to its pool, and the pool's health derives
// from its members. That is what surfaces "the proxy is up but every backend
// sits on the node you are about to reboot".
type Route struct {
	ID                 string  `db:"id"`
	FrontendEndpointID string  `db:"frontend_endpoint_id"`
	MatchType          string  `db:"match_type"`
	MatchValue         *string `db:"match_value"`
	BackendPoolID      string  `db:"backend_pool_id"`
	TLSTermination     *string `db:"tls_termination"`
	Priority           int     `db:"priority"`
}

// NewRoute validates and constructs a routing rule.
func NewRoute(id, frontendEndpointID, matchType, backendPoolID string) (*Route, error) {
	ve := &ValidationError{}
	checkRequired(ve, "frontend_endpoint_id", frontendEndpointID)
	checkRequired(ve, "backend_pool_id", backendPoolID)
	checkEnum(ve, "match_type", matchType, MatchTypes)
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	return &Route{
		ID: id, FrontendEndpointID: frontendEndpointID,
		MatchType: matchType, BackendPoolID: backendPoolID, Priority: 100,
	}, nil
}

// Identity kinds.
const (
	IdentityServiceAccount = "service_account"
	IdentityMachineAccount = "machine_account"
	IdentityAPIToken       = "api_token"
	IdentityCertSubject    = "cert_subject"
	IdentityHuman          = "human"
)

// IdentityKinds is the Go side of the identity.kind CHECK constraint.
var IdentityKinds = []string{
	IdentityServiceAccount, IdentityMachineAccount, IdentityAPIToken,
	IdentityCertSubject, IdentityHuman,
}

// Identity is a principal used to authenticate a dependency.
//
// SecretRef holds a *path* (a Vault path or similar), never a secret value.
// If a code path would put an actual secret here, that is a bug to raise, not
// to work around.
type Identity struct {
	ID   string `db:"id"`
	Kind string `db:"kind"`
	Name string `db:"name"`
	// NOT NULL with an empty-string default since 00003: a NULL realm made
	// UNIQUE (realm, name) not fire at all, because NULL <> NULL, so two
	// realm-less identities with the same name were both accepted. Kept as a
	// pointer so an unset realm is still expressible in Go, and normalised on
	// the way to the database rather than at every call site.
	Realm        *string `db:"realm"`
	SecretRef    *string `db:"secret_ref"`
	RotationDays *int    `db:"rotation_days"`
	LastRotated  *string `db:"last_rotated"`
	TeamID       *string `db:"team_id"`
	Lifecycle    string  `db:"lifecycle"`
	// RowVersion is the optimistic-concurrency token, migration 00069. See
	// internal/domain/version.go. auditFields (internal/store/diff.go:80-86)
	// excludes it from every diff: "an audit entry reading row_version: 4 -> 5
	// tells a reader nothing they can use".
	RowVersion int `db:"row_version"`
}

// IdentitySpec is everything a person declares about a credential reference.
//
// A SPEC RATHER THAN A POSITIONAL SIGNATURE, and this repo has now recorded the
// reason twice -- CableBundleSpec: "a positional signature had to be replaced
// mid-branch when it could not accept a required field, so a constructor that
// cannot pass a required value cannot build a valid one." NewIdentity(id, kind,
// name) could accept none of realm, secret_ref, rotation_days or team_id, so the
// seeder assigned four fields AFTER construction and the validation the
// constructor performed was not the validation the row got.
//
// IT DELIBERATELY HAS NO LastRotated. last_rotated has exactly one writer,
// RecordIdentityRotation (docs/identity-surface-design.md, "One writer for
// last_rotated"): declaring a credential that already exists and recording when
// it was last rotated are two acts and two audit entries, both of which are
// true. A field here would be a fifth way to stamp it.
type IdentitySpec struct {
	Kind, Name   string
	Realm        *string
	SecretRef    *string
	RotationDays *int
	TeamID       *string
}

// NewIdentity validates and constructs a principal.
//
// NO `now` PARAMETER, unlike almost every other constructor in this package,
// because the table has no timestamp to stamp -- migration 00069 gave identity
// row_version ONLY, for the reasons 00066 gave link. Worth saying out loud, or
// the next person adds the parameter back out of habit and then has to invent a
// column for it to fill.
func NewIdentity(id string, spec IdentitySpec) (*Identity, error) {
	i := &Identity{
		ID: id, Kind: spec.Kind, Name: spec.Name,
		Realm: spec.Realm, SecretRef: spec.SecretRef,
		RotationDays: spec.RotationDays, TeamID: spec.TeamID,
		Lifecycle:  LifecycleActive,
		RowVersion: 1,
	}
	if err := i.Validate(); err != nil {
		return nil, err
	}
	return i, nil
}

// Validate checks an identity against its business rules and normalises what the
// constructor always normalised.
//
// SEPARATE FROM THE CONSTRUCTOR because the update path has to run the same
// rules. Environment.Validate is the shape and its doc comment names the defect
// this prevents: the checks lived inside NewEnvironment, so UpdateEnvironment
// wrote whatever it was handed and the table CHECK was the only thing standing
// between a form and a blank name.
func (i *Identity) Validate() error {
	ve := &ValidationError{}
	i.Name = checkRequired(ve, "name", i.Name)
	checkEnum(ve, "kind", i.Kind, IdentityKinds)
	// Matches identity_rotation_days_check (migration 00003): a policy of zero
	// days is not a policy, it is a row that is overdue the moment it is saved.
	checkPositive(ve, "rotation_days", i.RotationDays)
	return ve.OrNil()
}

// RotationState is what this credential's rotation policy currently says about
// it. FIVE STATES RATHER THAN A BOOLEAN, and the split that matters is the first
// two: `rotation_days IS NULL` means nobody asked for this to be rotated and
// there is nothing to be late for, while `rotation_days` set with no recorded
// rotation means THE ESTATE HAS A RULE FOR THIS CREDENTIAL AND NO EVIDENCE IT
// HAS EVER BEEN FOLLOWED. Those are opposite facts. The boolean this replaced
// answered `false` to both, which is how a credential that has never been
// rotated in four years rendered identically to one nobody ever intended to
// rotate -- and all three identities in the demo estate were in the second state.
type RotationState string

const (
	// RotationUnmanaged: no policy. A cert_subject or a human row is often
	// legitimately here, and it is NOT a finding -- flagging every one would
	// swamp the page and teach people to ignore it.
	RotationUnmanaged RotationState = "unmanaged"
	// RotationNeverRecorded: a policy, and nothing has ever recorded a
	// rotation against it. Either it has never been rotated since the day it
	// was created, or it has and nobody wrote it down. invctl cannot tell
	// which, and both are worth somebody's attention -- which is exactly why
	// the finding for it is a Gap and not a Fault.
	RotationNeverRecorded RotationState = "never_recorded"
	RotationWithinWindow  RotationState = "within_window"
	RotationOverdue       RotationState = "overdue"
	// RotationUnreadable: the stored value will not parse. It exists because
	// the alternative is the failure this repo keeps finding -- the boolean
	// this replaced returned `false` on a parse error, so an unreadable value
	// read as HEALTHY. A state that cannot be read must never render as a
	// state that is fine.
	RotationUnreadable RotationState = "unreadable"
)

// RotationStatus answers what the policy says about this credential now.
//
// THE FIRST TWO STATES ARE OPPOSITE FACTS AND MUST NEVER BE COLLAPSED, which is
// the whole reason this returns a state rather than a bool. `unmanaged` means
// there is NO RULE TO BREAK -- nobody asked for this credential to be rotated,
// and a cert_subject or a human row is often legitimately here. `never_recorded`
// means THE ESTATE HAS A RULE FOR THIS CREDENTIAL AND NO EVIDENCE IT HAS EVER
// BEEN FOLLOWED: either it has never been rotated since the day it was created,
// or it has and nobody wrote it down, and invctl cannot tell which. Both are
// worth somebody's attention; neither is "fine".
//
// The boolean this replaced answered `false` to both, which is how a credential
// that has never been rotated in four years rendered identically to one nobody
// ever intended to rotate -- and all three identities in the demo estate were in
// the second state, so the surface would have reported the whole estate as
// healthy. Collapsing them again is the defect this work package exists to kill.
func (i *Identity) RotationStatus(now time.Time) RotationState {
	if i.RotationDays == nil {
		return RotationUnmanaged
	}
	if i.LastRotated == nil {
		return RotationNeverRecorded
	}
	last, err := ParseDate(*i.LastRotated)
	if err != nil {
		return RotationUnreadable
	}
	due := last.AddDate(0, 0, *i.RotationDays)
	// Compare by CALENDAR DAY, not by instant. due is always midnight UTC
	// (ParseDate never produces a time-of-day), so a `now` taken from the
	// wall clock at any hour on the due day itself must still read as
	// within the window -- "exactly on the due day is still inside" is the
	// case that would otherwise flip to overdue by early afternoon.
	today := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	if today.After(due) {
		return RotationOverdue
	}
	return RotationWithinWindow
}

// RotationDueOn is the date the next rotation is due, or nil when the question
// has no answer: no policy, no record, or a stored value that will not parse.
// The last of those is the one worth naming -- a due date computed from an
// unreadable value is a lie with a date on it.
func (i *Identity) RotationDueOn() *string {
	if i.RotationDays == nil || i.LastRotated == nil {
		return nil
	}
	last, err := ParseDate(*i.LastRotated)
	if err != nil {
		return nil
	}
	due := FormatDate(last.AddDate(0, 0, *i.RotationDays))
	return &due
}

// itoa avoids pulling strconv into every call site for small positive ints.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
