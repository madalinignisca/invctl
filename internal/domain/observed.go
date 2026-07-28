// Observed state: the vocabulary for what the estate reports about itself.
//
// The Go side of migration 00008_observed.sql and the normative rules in
// docs/AUDIT.md. Zero external dependencies, like every other file here.
//
// Nothing in this file writes anything. invctl presents state; it never acts on
// the estate (HANDOVER.md §1, docs/DECISIONS.md). Observed health may inform
// what is *displayed* -- labelled as observed, with its reporter and its age --
// because showing is not acting.
package domain

import (
	"strings"
	"time"
)

// HealthState is the observed condition of one entity as one reporter sees it.
//
// It is an enum with a CHECK behind every column that stores one (rule 13). A
// column an external credential authors and an operator reads mid-incident is
// the last place to accept arbitrary strings: vendor vocabularies (`firing`,
// `NotReady`, `2`) are mapped at the adapter, per reporter, and an unmapped
// value is 422 with the offending value echoed -- never coerced, never stored
// raw. That is why ParseHealthState does not case-fold or trim: normalising an
// input is a mapping decision, and mapping belongs in the adapter where it can
// be reviewed.
type HealthState string

const (
	// HealthUp: the reporter can see it and it is serving.
	HealthUp HealthState = "up"
	// HealthDegraded: serving, but not fully.
	HealthDegraded HealthState = "degraded"
	// HealthDown: the reporter can see it and it is not serving.
	HealthDown HealthState = "down"
	// HealthUnknown is a positive assertion of ignorance -- the reporter ran and
	// could not determine the answer. It is also what a stale reading renders
	// as, because silence is not health (rule 8).
	HealthUnknown HealthState = "unknown"
)

// HealthStates is the Go side of every `state` CHECK in 00008_observed.sql.
var HealthStates = []HealthState{HealthUp, HealthDegraded, HealthDown, HealthUnknown}

// String makes HealthState printable and usable as a query parameter.
func (h HealthState) String() string { return string(h) }

// Valid reports whether h is one of the four states.
func (h HealthState) Valid() bool {
	for _, s := range HealthStates {
		if h == s {
			return true
		}
	}
	return false
}

// HealthStateNames renders the vocabulary for an error message or a form.
func HealthStateNames() []string {
	out := make([]string, len(HealthStates))
	for i, s := range HealthStates {
		out[i] = string(s)
	}
	return out
}

// ParseHealthState converts a wire value, echoing the offending value on
// failure so a reporter's adapter can be fixed from the response alone.
func ParseHealthState(v string) (HealthState, error) {
	s := HealthState(v)
	if !s.Valid() {
		ve := &ValidationError{}
		ve.Add("state", "%q is not a health state; map it at the adapter to one of %s",
			v, strings.Join(HealthStateNames(), ", "))
		return "", ve
	}
	return s, nil
}

// What may be observed. The set is closed and mirrored by the entity_type CHECK
// on all four tables in 00008: an observation names a thing that can be up or
// down, and neither a `service` (a logical row with no host) nor an
// `environment` is one of those.
const (
	ObservableAsset           = "asset"
	ObservableServiceInstance = "service_instance"
)

// ObservableEntityTypes is the Go side of the entity_type CHECK on
// asset_health, observed_transition, unmatched_observation and health_override.
var ObservableEntityTypes = []string{ObservableAsset, ObservableServiceInstance}

// Transition kinds (rule 9). A `transition` is one state change; the two flap
// kinds bracket a compressed episode and are written unconditionally, so a
// reader always sees that suppression happened and how much it hid.
const (
	TransitionChange    = "transition"
	TransitionFlapOpen  = "flap_open"
	TransitionFlapClose = "flap_close"
)

// TransitionKinds is the Go side of the observed_transition.kind CHECK.
var TransitionKinds = []string{TransitionChange, TransitionFlapOpen, TransitionFlapClose}

