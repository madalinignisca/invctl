// Retention, separated by table rather than by predicate (docs/AUDIT.md rule 10).
//
// This file holds the only DELETE FROM in this codebase that removes a fact.
// Everything else that deletes -- asset_closure, asset_environment,
// dependency_data_class, search_index -- replaces a set the parent owns, inside
// the parent's own transaction, and is not deletion in the sense that matters.
//
// Four properties are structural here, not conventions:
//
//  1. The method takes NO table name. observed_transition is a constant in this
//     file and appears in exactly two statements. A prune that could be pointed
//     at a table is a prune that will eventually be pointed at change_log, and
//     change_log is append-only forever: no UPDATE, no DELETE, ever. Correcting
//     a wrong entry there means writing a new one.
//
//  2. Nothing is selected by actor_kind. That column records WHO WROTE a row,
//     not what kind of fact it is, and this repo already writes declared state
//     under non-user kinds -- UpsertLDAPUser creates accounts as `system`,
//     SystemActor seeds the declared inventory, and discovery agents create
//     dependency rows, including the chd-carrying edges, as `agent`. A prune
//     keyed on actor_kind would destroy exactly the scope-validation evidence
//     this tool exists to produce, and would do it silently.
//
//  3. There is a floor. A row belonging to an entity that resolves to an
//     environment with in_scope = TRUE is never removed inside
//     domain.MinInScopeRetentionDays, whatever the run was asked for. The floor
//     is applied per row and in SQL, so a short retention prunes the
//     out-of-scope estate and leaves the in-scope one intact rather than either
//     failing outright or quietly doing what it was told.
//
//  4. Every run writes one change_log row, in the same transaction as the
//     delete, recording the window, the counts and the actor. A deletion that
//     leaves no trace of having happened is the one kind of gap an audit trail
//     cannot recover from.
//
// The caller is cmd/invctl. Rule 10 says admin-invoked: never handler code,
// never an automatic side effect of a write path. Nothing in internal/web
// references this file, and a test asserts that.

package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gabriel/invctl/internal/domain"
)

// prunableTable is the whole of what may be pruned. It is a constant rather
// than a parameter on purpose -- see (1) in the file comment.
const prunableTable = "observed_transition"

// PruneOptions is what an operator may choose about a run.
//
// Note what is absent: there is no table, no entity type, no environment, no
// actor-kind filter and no "force". The only dial is how far back to reach, and
// the floor below still applies to it.
type PruneOptions struct {
	// Before is the cutoff. Rows older than this are candidates.
	Before time.Time
	// DryRun computes the counts and deletes nothing. It writes no change_log
	// row either: nothing happened, and an audit trail that records intentions
	// is one where a reader can no longer tell which entries are events.
	DryRun bool
}

// PruneReport is what a run did, for the operator and for the audit row.
type PruneReport struct {
	// RunID identifies this run and is the change_log row's entity_id. A prune
	// has no single entity, and reusing some other row's id would make the
	// entry look like a change to that row.
	RunID string
	// Before is the requested cutoff; InScopeFloor is the earliest instant the
	// run was allowed to reach for an in-scope entity. They are equal whenever
	// the request was already older than the floor.
	Before       time.Time
	InScopeFloor time.Time
	// Deleted is how many rows went. Protected is how many matched the cutoff
	// and were kept because their entity resolves to an in_scope environment.
	Deleted   int64
	Protected int64
	DryRun    bool
	At        time.Time
}

// FloorApplied reports whether the floor bit -- whether the run was asked to
// reach somewhere it was not allowed to go for in-scope entities. When it did,
// the estate now keeps two different retentions and the operator has to be told
// so, or they will read the deleted count as proof their setting took effect.
func (r PruneReport) FloorApplied() bool { return r.InScopeFloor.Before(r.Before) }

