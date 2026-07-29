// Reachability entities (docs/reachability-design.md, M2).
//
// Zero external dependencies, same as every other file in this package. These
// are the Go side of the six tables in migration 00007_reachability.sql: a
// second graph, deliberately separate from asset_closure, whose vertices are
// declared forwarder groups and whose edges are directed uplinks. Hosts attach
// to a group; they are never vertices themselves, so an end host can never
// become a transit node.
//
// asset_health is NOT part of this file. It was dropped from 00007 after
// docs/AUDIT.md's review: observed health needs a composite
// (entity_type, entity_id, reporter) key, state_since, and a companion
// observed_transition table for the transition ledger -- a simple asset_id
// PRIMARY KEY would let two monitors watching one asset clobber each other's
// reports. M6 builds the correct shape as its own migration.
//
// The reachability *algorithm* -- group status, union-find, pairwise reach --
// is internal/impact/reach.go and lands in M3. This file only has entities and
// validating constructors; nothing here is read by the impact engine yet.

package domain

import (
	"strings"
	"time"
)

// NetGroup kinds. Descriptive only -- the algorithm reads availability and
// failover_mode, not kind.
const (
	NetGroupStandalone = "standalone"
	NetGroupHAPair     = "ha_pair"
	NetGroupMCLAG      = "mclag"
	NetGroupStack      = "stack"
	NetGroupVRRP       = "vrrp"
	NetGroupCluster    = "cluster"
)

// NetGroupKinds is the Go side of the net_group.kind CHECK constraint.
var NetGroupKinds = []string{
	NetGroupStandalone, NetGroupHAPair, NetGroupMCLAG, NetGroupStack, NetGroupVRRP, NetGroupCluster,
}

// NetGroup roles. Load-bearing: derivation orients an undirected cable between
// two forwarders by rank (edge > core > distribution > access). Direction is
// the one thing a cable genuinely cannot tell you, so it is declared, never
// guessed.
const (
	NetRoleEdge         = "edge"
	NetRoleCore         = "core"
	NetRoleDistribution = "distribution"
	NetRoleAccess       = "access"
)

// NetGroupRoles is the Go side of the net_group.role CHECK constraint, ordered
// by derivation rank (highest first) so callers can compare index positions
// rather than re-deriving the rank table themselves.
var NetGroupRoles = []string{NetRoleEdge, NetRoleCore, NetRoleDistribution, NetRoleAccess}

// NetRoleRank returns a role's derivation rank: lower is "more upstream". An
// unrecognised role sorts last, so a bad value cannot silently win an
// orientation decision.
func NetRoleRank(role string) int {
	for i, r := range NetGroupRoles {
		if r == role {
			return i
		}
	}
	return len(NetGroupRoles)
}

// NetGroupAvailabilities is the Go side of the net_group.availability CHECK
// constraint. Deliberately excludes 'quorum' and 'sharded' from
// domain.Availabilities: a two-member "quorum" needs two survivors and would
// report DOWN when one is lost -- the exact inverse of MC-LAG -- and 'sharded'
// is meaningless for a forwarder. Reuse the evaluator, restrict the vocabulary.
var NetGroupAvailabilities = []string{AvailStandalone, AvailActiveActive, AvailActivePassive}

// NetGroupMemberRoles is the Go side of net_group_member.role -- the same
// vocabulary as service_instance.role, because the same evaluator reads it.
var NetGroupMemberRoles = []string{RolePrimary, RoleStandby, "member"}

// Planes is the Go side of the plane CHECK constraint shared by net_uplink,
// net_attachment and net_anchor. 'mgmt' is what stops a management-switch
// failure from reporting services down: derivation sets it from
// interface.is_mgmt on the host side of a cable.
var Planes = []string{"data", "mgmt", "storage"}

const (
	PlaneData    = "data"
	PlaneMgmt    = "mgmt"
	PlaneStorage = "storage"
)

