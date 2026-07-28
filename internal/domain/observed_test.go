package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func at(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := ParseTime(s)
	if err != nil {
		t.Fatalf("parsing %q: %v", s, err)
	}
	return v
}

// TestParseHealthState. Rule 13: an unmapped vendor value is rejected with the
// offending value echoed, never coerced. Coercion here is what puts `firing` in
// front of an operator as though it meant something in this vocabulary.
func TestParseHealthState(t *testing.T) {
	tests := []struct {
		in      string
		want    HealthState
		wantErr bool
	}{
		{in: "up", want: HealthUp},
		{in: "degraded", want: HealthDegraded},
		{in: "down", want: HealthDown},
		{in: "unknown", want: HealthUnknown},
		// Vendor vocabularies belong in a per-reporter adapter.
		{in: "firing", wantErr: true},
		{in: "NotReady", wantErr: true},
		{in: "2", wantErr: true},
		{in: "running", wantErr: true},
		{in: "", wantErr: true},
		// Normalising an input IS a mapping decision, and mapping belongs in
		// the adapter where it can be reviewed.
		{in: "UP", wantErr: true},
		{in: " up ", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseHealthState(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseHealthState(%q) = %q, want an error", tc.in, got)
				}
				if !errors.Is(err, ErrInvalid) {
					t.Errorf("error does not map to ErrInvalid: %v", err)
				}
				if tc.in != "" && !strings.Contains(err.Error(), tc.in) {
					t.Errorf("error must echo the offending value, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseHealthState(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestNewObservation covers rules 4, 5 and 13 at the constructor boundary.
func TestNewObservation(t *testing.T) {
	now := at(t, "2026-07-28T12:00:00Z")
	base := ObservationSpec{
		EntityType:      ObservableAsset,
		EntityID:        "asset-1",
		State:           "down",
		ReportedAt:      "2026-07-28T11:59:58Z",
		IntervalSeconds: 30,
	}
	with := func(mutate func(*ObservationSpec)) ObservationSpec {
		s := base
		mutate(&s)
		return s
	}

	tests := []struct {
		name      string
		spec      ObservationSpec
		reporter  string
		wantErr   bool
		wantField string
	}{
		{name: "valid", spec: base, reporter: "prom-1"},
		{
			name: "unknown entity type", spec: with(func(s *ObservationSpec) { s.EntityType = "service" }),
			reporter: "prom-1", wantErr: true, wantField: "entity_type",
		},
		{
			name: "no entity id", spec: with(func(s *ObservationSpec) { s.EntityID = " " }),
			reporter: "prom-1", wantErr: true, wantField: "entity_id",
		},
		{
			// Rule 4: RFC3339 TEXT sorts lexicographically, so a future date
			// pins to the top of every ORDER BY and wins every monotonicity
			// comparison from here on -- this entity could never be updated
			// again.
			name: "clock more than 300s ahead", spec: with(func(s *ObservationSpec) {
				s.ReportedAt = "2026-07-28T12:06:00Z"
			}),
			reporter: "prom-1", wantErr: true, wantField: "reported_at",
		},
		{
			name: "clock inside the skew allowance", spec: with(func(s *ObservationSpec) {
				s.ReportedAt = "2026-07-28T12:04:00Z"
			}),
			reporter: "prom-1",
		},
		{
			name: "unparseable timestamp", spec: with(func(s *ObservationSpec) {
				s.ReportedAt = "28/07/2026 11:59"
			}),
			reporter: "prom-1", wantErr: true, wantField: "reported_at",
		},
		{
			// Rule 8: without a declared cadence a reading can never be shown
			// to have gone quiet, and a dead collector looks exactly like a
			// healthy estate.
			name: "no interval", spec: with(func(s *ObservationSpec) { s.IntervalSeconds = 0 }),
			reporter: "prom-1", wantErr: true, wantField: "interval_seconds",
		},
		{
			name: "unmapped state", spec: with(func(s *ObservationSpec) { s.State = "firing" }),
			reporter: "prom-1", wantErr: true, wantField: "state",
		},
		{
			// ':' is AgentActorPrefix's separator; allowing it lets one
			// credential id spell another's namespaced actor.
			name: "reporter carrying a namespace", spec: base,
			reporter: "monitor:prom-1", wantErr: true, wantField: "reporter",
		},
		{name: "no reporter", spec: base, reporter: "", wantErr: true, wantField: "reporter"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewObservation(tc.spec, tc.reporter, now)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("NewObservation: %v", err)
				}
				if got.Reporter != tc.reporter {
					t.Errorf("reporter = %q, want %q", got.Reporter, tc.reporter)
				}
				return
			}
			if err == nil {
				t.Fatal("expected a validation error")
			}
			ve, ok := AsValidation(err)
			if !ok {
				t.Fatalf("expected a *ValidationError, got %T", err)
			}
			if _, named := ve.Messages()[tc.wantField]; !named {
				t.Errorf("expected an error on %q, got %v", tc.wantField, ve.Messages())
			}
		})
	}
}

// TestObservationSupersedes. Rule 4: a retry is newer or identical, a replay is
// older. Applying an older report turns a delayed `down` landing after `up`
// into two transitions that never happened, pointing an incident review at the
// wrong cause.
func TestObservationSupersedes(t *testing.T) {
	o := &Observation{ReportedAt: "2026-07-28T12:00:00Z"}
	tests := []struct {
		stored string
		want   bool
	}{
		{stored: "2026-07-28T11:59:59Z", want: true},
		{stored: "2026-07-28T12:00:00Z", want: false},
		{stored: "2026-07-28T12:00:01Z", want: false},
		{stored: "", want: true},
	}
	for _, tc := range tests {
		if got := o.Supersedes(tc.stored); got != tc.want {
			t.Errorf("Supersedes(%q) = %v, want %v", tc.stored, got, tc.want)
		}
	}
}

// TestAssetHealthStaleness. Rule 8: silence is not health. A stale reading
// renders as unknown, never as its last value -- an intruder's first act is
// killing the collector, and under transition-only logging a dead collector and
// a healthy estate are otherwise the same picture, forever.
func TestAssetHealthStaleness(t *testing.T) {
	interval := 30
	tests := []struct {
		name      string
		health    AssetHealth
		now       string
		wantStale bool
		wantState HealthState
	}{
		{
			name:      "fresh",
			health:    AssetHealth{State: HealthDown, LastReportAt: "2026-07-28T12:00:00Z", IntervalSeconds: &interval},
			now:       "2026-07-28T12:01:00Z",
			wantState: HealthDown,
		},
		{
			name:      "exactly at the horizon is still believed",
			health:    AssetHealth{State: HealthUp, LastReportAt: "2026-07-28T12:00:00Z", IntervalSeconds: &interval},
			now:       "2026-07-28T12:01:30Z",
			wantState: HealthUp,
		},
		{
			name:      "past three intervals",
			health:    AssetHealth{State: HealthUp, LastReportAt: "2026-07-28T12:00:00Z", IntervalSeconds: &interval},
			now:       "2026-07-28T12:01:31Z",
			wantStale: true, wantState: HealthUnknown,
		},
		{
			// An unknown horizon is not evidence of freshness.
			name:      "no declared interval",
			health:    AssetHealth{State: HealthUp, LastReportAt: "2026-07-28T12:00:00Z"},
			now:       "2026-07-28T12:00:01Z",
			wantStale: true, wantState: HealthUnknown,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			now := at(t, tc.now)
			if got := tc.health.IsStale(now); got != tc.wantStale {
				t.Errorf("IsStale = %v, want %v", got, tc.wantStale)
			}
			if got := tc.health.DisplayState(now); got != tc.wantState {
				t.Errorf("DisplayState = %q, want %q", got, tc.wantState)
			}
		})
	}
}

// TestNewHealthOverride. Rule 14: reason mandatory, expires_at mandatory and
// capped, and only a person may overrule a monitor.
func TestNewHealthOverride(t *testing.T) {
	now := at(t, "2026-07-28T12:00:00Z")
	user := Actor{ID: "01931-user", Name: "alice", Kind: ActorKindUser}
	agent, err := AgentActor("prom-1")
	if err != nil {
		t.Fatalf("AgentActor: %v", err)
	}
	base := HealthOverrideSpec{
		EntityType:    ObservableAsset,
		EntityID:      "asset-1",
		AssertedState: "up",
		Reason:        "the probe is reading the wrong port; INC-4412",
		ExpiresAt:     now.Add(4 * time.Hour),
	}
	with := func(mutate func(*HealthOverrideSpec)) HealthOverrideSpec {
		s := base
		mutate(&s)
		return s
	}

	tests := []struct {
		name      string
		spec      HealthOverrideSpec
		actor     Actor
		wantErr   bool
		wantField string
	}{
		{name: "valid", spec: base, actor: user},
		{
			// Forcing the author to write down why is most of the value.
			name: "no reason", spec: with(func(s *HealthOverrideSpec) { s.Reason = "   " }),
			actor: user, wantErr: true, wantField: "reason",
		},
		{
			// A permanent override is how a real outage stays invisible for six
			// weeks.
			name: "no expiry", spec: with(func(s *HealthOverrideSpec) { s.ExpiresAt = time.Time{} }),
			actor: user, wantErr: true, wantField: "expires_at",
		},
		{
			name: "beyond the 24h cap", spec: with(func(s *HealthOverrideSpec) {
				s.ExpiresAt = now.Add(MaxOverrideDuration + time.Minute)
			}),
			actor: user, wantErr: true, wantField: "expires_at",
		},
		{
			name: "exactly at the cap", spec: with(func(s *HealthOverrideSpec) {
				s.ExpiresAt = now.Add(MaxOverrideDuration)
			}),
			actor: user,
		},
		{
			name: "already expired", spec: with(func(s *HealthOverrideSpec) {
				s.ExpiresAt = now.Add(-time.Minute)
			}),
			actor: user, wantErr: true, wantField: "expires_at",
		},
		{
			// A compromised monitoring credential must not be able to silence
			// the estate it is reporting on.
			name: "an agent may not override", spec: base,
			actor: agent, wantErr: true, wantField: "actor",
		},
		{
			name: "the seeder may not override either", spec: base,
			actor: SystemActor, wantErr: true, wantField: "actor",
		},
		{
			name: "unmapped asserted state", spec: with(func(s *HealthOverrideSpec) {
				s.AssertedState = "maintenance"
			}),
			actor: user, wantErr: true, wantField: "asserted_state",
		},
		{
			name: "unobservable entity type", spec: with(func(s *HealthOverrideSpec) {
				s.EntityType = "environment"
			}),
			actor: user, wantErr: true, wantField: "entity_type",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewHealthOverride("ovr-1", tc.spec, tc.actor, now)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("NewHealthOverride: %v", err)
				}
				// The opaque id, never the username (docs/DECISIONS.md).
				if got.Actor != tc.actor.ID {
					t.Errorf("actor = %q, want the opaque id %q", got.Actor, tc.actor.ID)
				}
				if got.Actor == tc.actor.Name {
					t.Error("the actor column must not hold a username")
				}
				if !got.IsActiveAt(now) {
					t.Error("a fresh override should be active")
				}
				if got.IsActiveAt(at(t, "2026-07-30T00:00:00Z")) {
					t.Error("an override must lapse without a write")
				}
				return
			}
			if err == nil {
				t.Fatal("expected a validation error")
			}
			ve, ok := AsValidation(err)
			if !ok {
				t.Fatalf("expected a *ValidationError, got %T", err)
			}
			if _, named := ve.Messages()[tc.wantField]; !named {
				t.Errorf("expected an error on %q, got %v", tc.wantField, ve.Messages())
			}
		})
	}
}