// PruneObservedTransitions deletes observed transitions older than a cutoff.
//
// It is the only method in this package that removes a fact, and the only
// caller is the admin-invoked CLI subcommand.
//
// The actor must be a person. A prune attributed to `system` is a deletion
// nobody is accountable for, and rule 10 requires the run to record its actor;
// recording a placeholder satisfies the letter of that and none of the point.
// A machine credential is refused for the same reason it is refused everywhere
// else: an agent that can prune the transition ledger can erase the record of
// what it reported.
func (s *SQLStore) PruneObservedTransitions(ctx context.Context, actor domain.Actor, opts PruneOptions) (*PruneReport, error) {
	now := s.Now()

	ve := &domain.ValidationError{}
	if actor.Kind != domain.ActorKindUser {
		ve.Add("actor", "a prune is an operator's act and is recorded as one; a %s actor may not run it", actor.Kind)
	}
	if actor.ID == "" {
		ve.Add("actor", "is required: the run records who did it")
	}
	if opts.Before.IsZero() {
		ve.Add("before", "is required: a prune with no cutoff is a truncate")
	} else if opts.Before.UTC().After(now) {
		ve.Add("before", "must not be in the future")
	}
	if err := ve.OrNil(); err != nil {
		return nil, fmt.Errorf("pruning %s: %w", prunableTable, err)
	}

	before := opts.Before.UTC()
	// The effective reach for an in-scope entity is the EARLIER of the two: the
	// floor never moves the cutoff outwards. An operator asking for five years
	// of retention gets five years for everything, and the floor only bites when
	// a short request would reach inside an in-scope entity's year.
	floor := domain.InScopeRetentionFloor(now)
	if before.Before(floor) {
		floor = before
	}

	report := &PruneReport{
		RunID:        NewID(),
		Before:       before,
		InScopeFloor: floor,
		DryRun:       opts.DryRun,
		At:           now,
	}
	beforeText, floorText := domain.FormatTime(before), domain.FormatTime(floor)

	if opts.DryRun {
		// Counted on the reader pool: a dry run must not queue behind the
		// single SQLite writer, and it changes nothing that would need a
		// transaction to be consistent.
		for _, entityType := range domain.ObservableEntityTypes {
			inScope, ok := inScopeSubquery[entityType]
			if !ok {
				return nil, fmt.Errorf("pruning %s: %q is observable but has no in_scope resolution; "+
					"a new observable entity type must state how it resolves to an environment", prunableTable, entityType)
			}
			deletable, err := s.countOne(ctx, pruneDeletableCount(inScope), entityType, beforeText, floorText)
			if err != nil {
				return nil, fmt.Errorf("counting prunable %s rows for %s: %w", prunableTable, entityType, err)
			}
			protected, err := s.countOne(ctx, pruneProtectedCount(inScope), entityType, beforeText, floorText)
			if err != nil {
				return nil, fmt.Errorf("counting protected %s rows for %s: %w", prunableTable, entityType, err)
			}
			report.Deleted += deletable
			report.Protected += protected
		}
		return report, nil
	}

	err := s.write(ctx, actor, func(t *tx) error {
		for _, entityType := range domain.ObservableEntityTypes {
			inScope, ok := inScopeSubquery[entityType]
			if !ok {
				return fmt.Errorf("pruning %s: %q is observable but has no in_scope resolution; "+
					"a new observable entity type must state how it resolves to an environment", prunableTable, entityType)
			}

			// Counted before the delete and inside the same transaction, so the
			// number in the audit row is the number this run kept rather than
			// whatever a later reader would see.
			protected, err := t.countOne(ctx, pruneProtectedCount(inScope), entityType, beforeText, floorText)
			if err != nil {
				return fmt.Errorf("counting protected %s rows for %s: %w", prunableTable, entityType, err)
			}
			report.Protected += protected

			res, err := t.exec(ctx, pruneDelete(inScope), entityType, beforeText, floorText)
			if err != nil {
				return translateWriteErr(err, "pruning "+prunableTable)
			}
			n, err := res.RowsAffected()
			if err != nil {
				return fmt.Errorf("counting pruned %s rows for %s: %w", prunableTable, entityType, err)
			}
			report.Deleted += n
		}

		diff, err := pruneDiff(report)
		if err != nil {
			return err
		}
		// One row per run, whether or not anything matched. "The prune ran and
		// found nothing" and "the prune did not run" are different facts, and
		// the second is the one an auditor cares about.
		return t.log(ctx, prunableTable, report.RunID, domain.ActionDelete, diff)
	})
	if err != nil {
		return nil, err
	}
	return report, nil
}