// Flap compression thresholds (rule 9).
//
// These live here -- never in env, never as a per-row column, never in a
// payload -- because a writer that can raise its own suppression threshold has
// a mute button. A stolen token would otherwise trip the floor deliberately so
// that the real `down` five minutes later reads as a monitoring artefact.
const (
	// FlapThreshold is the number of transitions within FlapWindow, per
	// (entity, reporter), above which an episode opens.
	FlapThreshold = 5
	// FlapWindow is the sliding window FlapThreshold is counted over.
	FlapWindow = 5 * time.Minute
)

// MaxIntervalSeconds caps the cadence a reporter may declare. Six hours is far
// slower than any real health collector and still finite, so a silent reporter
// always becomes visibly stale within a working day.
//
// It is a domain constant rather than configuration for the same reason
// FlapThreshold is: a value the reporter can influence is a value the reporter
// can use to hide.
const MaxIntervalSeconds = 6 * 60 * 60

// MaxClockSkew is how far ahead of the server a reporter's own clock may be
// before its report is rejected (rule 4).
//
// A broken or hostile clock otherwise poisons ordering for everything it
// touches: timestamps are RFC3339 UTC TEXT and sort lexicographically, so a
// future date pins to the top of every ORDER BY and no later report can ever
// beat it on monotonicity.
const MaxClockSkew = 300 * time.Second

// StaleAfterIntervals is how many reporting intervals may elapse before a
// reading stops being believed (rule 8). Three is the usual "two may be lost to
// noise, the third is a signal" rule.
const StaleAfterIntervals = 3

// MinInScopeRetentionDays is the floor rule 10 puts under the transition ledger
// for anything resolving to an environment with in_scope = TRUE.
//
// A year, because that is the shape of the question these rows answer: an audit
// or a scope validation asks "was this thing reachable, and when", and it asks
// about a period that has already closed. A retention setting shorter than the
// review cycle it feeds is indistinguishable from having no evidence at all,
// and the operator who set it will not be the one who discovers that.
//
// It is a floor, not a default. An operator may keep more; the prune simply
// refuses to take an in-scope entity's history below it, per row, whatever the
// run was asked for.
const MinInScopeRetentionDays = 365

// InScopeRetentionFloor is the earliest instant a prune may reach for an entity
// inside an in_scope environment.
//
// Calendar arithmetic rather than 365*24h so a leap day does not quietly shave
// a day off the floor.
func InScopeRetentionFloor(now time.Time) time.Time {
	return now.UTC().AddDate(0, 0, -MinInScopeRetentionDays)
}

// Observation is one report from a monitoring credential, validated but not yet
// applied.
//
// Reporter is not part of ObservationSpec: attribution is server-assigned and
// structurally unforgeable (rule 5), so it is derived from the authenticated
// credential and passed to NewObservation separately. A caller-supplied
// reporter would forge another monitor's readings, and change_log.actor is free
// TEXT, so the same trick would forge a human's audit entry.
type Observation struct {
	// ObservationID is the reporter's idempotency key. Optional: without one,
	// ordering still comes from ReportedAt.
	ObservationID string
	EntityType    string
	EntityID      string
	// Reporter is the credential id, without the AgentActorPrefix namespace --
	// that prefix exists to keep change_log.actor unambiguous, and this column
	// has no such collision to avoid.
	Reporter string
	State    HealthState
	// ReportedAt is the reporter's own clock. Display only: a caller must never
	// write the field that decides how fresh its own data looks.
	ReportedAt string
	// IntervalSeconds is the reporter's declared cadence, and it is mandatory.
	// Staleness is computed at read time from it (rule 8); a reading whose
	// cadence is unknown can never be shown to have gone quiet, and a dead
	// collector then looks exactly like a healthy estate, forever.
	IntervalSeconds int
}

// ObservationSpec is the reporter-supplied half of a report. Everything a
// caller must not be able to choose -- the reporter, the server clock, the
// actor -- is absent by construction.
type ObservationSpec struct {
	ObservationID   string
	EntityType      string
	EntityID        string
	State           string
	ReportedAt      string
	IntervalSeconds int
}

