package domain

import (
	"testing"
	"time"
)

// TestPropagate is the nature table from HANDOVER §6 phase 3, asserted row by
// row. Getting one of these wrong makes every impact report subtly dishonest,
// which is worse than having no report.
func TestPropagate(t *testing.T) {
	const window = 600

	tests := []struct {
		name            string
		nature          string
		tolerance       *int
		provider        Status
		windowSeconds   int
		wantStatus      Status
		wantWontRestart bool
	}{
		// hard: the only nature that transmits an outage at full strength.
		{name: "hard with provider down", nature: NatureHard, provider: StatusDown, wantStatus: StatusDown},
		{name: "hard with provider degraded", nature: NatureHard, provider: StatusDegraded, wantStatus: StatusDegraded},
		{name: "hard with provider ok", nature: NatureHard, provider: StatusOK, wantStatus: StatusOK},

		// soft: degrades on an outage, unmoved by a degradation.
		{name: "soft with provider down", nature: NatureSoft, provider: StatusDown, wantStatus: StatusDegraded},
		{name: "soft with provider degraded", nature: NatureSoft, provider: StatusDegraded, wantStatus: StatusOK},

		// startup: never changes status, but flags the landmine.
		{
			name: "startup with provider down", nature: NatureStartup, provider: StatusDown,
			wantStatus: StatusOK, wantWontRestart: true,
		},
		{
			name: "startup with provider degraded", nature: NatureStartup, provider: StatusDegraded,
			wantStatus: StatusOK, wantWontRestart: false,
		},
		{
			name: "startup with provider ok", nature: NatureStartup, provider: StatusOK,
			wantStatus: StatusOK, wantWontRestart: false,
		},

		// async: the window is what decides.
		{
			name: "async outage shorter than tolerance", nature: NatureAsync, tolerance: ptrInt(900),
			provider: StatusDown, windowSeconds: window, wantStatus: StatusOK,
		},
		{
			name: "async outage longer than tolerance", nature: NatureAsync, tolerance: ptrInt(300),
			provider: StatusDown, windowSeconds: window, wantStatus: StatusDegraded,
		},
		{
			name: "async tolerance exactly equals the window", nature: NatureAsync, tolerance: ptrInt(600),
			provider: StatusDown, windowSeconds: window, wantStatus: StatusOK,
		},
		{
			// A degraded provider is still answering, so a buffer never drains.
			name: "async with provider degraded", nature: NatureAsync, tolerance: ptrInt(1),
			provider: StatusDegraded, windowSeconds: window, wantStatus: StatusOK,
		},

		// optional: never anything.
		{name: "optional with provider down", nature: NatureOptional, provider: StatusDown, wantStatus: StatusOK},
		{name: "optional with provider degraded", nature: NatureOptional, provider: StatusDegraded, wantStatus: StatusOK},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := &Dependency{Nature: tc.nature, ToleranceSeconds: tc.tolerance}
			got := d.Propagate(tc.provider, tc.windowSeconds)

			if got.Status != tc.wantStatus {
				t.Errorf("status = %s, want %s", got.Status, tc.wantStatus)
			}
			if got.WontRestart != tc.wantWontRestart {
				t.Errorf("wontRestart = %v, want %v", got.WontRestart, tc.wantWontRestart)
			}
		})
	}
}

