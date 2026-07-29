package domain

import (
	"errors"
	"testing"
	"time"
)

var reachNow = time.Date(2026, 3, 14, 15, 9, 26, 0, time.UTC)

func TestNewNetGroupValidation(t *testing.T) {
	one := 1
	auto := FailoverAuto

	cases := []struct {
		name    string
		spec    NetGroupSpec
		wantErr bool
	}{
		{
			name: "a standalone group needs nothing extra",
			spec: NetGroupSpec{Code: "sw-1", Name: "sw-1", Kind: NetGroupStandalone, Role: NetRoleAccess, Availability: AvailStandalone},
		},
		{
			name:    "active_active without min_healthy is rejected",
			spec:    NetGroupSpec{Code: "sw-2", Name: "sw-2", Kind: NetGroupMCLAG, Role: NetRoleCore, Availability: AvailActiveActive},
			wantErr: true,
		},
		{
			name: "active_active with min_healthy is fine",
			spec: NetGroupSpec{Code: "sw-3", Name: "sw-3", Kind: NetGroupMCLAG, Role: NetRoleCore,
				Availability: AvailActiveActive, MinHealthy: &one},
		},
		{
			name:    "active_passive without failover_mode is rejected",
			spec:    NetGroupSpec{Code: "fw-1", Name: "fw-1", Kind: NetGroupHAPair, Role: NetRoleEdge, Availability: AvailActivePassive},
			wantErr: true,
		},
		{
			name: "active_passive with failover_mode is fine",
			spec: NetGroupSpec{Code: "fw-2", Name: "fw-2", Kind: NetGroupHAPair, Role: NetRoleEdge,
				Availability: AvailActivePassive, FailoverMode: &auto},
		},
		{
			name:    "quorum is not a valid net group availability",
			spec:    NetGroupSpec{Code: "sw-4", Name: "sw-4", Kind: NetGroupCluster, Role: NetRoleCore, Availability: AvailQuorum},
			wantErr: true,
		},
		{
			name:    "an unknown role is rejected",
			spec:    NetGroupSpec{Code: "sw-5", Name: "sw-5", Kind: NetGroupStandalone, Role: "backbone", Availability: AvailStandalone},
			wantErr: true,
		},
		{
			name:    "a missing code is rejected",
			spec:    NetGroupSpec{Name: "no code", Kind: NetGroupStandalone, Role: NetRoleAccess, Availability: AvailStandalone},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewNetGroup("id-1", tc.spec, reachNow)
			if tc.wantErr && !errors.Is(err, ErrInvalid) {
				t.Errorf("error = %v, want ErrInvalid", err)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestNetRoleRank(t *testing.T) {
	if NetRoleRank(NetRoleEdge) >= NetRoleRank(NetRoleCore) {
		t.Error("edge must outrank core")
	}
	if NetRoleRank(NetRoleCore) >= NetRoleRank(NetRoleDistribution) {
		t.Error("core must outrank distribution")
	}
	if NetRoleRank(NetRoleDistribution) >= NetRoleRank(NetRoleAccess) {
		t.Error("distribution must outrank access")
	}
	if NetRoleRank("nonsense") <= NetRoleRank(NetRoleAccess) {
		t.Error("an unrecognised role must sort last, not win an orientation decision")
	}
}

func TestNewNetUplinkRejectsSelfLoop(t *testing.T) {
	_, err := NewNetUplink("id-1", "group-a", "group-a", PlaneData, reachNow)
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("error = %v, want ErrInvalid", err)
	}
}

func TestNewNetUplinkDefaultsPlane(t *testing.T) {
	u, err := NewNetUplink("id-1", "group-a", "group-b", "", reachNow)
	if err != nil {
		t.Fatalf("building uplink: %v", err)
	}
	if u.Plane != PlaneData {
		t.Errorf("plane = %q, want the data-plane default", u.Plane)
	}
}

func TestNewNetAttachmentValidation(t *testing.T) {
	if _, err := NewNetAttachment("id-1", "", "group-a", PlaneData, reachNow); !errors.Is(err, ErrInvalid) {
		t.Errorf("a missing asset_id must be rejected: %v", err)
	}
	a, err := NewNetAttachment("id-1", "asset-1", "group-a", "", reachNow)
	if err != nil {
		t.Fatalf("building attachment: %v", err)
	}
	if a.Plane != PlaneData {
		t.Errorf("plane = %q, want the data-plane default", a.Plane)
	}
	if a.Source != SourceDeclared {
		t.Errorf("source = %q, want declared", a.Source)
	}
}

func TestNewNetAnchorValidation(t *testing.T) {
	if _, err := NewNetAnchor("id-1", "internet", "Internet", "not-a-scope", "group-a", reachNow); !errors.Is(err, ErrInvalid) {
		t.Errorf("an invalid scope must be rejected: %v", err)
	}
	a, err := NewNetAnchor("id-1", "Internet", "Internet", "external", "group-a", reachNow)
	if err != nil {
		t.Fatalf("building anchor: %v", err)
	}
	if a.Code != "internet" {
		t.Errorf("code = %q, want lowercased", a.Code)
	}
	if a.Plane != PlaneData {
		t.Errorf("plane = %q, want the data-plane default", a.Plane)
	}
	if a.Source != SourceDeclared {
		t.Errorf("source = %q, want declared -- an anchor carries the same provenance columns as every other net_* table", a.Source)
	}
}

func TestNewNetGroupMemberDefaultsRole(t *testing.T) {
	m, err := NewNetGroupMember("group-a", "asset-1", "", reachNow)
	if err != nil {
		t.Fatalf("building member: %v", err)
	}
	if m.Role != "member" {
		t.Errorf("role = %q, want the member default", m.Role)
	}
	if _, err := NewNetGroupMember("group-a", "asset-1", "overlord", reachNow); !errors.Is(err, ErrInvalid) {
		t.Errorf("an unrecognised role must be rejected: %v", err)
	}
}