// NewObservation validates a report against rules 4, 5 and 13.
//
// now is the server clock and is used only to reject a report from the future;
// the caller records its own arrival time separately, because last_report_at
// belongs to the server and reported_at belongs to the caller.
func NewObservation(spec ObservationSpec, reporter string, now time.Time) (*Observation, error) {
	ve := &ValidationError{}

	checkEnum(ve, "entity_type", spec.EntityType, ObservableEntityTypes)
	entityID := checkRequired(ve, "entity_id", spec.EntityID)
	reporter = checkReporter(ve, "reporter", reporter)

	state, err := ParseHealthState(spec.State)
	if err != nil {
		if sub, ok := AsValidation(err); ok {
			ve.Fields = append(ve.Fields, sub.Fields...)
		}
	}

	reportedAt := checkRequired(ve, "reported_at", spec.ReportedAt)
	if reportedAt != "" {
		t, err := ParseTime(reportedAt)
		switch {
		case err != nil:
			ve.Add("reported_at", "must be RFC3339 UTC to the second, like %s", TimeFormat)
		case t.After(now.UTC().Add(MaxClockSkew)):
			// Not merely wrong: an unbounded future timestamp wins every
			// monotonicity comparison from here on, so this entity could never
			// be updated again.
			ve.Add("reported_at", "is more than %s ahead of the server clock", MaxClockSkew)
		case t.After(now.UTC()):
			// Inside the tolerance, so the report is kept -- but the timestamp
			// is CLAMPED to the server clock rather than stored as sent.
			//
			// Rejecting only beyond the tolerance still left a weapon: a
			// timestamp 295 seconds ahead is accepted, becomes the monotonicity
			// floor, and every truthful report from that same credential is
			// then discarded as stale for the next five minutes. Re-poison once
			// per window and the entity is frozen indefinitely while its
			// collector keeps reporting honestly into a black hole -- measured
			// at 108 consecutive discarded reports over an hour.
			//
			// Clamping keeps the tolerance doing its real job (a collector
			// whose clock is a few seconds fast is not an error) and removes
			// the ability to reserve the future. We cannot know the future, so
			// a report claiming it is treated as having arrived now.
			reportedAt = FormatTime(now.UTC())
		}
	}

	switch {
	case spec.IntervalSeconds <= 0:
		ve.Add("interval_seconds", "is required and must be positive: without a declared cadence a reading can never be shown as stale")
	case spec.IntervalSeconds > MaxIntervalSeconds:
		// A reporter that names its own staleness horizon has a mute button.
		// Declare a ten-year cadence and rule 8 never fires for you: the
		// collector dies, the estate stays green forever, and the one signal
		// that an intruder killed the collector is gone. The cap is the point
		// at which "how often do you report" stops being a fact about a
		// collector and starts being a claim about how long it may be trusted.
		ve.Add("interval_seconds", "is %d, above the %d second ceiling: a reporter may not declare its own staleness horizon",
			spec.IntervalSeconds, MaxIntervalSeconds)
	}

	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	return &Observation{
		ObservationID:   strings.TrimSpace(spec.ObservationID),
		EntityType:      spec.EntityType,
		EntityID:        entityID,
		Reporter:        reporter,
		State:           state,
		ReportedAt:      reportedAt,
		IntervalSeconds: spec.IntervalSeconds,
	}, nil
}

// Supersedes reports whether this report is newer than the stored one and may
// therefore be applied (rule 4).
//
// A retry is newer or identical and a replay is older, so "not newer" is
// discarded rather than applied: last-write-wins on arrival order turns a
// delayed `down` landing after `up` into two transitions that never happened,
// three minutes late, pointing an incident review at the wrong cause.
//
// Both sides are RFC3339 UTC TEXT, which compares correctly as a string on both
// engines -- that is the whole reason for the format.
func (o *Observation) Supersedes(storedReportedAt string) bool {
	return o.ReportedAt > storedReportedAt
}

// checkReporter validates a server-derived credential id.
func checkReporter(ve *ValidationError, field, reporter string) string {
	trimmed := checkRequired(ve, field, reporter)
	if trimmed == "" {
		return ""
	}
	// ':' is the AgentActorPrefix separator; allowing it here would let one
	// credential id spell another one's namespaced actor.
	if strings.ContainsAny(trimmed, ": \t\n") {
		ve.Add(field, "must not contain a colon or whitespace")
	}
	if trimmed != strings.ToLower(trimmed) {
		ve.Add(field, "must be lower case, like every other identifier read from the environment")
	}
	return trimmed
}

