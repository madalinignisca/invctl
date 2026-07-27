package domain

import (
	"errors"
	"testing"
	"time"
)

func ptrInt(n int) *int       { return &n }
func ptrStr(s string) *string { return &s }

// TestEvaluateCapacity is the table the impact engine's phase 2 is built on.
// Each row is a scenario an operator would recognise.
func TestEvaluateCapacity(t *testing.T) {
	tests := []struct {
		name         string
		availability string
		minHealthy   *int
		failover     *string
		instances    []InstanceHealth
		want         Status
	}{
		{
			name: "standalone alive", availability: AvailStandalone,
			instances: []InstanceHealth{{Alive: true}},
			want:      StatusOK,
		},
		{
			name: "standalone lost", availability: AvailStandalone,
			instances: []InstanceHealth{{Alive: false}},
			want:      StatusDown,
		},
		{
			// The headline case: losing one of three Vault nodes is not an
			// outage, and reporting it as one makes the whole tool noise.
			name: "quorum survives one of three", availability: AvailQuorum,
			instances: []InstanceHealth{{Alive: true}, {Alive: true}, {Alive: false}},
			want:      StatusOK,
		},
		{
			name: "quorum lost with two of three", availability: AvailQuorum,
			instances: []InstanceHealth{{Alive: true}, {Alive: false}, {Alive: false}},
			want:      StatusDown,
		},
		{
			name: "quorum of five survives two losses", availability: AvailQuorum,
			instances: []InstanceHealth{
				{Alive: true}, {Alive: true}, {Alive: true}, {Alive: false}, {Alive: false},
			},
			want: StatusOK,
		},
		{
			name: "quorum of five fails at three losses", availability: AvailQuorum,
			instances: []InstanceHealth{
				{Alive: true}, {Alive: true}, {Alive: false}, {Alive: false}, {Alive: false},
			},
			want: StatusDown,
		},
		{
			name: "active_active above minimum", availability: AvailActiveActive,
			minHealthy: ptrInt(2),
			instances: []InstanceHealth{
				{Alive: true}, {Alive: true}, {Alive: false},
			},
			want: StatusOK,
		},
		{
			name: "active_active below minimum degrades", availability: AvailActiveActive,
			minHealthy: ptrInt(2),
			instances: []InstanceHealth{
				{Alive: true}, {Alive: false}, {Alive: false},
			},
			want: StatusDegraded,
		},
		{
			name: "active_active with nothing left is down", availability: AvailActiveActive,
			minHealthy: ptrInt(1),
			instances:  []InstanceHealth{{Alive: false}, {Alive: false}},
			want:       StatusDown,
		},
		{
			// Manual promotion needs a human, so it is degraded until one acts.
			name: "active_passive loses primary with manual failover", availability: AvailActivePassive,
			failover: ptrStr(FailoverManual),
			instances: []InstanceHealth{
				{Role: RolePrimary, Alive: false}, {Role: RoleStandby, Alive: true},
			},
			want: StatusDegraded,
		},
		{
			// Automatic promotion is a blip, not an outage.
			name: "active_passive loses primary with automatic failover", availability: AvailActivePassive,
			failover: ptrStr(FailoverAuto),
			instances: []InstanceHealth{
				{Role: RolePrimary, Alive: false}, {Role: RoleStandby, Alive: true},
			},
			want: StatusOK,
		},
		{
			// The standby is not serving, so losing it costs nothing today.
			name: "active_passive loses standby", availability: AvailActivePassive,
			failover: ptrStr(FailoverManual),
			instances: []InstanceHealth{
				{Role: RolePrimary, Alive: true}, {Role: RoleStandby, Alive: false},
			},
			want: StatusOK,
		},
		{
			name: "active_passive loses both", availability: AvailActivePassive,
			failover: ptrStr(FailoverManual),
			instances: []InstanceHealth{
				{Role: RolePrimary, Alive: false}, {Role: RoleStandby, Alive: false},
			},
			want: StatusDown,
		},
		{
			name: "sharded keeps every shard", availability: AvailSharded,
			instances: []InstanceHealth{
				{Shard: "a", Alive: true}, {Shard: "a", Alive: false}, {Shard: "b", Alive: true},
			},
			want: StatusOK,
		},
		{
			name: "sharded loses a whole shard", availability: AvailSharded,
			instances: []InstanceHealth{
				{Shard: "a", Alive: false}, {Shard: "b", Alive: true},
			},
			want: StatusDegraded,
		},
		{
			// Not affected by an outage it does not participate in.
			name: "no instances at all", availability: AvailStandalone,
			instances: nil,
			want:      StatusOK,
		},
		{
			name: "nothing lost", availability: AvailQuorum,
			instances: []InstanceHealth{{Alive: true}, {Alive: true}, {Alive: true}},
			want:      StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &Service{
				Availability: tc.availability,
				MinHealthy:   tc.minHealthy,
				FailoverMode: tc.failover,
			}
			if got := svc.EvaluateCapacity(tc.instances); got != tc.want {
				t.Errorf("EvaluateCapacity = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestNewServiceValidates covers the pairings the database CHECK cannot
// express portably.
func TestNewServiceValidates(t *testing.T) {
	now := time.Now()
	base := ServiceSpec{
		Code: "svc", Name: "Service", Kind: SvcAPI,
		EnvironmentID: "env-1", Availability: AvailStandalone, Tier: 3,
	}

	tests := []struct {
		name      string
		mutate    func(*ServiceSpec)
		wantField string
	}{
		{name: "valid", mutate: func(*ServiceSpec) {}},
		{
			name:      "active_active needs a minimum",
			mutate:    func(s *ServiceSpec) { s.Availability = AvailActiveActive },
			wantField: "min_healthy",
		},
		{
			name: "active_active minimum must be positive",
			mutate: func(s *ServiceSpec) {
				s.Availability = AvailActiveActive
				s.MinHealthy = ptrInt(0)
			},
			wantField: "min_healthy",
		},
		{
			name:      "active_passive needs a failover mode",
			mutate:    func(s *ServiceSpec) { s.Availability = AvailActivePassive },
			wantField: "failover_mode",
		},
		{
			name:      "unknown availability policy",
			mutate:    func(s *ServiceSpec) { s.Availability = "best_effort" },
			wantField: "availability",
		},
		{
			name:      "unknown kind",
			mutate:    func(s *ServiceSpec) { s.Kind = "middleware" },
			wantField: "kind",
		},
		{
			name:      "tier out of range",
			mutate:    func(s *ServiceSpec) { s.Tier = 9 },
			wantField: "tier",
		},
		{
			name:      "code is required",
			mutate:    func(s *ServiceSpec) { s.Code = "   " },
			wantField: "code",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := base
			tc.mutate(&spec)

			svc, err := NewService("id-1", spec, now)
			if tc.wantField == "" {
				if err != nil {
					t.Fatalf("NewService: %v", err)
				}
				if svc.Lifecycle != LifecycleActive {
					t.Errorf("lifecycle = %q, want active", svc.Lifecycle)
				}
				if svc.Attrs != "{}" {
					t.Errorf("attrs = %q, want an empty JSON object", svc.Attrs)
				}
				return
			}
			if err == nil {
				t.Fatalf("NewService succeeded, want a %s failure", tc.wantField)
			}
			// Handlers branch on the sentinel, so it has to match.
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("error does not match ErrInvalid: %v", err)
			}
			ve, ok := AsValidation(err)
			if !ok {
				t.Fatalf("error is not a ValidationError: %v", err)
			}
			if _, present := ve.Messages()[tc.wantField]; !present {
				t.Errorf("messages = %v, want a %s entry", ve.Messages(), tc.wantField)
			}
		})
	}
}

// TestServiceCodeIsNormalised: a code is an identifier people paste, so
// casing must not create a second service.
func TestServiceCodeIsNormalised(t *testing.T) {
	svc, err := NewService("id-1", ServiceSpec{
		Code: "  Orders-API  ", Name: "Orders", Kind: SvcAPI,
		EnvironmentID: "env-1", Availability: AvailStandalone, Tier: 2,
	}, time.Now())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if svc.Code != "orders-api" {
		t.Errorf("code = %q, want %q", svc.Code, "orders-api")
	}
}

func TestStatusWorseIsMonotonic(t *testing.T) {
	tests := []struct {
		a, b, want Status
	}{
		{StatusOK, StatusOK, StatusOK},
		{StatusOK, StatusDegraded, StatusDegraded},
		{StatusDegraded, StatusOK, StatusDegraded},
		{StatusDegraded, StatusDown, StatusDown},
		{StatusDown, StatusDegraded, StatusDown},
		{StatusDown, StatusOK, StatusDown},
	}
	for _, tc := range tests {
		if got := tc.a.Worse(tc.b); got != tc.want {
			t.Errorf("%s.Worse(%s) = %s, want %s", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestAssetCanHostInstances(t *testing.T) {
	// Placing a workload on a rack or a patch panel is a data-entry mistake
	// that the impact engine would otherwise inherit silently.
	canHost := []string{KindServer, KindHypervisor, KindVM, KindK8sNode, KindCluster}
	cannot := []string{KindSite, KindRack, KindPDU, KindPatchPanel}

	for _, kind := range canHost {
		a := &Asset{Kind: kind}
		if !a.CanHostInstances() {
			t.Errorf("a %s should be able to host instances", kind)
		}
	}
	for _, kind := range cannot {
		a := &Asset{Kind: kind}
		if a.CanHostInstances() {
			t.Errorf("a %s should not be able to host instances", kind)
		}
	}
}
