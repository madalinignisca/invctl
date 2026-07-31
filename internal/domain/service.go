package domain

import (
	"strings"
	"time"
)

// Service kinds.
const (
	SvcDB         = "db"
	SvcCache      = "cache"
	SvcQueue      = "queue"
	SvcWeb        = "web"
	SvcAPI        = "api"
	SvcProxy      = "proxy"
	SvcLB         = "lb"
	SvcAuth       = "auth"
	SvcSecrets    = "secrets"
	SvcBatch      = "batch"
	SvcAgent      = "agent"
	SvcStorage    = "storage"
	SvcInfra      = "infra"
	SvcMonitoring = "monitoring"
)

// ServiceKinds are the service kinds this code knows by name. It is NOT the
// permitted set: since migration 00004 that lives in the service_kind table and
// service.kind is a FOREIGN KEY into it, so a new kind is an INSERT and appears
// in this slice never. The constants stay for the seed and the tests. Nothing
// branches on a service kind -- the impact engine reads Availability, never
// Kind -- which is why this one was safe to open.
var ServiceKinds = []string{
	SvcDB, SvcCache, SvcQueue, SvcWeb, SvcAPI, SvcLB, SvcProxy,
	SvcAuth, SvcSecrets, SvcBatch, SvcAgent, SvcStorage, SvcInfra, SvcMonitoring,
}

// Availability policies (HANDOVER §3.3). Without these, "reboot node 3"
// reports everything on node 3 as down, which is useless — losing one of three
// Vault nodes has to report ok.
const (
	AvailStandalone    = "standalone"
	AvailActiveActive  = "active_active"
	AvailActivePassive = "active_passive"
	AvailQuorum        = "quorum"
	AvailSharded       = "sharded"
)

// Availabilities is the Go side of the service.availability CHECK constraint.
var Availabilities = []string{
	AvailStandalone, AvailActiveActive, AvailActivePassive, AvailQuorum, AvailSharded,
}

// Failover modes. Manual failover is why an active/passive pair losing its
// primary reports degraded rather than ok — somebody has to be paged.
const (
	FailoverAuto   = "auto"
	FailoverManual = "manual"
	FailoverNone   = "none"
)

// FailoverModes is the Go side of the service.failover_mode CHECK constraint.
var FailoverModes = []string{FailoverAuto, FailoverManual, FailoverNone}

// ServiceLifecycles is the Go side of the service.lifecycle CHECK constraint.
// Note it has no 'maintenance' value, unlike asset.
var ServiceLifecycles = []string{
	LifecyclePlanned, LifecycleActive, LifecycleDeprecated, LifecycleRetired,
}

// Status is the health of a service under a simulated outage. It is monotonic
// within an impact run — ok -> degraded -> down, never back — which is what
// guarantees the fixed-point iteration terminates.
type Status string

const (
	StatusOK       Status = "ok"
	StatusDegraded Status = "degraded"
	StatusDown     Status = "down"
)

// rank orders statuses for monotonic merging.
func (s Status) rank() int {
	switch s {
	case StatusDown:
		return 2
	case StatusDegraded:
		return 1
	default:
		return 0
	}
}

// Worse returns the more severe of two statuses. Merging only ever moves a
// service further from ok.
func (s Status) Worse(other Status) Status {
	if other.rank() > s.rank() {
		return other
	}
	return s
}

// Better returns the less severe of two statuses. It is the mirror of Worse,
// used to fold alternative network paths: two paths, either working, means
// the asset is reachable, so the fold runs in the opposite direction from
// propagation's merge. Identity for a Better-fold is StatusDown; identity for
// a Worse-fold is StatusOK. Stating both is deliberate: getting the
// initialiser backwards is how a fold silently returns the absorbing element
// for every input.
func (s Status) Better(other Status) Status {
	if other.rank() < s.rank() {
		return other
	}
	return s
}

// Application used to live here. It grouped services under a business-facing
// name, and Project now does that job for services AND assets, with the
// owns/uses distinction it never had. Migration 00010 moved every application
// across, keeping its id, and dropped the table.
//
// A service therefore no longer carries who owns it. Ownership is a link, not a
// column, because "at most one owner" is enforceable as a partial unique index
// while "and also these fifteen things use it" is not enforceable as a column at
// all.

