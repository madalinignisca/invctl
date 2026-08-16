// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

// Storage as a dimension (migration 00046).
//
// THE SAME SHAPE AS capacity.go AND DELIBERATELY SO: raw capacity divided down
// to usable, claims counted against it, and everything nobody has measured
// counted rather than assumed. What differs is where the division comes from --
// a cluster loses capacity to a node it must survive, a pool loses it to
// replication -- and both end up as one number a share divides into.
//
// ONE POOL IS ONE DIMENSION. Block and bulk are different products at different
// prices per GB, and a project holding 350 GB of one and 300 GB of the other
// has two different shares of two different pools. docs/COST-ATTRIBUTION.md
// §5.7 is blunt about it: one storage figure would be meaningless.

// DefaultRawPerUsable is assumed when a pool names no storage kind: one raw
// gigabyte per usable one.
//
// THE PESSIMISTIC DIRECTION IS THE OTHER WAY ROUND HERE, and it is worth being
// explicit about why this is not DefaultCPUOvercommit's reasoning inverted.
// Assuming replication nobody declared would INVENT lost capacity and make a
// pool look smaller -- and a pool that looks smaller makes every project's
// share of it look larger. 1:1 reports exactly what was recorded and no more,
// which is the only assumption that cannot flatter or alarm anybody.
const DefaultRawPerUsable = 100

// StoragePool is one pool's capacity, before claims.
type StoragePool struct {
	AssetID string
	Name    string
	// Kind is the storage_kind code, blank when none is declared.
	Kind      string
	KindLabel string
	// RawGB is nil when nobody has measured it, which is counted and never
	// treated as zero.
	RawGB *int
	// RawPerUsable is hundredths of raw capacity per usable unit: 300 is 3x
	// replication. Declared through the kind, defaulted when there is none.
	RawPerUsable int
	// KindDeclared says whether the ratio came from a kind or from the default.
	KindDeclared bool
}

// UsableGB is what the pool can actually hold. Zero when unmeasured.
//
// Integer division, truncating: a pool reporting one gigabyte less than it has
// is honest in the direction that cannot oversell it.
func (p StoragePool) UsableGB() int {
	if p.RawGB == nil || p.RawPerUsable <= 0 {
		return 0
	}
	return *p.RawGB * 100 / p.RawPerUsable
}

// Measured reports whether anybody has recorded this pool's size.
func (p StoragePool) Measured() bool { return p.RawGB != nil }

// LostToRedundancyGB is raw capacity that redundancy consumes.
//
// REPORTED RATHER THAN FOLDED SILENTLY INTO A RATE, the same argument
// RedundancyPremium makes for compute: three-times replication means two thirds
// of an array buys nothing a reader can put a workload on, and that is a large
// enough number to belong on the page rather than inside a divisor.
func (p StoragePool) LostToRedundancyGB() int {
	if p.RawGB == nil {
		return 0
	}
	return *p.RawGB - p.UsableGB()
}

// StorageClaim is one workload's declared hold on one pool.
type StorageClaim struct {
	AssetID     string
	AssetName   string
	PoolID      string
	AllocatedGB int
	Note        *string
}

// StorageOccupancy is a pool and everything claimed against it.
type StorageOccupancy struct {
	Pool StoragePool
	// ClaimedGB is the sum of the claims, which may legitimately exceed
	// UsableGB -- thin provisioning is normal and overcommitting a pool is a
	// decision, not an impossibility. It is reported, never clamped.
	ClaimedGB int
	Claims    []StorageClaim
}

// Oversubscribed reports whether more has been promised than the pool holds.
func (o StorageOccupancy) Oversubscribed() bool {
	return o.Pool.UsableGB() > 0 && o.ClaimedGB > o.Pool.UsableGB()
}

// FreeGB is what is left, never negative.
func (o StorageOccupancy) FreeGB() int {
	if free := o.Pool.UsableGB() - o.ClaimedGB; free > 0 {
		return free
	}
	return 0
}

// NewStoragePool builds a pool's capacity view from what the row carries.
//
// rawPerUsable is the ratio from the named storage kind, or nil when the pool
// declares no kind.
func NewStoragePool(assetID, name, kind, kindLabel string, rawGB, rawPerUsable *int) StoragePool {
	p := StoragePool{
		AssetID: assetID, Name: name, Kind: kind, KindLabel: kindLabel,
		RawGB: rawGB, RawPerUsable: DefaultRawPerUsable,
	}
	if rawPerUsable != nil && *rawPerUsable > 0 {
		p.RawPerUsable, p.KindDeclared = *rawPerUsable, true
	}
	return p
}

// StorageClaims converts an occupancy into the claims a division divides,
// grouped by whoever owns each workload.
//
// SUBJECTS ARE PROJECTS, NOT WORKLOADS, because the question is what a project
// costs and a project routinely holds several machines in one pool. Workloads
// nobody has attributed to a project are gathered under one subject rather than
// dropped -- capacity held by nothing is exactly the kind of gap this estate
// reports rather than hides, and dropping it would make the remaining shares
// add up to a whole that was never the pool.
func StorageClaims(claims []StorageClaim, projectOf map[string]string,
	nameOf map[string]string, shared map[string]*Occupancy) []Claim {

	byProject := map[string]int{}
	order := []string{}
	add := func(id string, gb int) {
		if _, seen := byProject[id]; !seen {
			order = append(order, id)
		}
		byProject[id] += gb
	}
	for _, c := range claims {
		// A machine several tenants share divides its disk the same way it
		// divides its cores (WP-J5). The undeclared remainder is carried to
		// nobody rather than dropped, so the slices still account for the pool.
		if occ, ok := shared[c.AssetID]; ok && occ.Shared() {
			attributed := 0
			for id, part := range occ.Split(c.AllocatedGB) {
				add(id, part)
				attributed += part
			}
			if rest := c.AllocatedGB - attributed; rest > 0 {
				add("", rest)
			}
			continue
		}
		add(projectOf[c.AssetID], c.AllocatedGB)
	}
	out := make([]Claim, 0, len(order))
	for _, id := range order {
		subject := nameOf[id]
		if id == "" {
			subject = UnattributedSubject
		}
		out = append(out, Claim{SubjectID: id, Subject: subject, Amount: byProject[id]})
	}
	return out
}

// UnattributedSubject names capacity held by workloads no project owns. A
// different fact from idle capacity: idle is unclaimed, this is claimed by
// somebody nobody has written down.
const UnattributedSubject = "held by no project"