// AssetHealth is one row of the current-state table: what one reporter last
// said about one entity.
//
// Named for the table, which docs/AUDIT.md rule 1 names verbatim, even though
// entity_type makes it more general than assets alone.
type AssetHealth struct {
	EntityType string      `db:"entity_type"`
	EntityID   string      `db:"entity_id"`
	Reporter   string      `db:"reporter"`
	State      HealthState `db:"state"`
	// StateSince is the onset, written only on a transition. "Down since when"
	// is the first question anyone asks at 03:00.
	StateSince string `db:"state_since"`
	// ReportedAt is the reporter's clock; LastReportAt is the server's.
	ReportedAt      string  `db:"reported_at"`
	LastReportAt    string  `db:"last_report_at"`
	IntervalSeconds *int    `db:"interval_seconds"`
	ObservationID   *string `db:"observation_id"`

	// The open flap episode (rule 9), or four NULLs when nothing is being
	// compressed. State and StateSince above keep tracking THROUGHOUT an
	// episode: "flapping" is a second fact shown beside the state, never a
	// replacement for it, or a box that died twenty minutes ago still reads as
	// flapping.
	//
	// FlapSince is when the episode opened and is the window_start the closing
	// row reports. FlapCount is the episode's total churn: every transition
	// since it opened, of which everything except a novel-value escape was
	// compressed rather than given a row. Reported as the total rather than as
	// the hidden subset because the total is the number a reader can act on --
	// how many rows are missing is a subtraction against a ledger they are
	// already looking at. FlapFirstState is the value the entity held at the
	// moment it opened. FlapSeen is the comma-joined set of states seen since,
	// and it decides the escape hatch.
	FlapSince      *string `db:"flap_since"`
	FlapCount      *int    `db:"flap_count"`
	FlapFirstState *string `db:"flap_first_state"`
	FlapSeen       *string `db:"flap_seen"`
}

// Flapping reports whether an episode is currently open for this reading.
func (h AssetHealth) Flapping() bool {
	return h.FlapSince != nil && *h.FlapSince != ""
}

// FlapOpenedAt is when the open episode started, or the empty string.
func (h AssetHealth) FlapOpenedAt() string {
	if !h.Flapping() {
		return ""
	}
	return *h.FlapSince
}

// FlapTransitions is how many transitions the open episode has absorbed so far.
func (h AssetHealth) FlapTransitions() int {
	if h.FlapCount == nil {
		return 0
	}
	return *h.FlapCount
}

// FlapStates is the set of states seen since the episode opened.
func (h AssetHealth) FlapStates() []HealthState {
	if h.FlapSeen == nil {
		return nil
	}
	return DecodeHealthStates(*h.FlapSeen)
}

// SawDuringEpisode reports whether state has already appeared in the open
// episode -- rule 9's "already seen in the window".
//
// The window here IS the open episode, and that is the deliberate reading. A
// value that has appeared anywhere in an oscillation is part of that
// oscillation; the protection the rule describes is temporal, not positional.
// When the episode closes -- a full FlapWindow with no transition -- this set is
// discarded, which is exactly what makes the real `down` five minutes later its
// own row again.
func (h AssetHealth) SawDuringEpisode(state HealthState) bool {
	for _, s := range h.FlapStates() {
		if s == state {
			return true
		}
	}
	return false
}

// FlapSettled reports whether an open episode has gone quiet: a full FlapWindow
// with no transition, measured from the onset of the current state.
//
// StateSince is the time of the last transition by construction -- it moves only
// on a state change, compressed or not -- so no separate "last oscillation"
// timestamp is kept. Two columns that must agree about the same instant would
// eventually disagree.
func (h AssetHealth) FlapSettled(now time.Time) bool {
	if !h.Flapping() {
		return false
	}
	since, err := ParseTime(h.StateSince)
	if err != nil {
		// An unparseable onset cannot be shown to be recent, and leaving an
		// episode open forever suppresses more than closing one early.
		return true
	}
	if !now.UTC().Before(since.Add(FlapWindow)) {
		return true
	}
	// Quiet is not the only way out, and relying on it alone left a mute
	// button open. StateSince moves on every transition INCLUDING the
	// compressed ones, so any cadence faster than one change per FlapWindow
	// kept an episode alive indefinitely: toggling once every four minutes
	// produced twenty real state changes and zero ledger rows, at a rate that
	// would never have qualified for compression in the first place.
	//
	// So an episode also ends when it stops earning its suppression. The
	// qualifying rate is FlapThreshold changes per FlapWindow; once the
	// episode's own average falls below that, the entity is not flapping any
	// more, it is simply changing, and every change is again worth a row.
	return !h.FlapRateQualifies(now)
}