// NetworkSources is the Go side of the source CHECK on every net_* table.
//
// 'derived_link' marks a row proposed from the cable plant rather than declared
// by a person, and 'discovered_lldp' is reserved for a post-POC reconciler. Only
// a user actor may write 'declared' -- laundering provenance is how a proposed
// row becomes an authoritative one (docs/AUDIT.md).
var NetworkSources = []string{"declared", "derived_link", "discovered_lldp"}

// NetGroupSpec is everything needed to declare a forwarder group.
//
// A spec struct rather than positional arguments for the same reason
// ServiceSpec is one: availability and the fields it requires have to be
// validated together, and a constructor that accepts a half-built group and
// validates later is how invalid rows reach the database.
type NetGroupSpec struct {
	Code          string
	Name          string
	Kind          string
	Role          string
	Availability  string
	MinHealthy    *int
	FailoverMode  *string
	EnvironmentID *string
}

// AnchorScopes is the Go side of net_anchor.scope. It reuses
// domain.Exposures's vocabulary minus 'internal' (which needs no path): an
// endpoint's declared exposure names the anchor it must reach, so there is no
// parallel taxonomy.
var AnchorScopes = []string{"environment", "cross_env", "external"}

// NetGroup is a vertex in the forwarder graph: a lone switch is a group of one,
// an MC-LAG pair or an active/passive firewall pair is a group of two.
type NetGroup struct {
	ID            string   `db:"id"`
	Code          string   `db:"code"`
	Name          string   `db:"name"`
	Kind          string   `db:"kind"`
	Role          string   `db:"role"`
	Availability  string   `db:"availability"`
	MinHealthy    *int     `db:"min_healthy"`
	FailoverMode  *string  `db:"failover_mode"`
	EnvironmentID *string  `db:"environment_id"`
	Lifecycle     string   `db:"lifecycle"`
	Source        string   `db:"source"`
	Confidence    *float64 `db:"confidence"`
	FirstSeen     *string  `db:"first_seen"`
	LastSeen      *string  `db:"last_seen"`
	VerifiedBy    *string  `db:"verified_by"`
	VerifiedAt    *string  `db:"verified_at"`
	Attrs         string   `db:"attrs"`
	CreatedAt     string   `db:"created_at"`
	UpdatedAt     string   `db:"updated_at"`
}

// NewNetGroup validates and constructs a forwarder group.
//
// Mirrors Service.Validate's availability/min_healthy/failover_mode pairing on
// purpose: an active/passive firewall pair and an active/passive database pair
// fail identically, and the same rule should be enforced the same way.
func NewNetGroup(id string, spec NetGroupSpec, now time.Time) (*NetGroup, error) {
	source := SourceDeclared
	g := &NetGroup{
		ID: id, Code: strings.ToLower(strings.TrimSpace(spec.Code)), Name: spec.Name,
		Kind: spec.Kind, Role: spec.Role, Availability: spec.Availability,
		MinHealthy: spec.MinHealthy, FailoverMode: spec.FailoverMode,
		EnvironmentID: spec.EnvironmentID,
		Lifecycle:     LifecycleActive, Source: source, Attrs: "{}",
		CreatedAt: FormatTime(now), UpdatedAt: FormatTime(now),
	}
	if err := g.Validate(); err != nil {
		return nil, err
	}
	return g, nil
}