// pruneDiff renders the audit row's diff.
//
// Not a field-level diff, because a prune is not a field-level change: it is a
// window and two counts. The shape is a single named object so a reader
// scanning the change log sees `pruned` and knows what kind of entry it is
// without parsing anything -- and `diff` is opaque JSON that is never queried,
// so nothing downstream depends on its shape.
func pruneDiff(r *PruneReport) (string, error) {
	payload := map[string]any{
		"pruned": pruneAudit{
			Table:            prunableTable,
			Before:           domain.FormatTime(r.Before),
			InScopeFloor:     domain.FormatTime(r.InScopeFloor),
			InScopeMinDays:   domain.MinInScopeRetentionDays,
			Deleted:          r.Deleted,
			ProtectedInScope: r.Protected,
		},
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("rendering the prune audit entry: %w", err)
	}
	return string(out), nil
}

type pruneAudit struct {
	Table            string `json:"table"`
	Before           string `json:"before"`
	InScopeFloor     string `json:"in_scope_floor"`
	InScopeMinDays   int    `json:"in_scope_minimum_days"`
	Deleted          int64  `json:"deleted"`
	ProtectedInScope int64  `json:"protected_in_scope"`
}

// ---------------------------------------------------------------------------
// Statements
// ---------------------------------------------------------------------------

// inScopeSubquery resolves an observable entity type to the set of ids that sit
// in an in_scope environment. Keyed by entity type and checked against
// domain.ObservableEntityTypes at run time, so a third observable type fails the
// prune loudly instead of having its rows silently treated as out of scope.
//
// A service_instance inherits its host's environments: the placement is the
// physical fact, and a workload cannot be in an environment its host is not in.
// This mirrors entityEnvironments in observed.go, and the two must agree -- a
// credential that may not WRITE health for an in-scope entity and a prune that
// may not DELETE its history are the same scope boundary read from both sides.
var inScopeSubquery = map[string]string{
	domain.ObservableAsset: `
		SELECT ae.asset_id
		FROM asset_environment ae
		JOIN environment e ON e.id = ae.environment_id
		WHERE e.in_scope = TRUE`,
	domain.ObservableServiceInstance: `
		SELECT si.id
		FROM service_instance si
		JOIN asset_environment ae ON ae.asset_id = si.host_asset_id
		JOIN environment e ON e.id = ae.environment_id
		WHERE e.in_scope = TRUE`,
}

// pruneDelete removes rows older than the cutoff, except that a row whose
// entity is in scope must also be older than the floor.
//
// The floor is a predicate in the statement rather than a check in Go because
// the protection has to hold per row: a run against a mixed estate prunes the
// out-of-scope half and leaves the in-scope half where it is. Expressed as an
// up-front refusal it would either block the whole run or, worse, be forgotten
// on the second caller.
//
// Both engines accept `entity_id NOT IN (subquery)` here without a NULL trap:
// asset_environment.asset_id and service_instance.id are both NOT NULL, so the
// subquery cannot yield a NULL that would make NOT IN unknown for every row.
func pruneDelete(inScope string) string {
	return `DELETE FROM ` + prunableTable + `
		WHERE entity_type = ?
		  AND at < ?
		  AND (at < ? OR entity_id NOT IN (` + inScope + `))`
}

// pruneProtectedCount is how many rows the floor kept: old enough for the
// requested cutoff, too recent for an in-scope entity's floor.
func pruneProtectedCount(inScope string) string {
	return `SELECT COUNT(*) FROM ` + prunableTable + `
		WHERE entity_type = ?
		  AND at < ?
		  AND at >= ?
		  AND entity_id IN (` + inScope + `)`
}

// pruneDeletableCount mirrors pruneDelete exactly, for a dry run. It is written
// as a separate string rather than derived from the delete because a COUNT that
// drifts from the DELETE it predicts is a dry run that lies.
func pruneDeletableCount(inScope string) string {
	return `SELECT COUNT(*) FROM ` + prunableTable + `
		WHERE entity_type = ?
		  AND at < ?
		  AND (at < ? OR entity_id NOT IN (` + inScope + `))`
}

