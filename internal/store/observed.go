// Observed state: the write path for what the estate reports about itself, and
// the boundary that makes overreach a compile error rather than a review note.
//
// docs/AUDIT.md rule 1: "If a boundary can only be described as 'must not call
// X', it is not implemented." So the enforcement here is three things:
//
//  1. Every observed write in this codebase lives in this file. Nothing outside
//     it writes asset_health, observed_transition or unmatched_observation, and
//     nothing inside it writes anything else.
//  2. The observation webhook's store field is typed ObservedStore -- two
//     methods -- and never *SQLStore. *SQLStore deliberately does not satisfy
//     that interface, so a handler cannot reach a declared mutation even by
//     accident: reaching for one does not compile.
//  3. Writes here go through observedWrite, which has exec and get and NO
//     audit-log method at all. An observed write cannot reach the change-log
//     helpers because the type it holds does not have them. That is the same
//     argument as (2), one layer down.
//
// TestObservedPathTouchesNoDeclaredTable (a later task) parses this file and
// fails on any write naming a table or column off the observed allowlist, which
// is domain.ObservedTables. A note for whoever writes it: parse with go/ast and
// look at string literals and call expressions, not at raw file text. The
// comments in this file necessarily name the things they forbid, and a grep
// would flag the explanation as the violation. The declared tables named in SQL
// below (asset, service_instance, asset_environment, environment) appear only
// in SELECTs -- reads are not writes, and rule 6 requires exactly those two
// reads: "is this thing in the inventory at all" and "is it inside this
// credential's environment scope".
//
// What this file may write is the whole of rule 2's allowlist: the observed
// columns, and nothing else. A reported `down` never sets lifecycle, never sets
// desired_state, never deletes a placement. Only a person retires something.
//
// Nothing here acts on the estate. invctl presents state (HANDOVER.md §1); an
// observation changes what is displayed and never what is running.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/gabriel/invctl/internal/domain"
)

// ---------------------------------------------------------------------------
// The boundary
// ---------------------------------------------------------------------------

// ObservedStore is everything the observation webhook may reach: record a
// report, read back what is currently observed. Nothing else.
//
// This is the type the webhook handler's store field must have. Widening it to
// *SQLStore would hand a monitoring credential's request path every declared
// mutation in the package -- retire an asset, verify a dependency, disable a
// placement -- and the only thing standing between it and them would be that
// nobody wrote the call. Rule 1 exists because that is not a boundary.
//
// *SQLStore intentionally does NOT satisfy this interface. RecordObservation
// lives on *ObservedRecorder, which owns the buffer rule 4 requires, so the
// webhook has to be given the recorder and can be given nothing wider.
type ObservedStore interface {
	// RecordObservation applies one report from a monitoring credential.
	//
	// Both halves of the credential are parameters and neither is optional:
	// actor is who is speaking (rule 5) and scope is what it is allowed to
	// speak about (rule 6). A caller cannot record an observation without
	// stating both, which is why the environment check lives here rather than
	// in a handler that could forget it.
	RecordObservation(ctx context.Context, actor domain.Actor, scope domain.EnvironmentScope, spec domain.ObservationSpec) (*ObservationResult, error)
	// GetObservedState returns every reporter's current reading for an entity,
	// with staleness already applied.
	GetObservedState(ctx context.Context, entityType, entityID string) ([]ObservedReading, error)
}

var _ ObservedStore = (*ObservedRecorder)(nil)

// ---------------------------------------------------------------------------
// What happened to a report
// ---------------------------------------------------------------------------

// ObservationDisposition is what became of a report. A caller needs it because
// three of the four outcomes are silent by design, and a webhook that answered
// "202" to all of them would make a broken collector indistinguishable from a
// working one.
type ObservationDisposition string

const (
	// ObservationTransitioned: the state changed (including the first ever
	// reading, whose prior value is NULL), so a row was written to the
	// transition ledger and the current-state row was updated immediately.
	ObservationTransitioned ObservationDisposition = "transitioned"
	// ObservationUnchanged: the report repeated the stored state. Nothing was
	// logged and no write transaction was opened -- the last_report_at bump is
	// buffered and flushed on FlushInterval (rule 4).
	ObservationUnchanged ObservationDisposition = "unchanged"
	// ObservationDuplicate: the same observation_id as the stored one. A
	// re-delivery, so it is a no-op down to the staleness clock.
	ObservationDuplicate ObservationDisposition = "duplicate"
	// ObservationStale: reported_at is not newer than the stored one, so the
	// report is discarded rather than applied. A retry is newer or identical;
	// a replay is older.
	ObservationStale ObservationDisposition = "stale"
)

// ObservationResult is what a report did, plus the reading that is now current
// for that (entity, reporter).
//
// For ObservationUnchanged the Reading reflects the in-memory buffer rather
// than the database: the timestamps are real, they are simply not durable until
// the next flush. That is the trade rule 4 asks for, and losing at most one
// flush interval of staleness precision on a crash is the whole cost.
type ObservationResult struct {
	Disposition ObservationDisposition
	Reading     ObservedReading
}

// ---------------------------------------------------------------------------
// The recorder
// ---------------------------------------------------------------------------

// FlushInterval is how often buffered no-op reports are written down.
//
// Rule 4: a report that changes nothing must not open a write transaction on
// the request path. On SQLite the writer pool is a single connection, so a
// design where heartbeats can occupy it makes "retire this asset" time out at
// exactly the moment it matters. Thirty seconds of buffering turns one write
// per heartbeat into one write per interval for the whole estate.
const FlushInterval = 30 * time.Second

// healthKey identifies one reading. Reporter is part of it, not an attribute of
// it: two monitors watching one asset and disagreeing must show as two readings
// that disagree. Keyed on the entity alone they would overwrite each other on
// every poll and the entity would appear to flap forever.
type healthKey struct {
	EntityType string
	EntityID   string
	Reporter   string
}

// pendingBump is a report that changed no state: everything it moves is a
// timestamp or an idempotency key. state and state_since are absent from this
// struct on purpose -- a repeat report may not touch either, and the type is
// where that is enforced rather than the SQL.
type pendingBump struct {
	reportedAt      string
	lastReportAt    string
	observationID   string
	intervalSeconds int
}

// ObservedRecorder is the observed write path. It owns the buffer, so it is the
// only thing that can honour rule 4, which is why RecordObservation lives here
// and not on *SQLStore.
type ObservedRecorder struct {
	store *SQLStore

	mu      sync.Mutex
	pending map[healthKey]pendingBump
}