func TestNewDependencyValidates(t *testing.T) {
	now := time.Now()
	endpointID := "endpoint-1"
	routeID := "route-1"

	base := DependencySpec{
		ConsumerServiceID:  "svc-1",
		ProviderEndpointID: &endpointID,
		Nature:             NatureHard,
		FailureMode:        "Writes fail",
	}

	tests := []struct {
		name      string
		mutate    func(*DependencySpec)
		wantField string
	}{
		{name: "valid", mutate: func(*DependencySpec) {}},
		{
			// The table CHECK enforces this too, but a portable CHECK cannot
			// produce a message anyone can act on.
			name: "both provider kinds set",
			mutate: func(s *DependencySpec) {
				s.ProviderRouteID = &routeID
			},
			wantField: "provider",
		},
		{
			name: "neither provider kind set",
			mutate: func(s *DependencySpec) {
				s.ProviderEndpointID = nil
			},
			wantField: "provider",
		},
		{
			name:      "async needs a tolerance",
			mutate:    func(s *DependencySpec) { s.Nature = NatureAsync },
			wantField: "tolerance_seconds",
		},
		{
			// Forcing the author to write down what breaks is most of the
			// value of recording the edge at all.
			name:      "failure mode is required",
			mutate:    func(s *DependencySpec) { s.FailureMode = "  " },
			wantField: "failure_mode",
		},
		{
			name:      "unknown nature",
			mutate:    func(s *DependencySpec) { s.Nature = "eventual" },
			wantField: "nature",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := base
			tc.mutate(&spec)

			dep, err := NewDependency("dep-1", spec, now)
			if tc.wantField == "" {
				if err != nil {
					t.Fatalf("NewDependency: %v", err)
				}
				if dep.Source != SourceDeclared {
					t.Errorf("source = %q, want declared", dep.Source)
				}
				if dep.Lifecycle != LifecycleActive {
					t.Errorf("lifecycle = %q, want active", dep.Lifecycle)
				}
				return
			}
			if err == nil {
				t.Fatalf("NewDependency succeeded, want a %s failure", tc.wantField)
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

// TestEmptyProviderStringsBecomeNull: an HTML form submits "" for an unselected
// option, and an empty string would satisfy neither the Go check nor the table
// CHECK in the way the author expects.
func TestEmptyProviderStringsBecomeNull(t *testing.T) {
	empty := ""
	endpointID := "endpoint-1"

	d := &Dependency{
		ConsumerServiceID:  "svc-1",
		ProviderEndpointID: &endpointID,
		ProviderRouteID:    &empty,
		Nature:             NatureHard,
		FailureMode:        "Writes fail",
		Source:             SourceDeclared,
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if d.ProviderRouteID != nil {
		t.Errorf("provider_route_id = %q, want nil so the table CHECK is satisfied", *d.ProviderRouteID)
	}
}

func TestEndpointPortPathExclusivity(t *testing.T) {
	port := 8443
	path := "/var/run/app.sock"

	tests := []struct {
		name      string
		endpoint  Endpoint
		wantField string
	}{
		{
			name: "tcp with a port",
			endpoint: Endpoint{
				ServiceID: "s", Name: "https", L4Proto: ProtoTCP, Port: &port,
				BindScope: BindHost, TLSMode: "tls", Exposure: "internal",
			},
		},
		{
			name: "unix with a path",
			endpoint: Endpoint{
				ServiceID: "s", Name: "local", L4Proto: ProtoUnix, UnixPath: &path,
				BindScope: BindUnix, TLSMode: "none", Exposure: "internal",
			},
		},
		{
			name: "tcp without a port",
			endpoint: Endpoint{
				ServiceID: "s", Name: "https", L4Proto: ProtoTCP,
				BindScope: BindHost, TLSMode: "none", Exposure: "internal",
			},
			wantField: "port",
		},
		{
			name: "unix with a port",
			endpoint: Endpoint{
				ServiceID: "s", Name: "local", L4Proto: ProtoUnix, UnixPath: &path, Port: &port,
				BindScope: BindUnix, TLSMode: "none", Exposure: "internal",
			},
			wantField: "port",
		},
		{
			name: "unix without a path",
			endpoint: Endpoint{
				ServiceID: "s", Name: "local", L4Proto: ProtoUnix,
				BindScope: BindUnix, TLSMode: "none", Exposure: "internal",
			},
			wantField: "unix_path",
		},
		{
			name: "port out of range",
			endpoint: Endpoint{
				ServiceID: "s", Name: "https", L4Proto: ProtoTCP, Port: ptrInt(70000),
				BindScope: BindHost, TLSMode: "none", Exposure: "internal",
			},
			wantField: "port",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := tc.endpoint
			err := e.Validate()
			if tc.wantField == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			ve, ok := AsValidation(err)
			if !ok {
				t.Fatalf("Validate returned %v, want a ValidationError", err)
			}
			if _, present := ve.Messages()[tc.wantField]; !present {
				t.Errorf("messages = %v, want a %s entry", ve.Messages(), tc.wantField)
			}
		})
	}
}

func TestTimeRoundTripSortsLexicographically(t *testing.T) {
	earlier := FormatTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	later := FormatTime(time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC))

	// ORDER BY on a TEXT column depends entirely on this.
	if !(earlier < later) {
		t.Errorf("%q should sort before %q", earlier, later)
	}

	// A non-UTC input must still store as UTC, or the ordering breaks for
	// rows written from different offsets.
	zone := time.FixedZone("UTC+5", 5*3600)
	stored := FormatTime(time.Date(2026, 1, 2, 8, 4, 5, 0, zone))
	if stored != earlier {
		t.Errorf("FormatTime with an offset = %q, want %q", stored, earlier)
	}

	parsed, err := ParseTime(earlier)
	if err != nil {
		t.Fatalf("ParseTime: %v", err)
	}
	if !parsed.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("round trip = %v", parsed)
	}
}
