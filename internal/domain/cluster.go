// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import "strings"

// Clusters: hosts that can carry each other's guests.

const (
	ClusterProxmox = "proxmox"
	ClusterVMware  = "vmware"
	ClusterHyperV  = "hyperv"
	ClusterXen     = "xen"
	ClusterNutanix = "nutanix"
	ClusterOther   = "other"
)

// ClusterKinds is the Go side of the CHECK constraint.
var ClusterKinds = []string{
	ClusterProxmox, ClusterVMware, ClusterHyperV, ClusterXen, ClusterNutanix, ClusterOther,
}

const (
	// HANone: guests stay down with their host. What the engine did for every
	// cluster before this model existed, and still the right answer for a
	// cluster that shares storage with nothing.
	HANone = "none"
	// HARestart: guests come back on a surviving member, after a restart.
	HARestart = "restart"
)

// HAPolicies is the Go side of the CHECK constraint.
var HAPolicies = []string{HANone, HARestart}

// HAPolicyDescription is the help text. The engine branches on these values, so
// what they mean is a property of this package rather than a row somebody can
// reword.
func HAPolicyDescription(p string) string {
	switch p {
	case HANone:
		return "Guests stay down with their host. Correct for a cluster that shares " +
			"no storage, and for one where nobody has configured HA."
	case HARestart:
		return "Guests restart on a surviving member. They are not serving during the " +
			"restart, and they are serving afterwards — which is why an impact " +
			"report shows them as relocated rather than lost."
	default:
		return ""
	}
}

// Cluster is a set of hosts that can carry each other's guests.
type Cluster struct {
	ID       string `db:"id"`
	Name     string `db:"name"`
	Kind     string `db:"kind"`
	HAPolicy string `db:"ha_policy"`
	// MinHosts is CAPACITY, not quorum: how many members must survive for the
	// guests to fit. Nil means unknown, and unknown is treated as "any single
	// survivor will do" -- optimistic, and better stated than silently assumed.
	MinHosts *int `db:"min_hosts"`
	// CPUOvercommit is how far this cluster's CPU may be oversubscribed, in
	// hundredths: 300 is 3.0:1. Declared by its operator and NEVER inferred
	// from observed load -- a quiet cluster would raise its own apparent safe
	// ratio and licence exactly the overcommitment the finding exists to catch.
	//
	// Memory has no equivalent on purpose: it is rarely overcommitted and the
	// failure mode differs from CPU contention, so one ratio covering both
	// would be a single number pretending to be two.
	CPUOvercommit *int `db:"cpu_overcommit"`
	// CostSplitCPU is what percent of this cluster's cost is attributable to
	// CPU; memory takes the remainder (migration 00048). Nil means nobody has
	// decided, and nil divides NO money -- there is no conservative reading of
	// an undeclared split the way there is for an undeclared overcommit ratio.
	CostSplitCPU *int    `db:"cost_split_cpu"`
	Description  *string `db:"description"`
	Lifecycle    string  `db:"lifecycle"`
	CreatedAt    *string `db:"created_at"`
	UpdatedAt    *string `db:"updated_at"`
	RowVersion   int     `db:"row_version"`
}

// NewCluster validates and constructs.
func NewCluster(id, name, kind string) (*Cluster, error) {
	c := &Cluster{
		ID: id, Name: strings.TrimSpace(name), Kind: strings.TrimSpace(kind),
		HAPolicy: HANone, Lifecycle: LifecycleActive,
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Validate checks the kind, the policy and the capacity floor.
func (c *Cluster) Validate() error {
	ve := &ValidationError{}
	if strings.TrimSpace(c.Name) == "" {
		ve.Add("name", "a cluster needs a name")
	}
	known := false
	for _, k := range ClusterKinds {
		if c.Kind == k {
			known = true
		}
	}
	if !known {
		ve.Add("kind", "%q is not a cluster technology", c.Kind)
	}
	if c.CPUOvercommit != nil && (*c.CPUOvercommit < 100 || *c.CPUOvercommit > 6400) {
		ve.Add("cpu_overcommit", "must be between 1.0 and 64.0 to one")
	}
	if c.CostSplitCPU != nil && (*c.CostSplitCPU < 0 || *c.CostSplitCPU > 100) {
		ve.Add("cost_split_cpu", "is a percent of the cluster's cost, so it lies between 0 and 100")
	}
	if c.HAPolicy != HANone && c.HAPolicy != HARestart {
		ve.Add("ha_policy", "%q is not an HA policy", c.HAPolicy)
	}
	if c.MinHosts != nil && *c.MinHosts < 1 {
		ve.Add("min_hosts", "a cluster needs at least one surviving host to carry anything")
	}
	if c.Lifecycle != LifecycleActive && c.Lifecycle != LifecycleRetired {
		ve.Add("lifecycle", "%q is not a lifecycle", c.Lifecycle)
	}
	return ve.OrNil()
}

// Retired reports whether the cluster has been withdrawn.
func (c *Cluster) Retired() bool { return c.Lifecycle == LifecycleRetired }

// Relocation is what a cluster can do for the guests of a failed host.
type Relocation string

const (
	// RelocateNotConfigured: the cluster's policy is none, so guests stay down.
	RelocateNotConfigured Relocation = "not_configured"
	// RelocateOK: enough members survive, so guests restart elsewhere.
	RelocateOK Relocation = "relocated"
	// RelocateNoCapacity: HA is configured and too few members are left, so the
	// guests have nowhere to go. This is the finding worth having -- an estate
	// that believes it has HA and does not.
	RelocateNoCapacity Relocation = "no_capacity"
)

// CanRelocate decides what happens to the guests of failed members.
//
// ONE EXPRESSION OF THE RULE, because the engine and the report must not be
// able to disagree about whether a guest moved. surviving is how many members
// are still up; minHosts is the capacity floor, nil meaning unknown.
//
// UNKNOWN CAPACITY IS OPTIMISTIC, and deliberately so: an estate that has not
// worked out how many hosts its guests need gets "any survivor will do", which
// is what the operator believes and is therefore the belief worth testing
// against reality. Being pessimistic instead would report outages that will not
// happen and teach people to ignore the number.
func CanRelocate(policy string, surviving int, minHosts *int) Relocation {
	if policy != HARestart {
		return RelocateNotConfigured
	}
	need := 1
	if minHosts != nil && *minHosts > 1 {
		need = *minHosts
	}
	if surviving >= need {
		return RelocateOK
	}
	return RelocateNoCapacity
}

// ClusterMember is one host in a cluster. A set row: no id, no lifecycle,
// replaced wholesale with its cluster.
type ClusterMember struct {
	ClusterID string `db:"cluster_id"`
	AssetID   string `db:"asset_id"`
}