// FlapRateQualifies reports whether an open episode is still churning at least
// as fast as the rate that opened it.
//
// Measured across the whole episode rather than a trailing window because the
// suppressed transitions are, by design, not in the ledger to count. The
// episode's own count is the only complete record of them.
func (h AssetHealth) FlapRateQualifies(now time.Time) bool {
	if h.FlapCount == nil || h.FlapSince == nil {
		return false
	}
	since, err := ParseTime(*h.FlapSince)
	if err != nil {
		return false
	}
	elapsed := now.UTC().Sub(since)
	if elapsed <= FlapWindow {
		// Still inside the first window: it qualified to get here and has not
		// had time to prove otherwise.
		return true
	}
	// Hysteresis: an episode opens at the qualifying rate and closes at half
	// of it. Opening and closing on the same number would make the flap
	// detector itself flap, churning flap_open/flap_close pairs through the
	// ledger for an entity hovering at the threshold. Half is far enough below
	// to be stable and still catches the abuse case by a wide margin -- a
	// toggle every four minutes runs at a quarter of the qualifying rate.
	windows := float64(elapsed) / float64(FlapWindow)
	return float64(*h.FlapCount) >= float64(FlapThreshold)*windows*FlapCloseHysteresis
}

// FlapCloseHysteresis is the fraction of the opening rate an episode must fall
// below before it closes on rate alone.
const FlapCloseHysteresis = 0.5

// FlapQualifies reports whether a count of transitions inside FlapWindow --
// including the one about to be written -- is above the threshold.
//
// Strictly above: rule 9 says "above domain.FlapThreshold (5) transitions",
// so five is churn and the sixth opens an episode.
func FlapQualifies(transitionsInWindow int) bool {
	return transitionsInWindow > FlapThreshold
}

// FlapWindowStart is the earliest instant still inside the sliding window.
func FlapWindowStart(now time.Time) time.Time {
	return now.UTC().Add(-FlapWindow)
}

// EncodeHealthStates renders a set of states for the flap_seen column: sorted,
// deduplicated, comma-joined. Sorted so the same set always produces the same
// text and a diff of two rows is readable.
func EncodeHealthStates(states []HealthState) string {
	seen := make(map[HealthState]bool, len(states))
	var out []string
	for _, s := range HealthStates {
		for _, candidate := range states {
			if candidate != s || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, string(s))
		}
	}
	return strings.Join(out, ",")
}

// DecodeHealthStates parses the flap_seen column.
//
// An unrecognised entry is dropped rather than failing the read: this column is
// an optimisation over the ledger, and a reading that cannot be displayed
// because its flap bookkeeping is malformed is a worse outcome than one that
// briefly under-compresses.
func DecodeHealthStates(v string) []HealthState {
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]HealthState, 0, len(parts))
	for _, p := range parts {
		s := HealthState(p)
		if s.Valid() {
			out = append(out, s)
		}
	}
	return out
}

// StaleAfter returns the instant this reading stops being believed: three
// reporting intervals after the last report (rule 8).
//
// ok is false when the reading carries no interval, in which case staleness
// cannot be computed and the caller must say so rather than assume freshness.
func (h AssetHealth) StaleAfter() (time.Time, bool) {
	if h.IntervalSeconds == nil || *h.IntervalSeconds <= 0 {
		return time.Time{}, false
	}
	last, err := ParseTime(h.LastReportAt)
	if err != nil {
		return time.Time{}, false
	}
	return last.Add(time.Duration(StaleAfterIntervals) * time.Duration(*h.IntervalSeconds) * time.Second), true
}