// NewObservedRecorder builds the recorder over an open store.
//
// Call Run to get the periodic flush. Without it the buffered bumps still land
// -- the next transition for that key writes them through -- but an entity that
// never changes state would keep an ageing last_report_at and read as stale, so
// a deployment that skips Run is a deployment that invents outages.
func NewObservedRecorder(s *SQLStore) *ObservedRecorder {
	return &ObservedRecorder{store: s, pending: make(map[healthKey]pendingBump)}
}

// RecordObservation applies one report.
//
// Everything a caller must not be able to choose is absent from spec: the
// reporter comes from the authenticated credential, the server clock comes from
// the store, and the actor is not read from the payload (rule 5). A caller
// supplies reported_at, which is display-only and used for ordering, and never
// last_report_at -- a caller must never write the field that decides how fresh
// its own data looks.
//
// It writes nothing to the audit trail, does not bump updated_at, and does not
// reindex. Those are rule 3, and each has its own reason: the audit trail is
// for decisions, updated_at means "when a person last changed this row", and
// reindexing per heartbeat rewrites the FTS table thousands of times a day
// against a single SQLite writer.
//
// An observation for an entity this inventory does not have returns
// domain.ErrNotFound -- 404 at the HTTP layer -- and is queued as drift. It
// never creates the entity: `INSERT ... ON CONFLICT` is the natural verb for a
// webhook and it is exactly what turns a deliberately narrow monitoring token
// into an inventory-write vector (rule 6).
//
// An observation for an entity outside the credential's environment scope
// returns domain.ErrForbidden -- 403 -- and writes nothing at all. A dev
// collector's token must not be able to write health for an in_scope
// environment, and "must not" here means the write path refuses, not that no
// handler happens to call it.
func (r *ObservedRecorder) RecordObservation(ctx context.Context, actor domain.Actor, scope domain.EnvironmentScope, spec domain.ObservationSpec) (*ObservationResult, error) {
	reporter, err := observationReporter(actor)
	if err != nil {
		return nil, err
	}
	if scope.IsEmpty() {
		// A mis-wired route, not a bad request: every credential is scoped
		// explicitly at startup, so an empty scope here means the caller lost
		// it on the way. Refusing beats defaulting to "everything".
		ve := &domain.ValidationError{}
		ve.Add("scope", "a monitoring credential must arrive with its environment scope; this one has none")
		return nil, fmt.Errorf("authorising observation from %s: %w", reporter, ve)
	}

	now := r.store.Now()
	obs, err := domain.NewObservation(spec, reporter, now)
	if err != nil {
		return nil, fmt.Errorf("validating observation: %w", err)
	}

	// Rule 6, and the order matters: existence is checked before anything is
	// written, so an unresolvable report can never reach the current-state
	// table at all. A retired asset or a disabled placement still exists and is
	// still observed -- the collector has not been told yet, and hiding the
	// reading would hide the drift.
	known, err := r.store.observableExists(ctx, obs.EntityType, obs.EntityID)
	if err != nil {
		return nil, err
	}
	if !known {
		if err := r.store.queueUnmatched(ctx, obs, now); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("observing %s %s: %w", obs.EntityType, obs.EntityID, domain.ErrNotFound)
	}

	// Scope is checked after existence, and that ordering is a deliberate
	// trade. Checking it first would avoid distinguishing "no such entity" from
	// "not yours" -- but it would also mean an out-of-scope credential's
	// genuinely unknown entity never reaches the drift queue, and a collector
	// pointed at an inventory that has never heard of half its estate is the
	// finding rule 6 wants surfaced. The oracle this leaves is narrow: ids are
	// UUIDv7 and unguessable, so a 404 confirms nothing an attacker could have
	// enumerated.
	if err := r.store.authoriseScope(ctx, scope, obs); err != nil {
		return nil, err
	}

	key := healthKey{EntityType: obs.EntityType, EntityID: obs.EntityID, Reporter: obs.Reporter}
	current, err := r.currentReading(ctx, key)
	if err != nil {
		return nil, err
	}

	// The decision read above is against the reader pool, deliberately: a
	// report that changes nothing must not open a write transaction, so
	// classification cannot happen inside one.
	switch classify(obs, current) {
	case ObservationDuplicate:
		return settled(ObservationDuplicate, current, now), nil
	case ObservationStale:
		return settled(ObservationStale, current, now), nil
	case ObservationUnchanged:
		if current.FlapSettled(now) {
			// An episode that has gone quiet closes here, on the first report
			// after the quiet period, rather than being swept by a background
			// job. Rule 4's "off the write path" governs reports that change
			// NOTHING; closing an episode changes something, and it happens once
			// per episode rather than once per heartbeat.
			//
			// This is also what makes rule 9's security property work: a stolen
			// token that trips the floor and then stops has its episode closed by
			// the very next report, so the real `down` five minutes later is an
			// ordinary transition with a row of its own.
			return r.closeSettledEpisode(ctx, key, obs, current, now)
		}
		bumped := r.buffer(key, obs, current, now)
		return settled(ObservationUnchanged, &bumped, now), nil
	}
	// Nothing matched, so this is a transition -- including the first ever
	// reading. Transitions write through immediately; a state change is the one
	// thing an operator is waiting to see.
	return r.writeThrough(ctx, key, obs, now)
}

// GetObservedState satisfies ObservedStore by delegating to the read path,
// which the detail pages share. A webhook that can read back what it just wrote
// is worth the one line; a webhook that can read anything else is not.
func (r *ObservedRecorder) GetObservedState(ctx context.Context, entityType, entityID string) ([]ObservedReading, error) {
	return r.store.GetObservedState(ctx, entityType, entityID)
}

// classify decides what a report is, given what is currently stored.
//
// Duplicate is tested before stale because a re-delivery carries an identical
// reported_at and would otherwise be reported as stale -- true, but far less
// useful to whoever is reading the response while wondering why their collector
// looks broken.
func classify(obs *domain.Observation, current *domain.AssetHealth) ObservationDisposition {
	switch {
	case current == nil:
		// The first observation has no prior value and IS a transition. It is
		// the entry an incident reviewer looks for, so it is logged.
		return ObservationTransitioned
	case obs.ObservationID != "" && current.ObservationID != nil && *current.ObservationID == obs.ObservationID:
		return ObservationDuplicate
	case !obs.Supersedes(current.ReportedAt):
		// Discarded, not applied. Last-write-wins on arrival order turns a
		// delayed `down` landing after `up` into two transitions that never
		// happened, three minutes late, pointing an incident review at the
		// wrong cause.
		return ObservationStale
	case current.State != obs.State:
		return ObservationTransitioned
	default:
		return ObservationUnchanged
	}
}

