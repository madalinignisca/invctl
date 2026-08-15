// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import (
	"strings"
	"time"
)

// Environment roles. A transit environment exists to broker traffic between
// other environments — assets in one are expected to span, so span-detection
// reports treat them differently (see OpenQuestion 4 in docs/DECISIONS.md).
const (
	EnvRoleProduction = "production"
	EnvRoleStaging    = "staging"
	EnvRoleDev        = "dev"
	EnvRoleTransit    = "transit"
	EnvRoleShared     = "shared"
	EnvRoleDR         = "dr"
)

// EnvRoles are the environment roles this code knows by name. It is NOT the
// permitted set: since migration 00004 that lives in the environment_role
// table and environment.role is a FOREIGN KEY into it, so a role added there is
// valid immediately and appears in this slice never. Use it to refer to a role
// the code has an opinion about -- EnvRoleTransit below is read by IsTransit
// and by the spanning-assets query -- not as a vocabulary to validate against.
// Adding a constant here grants it nothing; adding a row to environment_role
// is what makes a value exist.
var EnvRoles = []string{
	EnvRoleProduction, EnvRoleStaging, EnvRoleDev,
	EnvRoleTransit, EnvRoleShared, EnvRoleDR,
}

// Environment is a segmentation boundary. Membership is many-to-many
// (HANDOVER §3.6) — it is deliberately not a column on asset.
type Environment struct {
	ID          string `db:"id"`
	Code        string `db:"code"`
	Name        string `db:"name"`
	Role        string `db:"role"`
	InScope     bool   `db:"in_scope"`
	Criticality int    `db:"criticality"`
	CreatedAt   string `db:"created_at"`
	UpdatedAt   string `db:"updated_at"`
	// RowVersion is the optimistic-concurrency token; see version.go.
	RowVersion int `db:"row_version"`
}

// NewEnvironment validates and constructs. The DB CHECK is a backstop; this is
// where a bad value is supposed to be caught.
func NewEnvironment(id, code, name, role string, inScope bool, criticality int, now time.Time) (*Environment, error) {
	ts := FormatTime(now)
	env := &Environment{
		ID: id, Code: code, Name: name, Role: role,
		InScope: inScope, Criticality: criticality,
		CreatedAt: ts, UpdatedAt: ts,
	}
	if err := env.Validate(); err != nil {
		return nil, err
	}
	return env, nil
}

// Validate checks an environment against its business rules, and normalises the
// code the way the constructor always has.
//
// SEPARATE FROM THE CONSTRUCTOR because an update has to run the same rules and
// could not: the checks lived inside NewEnvironment, so UpdateEnvironment wrote
// whatever it was handed and the table CHECK was the only thing standing
// between a form and a blank name. Every other entity here validates on the
// value; this one now does too.
func (e *Environment) Validate() error {
	ve := &ValidationError{}
	e.Code = strings.ToLower(checkRequired(ve, "code", e.Code))
	e.Name = checkRequired(ve, "name", e.Name)
	e.Role = checkVocabulary(ve, "role", e.Role)
	if e.Criticality < 1 || e.Criticality > 5 {
		ve.Add("criticality", "must be between 1 and 5")
	}
	return ve.OrNil()
}

// IsTransit reports whether this environment brokers cross-environment traffic.
// IsTransit is likewise decided by the environment_role lookup row, via
// store.RoleIsTransit and the is_transit flag decorateAssets joins in. The
// literal comparison that used to live here made every asset spanning a
// data-added brokering role a false positive on the segmentation report.

// Asset kinds spanning layers 1-3: physical network, physical compute,
// virtualization.
const (
	KindSite       = "site"
	KindRack       = "rack"
	KindPDU        = "pdu"
	KindFirewall   = "firewall"
	KindSwitch     = "switch"
	KindPatchPanel = "patch_panel"
	KindServer     = "server"
	KindHypervisor = "hypervisor"
	KindCluster    = "cluster"
	KindVM         = "vm"
	KindK8sNode    = "k8s_node"
	KindBridge     = "bridge"
	KindStorage    = "storage"
)

// AssetKinds are the asset kinds this code knows by name. It is NOT the
// permitted set: since migration 00004 that lives in the asset_kind table and
// asset.kind is a FOREIGN KEY into it, so a kind added there is valid
// immediately and appears in this slice never. The constants stay because code
// genuinely refers to specific kinds -- KindK8sNode in the impact graph's
// cluster expansion, the AttachableAssetKinds subset in reach.go -- and because
// the seed and the tests need names rather than string literals.
//
// They are well-known members of an open set, and the distinction bites:
// CanHostInstances and IsAttachable both switch over a hand-listed subset and
// both answer false for anything they have not heard of. A kind inserted into
// asset_kind is therefore non-hosting and non-attachable with no diagnostic.
// That is a known gap, recorded in migration 00004's header comment; closing it
// means carrying the behaviour as a column on the lookup table, which is an
// architecture decision and not a refactor.
var AssetKinds = []string{
	KindSite, KindRack, KindPDU, KindFirewall, KindSwitch, KindPatchPanel,
	KindServer, KindHypervisor, KindCluster, KindVM, KindK8sNode, KindBridge,
	KindStorage,
}