// IsStale reports whether this reading is too old to believe as of now.
// A reading with no computable staleness horizon counts as stale: an unknown
// horizon is not evidence of freshness.
func (h AssetHealth) IsStale(now time.Time) bool {
	horizon, ok := h.StaleAfter()
	if !ok {
		return true
	}
	return now.UTC().After(horizon)
}

// DisplayState is what a view renders: the reported state while it is fresh,
// and HealthUnknown once it is not.
//
// Never the last value. An intruder's first act is killing the collector, and
// under transition-only logging a dead collector and a healthy estate are
// otherwise the same picture, forever. Render StaleAfter beside this so the
// operator sees "unknown (stale since T)" rather than a bare unknown.
func (h AssetHealth) DisplayState(now time.Time) HealthState {
	if h.IsStale(now) {
		return HealthUnknown
	}
	return h.State
}

// ObservedTransition is one row of the transition ledger -- the only prunable
// table in this schema (rule 10).
//
// FromState is nil on the first observation of an entity by a reporter, which
// is itself a transition and is logged: it is the entry an incident reviewer
// looks for. ToState is set for every kind and always means "the state after
// this row", so an incident timeline reads one column.
//
// The flap fields are set only on a TransitionFlapClose row, where rule 9
// requires the count, the window, the first and last values seen inside it and
// the value it settled at.
type ObservedTransition struct {
	ID         string `db:"id"`
	EntityType string `db:"entity_type"`
	EntityID   string `db:"entity_id"`
	Reporter   string `db:"reporter"`
	Kind       string `db:"kind"`
	// The nullable state columns are *string rather than *HealthState so that a
	// value that somehow got past the CHECK still scans and can be shown to a
	// human, instead of failing the read that was meant to explain it.
	FromState *string     `db:"from_state"`
	ToState   HealthState `db:"to_state"`
	At        string      `db:"at"`

	FlapCount    *int    `db:"flap_count"`
	WindowStart  *string `db:"window_start"`
	WindowEnd    *string `db:"window_end"`
	FirstState   *string `db:"first_state"`
	LastState    *string `db:"last_state"`
	SettledState *string `db:"settled_state"`
}

// UnmatchedObservation is a report about something this inventory does not
// have (rule 6).
//
// EntityRef is what the reporter claimed and resolves to nothing here -- an
// observation never creates an entity, because `INSERT ... ON CONFLICT` is the
// natural verb for a webhook and it is exactly what turns a deliberately narrow
// monitoring token into an inventory-write vector.
//
// It is a finding, not noise: an asset the estate has and the inventory does
// not is drift worth surfacing. ReportCount and the first/last window are what
// make it readable when a misconfigured collector repeats it every 30 seconds.
type UnmatchedObservation struct {
	ID          string      `db:"id"`
	EntityType  string      `db:"entity_type"`
	EntityRef   string      `db:"entity_ref"`
	Reporter    string      `db:"reporter"`
	State       HealthState `db:"state"`
	ReportedAt  string      `db:"reported_at"`
	FirstSeenAt string      `db:"first_seen_at"`
	LastSeenAt  string      `db:"last_seen_at"`
	ReportCount int         `db:"report_count"`
}

// Health override lifecycles. Clearing an override is not retiring an entity,
// so the vocabulary deliberately differs from every other lifecycle here.
const (
	OverrideActive  = "active"
	OverrideCleared = "cleared"
)

// OverrideLifecycles is the Go side of the health_override.lifecycle CHECK.
var OverrideLifecycles = []string{OverrideActive, OverrideCleared}

// MaxOverrideDuration caps how long an operator may shadow an observation
// (rule 14). A permanent override is how a real outage stays invisible for six
// weeks; renewing one is cheap and leaves a trail, which is the point.
const MaxOverrideDuration = 24 * time.Hour