// Validate checks a net group against its business rules. The DB CHECK is the
// second line of defence, not the first.
func (g *NetGroup) Validate() error {
	ve := &ValidationError{}
	g.Code = strings.ToLower(checkRequired(ve, "code", g.Code))
	g.Name = checkRequired(ve, "name", g.Name)
	checkEnum(ve, "kind", g.Kind, NetGroupKinds)
	checkEnum(ve, "role", g.Role, NetGroupRoles)
	checkEnum(ve, "availability", g.Availability, NetGroupAvailabilities)
	checkEnum(ve, "lifecycle", g.Lifecycle, ServiceLifecycles)
	checkEnum(ve, "source", g.Source, NetworkSources)
	if g.FailoverMode != nil && *g.FailoverMode != "" {
		checkEnum(ve, "failover_mode", *g.FailoverMode, FailoverModes)
	}
	if g.Availability == AvailActiveActive {
		if g.MinHealthy == nil {
			ve.Add("min_healthy", "is required for active_active")
		} else if *g.MinHealthy < 1 {
			ve.Add("min_healthy", "must be at least 1")
		}
	}
	if g.Availability == AvailActivePassive && (g.FailoverMode == nil || *g.FailoverMode == "") {
		ve.Add("failover_mode", "is required for active_passive")
	}
	if g.Confidence != nil && (*g.Confidence < 0 || *g.Confidence > 1) {
		ve.Add("confidence", "must be between 0 and 1")
	}
	if strings.TrimSpace(g.Attrs) == "" {
		g.Attrs = "{}"
	}
	return ve.OrNil()
}

// IsRetired reports whether the group has been soft-deleted.
func (g *NetGroup) IsRetired() bool { return g.Lifecycle == LifecycleRetired }

// NetGroupMember places an asset into a forwarder group. A device may belong
// to at most one active group -- see idx_net_group_member_one -- because a
// device in two forwarding groups is a modelling error, not a topology.
type NetGroupMember struct {
	GroupID   string `db:"group_id"`
	AssetID   string `db:"asset_id"`
	Role      string `db:"role"`
	Lifecycle string `db:"lifecycle"`
	CreatedAt string `db:"created_at"`
	UpdatedAt string `db:"updated_at"`
}