// Service is a logical workload: one row regardless of how many replicas run
// (HANDOVER §3.2). Dependencies, ownership and SLOs attach here, not to the
// instance — otherwise every dependency edge has to be written once per replica.
type Service struct {
	ID            string  `db:"id"`
	Code          string  `db:"code"`
	Name          string  `db:"name"`
	Kind          string  `db:"kind"`
	EnvironmentID string  `db:"environment_id"`
	Availability  string  `db:"availability"`
	MinHealthy    *int    `db:"min_healthy"`
	FailoverMode  *string `db:"failover_mode"`
	Tier          int     `db:"tier"`
	RTOMinutes    *int    `db:"rto_minutes"`
	RPOMinutes    *int    `db:"rpo_minutes"`
	TeamID        *string `db:"team_id"`
	ManagerRole   *string `db:"manager_role"`
	Lifecycle     string  `db:"lifecycle"`
	// EOLDate is when this stops being supportable -- a licence that will not
	// renew, a release past its support window. Declared, optional, inert.
	EOLDate   *string `db:"eol_date"`
	Attrs     string  `db:"attrs"`
	CreatedAt string  `db:"created_at"`
	UpdatedAt string  `db:"updated_at"`
}

// ServiceSpec is everything needed to define a service.
//
// It exists because the availability policy and the fields it requires have to
// be validated together: min_healthy is mandatory for active_active and
// meaningless otherwise, and failover_mode is mandatory for active_passive.
// A positional constructor would either have to take those fields it does not
// always need, or accept a half-built service and validate it later -- and
// "validate it later" is how invalid rows reach the database.
type ServiceSpec struct {
	Code          string
	Name          string
	Kind          string
	EnvironmentID string
	Availability  string
	Tier          int
	MinHealthy    *int
	FailoverMode  *string
	RTOMinutes    *int
	RPOMinutes    *int
	TeamID        *string
	ManagerRole   *string
	EOLDate       *string
}