// writeThrough applies a transition immediately. Transitions and declared
// mutations are never buffered: the buffer exists for reports that change
// nothing, and a state change is the one thing an operator is waiting to see.
func (r *ObservedRecorder) writeThrough(ctx context.Context, key healthKey, obs *domain.Observation, now time.Time) (*ObservationResult, error) {
	at := domain.FormatTime(now)
	var out *ObservationResult

	err := r.store.observedTx(ctx, func(w *observedWrite) error {
		// Re-read inside the transaction. The classification above was taken
		// against the reader pool, which is a different connection and so a
		// slightly older snapshot on both engines -- and from_state must name
		// what this write actually replaces, or the ledger describes a
		// transition that did not happen.
		current, err := w.readHealth(ctx, key)
		if err != nil {
			return err
		}

		switch classify(obs, current) {
		case ObservationDuplicate:
			out = settled(ObservationDuplicate, current, now)
			return nil
		case ObservationStale:
			out = settled(ObservationStale, current, now)
			return nil
		case ObservationUnchanged:
			// Another writer got there first and the state now matches. The
			// transaction is already open, so the bump is written here rather
			// than buffered -- but through bumpHealth, which cannot move
			// state_since. Using the upsert would reset the onset, and "down
			// since when" is the first question anyone asks at 03:00.
			bumped := bumpFor(obs, now)
			if err := w.bumpHealth(ctx, key, bumped); err != nil {
				return err
			}
			health := applyBump(current, key, bumped)
			out = settled(ObservationUnchanged, &health, now)
			return nil
		}

		// Nothing matched, so this is a transition. state_since moves only here,
		// and only to the server's clock: a reporter's clock decides ordering
		// and display, and never how long something has been down. It moves for
		// a COMPRESSED transition too -- rule 9 is explicit that the current
		// value and its onset keep tracking throughout an episode, or a box that
		// died twenty minutes ago still shows as flapping instead of down.
		//
		// The count is taken unconditionally rather than only when no episode is
		// open, which costs one indexed COUNT per state change. Transitions are
		// rare by construction (rule 3 keeps heartbeats off this path entirely),
		// and it lets the compression decision be a pure function of values the
		// caller can see.
		churn, err := w.countRecentTransitions(ctx, key, domain.FormatTime(domain.FlapWindowStart(now)))
		if err != nil {
			return err
		}
		plan := planFlap(current, obs.State, at, churn, now)

		interval := obs.IntervalSeconds
		health := domain.AssetHealth{
			EntityType:      key.EntityType,
			EntityID:        key.EntityID,
			Reporter:        key.Reporter,
			State:           obs.State,
			StateSince:      at,
			ReportedAt:      obs.ReportedAt,
			LastReportAt:    at,
			IntervalSeconds: &interval,
			ObservationID:   optionalText(obs.ObservationID),
		}
		plan.episode.applyTo(&health)

		applied, err := w.upsertHealth(ctx, health)
		if err != nil {
			return err
		}
		if !applied {
			// The trailing guard on the upsert refused it: a newer report
			// landed between the re-read and the write. Structural monotonicity,
			// backing up the Go-side check rather than replacing it. Nothing has
			// been appended to the ledger yet, which is why the plan is executed
			// only after this point.
			refreshed, err := w.readHealth(ctx, key)
			if err != nil {
				return err
			}
			out = settled(ObservationStale, refreshed, now)
			return nil
		}

		// Order matters for a reader: the episode that just ended is bracketed
		// before the transition that ended it, so a timeline reads
		// "flap_open ... flap_close (hid 14) ... down".
		if plan.closing != nil {
			if err := w.recordFlapClose(ctx, key, *plan.closing, at); err != nil {
				return err
			}
		}
		switch {
		case plan.opens:
			// The opening transition is carried BY the flap_open row rather than
			// by a transition row beside it: flap_open names both the state it
			// left and the state it went to, so nothing about that change is
			// lost, and "stop emitting one row per oscillation" starts at the
			// oscillation that qualified rather than one later.
			if err := w.recordFlapOpen(ctx, key, plan.episode, obs.State, at); err != nil {
				return err
			}
		case plan.emit:
			from := (*string)(nil)
			if current != nil {
				prior := string(current.State)
				from = &prior
			}
			if err := w.recordTransition(ctx, key, from, obs.State, at); err != nil {
				return err
			}
		}
		out = settled(ObservationTransitioned, &health, now)
		return nil
	})
	if err != nil {
		return nil, err
	}

	// A durable write supersedes anything buffered for this key, and a buffered
	// bump that outlived it would rewind reported_at on the next flush.
	r.forget(key)
	return out, nil
}

// ---------------------------------------------------------------------------
// Flap compression (rule 9)
// ---------------------------------------------------------------------------
//
// Compressed, never suppressed. Above domain.FlapThreshold transitions inside
// domain.FlapWindow, per (entity, reporter), the writer stops emitting one row
// per oscillation and brackets the churn between a flap_open and a flap_close.
// Both bracket rows are UNCONDITIONAL and flap_close carries the count, the
// window, the first and last values and the settled value, so a reader always
// sees that suppression happened and exactly how much it hid.
//
// Three properties are load-bearing and none of them is cosmetic:
//
//   - The thresholds are domain constants. Never env, never a column, never a
//     payload field. A writer that can raise its own suppression threshold has
//     a mute button.
//   - A transition to a value not already seen in the episode is ALWAYS its own
//     row, and it ends the episode. A novel value is by definition not part of
//     the oscillation being compressed, and this is what stops a stolen token
//     deliberately tripping the floor so that the real `down` five minutes later
//     reads as a monitoring artefact.
//   - state and state_since keep tracking throughout. "Flapping" is a second
//     fact displayed beside the state, never instead of it.

// flapEpisode is the open-episode bookkeeping carried on the health row. The
// zero value means no episode, which is why it is a value rather than a pointer:
// "no episode" has to be as easy to express as an open one.
type flapEpisode struct {
	since string
	count int
	first domain.HealthState
	seen  []domain.HealthState
}

func (e flapEpisode) open() bool { return e.since != "" }

// applyTo writes the episode onto a health row, or clears the four columns when
// there is no episode. They move together or the row can express a half-open
// episode that suppresses transitions with nothing able to close it.
func (e flapEpisode) applyTo(h *domain.AssetHealth) {
	if !e.open() {
		h.FlapSince, h.FlapCount, h.FlapFirstState, h.FlapSeen = nil, nil, nil, nil
		return
	}
	since, count, first := e.since, e.count, string(e.first)
	seen := domain.EncodeHealthStates(e.seen)
	h.FlapSince, h.FlapCount, h.FlapFirstState, h.FlapSeen = &since, &count, &first, &seen
}

// withState returns the episode having absorbed one more transition.
func (e flapEpisode) withState(state domain.HealthState) flapEpisode {
	next := e
	next.count++
	next.seen = append(append([]domain.HealthState(nil), e.seen...), state)
	return next
}

