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

// EnvRoles is the Go side of the environment.role CHECK constraint.
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
}

// NewEnvironment validates and constructs. The DB CHECK is a backstop; this is
// where a bad value is supposed to be caught.
func NewEnvironment(id, code, name, role string, inScope bool, criticality int, now time.Time) (*Environment, error) {
	ve := &ValidationError{}
	code = checkRequired(ve, "code", code)
	name = checkRequired(ve, "name", name)
	checkEnum(ve, "role", role, EnvRoles)
	if criticality < 1 || criticality > 5 {
		ve.Add("criticality", "must be between 1 and 5")
	}
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	ts := FormatTime(now)
	return &Environment{
		ID: id, Code: strings.ToLower(code), Name: name, Role: role,
		InScope: inScope, Criticality: criticality,
		CreatedAt: ts, UpdatedAt: ts,
	}, nil
}

// IsTransit reports whether this environment brokers cross-environment traffic.
func (e *Environment) IsTransit() bool { return e.Role == EnvRoleTransit }

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
	KindStorage    = "storage"
)

// AssetKinds is the Go side of the asset.kind CHECK constraint.
var AssetKinds = []string{
	KindSite, KindRack, KindPDU, KindFirewall, KindSwitch, KindPatchPanel,
	KindServer, KindHypervisor, KindCluster, KindVM, KindK8sNode, KindStorage,
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
	ID        string  `db:"id"`
	Kind      string  `db:"kind"`
	Name      string  `db:"name"`
	ParentID  *string `db:"parent_id"`
	Serial    *string `db:"serial"`
	AssetTag  *string `db:"asset_tag"`
	Vendor    *string `db:"vendor"`
	Model     *string `db:"model"`
	Lifecycle string  `db:"lifecycle"`
	OwnerTeam *string `db:"owner_team"`
	Attrs     string  `db:"attrs"`
	CreatedAt string  `db:"created_at"`
	UpdatedAt string  `db:"updated_at"`
}

// NewAsset validates and constructs an asset.
func NewAsset(id, kind, name string, parentID *string, now time.Time) (*Asset, error) {
	ve := &ValidationError{}
	name = checkRequired(ve, "name", name)
	checkEnum(ve, "kind", kind, AssetKinds)
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
	checkEnum(ve, "kind", a.Kind, AssetKinds)
	checkEnum(ve, "lifecycle", a.Lifecycle, AssetLifecycles)
	if a.ParentID != nil && *a.ParentID == a.ID {
		ve.Add("parent_id", "an asset cannot contain itself")
	}
	if strings.TrimSpace(a.Attrs) == "" {
		a.Attrs = "{}"
	}
	return ve.OrNil()
}

// IsRetired reports whether the asset has been soft-deleted.
func (a *Asset) IsRetired() bool { return a.Lifecycle == LifecycleRetired }

// CanHostInstances reports whether a service instance may be placed here.
// Placing a workload on a rack or a patch panel is a data-entry mistake, and
// catching it here keeps the impact engine's placement phase meaningful.
func (a *Asset) CanHostInstances() bool {
	switch a.Kind {
	case KindServer, KindHypervisor, KindVM, KindK8sNode, KindCluster,
		KindFirewall, KindSwitch, KindStorage:
		return true
	default:
		return false
	}
}