// NewService validates and constructs a service.
//
// The availability/min_healthy pairing is validated here rather than in SQL
// because a CHECK spanning two columns with policy-dependent semantics is
// exactly the kind of expression that behaves differently across engines.
func NewService(id string, spec ServiceSpec, now time.Time) (*Service, error) {
	s := &Service{
		ID:            id,
		Code:          strings.ToLower(strings.TrimSpace(spec.Code)),
		Name:          spec.Name,
		Kind:          spec.Kind,
		EnvironmentID: spec.EnvironmentID,
		Availability:  spec.Availability,
		MinHealthy:    spec.MinHealthy,
		FailoverMode:  spec.FailoverMode,
		Tier:          spec.Tier,
		RTOMinutes:    spec.RTOMinutes,
		RPOMinutes:    spec.RPOMinutes,
		TeamID:        spec.TeamID,
		ManagerRole:   spec.ManagerRole,
		EOLDate:       spec.EOLDate,
		Lifecycle:     LifecycleActive,
		Attrs:         "{}",
		CreatedAt:     FormatTime(now),
		UpdatedAt:     FormatTime(now),
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return s, nil
}

// Validate checks a service against its business rules.
func (s *Service) Validate() error {
	ve := &ValidationError{}
	s.Code = strings.ToLower(checkRequired(ve, "code", s.Code))
	s.Name = checkRequired(ve, "name", s.Name)
	s.Kind = checkVocabulary(ve, "kind", s.Kind)
	// availability and lifecycle stay checkEnum: AvailabilityPolicy.Evaluate
	// and the impact engine's capacity arithmetic both switch on availability,
	// and lifecycle drives retirement.
	checkEnum(ve, "availability", s.Availability, Availabilities)
	checkEnum(ve, "lifecycle", s.Lifecycle, ServiceLifecycles)
	checkRequired(ve, "environment_id", s.EnvironmentID)
	if s.Tier < 1 || s.Tier > 4 {
		ve.Add("tier", "must be between 1 and 4")
	}
	if s.FailoverMode != nil && *s.FailoverMode != "" {
		checkEnum(ve, "failover_mode", *s.FailoverMode, FailoverModes)
	}
	s.EOLDate = checkDate(ve, "eol_date", s.EOLDate)
	s.TeamID, s.ManagerRole = checkResponsibility(ve, s.TeamID, s.ManagerRole)
	// active_active is the only policy where min_healthy carries meaning: the
	// others derive their threshold from the instance count or the role.
	if s.Availability == AvailActiveActive {
		if s.MinHealthy == nil {
			ve.Add("min_healthy", "is required for active_active")
		} else if *s.MinHealthy < 1 {
			ve.Add("min_healthy", "must be at least 1")
		}
	}
	if s.Availability == AvailActivePassive && (s.FailoverMode == nil || *s.FailoverMode == "") {
		ve.Add("failover_mode", "is required for active_passive")
	}
	if strings.TrimSpace(s.Attrs) == "" {
		s.Attrs = "{}"
	}
	return ve.OrNil()
}

// InstanceHealth is the minimal per-instance input to a capacity decision.
// Shard is the shard key for a sharded service and is otherwise empty.
type InstanceHealth struct {
	ID    string
	Role  string
	Shard string
	Alive bool
}

// Instance roles that matter to capacity evaluation.
const (
	RolePrimary = "primary"
	RoleStandby = "standby"
)

// AvailabilityPolicy is an availability/min_healthy/failover_mode triple,
// extracted from Service so that anything else needing the same failure
// arithmetic -- net_group chief among them -- can reuse it verbatim rather
// than reimplementing it. An active/passive firewall pair and an
// active/passive database pair fail identically, and one tested function for
// both is one set of bugs rather than two.
type AvailabilityPolicy struct {
	Availability string
	MinHealthy   *int
	FailoverMode *string
}

// Evaluate applies the policy to the surviving instances and returns the
// resulting status (HANDOVER §6 phase 2). This is the exact body that used to
// live in Service.EvaluateCapacity; service_test.go passing unmodified after
// the extraction is the correctness proof.
//
// This is the whole point of modelling availability: without it, any lost
// instance reads as a lost service.
func (p AvailabilityPolicy) Evaluate(instances []InstanceHealth) Status {
	total := len(instances)
	if total == 0 {
		// A service with no instances at all cannot be reasoned about; it is
		// not affected by losing a host it does not run on.
		return StatusOK
	}
	surviving := 0
	for _, i := range instances {
		if i.Alive {
			surviving++
		}
	}
	if surviving == total {
		return StatusOK
	}
	if surviving == 0 {
		return StatusDown
	}

	switch p.Availability {
	case AvailStandalone:
		// Any survivor means at least one copy is serving. Reaching here with
		// 0 < surviving < total implies several instances under a standalone
		// policy, which is a modelling smell but not an outage.
		return StatusOK

	case AvailQuorum:
		// Raft/Paxos: strictly more than half of the *configured* members.
		if surviving < total/2+1 {
			return StatusDown
		}
		return StatusOK

	case AvailActiveActive:
		min := 1
		if p.MinHealthy != nil && *p.MinHealthy > 0 {
			min = *p.MinHealthy
		}
		if surviving < min {
			return StatusDegraded
		}
		return StatusOK

	case AvailActivePassive:
		primaryAlive := false
		standbyAlive := false
		hasPrimary := false
		for _, i := range instances {
			switch i.Role {
			case RolePrimary:
				hasPrimary = true
				if i.Alive {
					primaryAlive = true
				}
			case RoleStandby:
				if i.Alive {
					standbyAlive = true
				}
			}
		}
		if !hasPrimary {
			// Roles were never declared; fall back to "something is alive".
			return StatusDegraded
		}
		if primaryAlive {
			return StatusOK
		}
		if !standbyAlive {
			return StatusDown
		}
		// Primary is gone but a standby can take over. Automatic promotion is
		// a blip; manual promotion needs a human, so it is degraded until then.
		if p.FailoverMode != nil && *p.FailoverMode == FailoverAuto {
			return StatusOK
		}
		return StatusDegraded

	case AvailSharded:
		// Data is partitioned: a shard with no surviving replica means that
		// slice of the keyspace is unavailable even though the service as a
		// whole still answers.
		alive := map[string]int{}
		for _, i := range instances {
			shard := i.Shard
			if shard == "" {
				shard = i.Role
			}
			if _, ok := alive[shard]; !ok {
				alive[shard] = 0
			}
			if i.Alive {
				alive[shard]++
			}
		}
		for _, n := range alive {
			if n == 0 {
				return StatusDegraded
			}
		}
		return StatusOK
	}
	return StatusDegraded
}

// EvaluateCapacity applies the service's availability policy to the surviving
// instances and returns the resulting status (HANDOVER §6 phase 2).
func (s *Service) EvaluateCapacity(instances []InstanceHealth) Status {
	return AvailabilityPolicy{
		Availability: s.Availability, MinHealthy: s.MinHealthy, FailoverMode: s.FailoverMode,
	}.Evaluate(instances)
}

// Runtime types for a service instance.
const (
	RuntimeSystemd        = "systemd"
	RuntimeWindowsService = "windows_service"
	RuntimeContainer      = "container"
	RuntimeK8sWorkload    = "k8s_workload"
	RuntimeAppliance      = "appliance"
)

// RuntimeTypes is the Go side of the service_instance.runtime_type CHECK.
var RuntimeTypes = []string{
	RuntimeSystemd, RuntimeWindowsService, RuntimeContainer,
	RuntimeK8sWorkload, RuntimeAppliance,
}

// DesiredStates is the Go side of the service_instance.desired_state CHECK.
var DesiredStates = []string{"running", "stopped", "disabled"}

// Sources record where a fact came from (HANDOVER §3.5). Declared data is
// authoritative and is never silently overwritten by a reconciler.
const (
	SourceDeclared          = "declared"
	SourceDiscoveredNetstat = "discovered_netstat"
	SourceDiscoveredSystemd = "discovered_systemd"
	SourceDiscoveredK8s     = "discovered_k8s"
	SourceDiscoveredConfig  = "discovered_config"
)

// DependencySources is the Go side of the dependency.source CHECK constraint.
var DependencySources = []string{
	SourceDeclared, SourceDiscoveredNetstat, SourceDiscoveredSystemd,
	SourceDiscoveredK8s, SourceDiscoveredConfig,
}

// ServiceInstanceSources is the Go side of the service_instance.source CHECK
// added in 00008_observed.sql. Until then this column was the only
// unconstrained provenance column in the schema (docs/AUDIT.md rule 7).
//
// 'discovered_netstat' is deliberately absent: netstat discovers connections --
// dependency edges -- not placements, so nothing could ever legitimately set it
// here, and a vocabulary admitting values the writer cannot produce is not a
// constraint.
var ServiceInstanceSources = []string{
	SourceDeclared, SourceDiscoveredSystemd, SourceDiscoveredK8s, SourceDiscoveredConfig,
}

// ServiceInstance is one running copy of a service on one host.
//
// Placement and declared intent live here; everything logical lives on Service.
// Shard is used only by the sharded availability policy.
//
// Observed state does NOT live here. Migration 00008 moved observed_state and
// observed_at to asset_health, keyed (entity_type, entity_id, reporter), because
// a mixed row produces a mixed audit entry that no portable query can classify
// -- the distinguishing information would sit inside change_log.diff, and
// querying inside JSON is banned. Concretely: UpdateInstance used to write
// desired_state and observed_state in one statement from a round-tripped
// struct, so a stale read silently reverted a concurrent operator edit and the
// audit trail attributed the revert to the human. Read health through the
// observed store; never add a health column back here (docs/AUDIT.md rule 1).
type ServiceInstance struct {
	ID          string  `db:"id"`
	ServiceID   string  `db:"service_id"`
	HostAssetID string  `db:"host_asset_id"`
	RuntimeType string  `db:"runtime_type"`
	Role        *string `db:"role"`
	Shard       *string `db:"shard"`
	Ordinal     int     `db:"ordinal"`
	// DesiredState is INTENT: what this placement is supposed to be doing.
	// Lifecycle is EXISTENCE: whether the placement is still part of the
	// estate. They were one column until 00002, and collapsing them is what
	// AUDIT.md warns makes drift undetectable -- "observed stopped, therefore
	// desired stopped" is the same mistake in the other direction.
	DesiredState string `db:"desired_state"`
	Lifecycle    string `db:"lifecycle"`
	Source       string `db:"source"`
	CreatedAt    string `db:"created_at"`
	UpdatedAt    string `db:"updated_at"`
}

// PlacementLifecycles is the Go side of the service_instance.lifecycle CHECK.
//
// Deliberately narrower than ServiceLifecycles: a placement either exists or it
// does not. 'planned' and 'deprecated' are states of the SERVICE, and giving a
// placement the same vocabulary would invite writing them here, where nothing
// reads them.
var PlacementLifecycles = []string{LifecycleActive, LifecycleRetired}

// Retired reports whether this placement has been withdrawn from the estate.
// Distinct from Disabled, which is intent: a disabled placement still exists
// and is expected back.
func (si *ServiceInstance) Retired() bool { return si.Lifecycle == LifecycleRetired }

// NewServiceInstance validates and constructs a placement.
func NewServiceInstance(id, serviceID, hostAssetID, runtimeType string, ordinal int, now time.Time) (*ServiceInstance, error) {
	si := &ServiceInstance{
		Lifecycle: LifecycleActive,
		ID:        id, ServiceID: serviceID, HostAssetID: hostAssetID,
		RuntimeType: runtimeType, Ordinal: ordinal,
		DesiredState: "running", Source: SourceDeclared,
		CreatedAt: FormatTime(now), UpdatedAt: FormatTime(now),
	}
	if err := si.Validate(); err != nil {
		return nil, err
	}
	return si, nil
}

// Validate checks an instance against its business rules.
func (si *ServiceInstance) Validate() error {
	ve := &ValidationError{}
	checkRequired(ve, "service_id", si.ServiceID)
	checkRequired(ve, "host_asset_id", si.HostAssetID)
	checkEnum(ve, "runtime_type", si.RuntimeType, RuntimeTypes)
	checkEnum(ve, "desired_state", si.DesiredState, DesiredStates)
	checkEnum(ve, "lifecycle", si.Lifecycle, PlacementLifecycles)
	// The DB CHECK is the second line of defence, not the first: a provenance
	// value that reaches the driver has already been accepted by a handler.
	checkEnum(ve, "source", si.Source, ServiceInstanceSources)
	if si.Ordinal < 0 {
		ve.Add("ordinal", "must not be negative")
	}
	return ve.OrNil()
}

// RoleOrEmpty dereferences Role for capacity evaluation.
func (si *ServiceInstance) RoleOrEmpty() string {
	if si.Role == nil {
		return ""
	}
	return *si.Role
}

// ShardOrEmpty dereferences Shard for capacity evaluation.
func (si *ServiceInstance) ShardOrEmpty() string {
	if si.Shard == nil {
		return ""
	}
	return *si.Shard
}

// Runtime detail tables. Exactly one applies per instance, keyed by
// runtime_type; they are separate tables rather than a wide nullable one so
// that a Windows service's run-as identity is a real foreign key.

// RTSystemd holds systemd unit detail. UnitAfter and UnitRequires are JSON
// arrays stored as opaque TEXT and parsed in Go — never queried in SQL.
type RTSystemd struct {
	InstanceID   string  `db:"instance_id"`
	UnitName     string  `db:"unit_name"`
	UnitType     *string `db:"unit_type"`
	ExecStart    *string `db:"exec_start"`
	RunAsUser    *string `db:"run_as_user"`
	RunAsGroup   *string `db:"run_as_group"`
	Restart      *string `db:"restart"`
	UnitAfter    string  `db:"unit_after"`
	UnitRequires string  `db:"unit_requires"`
	DropIns      string  `db:"drop_ins"`
}

// RTWindows holds Windows service detail, including the run-as account, which
// is the usual reason a Windows service dies after a credential rotation.
type RTWindows struct {
	InstanceID      string  `db:"instance_id"`
	ServiceName     string  `db:"service_name"`
	DisplayName     *string `db:"display_name"`
	BinaryPath      *string `db:"binary_path"`
	StartType       *string `db:"start_type"`
	LogonIdentityID *string `db:"logon_identity_id"`
	DependsOnSvc    string  `db:"depends_on_svc"`
	RecoveryAction  *string `db:"recovery_action"`
}

// RTContainer holds container runtime detail.
type RTContainer struct {
	InstanceID     string  `db:"instance_id"`
	Engine         *string `db:"engine"`
	ContainerName  *string `db:"container_name"`
	ComposeProject *string `db:"compose_project"`
	ComposeService *string `db:"compose_service"`
	ImageRepo      *string `db:"image_repo"`
	ImageTag       *string `db:"image_tag"`
	ImageDigest    *string `db:"image_digest"`
	RestartPolicy  *string `db:"restart_policy"`
	NetworkMode    *string `db:"network_mode"`
	Rootless       bool    `db:"rootless"`
}

// RTK8s holds Kubernetes workload detail.
type RTK8s struct {
	InstanceID      string  `db:"instance_id"`
	ClusterAssetID  *string `db:"cluster_asset_id"`
	Namespace       *string `db:"namespace"`
	WorkloadKind    *string `db:"workload_kind"`
	WorkloadName    *string `db:"workload_name"`
	ReplicasDesired *int    `db:"replicas_desired"`
	ServiceAccount  *string `db:"service_account"`
	ImageDigest     *string `db:"image_digest"`
}