// Lifecycle values. Nothing is ever hard-deleted (HANDOVER §3.7); retirement
// is a lifecycle transition so the decommissioned server is still answerable
// to an auditor six months later.
const (
	LifecyclePlanned     = "planned"
	LifecycleActive      = "active"
	LifecycleMaintenance = "maintenance"
	LifecycleDeprecated  = "deprecated"
	LifecycleRetired     = "retired"
)

// AssetLifecycles is the Go side of the asset.lifecycle CHECK constraint.
var AssetLifecycles = []string{
	LifecyclePlanned, LifecycleActive, LifecycleMaintenance,
	LifecycleDeprecated, LifecycleRetired,
}

// Asset is anything physical or virtual in layers 1-3.
//
// ParentID is the containment tree (site -> rack -> hypervisor -> vm). It is
// strictly a tree; every other relationship is an explicit edge table. The
// flattened form lives in asset_closure and is maintained by the store.
type Asset struct {
	ID       string  `db:"id"`
	Kind     string  `db:"kind"`
	Name     string  `db:"name"`
	ParentID *string `db:"parent_id"`
	Serial   *string `db:"serial"`
	AssetTag *string `db:"asset_tag"`
	Vendor   *string `db:"vendor"`
	Model    *string `db:"model"`
	// DeviceTypeID links this box to a catalogued model. Optional, and it stays
	// optional: an inventory that refused to hold a server until somebody had
	// modelled its type would be an inventory that stayed empty.
	//
	// It DESCRIBES the thing rather than deciding what it is attached to, so it
	// is editable like vendor and model -- unlike parent_id, which moves the
	// graph and has its own flow.
	DeviceTypeID *string `db:"device_type_id"`
	// UHeight is a RACK's own capacity in units. Nil on everything else -- a
	// mounted box's height is a property of its catalogued model, not of the
	// box.
	UHeight *int `db:"u_height"`
	// RackPosition is the lowest unit this box occupies, counting from the
	// floor, and RackFace which side it is mounted on. Both nil until somebody
	// records where it actually is, which is the ordinary state of an estate
	// that has just been entered.
	RackPosition *int    `db:"rack_position"`
	RackFace     *string `db:"rack_face"`
	// A RACK's own physical measurements, nil on everything else and usually nil
	// on racks too -- an estate that has not been round with a tape measure is
	// the ordinary starting state, and every check built on these reports "not
	// recorded" rather than assuming a value. See domain/fit.go.
	//
	// UsableDepthMM is rail face to rear door and is MEASURED, never derived
	// from a cabinet's external depth: where the rails sit is an installation
	// choice. WidthMM is the external width, and side clearance MAY be derived
	// from it, because EIA-310 fixes the equipment width and a standard pinning
	// the missing term is what separates arithmetic from a guess.
	UsableDepthMM *int `db:"usable_depth_mm"`
	WidthMM       *int `db:"width_mm"`
	MaxLoadGrams  *int `db:"max_load_grams"`
	// Compute capacity (WP-J3). Nil throughout is the ordinary state and never
	// an error: most assets are neither a host nor a workload, and an estate
	// that has recorded nothing must report that it knows nothing rather than
	// that everything fits.
	//
	// CPUCores and MemoryMB are what the machine HAS -- a host's own capacity.
	CPUCores *int `db:"cpu_cores"`
	MemoryMB *int `db:"memory_mb"`
	// What a workload may take, and what it is charged for. Both are kept
	// because they answer different questions and routinely differ: a VM given
	// twice the memory "to be safe" while the deal was priced on half is not a
	// mistake, it is a decision somebody made without pricing it, and the gap
	// between these two is what makes that visible.
	VCPUProvisioned     *int `db:"vcpu_provisioned"`
	VCPUAllocated       *int `db:"vcpu_allocated"`
	MemoryProvisionedMB *int `db:"memory_provisioned_mb"`
	MemoryAllocatedMB   *int `db:"memory_allocated_mb"`

	// A storage POOL's own size (migration 00046). Nil on everything that is
	// not a pool, which is almost everything -- the ordinary state, as with
	// every other capacity column here.
	//
	// RAW, NOT USABLE. An operator knows how many disks went in the box; how
	// much survives replication is arithmetic, and it belongs in one place
	// rather than in whoever typed the number. The ratio comes from
	// StorageKind, which is a lookup row so a site with an unusual erasure
	// profile can add one without a release.
	StorageKind   *string `db:"storage_kind"`
	RawCapacityGB *int    `db:"raw_capacity_gb"`

	// ReplacesAssetID is the box this one took over from (WP-J1). The only
	// fact needed to compare what a refresh cost against what it succeeded --
	// both assets already carry their own cost lines, vendor and dates.
	//
	// Nil is the ordinary case and never an error. An asset that replaced
	// nothing is most assets.
	ReplacesAssetID *string `db:"replaces_asset_id"`
	Lifecycle       string  `db:"lifecycle"`
	// Who looks after it, and in what capacity. Both optional; see team.go.
	TeamID      *string `db:"team_id"`
	ManagerRole *string `db:"manager_role"`
	// EOLDate is when this stops being supportable. Declared, optional, and
	// nothing in this codebase acts on it passing -- see eol.go.
	EOLDate   *string `db:"eol_date"`
	Attrs     string  `db:"attrs"`
	CreatedAt string  `db:"created_at"`
	UpdatedAt string  `db:"updated_at"`
	// RowVersion is the optimistic-concurrency token; see version.go.
	RowVersion int `db:"row_version"`
}