// HealthOverride is an operator's assertion that a reading is wrong.
//
// This is DECLARED state, not observed: a person decided it, so create, amend
// and clear each write a change_log row and sit behind CSRF and RequireAdmin
// like any other declared mutation. It shadows the observation at read time and
// never mutates it -- the reporter keeps recording the truth underneath,
// because when the override lapses you need to know what actually happened
// while it was in force.
//
// Actor holds an opaque app_user.id, never a username: any column recording who
// did something stores an id (docs/DECISIONS.md, 2026-07-28), so the row
// carries no personal data and the UI joins to resolve a display name.
type HealthOverride struct {
	ID            string      `db:"id"`
	EntityType    string      `db:"entity_type"`
	EntityID      string      `db:"entity_id"`
	AssertedState HealthState `db:"asserted_state"`
	Reason        string      `db:"reason"`
	Actor         string      `db:"actor"`
	Lifecycle     string      `db:"lifecycle"`
	CreatedAt     string      `db:"created_at"`
	UpdatedAt     string      `db:"updated_at"`
	ExpiresAt     string      `db:"expires_at"`
}

// HealthOverrideSpec is everything an operator supplies. The actor is not in
// it: attribution is server-derived from the session, never read from a form.
type HealthOverrideSpec struct {
	EntityType    string
	EntityID      string
	AssertedState string
	Reason        string
	ExpiresAt     time.Time
}

// NewHealthOverride validates and constructs an override.
//
// The actor must be a person. A machine credential never reaches this: an
// override is an operator overruling a monitor, and letting a monitor write one
// would let a compromised credential silence the estate it is reporting on.
func NewHealthOverride(id string, spec HealthOverrideSpec, actor Actor, now time.Time) (*HealthOverride, error) {
	ve := &ValidationError{}

	if actor.Kind != ActorKindUser {
		ve.Add("actor", "only a signed-in operator may override an observation, not a %s actor", actor.Kind)
	}
	if strings.TrimSpace(actor.ID) == "" {
		ve.Add("actor", "is required")
	}

	state, err := ParseHealthState(spec.AssertedState)
	if err != nil {
		if sub, ok := AsValidation(err); ok {
			for _, f := range sub.Fields {
				ve.Add("asserted_state", "%s", f.Message)
			}
		}
	}

	now = now.UTC()
	expires := spec.ExpiresAt.UTC()
	switch {
	case spec.ExpiresAt.IsZero():
		ve.Add("expires_at", "is required: an override with no end is how a real outage stays invisible for six weeks")
	case !expires.After(now):
		ve.Add("expires_at", "must be in the future")
	case expires.After(now.Add(MaxOverrideDuration)):
		ve.Add("expires_at", "must be no more than %s from now", MaxOverrideDuration)
	}

	o := &HealthOverride{
		ID:            id,
		EntityType:    spec.EntityType,
		EntityID:      spec.EntityID,
		AssertedState: state,
		Reason:        spec.Reason,
		Actor:         strings.TrimSpace(actor.ID),
		Lifecycle:     OverrideActive,
		CreatedAt:     FormatTime(now),
		UpdatedAt:     FormatTime(now),
		ExpiresAt:     FormatTime(expires),
	}
	if err := o.Validate(); err != nil {
		if sub, ok := AsValidation(err); ok {
			ve.Fields = append(ve.Fields, sub.Fields...)
		}
	}
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	return o, nil
}