// episodeFrom reconstructs the open episode carried on a health row.
func episodeFrom(h *domain.AssetHealth) flapEpisode {
	if h == nil || !h.Flapping() {
		return flapEpisode{}
	}
	return flapEpisode{
		since: h.FlapOpenedAt(),
		count: h.FlapTransitions(),
		first: domain.HealthState(derefText(h.FlapFirstState)),
		seen:  h.FlapStates(),
	}
}

// flapClosure is everything a flap_close row reports.
type flapClosure struct {
	episode flapEpisode
	// last is the value the oscillation was sitting at when the episode ended;
	// settled is the value it ended up at. They coincide when an episode simply
	// goes quiet, and differ when a novel value ends it -- "it flapped between
	// up and down fourteen times and then went degraded" is a different fact
	// from "it flapped fourteen times and settled at up", and an incident review
	// needs to be able to tell them apart.
	last    domain.HealthState
	settled domain.HealthState
}

// flapPlan is what one transition does about compression. It is produced by a
// pure function so the decision can be reasoned about without a database.
type flapPlan struct {
	// closing is set when this write ends an episode; its row is appended first.
	closing *flapClosure
	// opens means write a flap_open row instead of a transition row.
	opens bool
	// emit means write an ordinary transition row.
	emit bool
	// episode is the state to store on the health row after this write.
	episode flapEpisode
}

// planFlap decides what a state change does about compression.
//
// current is the reading being replaced (nil for the first ever observation),
// churn is how many transitions this (entity, reporter) has already recorded
// inside the sliding window, and at is the server clock for this write.
func planFlap(current *domain.AssetHealth, to domain.HealthState, at string, churn int, now time.Time) flapPlan {
	// The first observation has no prior value, is a transition, and cannot be
	// part of an oscillation. It is the row an incident reviewer looks for.
	if current == nil {
		return flapPlan{emit: true}
	}

	// An episode that has gone quiet closes before this transition is
	// classified, so a state change arriving after the quiet period is an
	// ordinary one again rather than being absorbed into a stale episode.
	settledClose := (*flapClosure)(nil)
	if current.FlapSettled(now) {
		settledClose = &flapClosure{
			episode: episodeFrom(current),
			last:    current.State,
			settled: current.State,
		}
		cleared := *current
		flapEpisode{}.applyTo(&cleared)
		current = &cleared
	}

	if current.Flapping() {
		episode := episodeFrom(current)
		if current.SawDuringEpisode(to) {
			// Part of the oscillation being compressed: no row, but the count
			// and the current value both keep moving.
			return flapPlan{closing: settledClose, episode: episode.withState(to)}
		}
		// Novel. Always its own row, and the oscillation being compressed is
		// over: keeping the episode open would let a wider set of values keep
		// oscillating under one qualification. Re-qualifying costs the estate
		// nothing and costs an attacker another threshold's worth of visible
		// rows.
		return flapPlan{
			closing: &flapClosure{episode: episode.withState(to), last: current.State, settled: to},
			emit:    true,
		}
	}

	// No episode. Every transition is its own row unless this one qualifies --
	// churn counts what is already in the window, so +1 for this change.
	if settledClose == nil && domain.FlapQualifies(churn+1) {
		return flapPlan{
			opens: true,
			episode: flapEpisode{
				since: at,
				count: 1,
				first: current.State,
				seen:  []domain.HealthState{current.State, to},
			},
		}
	}
	// A settled episode does not re-qualify on the same write. The rows it
	// suppressed are not in the window count, so re-opening here would be
	// arithmetic on numbers that are deliberately incomplete; the next threshold
	// crossing is measured from emitted rows, which are all there are.
	return flapPlan{closing: settledClose, emit: true}
}