// countOne runs a scalar COUNT against the reader pool.
func (s *SQLStore) countOne(ctx context.Context, query string, args ...any) (int64, error) {
	var n int64
	if err := s.readOne(ctx, &n, query, args...); err != nil {
		return 0, err
	}
	return n, nil
}

// countOne runs a scalar COUNT inside a transaction.
func (t *tx) countOne(ctx context.Context, query string, args ...any) (int64, error) {
	var n int64
	if err := t.get(ctx, &n, query, args...); err != nil {
		return 0, err
	}
	return n, nil
}

// unmatchedTable is the second and last prunable table. Like prunableTable it
// is a constant, and PruneUnmatchedObservations takes no table parameter, so
// the rule-10 property holds: a prune method names its own target and a caller
// cannot redirect one at change_log.
const unmatchedTable = "unmatched_observation"

// PruneUnmatchedObservations clears resolved drift.
//
// The drift queue records reports about entities the inventory does not hold.
// Rule 6 wants those surfaced as findings, and a finding that has been dealt
// with -- the asset was entered, or the reporter was fixed -- should not sit in
// the queue forever making the real backlog harder to read. It is also the one
// observed table an authenticated credential can grow at will, which is why it
// needs a way out at all: MaxUnmatchedPerReporter bounds it, and this empties
// it.
//
// Deliberately NOT folded into PruneObservedTransitions as an extra flag. The
// two have genuinely different safety arguments: the transition ledger is
// evidence of what an estate did and carries the 365-day in-scope floor, while
// this queue is a worklist. Sharing an entry point would mean one set of
// options guarding two different risks, and the weaker argument would win.
//
// Same actor rule: an operator only. An agent that can clear the record of
// entities it named is an agent that can cover its own reconnaissance.
func (s *SQLStore) PruneUnmatchedObservations(ctx context.Context, actor domain.Actor, opts PruneOptions) (*PruneReport, error) {
	now := s.Now()

	ve := &domain.ValidationError{}
	if actor.Kind != domain.ActorKindUser {
		ve.Add("actor", "a prune is an operator's act and is recorded as one; a %s actor may not run it", actor.Kind)
	}
	if actor.ID == "" {
		ve.Add("actor", "is required: the run records who did it")
	}
	if opts.Before.IsZero() {
		ve.Add("before", "is required: a prune with no cutoff is a truncate")
	} else if opts.Before.UTC().After(now) {
		ve.Add("before", "must not be in the future")
	}
	if err := ve.OrNil(); err != nil {
		return nil, fmt.Errorf("pruning %s: %w", unmatchedTable, err)
	}

	before := opts.Before.UTC()
	cutoff := domain.FormatTime(before)
	report := &PruneReport{RunID: NewID(), Before: before, DryRun: opts.DryRun, At: now}

	// last_seen_at, not first_seen_at: a ref still being reported is still a live
	// finding however long ago it first appeared.
	n, err := s.countOne(ctx, `SELECT COUNT(*) FROM unmatched_observation WHERE last_seen_at < ?`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("counting prunable %s rows: %w", unmatchedTable, err)
	}
	report.Deleted = n
	if opts.DryRun {
		return report, nil
	}

	err = s.write(ctx, actor, func(t *tx) error {
		if _, err := t.exec(ctx,
			`DELETE FROM unmatched_observation WHERE last_seen_at < ?`, cutoff); err != nil {
			return translateWriteErr(err, "pruning "+unmatchedTable)
		}
		diff, err := pruneDiff(report)
		if err != nil {
			return err
		}
		// One row per run whether or not anything matched, for the same reason
		// the transition prune does it: "ran and found nothing" and "did not
		// run" are different facts and the second is the one an auditor wants.
		return t.log(ctx, unmatchedTable, report.RunID, domain.ActionDelete, diff)
	})
	if err != nil {
		return nil, err
	}
	return report, nil
}