// Amend applies an operator's edit to an existing override: what it asserts,
// why, and how long it lasts. What it may never change is which entity it
// shadows -- moving an override to another entity would silence something
// nobody agreed to silence while carrying the original's justification.
//
// The 24h ceiling is measured from now rather than from CreatedAt. Renewing is
// meant to be cheap; what it must never be is silent, and every renewal writes
// its own change_log row naming the new expiry. An override that could be
// extended without an entry is a permanent one wearing a deadline.
//
// A cleared override is not amendable. Reopening one would reuse a row whose
// reason was written about a situation that has already been declared over;
// the operator writes a new override and says why, which is the whole point of
// the mandatory reason.
func (o *HealthOverride) Amend(spec HealthOverrideSpec, actor Actor, now time.Time) error {
	ve := &ValidationError{}

	if actor.Kind != ActorKindUser {
		ve.Add("actor", "only a signed-in operator may amend an override, not a %s actor", actor.Kind)
	}
	if o.Lifecycle != OverrideActive {
		ve.Add("lifecycle", "this override has been cleared; write a new one and say why")
	}

	state, err := ParseHealthState(spec.AssertedState)
	if err != nil {
		if sub, ok := AsValidation(err); ok {
			for _, f := range sub.Fields {
				ve.Add("asserted_state", "%s", f.Message)
			}
		}
	}

	now = now.UTC()
	expires := spec.ExpiresAt.UTC()
	switch {
	case spec.ExpiresAt.IsZero():
		ve.Add("expires_at", "is required: an override with no end is how a real outage stays invisible for six weeks")
	case !expires.After(now):
		ve.Add("expires_at", "must be in the future")
	case expires.After(now.Add(MaxOverrideDuration)):
		ve.Add("expires_at", "must be no more than %s from now", MaxOverrideDuration)
	}

	if err := ve.OrNil(); err != nil {
		return err
	}

	amended := *o
	amended.AssertedState = state
	amended.Reason = spec.Reason
	amended.ExpiresAt = FormatTime(expires)
	amended.UpdatedAt = FormatTime(now)
	if err := amended.Validate(); err != nil {
		return err
	}
	*o = amended
	return nil
}

// Clear ends an override early. The row is kept -- soft delete applies here as
// everywhere else -- because "who silenced this, and for how long" outlives the
// silence, and an incident review reads it after the fact.
func (o *HealthOverride) Clear(actor Actor, now time.Time) error {
	ve := &ValidationError{}
	if actor.Kind != ActorKindUser {
		ve.Add("actor", "only a signed-in operator may clear an override, not a %s actor", actor.Kind)
	}
	if o.Lifecycle != OverrideActive {
		ve.Add("lifecycle", "this override has already been cleared")
	}
	if err := ve.OrNil(); err != nil {
		return err
	}
	o.Lifecycle = OverrideCleared
	o.UpdatedAt = FormatTime(now.UTC())
	return nil
}

// Validate checks the invariants that do not depend on the current time, so a
// round-tripped row can be re-checked before an amendment is written.
func (o *HealthOverride) Validate() error {
	ve := &ValidationError{}
	checkEnum(ve, "entity_type", o.EntityType, ObservableEntityTypes)
	checkRequired(ve, "entity_id", o.EntityID)
	checkEnum(ve, "lifecycle", o.Lifecycle, OverrideLifecycles)
	if !o.AssertedState.Valid() {
		checkEnum(ve, "asserted_state", string(o.AssertedState), HealthStateNames())
	}
	// Mandatory for the same reason failure_mode is mandatory on a dependency:
	// forcing the author to write down why is most of the value of the row.
	o.Reason = checkRequired(ve, "reason", o.Reason)
	checkRequired(ve, "actor", o.Actor)
	if o.ExpiresAt <= o.CreatedAt {
		ve.Add("expires_at", "must be after the override was created")
	}
	// The 24h ceiling belongs here, not only in the constructors.
	//
	// Rule 14 caps it because a permanent override is how a real outage stays
	// invisible for six weeks. A cap that lives only in NewHealthOverride and
	// Amend is a cap that can be described as "must not use the other path" --
	// exactly the shape rule 1 rejects -- and Validate is what the store entry
	// point actually runs, so a future admin CLI, seeder or bulk
	// maintenance-window importer building the struct directly would have
	// bypassed it entirely.
	//
	// Enforced in Go rather than as a CHECK because timestamp arithmetic is not
	// portable across both engines; SQL enforces direction only.
	if created, err := ParseTime(o.CreatedAt); err == nil {
		if expires, err := ParseTime(o.ExpiresAt); err == nil {
			if expires.Sub(created) > MaxOverrideDuration {
				ve.Add("expires_at", "is more than %s after creation: an override that outlives an "+
					"incident hides the next one", MaxOverrideDuration)
			}
		}
	}
	return ve.OrNil()
}

// IsActiveAt reports whether this override still shadows the observation.
// Expiry is evaluated at read time rather than swept by a job, so a lapsed
// override needs no write to stop applying.
func (o *HealthOverride) IsActiveAt(now time.Time) bool {
	return o.Lifecycle == OverrideActive && FormatTime(now) < o.ExpiresAt
}