// TestAgentActor. Rule 5: attribution is structurally unforgeable, and the
// struct-literal path must be clearly wrong -- a typo in a hand-built actor
// otherwise surfaces as a CHECK failure inside a webhook at 03:00.
func TestAgentActor(t *testing.T) {
	got, err := AgentActor("prom-1")
	if err != nil {
		t.Fatalf("AgentActor: %v", err)
	}
	if got.ID != AgentActorPrefix+"prom-1" {
		t.Errorf("ID = %q, want the namespaced form", got.ID)
	}
	if got.Kind != ActorKindAgent {
		t.Errorf("Kind = %q", got.Kind)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("a constructed agent actor must validate: %v", err)
	}
	if id, ok := got.CredentialID(); !ok || id != "prom-1" {
		t.Errorf("CredentialID() = %q, %v", id, ok)
	}

	for _, bad := range []string{"", "  ", "prom:1", "prom 1", "PROM-1", AgentActorPrefix + "prom-1"} {
		if _, err := AgentActor(bad); err == nil {
			t.Errorf("AgentActor(%q) was accepted", bad)
		}
	}
}

func TestActorValidate(t *testing.T) {
	tests := []struct {
		name    string
		actor   Actor
		wantErr bool
	}{
		{name: "system", actor: SystemActor},
		{name: "seeder", actor: Actor{ID: "seed", Name: "seed", Kind: ActorKindSystem}},
		{name: "user", actor: Actor{ID: "0193-abc", Name: "alice", Kind: ActorKindUser}},
		{
			// The whole point: this is what a hand-built agent actor looks like.
			name:    "hand-built agent without the namespace",
			actor:   Actor{ID: "prom-1", Name: "prom-1", Kind: ActorKindAgent},
			wantErr: true,
		},
		{
			name:    "a person wearing the monitor namespace",
			actor:   Actor{ID: AgentActorPrefix + "prom-1", Kind: ActorKindUser},
			wantErr: true,
		},
		{name: "typo in kind", actor: Actor{ID: "x", Kind: "agentt"}, wantErr: true},
		{name: "no id", actor: Actor{Kind: ActorKindUser}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.actor.Validate()
			if tc.wantErr && err == nil {
				t.Error("expected a validation error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// TestCheckProvenanceWrite. Rule 7: no machine may assert that a fact was
// hand-declared, because that laundering is how a fabricated workload inside an
// in_scope environment renders to an operator as hand-asserted fact.
func TestCheckProvenanceWrite(t *testing.T) {
	agent, err := AgentActor("collector-1")
	if err != nil {
		t.Fatalf("AgentActor: %v", err)
	}
	user := Actor{ID: "0193-abc", Kind: ActorKindUser}

	tests := []struct {
		name    string
		actor   Actor
		source  string
		wantErr bool
	}{
		{name: "operator declares", actor: user, source: SourceDeclared},
		{name: "operator records a discovery", actor: user, source: SourceDiscoveredK8s},
		{name: "agent discovers", actor: agent, source: SourceDiscoveredSystemd},
		{name: "agent launders", actor: agent, source: SourceDeclared, wantErr: true},
		// SystemActor is ALLOWED, and that is a resolution of a contradiction in
		// AUDIT.md rather than a relaxation. Rule 7's text says "only a user
		// actor"; rule 10 names SystemActor as a legitimate writer of declared
		// state ("SystemActor seeds the declared inventory") and UpsertLDAPUser
		// creates accounts as Kind:"system". Both cannot hold once the check is
		// wired into CreateInstance and CreateDependency, and it was verified
		// empirically: the strict reading fails the seed at "seeding instance
		// of vault on vm-vault-1".
		//
		// Agent is the principal the rule is actually about. Laundering matters
		// because a credential arrives over the network and asserts more
		// authority than it was issued; SystemActor is this process, is not
		// reachable from outside, and denying it closes nothing.
		{name: "the seeder and LDAP upsert may still declare", actor: SystemActor, source: SourceDeclared},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckProvenanceWrite(tc.actor, tc.source)
			if tc.wantErr && err == nil {
				t.Error("expected a validation error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// TestServiceInstanceSourceIsValidated. Rule 7 wanted a Go constant set behind
// the new CHECK; the DB constraint is the second line of defence, not the first.
func TestServiceInstanceSourceIsValidated(t *testing.T) {
	now := at(t, "2026-07-28T12:00:00Z")
	si, err := NewServiceInstance("si-1", "svc-1", "asset-1", RuntimeSystemd, 0, now)
	if err != nil {
		t.Fatalf("NewServiceInstance: %v", err)
	}
	if si.Source != SourceDeclared {
		t.Errorf("Source = %q, want %q", si.Source, SourceDeclared)
	}
	// netstat discovers connections, not placements.
	for _, bad := range []string{"", "discovered_netstat", "guessed"} {
		si.Source = bad
		if err := si.Validate(); err == nil {
			t.Errorf("source %q was accepted", bad)
		}
	}
	for _, good := range ServiceInstanceSources {
		si.Source = good
		if err := si.Validate(); err != nil {
			t.Errorf("source %q was rejected: %v", good, err)
		}
	}
}

// TestClassificationIsSelfConsistent guards the hand-maintained census against
// a copy-paste that puts one column in two classes -- at which point
// ClassifyColumn silently returns whichever map it checks first and the census
// looks complete while being wrong.
//
// That the census matches the LIVE schema on both engines is
// TestEveryColumnIsClassified's job; it needs a database, so it lives in the
// store package.
func TestClassificationIsSelfConsistent(t *testing.T) {
	if bad := ClassificationConflicts(); len(bad) > 0 {
		t.Errorf("classification contradicts itself:\n  %s", strings.Join(bad, "\n  "))
	}

	// A column nobody classified has no class. Without this, a default would
	// make the boundary test impossible to write: nothing could ever be
	// unclassified, and a new column would silently inherit a class nobody chose.
	if class, ok := ClassifyColumn("asset", "health_state"); ok {
		t.Errorf("an unlisted column was classified as %q", class)
	}
	if _, ok := ClassifyColumn("no_such_table", "id"); ok {
		t.Error("a column of an unknown table was classified")
	}

	tests := []struct {
		table, column string
		want          ColumnClass
	}{
		// Naming is a hint, not the rule -- these four are the ones people get
		// wrong.
		{"service_instance", "desired_state", ClassDeclared},
		{"dependency", "verified_at", ClassDeclared},
		{"app_user", "last_login_at", ClassObserved},
		{"service_instance", "source", ClassProvenance},
		// Whole-table classification.
		{"asset_health", "state_since", ClassObserved},
		{"observed_transition", "at", ClassObserved},
		{"unmatched_observation", "entity_ref", ClassObserved},
		// An override is a person overruling a monitor: declared.
		{"health_override", "asserted_state", ClassDeclared},
	}
	for _, tc := range tests {
		got, ok := ClassifyColumn(tc.table, tc.column)
		if !ok {
			t.Errorf("%s.%s is unclassified", tc.table, tc.column)
			continue
		}
		if got != tc.want {
			t.Errorf("%s.%s = %q, want %q", tc.table, tc.column, got, tc.want)
		}
	}
}

// TestAFutureTimestampCannotReserveTheFuture.
//
// Rejecting only beyond MaxClockSkew left a weapon inside it. A report stamped
// 295 seconds ahead was accepted and stored as sent, became the monotonicity
// floor, and every truthful report from that credential was then discarded as
// stale for the next five minutes -- re-poison once per window and the entity
// is frozen indefinitely while its collector reports honestly into a black
// hole. The tolerance exists for a collector whose clock is a few seconds fast,
// not to let one reserve a position in the ordering.
func TestAFutureTimestampCannotReserveTheFuture(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	spec := func(offset time.Duration) ObservationSpec {
		return ObservationSpec{
			EntityType: ObservableAsset, EntityID: "a1", State: "up",
			ReportedAt: FormatTime(now.Add(offset)), IntervalSeconds: 30,
		}
	}

	t.Run("a timestamp inside the tolerance is clamped, not stored as sent", func(t *testing.T) {
		obs, err := NewObservation(spec(MaxClockSkew-5*time.Second), "prom-a", now)
		if err != nil {
			t.Fatalf("a report inside the skew tolerance must still be accepted: %v", err)
		}
		if obs.ReportedAt != FormatTime(now) {
			t.Errorf("reported_at = %q, want it clamped to the server clock %q; stored as sent it "+
				"becomes a monotonicity floor no truthful report can clear for %s",
				obs.ReportedAt, FormatTime(now), MaxClockSkew)
		}
	})

	t.Run("beyond the tolerance is still refused outright", func(t *testing.T) {
		if _, err := NewObservation(spec(MaxClockSkew+time.Second), "prom-a", now); err == nil {
			t.Error("a report beyond the skew tolerance was accepted")
		}
	})

	t.Run("a past timestamp is untouched", func(t *testing.T) {
		obs, err := NewObservation(spec(-90*time.Second), "prom-a", now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if obs.ReportedAt != FormatTime(now.Add(-90*time.Second)) {
			t.Errorf("reported_at = %q, want the reporter's own value preserved", obs.ReportedAt)
		}
	})
}