// NewAsset validates and constructs an asset.
func NewAsset(id, kind, name string, parentID *string, now time.Time) (*Asset, error) {
	ve := &ValidationError{}
	name = checkRequired(ve, "name", name)
	kind = checkVocabulary(ve, "kind", kind)
	if parentID != nil && *parentID == id {
		ve.Add("parent_id", "an asset cannot contain itself")
	}
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	ts := FormatTime(now)
	return &Asset{
		ID: id, Kind: kind, Name: name, ParentID: parentID,
		Lifecycle: LifecycleActive, Attrs: "{}",
		CreatedAt: ts, UpdatedAt: ts,
	}, nil
}

// Validate re-checks an asset after field updates.
func (a *Asset) Validate() error {
	ve := &ValidationError{}
	a.Name = checkRequired(ve, "name", a.Name)
	a.Kind = checkVocabulary(ve, "kind", a.Kind)
	// lifecycle stays a checkEnum: IsRetired, lifecycleClass and the
	// spanning/coverage queries all branch on its value.
	checkEnum(ve, "lifecycle", a.Lifecycle, AssetLifecycles)
	if a.ParentID != nil && *a.ParentID == a.ID {
		ve.Add("parent_id", "an asset cannot contain itself")
	}
	// A box cannot succeed itself. The same shape as the containment check
	// above, and refused for the same reason: it is not a strange lineage, it
	// is a row that makes every reader of the comparison ask what it means.
	//
	// A CHAIN IS FINE AND WANTED -- C replaces B replaces A gives price
	// movement over three generations, which is the whole point. Only the
	// self-reference is nonsense, and a longer cycle cannot be built without
	// the store seeing both ends, so it is refused there rather than guessed
	// at here.
	if a.ReplacesAssetID != nil {
		if *a.ReplacesAssetID == a.ID {
			ve.Add("replaces_asset_id", "an asset cannot replace itself")
		}
		if strings.TrimSpace(*a.ReplacesAssetID) == "" {
			a.ReplacesAssetID = nil
		}
	}
	a.EOLDate = checkDate(ve, "eol_date", a.EOLDate)
	// Capacity: positive or absent. Zero cores is not a small machine, it is a
	// value somebody typed by accident, and it would divide a cluster's cost by
	// nothing.
	checkPositive(ve, "cpu_cores", a.CPUCores)
	checkPositive(ve, "memory_mb", a.MemoryMB)
	checkPositive(ve, "vcpu_provisioned", a.VCPUProvisioned)
	checkPositive(ve, "vcpu_allocated", a.VCPUAllocated)
	checkPositive(ve, "raw_capacity_gb", a.RawCapacityGB)
	checkPositive(ve, "memory_provisioned_mb", a.MemoryProvisionedMB)
	checkPositive(ve, "memory_allocated_mb", a.MemoryAllocatedMB)
	checkMillimetres(ve, "usable_depth_mm", a.UsableDepthMM)
	checkMillimetres(ve, "width_mm", a.WidthMM)
	checkGrams(ve, "max_load_grams", a.MaxLoadGrams)
	a.TeamID, a.ManagerRole = checkResponsibility(ve, a.TeamID, a.ManagerRole)
	if strings.TrimSpace(a.Attrs) == "" {
		a.Attrs = "{}"
	}
	return ve.OrNil()
}

// IsRetired reports whether the asset has been soft-deleted.
func (a *Asset) IsRetired() bool { return a.Lifecycle == LifecycleRetired }

// Whether a kind may host instances or take a network attachment is NOT
// decided here any more. It is a column on the asset_kind lookup row
// (can_host_instances, is_attachable), read by store.AssetKindBehaviour and
// carried on store.AssetRow.
//
// It moved because a switch here could only ever answer for kinds this package
// had been compiled with, so a kind added by INSERT -- the entire point of the
// lookup table -- was storable and then silently unusable. Two sources of truth
// for the same fact is worse than one in the less convenient place.

// checkPositive refuses a recorded quantity of zero or less.
//
// ABSENT AND ZERO ARE DIFFERENT and the distinction is load-bearing here. Nil
// means nobody has measured it, which every capacity check reports as a gap.
// Zero means somebody asserted the machine has no cores, which is not a small
// machine -- it is a typo that would divide a cluster's cost by nothing.
func checkPositive(ve *ValidationError, field string, v *int) {
	if v != nil && *v <= 0 {
		ve.Add(field, "must be greater than zero, or left empty if nobody has measured it")
	}
}
