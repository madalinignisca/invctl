// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package impact

import (
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Cluster HA is the first thing here that changes what the engine CONCLUDES
// rather than what it reports beside the conclusion, so these cover both
// directions: a guest that must come back, and a guest that must not.

func threeNode(policy string, minHosts *int) Cluster {
	return Cluster{
		ID: "c1", Name: "prod-pve", HAPolicy: policy, MinHosts: minHosts,
		MemberAssetIDs: []string{"hv-1", "hv-2", "hv-3"},
		GuestsByHost: map[string][]string{
			"hv-1": {"vm-a", "vm-b"},
			"hv-2": {"vm-c"},
			"hv-3": {"vm-d"},
		},
	}
}

func TestAGuestComesBackWhenTheClusterCanAbsorbIt(t *testing.T) {
	down := map[string]bool{"hv-1": true, "vm-a": true, "vm-b": true}
	revive, findings := applyClusterHA([]Cluster{threeNode(domain.HARestart, nil)}, down)

	for _, vm := range []string{"vm-a", "vm-b"} {
		if !revive[vm] {
			t.Errorf("%s was not revived; two hosts survive and the policy is restart", vm)
		}
	}
	if revive["hv-1"] {
		t.Error("the failed HOST was revived, which would undo the outage itself")
	}
	if len(findings) != 1 || !findings[0].Relocated() {
		t.Fatalf("expected one relocation finding, got %+v", findings)
	}
	if findings[0].Guests != 2 {
		t.Errorf("the finding says %d guests, want 2", findings[0].Guests)
	}
}

// TestNoCapacityMeansTheGuestsStayDown. The finding this whole work package is
// worth having: an estate that believes it has HA and has outgrown it.
func TestNoCapacityMeansTheGuestsStayDown(t *testing.T) {
	need := 2
	down := map[string]bool{
		"hv-1": true, "vm-a": true, "vm-b": true,
		"hv-2": true, "vm-c": true,
	}
	revive, findings := applyClusterHA([]Cluster{threeNode(domain.HARestart, &need)}, down)

	if len(revive) != 0 {
		t.Errorf("guests were revived with one host left and two needed: %v", revive)
	}
	if len(findings) != 1 {
		t.Fatalf("expected one finding, got %+v", findings)
	}
	if findings[0].Relocated() {
		t.Error("the finding claims the guests relocated")
	}
	if findings[0].Outcome != domain.RelocateNoCapacity {
		t.Errorf("outcome = %q, want %q", findings[0].Outcome, domain.RelocateNoCapacity)
	}
}

// TestNoHAPolicyBehavesExactlyAsBeforeThisExisted, and says nothing. A cluster
// without HA is what the engine already did, and a finding on every simulation
// saying "this changed nothing" is noise.
func TestNoHAPolicyBehavesExactlyAsBeforeThisExisted(t *testing.T) {
	down := map[string]bool{"hv-1": true, "vm-a": true, "vm-b": true}
	revive, findings := applyClusterHA([]Cluster{threeNode(domain.HANone, nil)}, down)

	if len(revive) != 0 {
		t.Errorf("guests were revived by a cluster with no HA policy: %v", revive)
	}
	if len(findings) != 0 {
		t.Errorf("a cluster with no HA produced findings: %+v", findings)
	}
}

// TestLosingEveryHostRelocatesNothing. Nowhere to go is nowhere to go, whatever
// the policy says.
func TestLosingEveryHostRelocatesNothing(t *testing.T) {
	down := map[string]bool{
		"hv-1": true, "hv-2": true, "hv-3": true,
		"vm-a": true, "vm-b": true, "vm-c": true, "vm-d": true,
	}
	revive, findings := applyClusterHA([]Cluster{threeNode(domain.HARestart, nil)}, down)

	if len(revive) != 0 {
		t.Errorf("guests were revived with no host left at all: %v", revive)
	}
	if len(findings) != 1 || findings[0].Outcome != domain.RelocateNoCapacity {
		t.Fatalf("expected a no-capacity finding, got %+v", findings)
	}
}

// TestNothingDownIsNoFinding.
func TestNothingDownIsNoFinding(t *testing.T) {
	revive, findings := applyClusterHA([]Cluster{threeNode(domain.HARestart, nil)}, map[string]bool{})
	if len(revive) != 0 || len(findings) != 0 {
		t.Errorf("an outage of nothing produced %v / %+v", revive, findings)
	}
}

// TestAFailedHostCarryingNothingIsNotAFinding. Losing an empty member of a
// cluster relocates nothing, and saying "0 guests restarted" is noise.
func TestAFailedHostCarryingNothingIsNotAFinding(t *testing.T) {
	c := threeNode(domain.HARestart, nil)
	delete(c.GuestsByHost, "hv-3")
	_, findings := applyClusterHA([]Cluster{c}, map[string]bool{"hv-3": true})
	if len(findings) != 0 {
		t.Errorf("losing an empty host produced %+v", findings)
	}
}

// TestFailuresSortAboveSuccesses. A cluster that could not absorb the loss must
// not sit under three that did.
func TestFailuresSortAboveSuccesses(t *testing.T) {
	need := 3
	ok := threeNode(domain.HARestart, nil)
	ok.ID, ok.Name = "c-ok", "aaa-fine"
	bad := threeNode(domain.HARestart, &need)
	bad.ID, bad.Name = "c-bad", "zzz-broken"
	bad.MemberAssetIDs = []string{"hv-4", "hv-5"}
	bad.GuestsByHost = map[string][]string{"hv-4": {"vm-x"}}

	_, findings := applyClusterHA([]Cluster{ok, bad},
		map[string]bool{"hv-1": true, "hv-4": true})

	if len(findings) != 2 {
		t.Fatalf("expected two findings, got %+v", findings)
	}
	if findings[0].Relocated() {
		t.Errorf("the first finding is %q, which succeeded; the failure must come first",
			findings[0].ClusterName)
	}
}

// TestTheRelocationRuleIsOneExpression. The engine and any report must not be
// able to disagree about whether a guest moved.
func TestTheRelocationRuleIsOneExpression(t *testing.T) {
	two := 2
	cases := []struct {
		policy    string
		surviving int
		minHosts  *int
		want      domain.Relocation
	}{
		{domain.HANone, 5, nil, domain.RelocateNotConfigured},
		{domain.HARestart, 1, nil, domain.RelocateOK},
		{domain.HARestart, 0, nil, domain.RelocateNoCapacity},
		{domain.HARestart, 2, &two, domain.RelocateOK},
		{domain.HARestart, 1, &two, domain.RelocateNoCapacity},
	}
	for _, tc := range cases {
		if got := domain.CanRelocate(tc.policy, tc.surviving, tc.minHosts); got != tc.want {
			t.Errorf("CanRelocate(%s, %d, %v) = %q, want %q",
				tc.policy, tc.surviving, tc.minHosts, got, tc.want)
		}
	}
}
