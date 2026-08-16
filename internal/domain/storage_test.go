// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain_test

import (
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// TestReplicationIsArithmeticNobodyHasToRemember.
//
// The reason raw is what gets recorded: an operator knows how many disks are in
// the box, and how much survives three-times replication is a factor people get
// wrong in conversation. A pool that stored usable directly would be trusting
// whoever typed it to have applied the right ratio.
func TestReplicationIsArithmeticNobodyHasToRemember(t *testing.T) {
	cases := []struct {
		name       string
		rawGB      int
		perUsable  *int
		wantUsable int
		wantLostGB int
	}{
		{"three-times replication", 30720, ptr(300), 10240, 20480},
		{"mirrored", 2048, ptr(200), 1024, 1024},
		{"raid6 on eight disks", 8000, ptr(133), 6015, 1985},
		{"local disk", 1000, ptr(100), 1000, 0},
		{"no kind declared reports what was recorded", 1000, nil, 1000, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := domain.NewStoragePool("p1", "pool", "", "", ptr(tc.rawGB), tc.perUsable)
			if got := p.UsableGB(); got != tc.wantUsable {
				t.Errorf("usable is %d GB, want %d", got, tc.wantUsable)
			}
			if got := p.LostToRedundancyGB(); got != tc.wantLostGB {
				t.Errorf("lost to redundancy is %d GB, want %d", got, tc.wantLostGB)
			}
			if p.UsableGB()+p.LostToRedundancyGB() != tc.rawGB {
				t.Error("usable plus lost does not account for the raw capacity")
			}
		})
	}
}

// TestAnUnmeasuredPoolReportsNothingRatherThanZero. The rule the whole estate
// follows: an inventory that has recorded nothing must say it knows nothing,
// never that everything fits.
func TestAnUnmeasuredPoolReportsNothingRatherThanZero(t *testing.T) {
	p := domain.NewStoragePool("p1", "pool", "ceph_3x", "Ceph", nil, ptr(300))
	if p.Measured() {
		t.Error("a pool nobody has measured reports itself as measured")
	}
	if p.UsableGB() != 0 || p.LostToRedundancyGB() != 0 {
		t.Error("an unmeasured pool invented a capacity")
	}
	o := domain.StorageOccupancy{Pool: p, ClaimedGB: 500}
	if o.Oversubscribed() {
		t.Error("a pool nobody has measured is reported as oversubscribed, " +
			"which is a claim about a number that does not exist")
	}
}

// TestAThinlyProvisionedPoolIsReportedNotClamped. Promising more than a pool
// holds is ordinary practice, not an impossibility, so the figure survives
// intact and FreeGB stops at zero rather than going negative.
func TestAThinlyProvisionedPoolIsReportedNotClamped(t *testing.T) {
	p := domain.NewStoragePool("p1", "block", "ceph_3x", "Ceph", ptr(3000), ptr(300))
	o := domain.StorageOccupancy{Pool: p, ClaimedGB: 1500} // 1000 usable
	if !o.Oversubscribed() {
		t.Error("a pool promised more than it holds does not say so")
	}
	if o.ClaimedGB != 1500 {
		t.Error("the claim was clamped, which hides the decision somebody made")
	}
	if o.FreeGB() != 0 {
		t.Errorf("free space is %d GB, want 0 and never negative", o.FreeGB())
	}
}

// TestCapacityHeldByNoProjectIsItsOwnSubject.
//
// Dropping it would make the remaining shares add up to a whole that was never
// the pool -- §5.3's rule, arrived at from the other direction. It is also a
// different fact from idle: idle is unclaimed, this is claimed by somebody
// nobody has written down.
func TestCapacityHeldByNoProjectIsItsOwnSubject(t *testing.T) {
	claims := []domain.StorageClaim{
		{AssetID: "vm-a", PoolID: "block", AllocatedGB: 200},
		{AssetID: "vm-b", PoolID: "block", AllocatedGB: 150},
		{AssetID: "vm-orphan", PoolID: "block", AllocatedGB: 50},
	}
	projectOf := map[string]string{"vm-a": "p1", "vm-b": "p1"} // orphan has none
	nameOf := map[string]string{"p1": "orders"}

	got := domain.StorageClaims(claims, projectOf, nameOf, nil)
	if len(got) != 2 {
		t.Fatalf("got %d subjects, want the project and the unattributed", len(got))
	}
	if got[0].Subject != "orders" || got[0].Amount != 350 {
		t.Errorf("the project holds %d GB as %q, want 350 as orders",
			got[0].Amount, got[0].Subject)
	}
	if got[1].Subject != domain.UnattributedSubject || got[1].Amount != 50 {
		t.Errorf("unattributed capacity is %d GB as %q, want 50 as %q",
			got[1].Amount, got[1].Subject, domain.UnattributedSubject)
	}

	// And it divides: every gigabyte in the pool lands in a slice.
	d := domain.Divide("Block storage", "GB", 1000, got)
	total := 0
	for _, s := range d.Total() {
		total += s.Amount
	}
	if total != 1000 {
		t.Errorf("the slices account for %d GB of a 1000 GB pool", total)
	}
}

// TestASharedMachineDividesItsDiskToo. Cores and disk follow the same
// declaration: a box four tenants share does not hold its storage for one of
// them. The undeclared remainder is carried to nobody so the slices still
// account for the whole pool.
func TestASharedMachineDividesItsDiskToo(t *testing.T) {
	claims := []domain.StorageClaim{
		{AssetID: "vm-shared", PoolID: "block", AllocatedGB: 100},
	}
	shared := map[string]*domain.Occupancy{
		"vm-shared": {AssetID: "vm-shared", Occupants: []domain.Occupant{
			{ProjectID: "p1", Percent: 60},
			{ProjectID: "p2", Percent: 30},
			// 90% declared: a tenth belongs to nobody and must stay visible.
		}},
	}
	names := map[string]string{"p1": "orders", "p2": "platform"}

	got := domain.StorageClaims(claims, map[string]string{}, names, shared)
	byName := map[string]int{}
	for _, c := range got {
		byName[c.Subject] = c.Amount
	}
	if byName["orders"] != 60 || byName["platform"] != 30 {
		t.Errorf("the tenants hold %v, want 60 and 30", byName)
	}
	if byName[domain.UnattributedSubject] != 10 {
		t.Errorf("the undeclared tenth is %d, want 10 held by nobody",
			byName[domain.UnattributedSubject])
	}
}