// closeSettledEpisode ends a quiet episode on a report that changed no state.
//
// It writes through rather than buffering because it changes something: the
// bracket row a reader needs in order to know that compression happened, and
// how much it hid. That is once per episode, not once per heartbeat.
func (r *ObservedRecorder) closeSettledEpisode(ctx context.Context, key healthKey,
	obs *domain.Observation, current *domain.AssetHealth, now time.Time) (*ObservationResult, error) {
	at := domain.FormatTime(now)
	bump := bumpFor(obs, now)
	var out *ObservationResult

	err := r.store.observedTx(ctx, func(w *observedWrite) error {
		// Re-read inside the transaction: the reader pool is a different
		// connection, and a concurrent transition may have moved the onset or
		// closed the episode already. Closing twice would tell a reader the
		// estate flapped twice.
		fresh, err := w.readHealth(ctx, key)
		if err != nil {
			return err
		}
		if fresh == nil {
			return nil
		}
		if fresh.FlapSettled(now) {
			closure := flapClosure{episode: episodeFrom(fresh), last: fresh.State, settled: fresh.State}
			if err := w.recordFlapClose(ctx, key, closure, at); err != nil {
				return err
			}
			if err := w.clearFlap(ctx, key); err != nil {
				return err
			}
			flapEpisode{}.applyTo(fresh)
		}
		// The transaction is already open, so the timestamp bump is written here
		// rather than buffered -- through bumpHealth, which cannot move
		// state_since.
		if err := w.bumpHealth(ctx, key, bump); err != nil {
			return err
		}
		health := applyBump(fresh, key, bump)
		out = settled(ObservationUnchanged, &health, now)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		// The row vanished between the classification read and the write. There
		// is nothing to bump and nothing to close; report what we last saw.
		return settled(ObservationUnchanged, current, now), nil
	}
	r.forget(key)
	return out, nil
}

// ---------------------------------------------------------------------------
// The buffer (rule 4)
// ---------------------------------------------------------------------------

// buffer records a no-op report in memory and returns the reading as it now
// stands. No write transaction is opened; that is the entire point.
//
// A report older than one already buffered for the same key is kept out, for
// the same reason an older report is kept out of the table: ordering is decided
// by the reporter's clock, and the buffer is where that decision lives until
// the next flush.
func (r *ObservedRecorder) buffer(key healthKey, obs *domain.Observation, current *domain.AssetHealth, now time.Time) domain.AssetHealth {
	bump := bumpFor(obs, now)

	r.mu.Lock()
	if prev, ok := r.pending[key]; !ok || prev.reportedAt <= bump.reportedAt {
		r.pending[key] = bump
	}
	r.mu.Unlock()

	return applyBump(current, key, bump)
}

// forget drops a buffered bump, because something durable has superseded it.
func (r *ObservedRecorder) forget(key healthKey) {
	r.mu.Lock()
	delete(r.pending, key)
	r.mu.Unlock()
}

// currentReading is the stored row overlaid with anything buffered for it.
//
// The overlay is not a nicety: monotonicity and idempotency are both decided
// against "the last report we accepted", and for a buffered key that value is
// in memory rather than on disk. Comparing against the disk copy alone would
// re-accept a replay of a report already seen this interval.
func (r *ObservedRecorder) currentReading(ctx context.Context, key healthKey) (*domain.AssetHealth, error) {
	stored, err := r.store.readHealth(ctx, key)
	if err != nil {
		return nil, err
	}
	if stored == nil {
		// Nothing stored means nothing buffered: a bump is only ever recorded
		// for a key that already has a row, because the first report is a
		// transition and writes through.
		return nil, nil
	}

	r.mu.Lock()
	bump, buffered := r.pending[key]
	r.mu.Unlock()
	if !buffered {
		return stored, nil
	}
	overlaid := applyBump(stored, key, bump)
	return &overlaid, nil
}

// applyBump returns the reading a bump produces. It is the single definition of
// what a repeat report changes, shared by the buffered path, the read-back in
// currentReading and the in-transaction fallback in writeThrough -- three places
// that must agree, and would otherwise drift.
func applyBump(stored *domain.AssetHealth, key healthKey, bump pendingBump) domain.AssetHealth {
	out := domain.AssetHealth{EntityType: key.EntityType, EntityID: key.EntityID, Reporter: key.Reporter}
	if stored != nil {
		out = *stored
	}
	if bump.reportedAt > out.ReportedAt {
		out.ReportedAt = bump.reportedAt
	}
	if bump.lastReportAt > out.LastReportAt {
		out.LastReportAt = bump.lastReportAt
	}
	out.ObservationID = optionalText(bump.observationID)
	if bump.intervalSeconds > 0 {
		interval := bump.intervalSeconds
		out.IntervalSeconds = &interval
	}
	return out
}

func bumpFor(obs *domain.Observation, now time.Time) pendingBump {
	return pendingBump{
		reportedAt:      obs.ReportedAt,
		lastReportAt:    domain.FormatTime(now),
		observationID:   obs.ObservationID,
		intervalSeconds: obs.IntervalSeconds,
	}
}

// Flush writes every buffered bump in one transaction and reports how many.
//
// One transaction for the whole estate is the trade rule 4 asks for: a
// thousand heartbeats a minute become two writes, and the single SQLite writer
// stays free for the declared mutation an operator is waiting on.
//
// Deterministic on purpose -- keys are sorted, and nothing here consults a
// wall clock -- so a test drives it by calling it rather than by sleeping.
func (r *ObservedRecorder) Flush(ctx context.Context) (int, error) {
	r.mu.Lock()
	pending := r.pending
	r.pending = make(map[healthKey]pendingBump, len(pending))
	r.mu.Unlock()

	if len(pending) == 0 {
		return 0, nil
	}

	keys := make([]healthKey, 0, len(pending))
	for key := range pending {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.EntityType != b.EntityType {
			return a.EntityType < b.EntityType
		}
		if a.EntityID != b.EntityID {
			return a.EntityID < b.EntityID
		}
		return a.Reporter < b.Reporter
	})

	err := r.store.observedTx(ctx, func(w *observedWrite) error {
		for _, key := range keys {
			if err := w.bumpHealth(ctx, key, pending[key]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		// Put them back rather than dropping them. The map is keyed, so a
		// database that is down for an hour costs one entry per (entity,
		// reporter) rather than one per report -- and losing them silently
		// would make every quiet entity read as stale for no reason.
		r.restore(pending)
		return 0, fmt.Errorf("flushing %d buffered observation(s): %w", len(pending), err)
	}
	return len(keys), nil
}

// restore puts bumps back after a failed flush, without overwriting anything
// newer that arrived while the flush was in flight.
func (r *ObservedRecorder) restore(pending map[healthKey]pendingBump) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, bump := range pending {
		if newer, ok := r.pending[key]; ok && newer.reportedAt >= bump.reportedAt {
			continue
		}
		r.pending[key] = bump
	}
}

// Run flushes buffered reports until ctx is cancelled, then flushes once more.
//
// tick supplies the cadence rather than a ticker built here: production passes
// time.NewTicker(FlushInterval).C and a test passes a channel it controls, so
// nothing in the test path sleeps and the behaviour under test is the flush
// rather than the scheduler.
//
// A failed flush is logged and the loop continues. Returning would leave every
// entity's last_report_at frozen, and under rule 8 a frozen last_report_at
// renders as unknown -- so a transient database blip would empty the health
// panel for the whole estate and keep it empty.
func (r *ObservedRecorder) Run(ctx context.Context, tick <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			// Shutdown must not discard the staleness clock for every entity
			// that is reporting normally, so the final flush gets its own
			// deadline on a context the cancellation has not already closed.
			final, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			if n, err := r.Flush(final); err != nil {
				slog.Warn("final observation flush failed", "error", err)
			} else if n > 0 {
				slog.Info("flushed buffered observations on shutdown", "count", n)
			}
			cancel()
			return
		case <-tick:
			if _, err := r.Flush(ctx); err != nil {
				slog.Warn("observation flush failed", "error", err)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Reads
// ---------------------------------------------------------------------------

// ObservedReading is one reporter's current view of one entity, with rule 8's
// staleness already applied.
//
// Staleness is computed here, at read time, and never by a background job that
// invents an `unknown` transition. An intruder's first act is killing the
// collector, and under transition-only logging a dead collector and a healthy
// estate are otherwise the same picture, forever.
type ObservedReading struct {
	domain.AssetHealth

	// Display is what a view renders: the reported state while it is fresh and
	// HealthUnknown once it is not. Never the last value.
	Display domain.HealthState
	// Stale reports whether the reading is past its horizon.
	Stale bool
	// StaleSince is the instant the reading stopped being believed, for
	// rendering "unknown (stale since T)". Empty while the reading is fresh.
	StaleSince string
}

// GetObservedState returns every reporter's current reading for one entity.
//
// Two monitors that disagree produce two rows and are shown as two rows: this
// deliberately does not reduce them to one verdict. Rule 2 forbids observed
// state being the sole input to a decision, and picking a winner here would be
// exactly that, made invisibly.
//
// An empty result is not health. An entity nothing has ever reported on is
// unobserved, and a view must say so rather than defaulting to up.
func (s *SQLStore) GetObservedState(ctx context.Context, entityType, entityID string) ([]ObservedReading, error) {
	var rows []domain.AssetHealth
	err := s.read(ctx, &rows,
		`SELECT * FROM asset_health WHERE entity_type = ? AND entity_id = ? ORDER BY reporter`,
		entityType, entityID)
	if err != nil {
		return nil, fmt.Errorf("getting observed state for %s %s: %w", entityType, entityID, err)
	}

	now := s.Now()
	out := make([]ObservedReading, 0, len(rows))
	for _, h := range rows {
		out = append(out, newObservedReading(h, now))
	}
	return out, nil
}

// ListUnmatchedObservations returns the drift queue, most recently seen first.
//
// An asset the estate has and the inventory does not is a finding, not noise,
// and a queue nothing reads is a queue that may as well not exist.
func (s *SQLStore) ListUnmatchedObservations(ctx context.Context, limit int) ([]domain.UnmatchedObservation, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows []domain.UnmatchedObservation
	err := s.read(ctx, &rows,
		`SELECT * FROM unmatched_observation ORDER BY last_seen_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("listing unmatched observations: %w", err)
	}
	return rows, nil
}

// ListTransitions returns one entity's transition ledger, newest first. This is
// the 03:00 query, and rule 15 folds it with change_log at read time.
func (s *SQLStore) ListTransitions(ctx context.Context, entityType, entityID string, limit int) ([]domain.ObservedTransition, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows []domain.ObservedTransition
	err := s.read(ctx, &rows,
		`SELECT * FROM observed_transition
		 WHERE entity_type = ? AND entity_id = ?
		 ORDER BY at DESC, id DESC
		 LIMIT ?`, entityType, entityID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing transitions for %s %s: %w", entityType, entityID, err)
	}
	return rows, nil
}

// newObservedReading applies the staleness arithmetic to one stored row.
func newObservedReading(h domain.AssetHealth, now time.Time) ObservedReading {
	reading := ObservedReading{
		AssetHealth: h,
		Display:     h.DisplayState(now),
		Stale:       h.IsStale(now),
	}
	if !reading.Stale {
		return reading
	}
	if horizon, ok := h.StaleAfter(); ok {
		reading.StaleSince = domain.FormatTime(horizon)
		return reading
	}
	// No declared cadence, so there is no horizon to name. The last report is
	// the most honest thing to show, and the reading is still not believed --
	// an unknown horizon is not evidence of freshness.
	reading.StaleSince = h.LastReportAt
	return reading
}

// settled builds the result for a report that did not open a transition.
func settled(disposition ObservationDisposition, health *domain.AssetHealth, now time.Time) *ObservationResult {
	out := &ObservationResult{Disposition: disposition}
	if health != nil {
		out.Reading = newObservedReading(*health, now)
	}
	return out
}

// readHealth loads one reading from the reader pool. A missing row is not an
// error: it is the first observation, and the first observation is a transition.
func (s *SQLStore) readHealth(ctx context.Context, key healthKey) (*domain.AssetHealth, error) {
	var h domain.AssetHealth
	err := s.readOne(ctx, &h, healthSelect, key.EntityType, key.EntityID, key.Reporter)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("reading observed state for %s %s from %s: %w",
			key.EntityType, key.EntityID, key.Reporter, err)
	}
	return &h, nil
}

// observableExists answers rule 6's question: does this inventory have the
// thing being reported on?
//
// These are the only two declared tables this file names, and both are named in
// a SELECT. The check exists precisely so that no observed write can create an
// entity: `INSERT ... ON CONFLICT` on the health table keyed by the reported id
// would be the natural implementation and it is exactly what turns a narrow
// monitoring token into an inventory-write vector.
func (s *SQLStore) observableExists(ctx context.Context, entityType, entityID string) (bool, error) {
	var query string
	switch entityType {
	case domain.ObservableAsset:
		query = existsAssetSelect
	case domain.ObservableServiceInstance:
		query = existsInstanceSelect
	default:
		return false, fmt.Errorf("checking observable %s %s: %q is not an observable entity type: %w",
			entityType, entityID, entityType, domain.ErrInvalid)
	}

	var found int
	err := s.readOne(ctx, &found, query, entityID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("checking whether %s %s exists: %w", entityType, entityID, err)
	}
	return true, nil
}

// authoriseScope enforces rule 6's environment scope.
//
// A credential is scoped to an explicit set of environments, and the entity's
// environments are read from declared data rather than taken from the report:
// a reporter that could name its own environment could name an in_scope one.
//
// An entity in no environment at all is refused rather than allowed. That is a
// data gap -- somebody created an asset without placing it -- and surfacing it
// as a denial gets it fixed, where an implicit allow would make the scope
// silently optional for exactly the rows nobody has looked at.
func (s *SQLStore) authoriseScope(ctx context.Context, scope domain.EnvironmentScope, obs *domain.Observation) error {
	codes, err := s.entityEnvironments(ctx, obs.EntityType, obs.EntityID)
	if err != nil {
		return err
	}
	// AllowsAll, not AllowsAny: a reading about an entity is visible in every
	// environment that entity is in, so the credential must cover all of them.
	if scope.AllowsAll(codes) {
		return nil
	}
	// The message names the scope and the entity's environments -- neither is
	// personal data, and a 403 that does not say which side is wrong takes an
	// afternoon to diagnose.
	return fmt.Errorf("observing %s %s: credential %s is scoped to [%s] and that entity is in [%s], which it does not fully cover: %w",
		obs.EntityType, obs.EntityID, obs.Reporter, scope, strings.Join(codes, ","), domain.ErrForbidden)
}

// entityEnvironments resolves the environment codes an observable sits in.
//
// A service_instance inherits the environments of the asset it is placed on:
// the placement is the physical fact, and a workload cannot be in an
// environment its host is not in.
func (s *SQLStore) entityEnvironments(ctx context.Context, entityType, entityID string) ([]string, error) {
	var query string
	switch entityType {
	case domain.ObservableAsset:
		query = assetEnvironmentsSelect
	case domain.ObservableServiceInstance:
		query = instanceEnvironmentsSelect
	default:
		return nil, fmt.Errorf("resolving environments for %s %s: %q is not an observable entity type: %w",
			entityType, entityID, entityType, domain.ErrInvalid)
	}

	var codes []string
	if err := s.read(ctx, &codes, query, entityID); err != nil {
		return nil, fmt.Errorf("resolving environments for %s %s: %w", entityType, entityID, err)
	}
	return codes, nil
}

// ---------------------------------------------------------------------------
// Statements. Every table named in a write below is in domain.ObservedTables.
// ---------------------------------------------------------------------------

const healthSelect = `
	SELECT * FROM asset_health
	WHERE entity_type = ? AND entity_id = ? AND reporter = ?`

// Read-only existence probes. Reads, not writes; see observableExists.
const (
	existsAssetSelect    = `SELECT 1 FROM asset WHERE id = ?`
	existsInstanceSelect = `SELECT 1 FROM service_instance WHERE id = ?`
)

// Read-only environment probes for rule 6's scope check. Reads, not writes; see
// authoriseScope. Ordered so a 403 message reads the same way twice.
const (
	assetEnvironmentsSelect = `
		SELECT e.code
		FROM asset_environment ae
		JOIN environment e ON e.id = ae.environment_id
		WHERE ae.asset_id = ?
		ORDER BY e.code`

	instanceEnvironmentsSelect = `
		SELECT e.code
		FROM service_instance si
		JOIN asset_environment ae ON ae.asset_id = si.host_asset_id
		JOIN environment e ON e.id = ae.environment_id
		WHERE si.id = ?
		ORDER BY e.code`
)

// healthUpsert writes a transition. The trailing WHERE is the structural half
// of rule 4's monotonicity: an older report cannot land even if two arrive at
// once and both pass the Go-side check. Verified on both engines -- SQLite and
// PostgreSQL agree on ON CONFLICT ... DO UPDATE ... WHERE, on referencing the
// existing row as asset_health.col, and on reporting zero rows affected when
// the guard excludes it.
const healthUpsert = `
	INSERT INTO asset_health (entity_type, entity_id, reporter, state, state_since,
	                          reported_at, last_report_at, interval_seconds, observation_id,
	                          flap_since, flap_count, flap_first_state, flap_seen)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT (entity_type, entity_id, reporter) DO UPDATE SET
	  state = EXCLUDED.state,
	  state_since = EXCLUDED.state_since,
	  reported_at = EXCLUDED.reported_at,
	  last_report_at = EXCLUDED.last_report_at,
	  interval_seconds = EXCLUDED.interval_seconds,
	  observation_id = EXCLUDED.observation_id,
	  flap_since = EXCLUDED.flap_since,
	  flap_count = EXCLUDED.flap_count,
	  flap_first_state = EXCLUDED.flap_first_state,
	  flap_seen = EXCLUDED.flap_seen
	WHERE asset_health.reported_at < EXCLUDED.reported_at`

// healthClearFlap ends an episode on the no-op path, where no upsert runs.
// state and state_since are absent from it for the same reason they are absent
// from healthBumpUpdate: closing an episode must not disturb the reading it was
// bracketing.
const healthClearFlap = `
	UPDATE asset_health
	SET flap_since = NULL, flap_count = NULL, flap_first_state = NULL, flap_seen = NULL
	WHERE entity_type = ? AND entity_id = ? AND reporter = ?`

// recentTransitionCount is rule 9's sliding window: how many state changes this
// (entity, reporter) has recorded since windowStart.
//
// flap_open counts because it stands in for exactly one state change -- the one
// that opened the episode. flap_close does not: it is a bracket spanning an
// episode, and counting it would let a single closure push a quiet entity back
// over the threshold. Transitions suppressed inside an episode are not counted
// either, and cannot be: they have no rows. That makes re-qualification after an
// episode strictly harder than the first time, which is the safe direction.
const recentTransitionCount = `
	SELECT COUNT(*) FROM observed_transition
	WHERE entity_type = ? AND entity_id = ? AND reporter = ?
	  AND kind IN (?, ?) AND at >= ?`

// healthBumpUpdate is the no-op path, and the columns it does NOT name are the
// point: state and state_since are absent, so a repeat report cannot move the
// onset. "Down since when" survives any number of heartbeats.
const healthBumpUpdate = `
	UPDATE asset_health
	SET last_report_at = ?, reported_at = ?, observation_id = ?, interval_seconds = ?
	WHERE entity_type = ? AND entity_id = ? AND reporter = ? AND reported_at <= ?`

// transitionInsert is the ledger. It carries all three kinds: an ordinary
// `transition`, and the two rule 9 bracket rows. The six flap_* columns are NULL
// on everything but a flap_close, where a table CHECK requires all of them --
// a flap_close that cannot say how much it hid is worse than no compression.
const transitionInsert = `
	INSERT INTO observed_transition (id, entity_type, entity_id, reporter, kind,
	                                 from_state, to_state, at,
	                                 flap_count, window_start, window_end,
	                                 first_state, last_state, settled_state)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// unmatchedUpsert counts repeats rather than accumulating a row per report: a
// collector pointed at a host this inventory has never heard of will say so
// every thirty seconds, and a queue that floods is a queue nobody reads.
const unmatchedUpsert = `
	INSERT INTO unmatched_observation (id, entity_type, entity_ref, reporter, state,
	                                   reported_at, first_seen_at, last_seen_at, report_count)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1)
	ON CONFLICT (entity_type, entity_ref, reporter) DO UPDATE SET
	  state = EXCLUDED.state,
	  reported_at = EXCLUDED.reported_at,
	  last_seen_at = EXCLUDED.last_seen_at,
	  report_count = unmatched_observation.report_count + 1`

// ---------------------------------------------------------------------------
// The write helper: a transaction that cannot reach the audit trail
// ---------------------------------------------------------------------------

// observedWrite is a transaction with no audit-log methods.
//
// Deliberately not the package's tx type. That one carries a domain.Actor and
// offers logCreate/logUpdate, and an observed write must reach neither: it has
// no actor to attribute -- attribution belongs to decisions, and nobody decided
// a heartbeat -- and routing an observation through a struct diff would log
// every poll via the moving timestamp, which is the unbounded growth the whole
// split exists to prevent. Here that is not a rule to remember. The methods do
// not exist, so the mistake does not compile.
type observedWrite struct {
	tx *sqlx.Tx
	db *DB
}

func (w *observedWrite) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return w.tx.ExecContext(ctx, w.db.Writer.Rebind(query), args...)
}

func (w *observedWrite) get(ctx context.Context, dest any, query string, args ...any) error {
	return w.tx.GetContext(ctx, dest, w.db.Writer.Rebind(query), args...)
}

// observedTx runs fn inside a write transaction carrying no audit actor.
func (s *SQLStore) observedTx(ctx context.Context, fn func(*observedWrite) error) error {
	sqlTx, err := s.db.Writer.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning observed transaction: %w", err)
	}
	w := &observedWrite{tx: sqlTx, db: s.db}
	if err := fn(w); err != nil {
		if rbErr := sqlTx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return fmt.Errorf("%w (rollback also failed: %v)", err, rbErr)
		}
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("committing observed transaction: %w", err)
	}
	return nil
}

// readHealth loads one reading inside the transaction.
func (w *observedWrite) readHealth(ctx context.Context, key healthKey) (*domain.AssetHealth, error) {
	var h domain.AssetHealth
	err := w.get(ctx, &h, healthSelect, key.EntityType, key.EntityID, key.Reporter)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("reading observed state for %s %s from %s: %w",
			key.EntityType, key.EntityID, key.Reporter, err)
	}
	return &h, nil
}

// upsertHealth writes a transition's current-state row. applied is false when
// the monotonicity guard refused it.
func (w *observedWrite) upsertHealth(ctx context.Context, h domain.AssetHealth) (bool, error) {
	res, err := w.exec(ctx, healthUpsert,
		h.EntityType, h.EntityID, h.Reporter, string(h.State), h.StateSince,
		h.ReportedAt, h.LastReportAt, h.IntervalSeconds, h.ObservationID,
		h.FlapSince, h.FlapCount, h.FlapFirstState, h.FlapSeen)
	if err != nil {
		return false, translateWriteErr(err, "recording observed state")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("recording observed state for %s %s: %w", h.EntityType, h.EntityID, err)
	}
	return n > 0, nil
}

// bumpHealth applies a no-op report's timestamps. The trailing predicate makes
// it safe to apply out of order: a bump older than what is stored does nothing.
func (w *observedWrite) bumpHealth(ctx context.Context, key healthKey, bump pendingBump) error {
	var interval *int
	if bump.intervalSeconds > 0 {
		interval = &bump.intervalSeconds
	}
	_, err := w.exec(ctx, healthBumpUpdate,
		bump.lastReportAt, bump.reportedAt, optionalText(bump.observationID), interval,
		key.EntityType, key.EntityID, key.Reporter, bump.reportedAt)
	if err != nil {
		return translateWriteErr(err, "bumping observed report time")
	}
	return nil
}

// recordTransition appends to the ledger. from is nil for the first ever
// reading, which is a transition and is logged.
func (w *observedWrite) recordTransition(ctx context.Context, key healthKey, from *string, to domain.HealthState, at string) error {
	_, err := w.exec(ctx, transitionInsert,
		NewID(), key.EntityType, key.EntityID, key.Reporter, domain.TransitionChange,
		from, string(to), at, nil, nil, nil, nil, nil, nil)
	if err != nil {
		return translateWriteErr(err, "recording observed transition")
	}
	return nil
}

// recordFlapOpen brackets the start of a compressed episode (rule 9).
//
// It is unconditional: a reader must always be able to see that compression
// began, and when. Its from_state and to_state name the state change that
// qualified, so no information about that oscillation is lost by not also
// writing a transition row for it.
func (w *observedWrite) recordFlapOpen(ctx context.Context, key healthKey,
	episode flapEpisode, to domain.HealthState, at string) error {
	from := string(episode.first)
	_, err := w.exec(ctx, transitionInsert,
		NewID(), key.EntityType, key.EntityID, key.Reporter, domain.TransitionFlapOpen,
		&from, string(to), at, nil, nil, nil, nil, nil, nil)
	if err != nil {
		return translateWriteErr(err, "opening a flap episode")
	}
	return nil
}

// recordFlapClose brackets the end of a compressed episode and reports what it
// hid (rule 9).
//
// Also unconditional, and it carries every number a reader needs to size the
// suppression: how many transitions happened, over what window, what the entity
// was when it started, what it was at the last oscillation, and what it is now.
// from_state and to_state span the whole episode -- first to settled -- so a
// timeline that reads only those two columns still tells the truth.
func (w *observedWrite) recordFlapClose(ctx context.Context, key healthKey, c flapClosure, at string) error {
	first, last, settled := string(c.episode.first), string(c.last), string(c.settled)
	_, err := w.exec(ctx, transitionInsert,
		NewID(), key.EntityType, key.EntityID, key.Reporter, domain.TransitionFlapClose,
		&first, settled, at,
		c.episode.count, c.episode.since, at, first, last, settled)
	if err != nil {
		return translateWriteErr(err, "closing a flap episode")
	}
	return nil
}

// clearFlap ends an episode on the row that carries it, without disturbing the
// reading itself.
func (w *observedWrite) clearFlap(ctx context.Context, key healthKey) error {
	_, err := w.exec(ctx, healthClearFlap, key.EntityType, key.EntityID, key.Reporter)
	if err != nil {
		return translateWriteErr(err, "clearing a flap episode")
	}
	return nil
}

// countRecentTransitions is the sliding-window count rule 9 compares against
// domain.FlapThreshold.
func (w *observedWrite) countRecentTransitions(ctx context.Context, key healthKey, windowStart string) (int, error) {
	var n int
	err := w.get(ctx, &n, recentTransitionCount,
		key.EntityType, key.EntityID, key.Reporter,
		domain.TransitionChange, domain.TransitionFlapOpen, windowStart)
	if err != nil {
		return 0, fmt.Errorf("counting recent transitions for %s %s from %s: %w",
			key.EntityType, key.EntityID, key.Reporter, err)
	}
	return n, nil
}

// queueUnmatched records a report about something this inventory does not have.
//
// It writes through rather than buffering: an unmatched report changes the
// count and the window, so there is something to record, and rule 6's per
// credential rate limit is what bounds the volume. That limit lives at the HTTP
// layer, where a caller can be told it is being throttled.
func (s *SQLStore) queueUnmatched(ctx context.Context, obs *domain.Observation, now time.Time) error {
	at := domain.FormatTime(now)
	return s.observedTx(ctx, func(w *observedWrite) error {
		_, err := w.exec(ctx, unmatchedUpsert,
			NewID(), obs.EntityType, obs.EntityID, obs.Reporter, string(obs.State),
			obs.ReportedAt, at, at)
		if err != nil {
			return translateWriteErr(err, "queueing unmatched observation")
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

// observationReporter derives the reporter from the authenticated credential.
//
// Rule 5: attribution is server-assigned and structurally unforgeable. A
// caller-supplied reporter would forge another monitor's readings, and since
// the reporter is part of the identity key it would also let one credential
// overwrite another's history rather than sit beside it.
//
// A non-agent actor here is a mis-wired route, not a bad request: an operator
// who knows a reading is wrong writes a health_override (rule 14), which is
// declared state and audited, rather than editing an observed column that the
// next poll would clobber thirty seconds later.
func observationReporter(actor domain.Actor) (string, error) {
	if err := actor.Validate(); err != nil {
		return "", fmt.Errorf("attributing observation: %w", err)
	}
	id, ok := actor.CredentialID()
	if !ok {
		ve := &domain.ValidationError{}
		ve.Add("actor", "an observation is attributed to a monitoring credential, not a %s actor; build one with domain.AgentActor",
			actor.Kind)
		return "", fmt.Errorf("attributing observation: %w", ve)
	}
	return id, nil
}

// optionalText renders an empty string as SQL NULL, for the two nullable
// columns a reporter may legitimately omit.
func optionalText(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// derefText reads a nullable TEXT column, treating NULL as the empty string.
func derefText(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