// NewNetGroupMember validates and constructs a membership row.
func NewNetGroupMember(groupID, assetID, role string, now time.Time) (*NetGroupMember, error) {
	if role == "" {
		role = "member"
	}
	m := &NetGroupMember{
		GroupID: groupID, AssetID: assetID, Role: role,
		Lifecycle: LifecycleActive, CreatedAt: FormatTime(now), UpdatedAt: FormatTime(now),
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// Validate checks a group membership against its business rules.
func (m *NetGroupMember) Validate() error {
	ve := &ValidationError{}
	checkRequired(ve, "group_id", m.GroupID)
	checkRequired(ve, "asset_id", m.AssetID)
	checkEnum(ve, "role", m.Role, NetGroupMemberRoles)
	if m.Lifecycle == "" {
		m.Lifecycle = LifecycleActive
	}
	checkEnum(ve, "lifecycle", m.Lifecycle, []string{LifecycleActive, LifecycleRetired})
	return ve.OrNil()
}

// NetUplink is a directed edge from a group to the group it uplinks to.
// Several rows from one group are ALTERNATE paths (best-of), never
// both-required -- "both required" is one uplink to a group whose
// availability says active_active with min_healthy = 2.
type NetUplink struct {
	ID              string   `db:"id"`
	GroupID         string   `db:"group_id"`
	UpstreamGroupID string   `db:"upstream_group_id"`
	Plane           string   `db:"plane"`
	Lifecycle       string   `db:"lifecycle"`
	Source          string   `db:"source"`
	Confidence      *float64 `db:"confidence"`
	FirstSeen       *string  `db:"first_seen"`
	LastSeen        *string  `db:"last_seen"`
	VerifiedBy      *string  `db:"verified_by"`
	VerifiedAt      *string  `db:"verified_at"`
	CreatedAt       string   `db:"created_at"`
	UpdatedAt       string   `db:"updated_at"`
}

// NewNetUplink validates and constructs an uplink edge.
func NewNetUplink(id, groupID, upstreamGroupID, plane string, now time.Time) (*NetUplink, error) {
	if plane == "" {
		plane = PlaneData
	}
	u := &NetUplink{
		ID: id, GroupID: groupID, UpstreamGroupID: upstreamGroupID, Plane: plane,
		Lifecycle: LifecycleActive, Source: SourceDeclared,
		CreatedAt: FormatTime(now), UpdatedAt: FormatTime(now),
	}
	if err := u.Validate(); err != nil {
		return nil, err
	}
	return u, nil
}

// Validate checks an uplink against its business rules.
func (u *NetUplink) Validate() error {
	ve := &ValidationError{}
	checkRequired(ve, "group_id", u.GroupID)
	checkRequired(ve, "upstream_group_id", u.UpstreamGroupID)
	if u.GroupID != "" && u.GroupID == u.UpstreamGroupID {
		ve.Add("upstream_group_id", "a group cannot uplink to itself")
	}
	checkEnum(ve, "plane", u.Plane, Planes)
	checkEnum(ve, "lifecycle", u.Lifecycle, []string{LifecycleActive, LifecycleRetired})
	checkEnum(ve, "source", u.Source, NetworkSources)
	if u.Confidence != nil && (*u.Confidence < 0 || *u.Confidence > 1) {
		ve.Add("confidence", "must be between 0 and 1")
	}
	return ve.OrNil()
}

// IsRetired reports whether the uplink has been withdrawn.
func (u *NetUplink) IsRetired() bool { return u.Lifecycle == LifecycleRetired }

// NetAttachment is a host's attachment to a forwarding group, on one plane.
//
// asset_id is restricted in Go (see ValidateAttachable) to non-forwarder kinds
// -- a rack or a site is not network-attached, and allowing one would let a
// single row fake full coverage for a subtree.
type NetAttachment struct {
	ID         string   `db:"id"`
	AssetID    string   `db:"asset_id"`
	GroupID    string   `db:"group_id"`
	Plane      string   `db:"plane"`
	Lifecycle  string   `db:"lifecycle"`
	Source     string   `db:"source"`
	Confidence *float64 `db:"confidence"`
	FirstSeen  *string  `db:"first_seen"`
	LastSeen   *string  `db:"last_seen"`
	VerifiedBy *string  `db:"verified_by"`
	VerifiedAt *string  `db:"verified_at"`
	CreatedAt  string   `db:"created_at"`
	UpdatedAt  string   `db:"updated_at"`
}

// NewNetAttachment validates and constructs an attachment.
func NewNetAttachment(id, assetID, groupID, plane string, now time.Time) (*NetAttachment, error) {
	if plane == "" {
		plane = PlaneData
	}
	a := &NetAttachment{
		ID: id, AssetID: assetID, GroupID: groupID, Plane: plane,
		Lifecycle: LifecycleActive, Source: SourceDeclared,
		CreatedAt: FormatTime(now), UpdatedAt: FormatTime(now),
	}
	if err := a.Validate(); err != nil {
		return nil, err
	}
	return a, nil
}

// Validate checks an attachment against its business rules.
func (a *NetAttachment) Validate() error {
	ve := &ValidationError{}
	checkRequired(ve, "asset_id", a.AssetID)
	checkRequired(ve, "group_id", a.GroupID)
	checkEnum(ve, "plane", a.Plane, Planes)
	checkEnum(ve, "lifecycle", a.Lifecycle, []string{LifecycleActive, LifecycleRetired})
	checkEnum(ve, "source", a.Source, NetworkSources)
	if a.Confidence != nil && (*a.Confidence < 0 || *a.Confidence > 1) {
		ve.Add("confidence", "must be between 0 and 1")
	}
	return ve.OrNil()
}

// IsRetired reports whether the attachment has been withdrawn.
func (a *NetAttachment) IsRetired() bool { return a.Lifecycle == LifecycleRetired }

// AttachableAssetKinds is the set of asset kinds a net_attachment may name.
// A rack or a site is not network-attached; allowing one would let a single
// row fake full coverage for a subtree.
var AttachableAssetKinds = []string{
	KindServer, KindHypervisor, KindCluster, KindVM, KindK8sNode, KindStorage,
}

// IsAttachable moved to the asset_kind lookup row for the same reason
// CanHostInstances did -- see internal/domain/asset.go. AttachableAssetKinds
// remains as the set the migration seeds is_attachable = TRUE for, so the two
// cannot silently disagree at install time.

// NetAttachmentMember names which chassis of the group an attachment actually
// lands on. An empty member set means "attached to the group as a whole,
// survives while the group does"; a non-empty set means the attachment
// survives only while at least one listed chassis survives.
type NetAttachmentMember struct {
	AttachmentID string  `db:"attachment_id"`
	AssetID      string  `db:"asset_id"`
	InterfaceID  *string `db:"interface_id"`
}

// NewNetAttachmentMember validates and constructs an attachment member.
func NewNetAttachmentMember(attachmentID, assetID string, interfaceID *string) (*NetAttachmentMember, error) {
	ve := &ValidationError{}
	checkRequired(ve, "attachment_id", attachmentID)
	checkRequired(ve, "asset_id", assetID)
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	return &NetAttachmentMember{AttachmentID: attachmentID, AssetID: assetID, InterfaceID: interfaceID}, nil
}

// NetAnchor is where a reachability scope enters the estate. It hangs off a
// GROUP, not an interface: an active/passive pair is ONE point of presence,
// and pinning to a port would need one duplicate row per chassis whose
// omission silently changes every external verdict in the estate.
//
// Carries the same provenance columns as every other net_* table. A
// misplaced anchor is the single highest-leverage wrong row in this model --
// one row silently changes every external-reachability verdict in the estate.
type NetAnchor struct {
	ID            string   `db:"id"`
	Code          string   `db:"code"`
	Name          string   `db:"name"`
	Scope         string   `db:"scope"`
	GroupID       string   `db:"group_id"`
	EnvironmentID *string  `db:"environment_id"`
	Plane         string   `db:"plane"`
	Lifecycle     string   `db:"lifecycle"`
	Source        string   `db:"source"`
	Confidence    *float64 `db:"confidence"`
	FirstSeen     *string  `db:"first_seen"`
	LastSeen      *string  `db:"last_seen"`
	VerifiedBy    *string  `db:"verified_by"`
	VerifiedAt    *string  `db:"verified_at"`
	CreatedAt     string   `db:"created_at"`
	UpdatedAt     string   `db:"updated_at"`
}

// NewNetAnchor validates and constructs an anchor.
func NewNetAnchor(id, code, name, scope, groupID string, now time.Time) (*NetAnchor, error) {
	a := &NetAnchor{
		ID: id, Code: strings.ToLower(strings.TrimSpace(code)), Name: name,
		Scope: scope, GroupID: groupID, Plane: PlaneData,
		Lifecycle: LifecycleActive, Source: SourceDeclared,
		CreatedAt: FormatTime(now), UpdatedAt: FormatTime(now),
	}
	if err := a.Validate(); err != nil {
		return nil, err
	}
	return a, nil
}

// Validate checks an anchor against its business rules.
func (a *NetAnchor) Validate() error {
	ve := &ValidationError{}
	a.Code = strings.ToLower(checkRequired(ve, "code", a.Code))
	a.Name = checkRequired(ve, "name", a.Name)
	checkEnum(ve, "scope", a.Scope, AnchorScopes)
	checkRequired(ve, "group_id", a.GroupID)
	if a.Plane == "" {
		a.Plane = PlaneData
	}
	checkEnum(ve, "plane", a.Plane, Planes)
	checkEnum(ve, "lifecycle", a.Lifecycle, []string{LifecycleActive, LifecycleRetired})
	checkEnum(ve, "source", a.Source, NetworkSources)
	if a.Confidence != nil && (*a.Confidence < 0 || *a.Confidence > 1) {
		ve.Add("confidence", "must be between 0 and 1")
	}
	return ve.OrNil()
}

// IsRetired reports whether the anchor has been withdrawn.
func (a *NetAnchor) IsRetired() bool { return a.Lifecycle == LifecycleRetired }
