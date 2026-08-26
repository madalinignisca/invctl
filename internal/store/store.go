// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/madalinignisca/invctl/internal/domain"
)

// SQLStore is the portable implementation of Store. Everything it executes
// runs unmodified on both engines; the two dialect-specific concerns (search,
// session storage) are isolated behind their own types.
type SQLStore struct {
	db  *DB
	now func() time.Time
}

// New builds a store over an open database.
func New(db *DB) *SQLStore {
	return &SQLStore{db: db, now: time.Now}
}

// WithClock replaces the time source. Tests use it to get deterministic
// timestamps; nothing in production should.
func (s *SQLStore) WithClock(now func() time.Time) *SQLStore {
	clone := *s
	clone.now = now
	return &clone
}

// DB exposes the underlying pools for the search implementations and the
// session store, which need a *sql.DB and are dialect-aware by design.
func (s *SQLStore) DB() *DB { return s.db }

// NewID returns a UUIDv7: time-sortable, so rows created together cluster
// together in the index instead of scattering the way UUIDv4 does.
func NewID() string {
	id, err := uuid.NewV7()
	if err != nil {
		// NewV7 only fails if the system entropy source fails, at which point
		// a random fallback is strictly better than refusing to write.
		return uuid.NewString()
	}
	return id.String()
}

// Now returns the store's clock, truncated the same way stored timestamps are.
func (s *SQLStore) Now() time.Time { return s.now().UTC() }

// tx is a transaction with the audit trail attached.
//
// Every mutation goes through one of these and calls log() before it returns.
// The change_log row is written inside the same transaction as the mutation,
// so an audit entry cannot survive a rolled-back change and a change cannot
// escape without an entry.
type tx struct {
	tx     *sqlx.Tx
	db     *DB
	permit domain.Permit
	actor  domain.Actor
	at     string
}

// rebind rewrites `?` placeholders for the target engine.
func (t *tx) rebind(query string) string { return t.db.Writer.Rebind(query) }

func (t *tx) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(ctx, t.rebind(query), args...)
}

func (t *tx) get(ctx context.Context, dest any, query string, args ...any) error {
	return t.tx.GetContext(ctx, dest, t.rebind(query), args...)
}

// selectAll reads many rows inside the transaction.
//
// Inside it, not through s.read, and the difference matters: the reader pool is
// a separate connection that cannot see this transaction's uncommitted writes.
// A bulk import resolves each row's parent against assets earlier rows just
// created, so reading from outside would miss exactly the rows the resolution
// depends on.
func (t *tx) selectAll(ctx context.Context, dest any, query string, args ...any) error {
	return t.tx.SelectContext(ctx, dest, t.rebind(query), args...)
}

// log writes the audit row. Callers pass a pre-rendered JSON diff.
//
// batchID is NULL for the overwhelming majority of writes -- one operator's
// edit to one row needs nothing else to reconstruct it. Pass a non-empty
// value only when several change_log rows across different entities are the
// outcome of ONE bulk operation (currently: team-retirement reassignment,
// WP-G7 piece 2, docs/ownership-report-design.md §4) -- timestamp clustering
// alone cannot tell a reader "one operator claimed fifty things" from
// "fifty independent edits landed in the same second", and the column cannot
// be added retroactively because change_log admits no UPDATE. This is the
// ONLY INSERT into change_log in the codebase; every other writer in this
// file and every caller elsewhere goes through log, logCreate or logUpdate
// rather than a second insertion point.
// authorize is THE OBJECT-LEVEL AUTHORIZATION CHECK, and it is called from
// two places deliberately -- log (so every audited write is gated) and
// logUpdateBatch, BEFORE the no-op short-circuit (so a write that changes
// nothing is still refused rather than silently allowed to bump row_version
// unaudited). It used to live inline inside log alone; a no-op update that
// returned before ever reaching log's INSERT bypassed it entirely -- the
// physical `UPDATE ... row_version = row_version + 1` had already run by
// then, so a ScopedPermit covering nothing could still mutate a row it did
// not own, provided the caller resubmitted the row's current values. See
// TestANoOpUpdateIsStillAuthorized.
//
// Extracting this out of log does not change log's contract: log is still
// the only insertion point into change_log (see its doc above), and
// therefore the only place every declared mutation that DOES produce a diff
// is guaranteed to pass through. Moving the check into individual store
// methods instead of a shared helper means forty places to forget it, which
// is precisely the failure mode docs/rbac-design.md §6 names and rejects.
// And the converse is now also true and worth stating plainly: if a second
// `INSERT INTO change_log` is ever added anywhere in this codebase, that
// statement has also added a second, unguarded authorization bypass --
// which is exactly what TestChangeLogIsAppendOnly and
// TestNoAssembledWriteReachesChangeLog already exist to make impossible to
// add unnoticed.
//
// A nil permit is refused rather than treated as "no restriction": every
// caller of write/writeSerializable/writeTx is required to supply one
// (they take domain.Permit, not domain.Actor, precisely so a caller cannot
// pass "nothing"), so a nil here means a bug in this package, not an
// absent credential -- and the fail-closed answer to a bug in the
// authorization path is deny, not allow.
func (t *tx) authorize(entityType, entityID string) error {
	if t.permit == nil || !t.permit.Covers(entityType, entityID) {
		return fmt.Errorf("writing change log for %s %s: %w", entityType, entityID, domain.ErrForbidden)
	}
	return nil
}

func (t *tx) log(ctx context.Context, entityType, entityID, action, diff, batchID string) error {
	if err := t.authorize(entityType, entityID); err != nil {
		return err
	}

	var batch any
	if batchID != "" {
		batch = batchID
	}
	_, err := t.exec(ctx,
		`INSERT INTO change_log (id, entity_type, entity_id, action, actor, actor_kind, diff, batch_id, at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		NewID(), entityType, entityID, action, t.actor.ID, t.actor.Kind, diff, batch, t.at)
	if err != nil {
		return fmt.Errorf("writing change log for %s %s: %w", entityType, entityID, err)
	}
	return nil
}

// logCreate records a full snapshot of a newly created row.
func (t *tx) logCreate(ctx context.Context, entityType, entityID string, entity any) error {
	snapshot, err := snapshotJSON(entity)
	if err != nil {
		return err
	}
	return t.log(ctx, entityType, entityID, domain.ActionCreate, snapshot, "")
}

// logUpdate records the field-level difference. A no-op update writes nothing:
// an audit trail full of empty entries is worse than one without them.
func (t *tx) logUpdate(ctx context.Context, entityType, entityID string, before, after any) error {
	return t.logUpdateBatch(ctx, entityType, entityID, before, after, "")
}

// logUpdateBatch is logUpdate with a batch id attached -- the WP-G4a piece 3
// counterpart of what team_reassignment.go and bulk_ownership.go already do
// for ownership moves: several change_log rows across different entities,
// one per entity, sharing one batch_id because they are all the outcome of
// ONE bulk operation (docs/tags-design.md §4a: "each entity gets its own
// change_log row sharing one batch id"). logUpdate is the batchID=""
// special case, kept as its own name because that is still the overwhelming
// majority of writes in this codebase.
func (t *tx) logUpdateBatch(ctx context.Context, entityType, entityID string, before, after any, batchID string) error {
	// Authorize BEFORE the no-op short-circuit below, not after. A refused
	// write must be refused whether or not it happens to produce a diff --
	// see authorize's doc comment for the bypass this closes.
	if err := t.authorize(entityType, entityID); err != nil {
		return err
	}
	diff, changed, err := diffJSON(before, after)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	return t.log(ctx, entityType, entityID, domain.ActionUpdate, diff, batchID)
}

// write runs fn inside a transaction on the writer pool.
//
// On SQLite the writer pool holds a single connection, so concurrent callers
// queue here rather than colliding on the database lock.
//
// Takes a domain.Permit, not a domain.Actor -- WP-G1 Task 7. domain.Actor is
// deliberately unable to satisfy domain.Permit (see that interface's doc
// comment), so this is a compile-time requirement that every write transaction,
// present and future, carries an authorization decision rather than merely an
// identity to blame the row on afterwards.
func (s *SQLStore) write(ctx context.Context, p domain.Permit, fn func(*tx) error) error {
	return s.writeTx(ctx, p, nil, fn)
}

// writeSerializable runs fn under an isolation level that actually prevents
// check-then-act races, retrying if the engine aborts the transaction.
//
// Use it wherever a write asserts an invariant it just SELECTed -- "this port
// has no active cable", "this asset is not already attached". At PostgreSQL's
// default read-committed level two such transactions both see the old state and
// both commit, which was verified rather than assumed: forcing the interleaving
// produced two active cables on one port. SQLite never showed the bug because
// its writer pool holds a single connection, so the primary development engine
// silently masks a defect that is live on the deployment target.
func (s *SQLStore) writeSerializable(ctx context.Context, p domain.Permit, fn func(*tx) error) error {
	opts := &sql.TxOptions{Isolation: sql.LevelSerializable}
	if s.db.Driver != DriverPostgres {
		// SQLite serialises writes already, and modernc rejects an explicit
		// isolation level it does not implement.
		opts = nil
	}

	// A serialization failure means "you raced, try again", not "this is
	// impossible" -- so retry rather than surfacing it to the operator.
	//
	// p is passed to every attempt UNCHANGED, and that is not merely
	// convenient -- it is the property TestAPermitIsUnchangedByARolledBackTransaction
	// pins. A Permit carries no transaction-scoped state (see scopedPermit's
	// doc comment in internal/domain/role.go), so a discarded, retried
	// transaction has nothing to reset the permit to: the same value that
	// authorized attempt 1 authorizes attempt 3, because it is the literal
	// same value, not a rebuilt one.
	const attempts = 3
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		err = s.writeTx(ctx, p, opts, fn)
		if err == nil || !isSerializationFailure(err) {
			return err
		}
	}
	return fmt.Errorf("after %d serialization retries: %w", attempts, err)
}

// isSerializationFailure reports whether the engine aborted the transaction
// because it could not be serialised. PostgreSQL raises SQLSTATE 40001.
func isSerializationFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "40001") ||
		strings.Contains(msg, "could not serialize") ||
		strings.Contains(msg, "concurrent update")
}

func (s *SQLStore) writeTx(ctx context.Context, p domain.Permit, opts *sql.TxOptions, fn func(*tx) error) error {
	sqlTx, err := s.db.Writer.BeginTxx(ctx, opts)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	// actor is derived from the permit rather than carried separately, so
	// log's INSERT -- which names t.actor.ID and t.actor.Kind -- needs no
	// change at all: the permit is now the single source both of "who gets
	// blamed" and "what may they touch".
	//
	// p is guarded against nil here, rather than left to panic on p.Actor():
	// a nil Permit reaching this far is already a bug in this package (every
	// public store method requires one), and the fail-closed answer to a bug
	// on the authorization path is log's ErrForbidden, not a crash that
	// takes the request down before the transaction even rolls back cleanly.
	var actor domain.Actor
	if p != nil {
		actor = p.Actor()
	}
	t := &tx{tx: sqlTx, db: s.db, permit: p, actor: actor, at: domain.FormatTime(s.now())}

	if err := fn(t); err != nil {
		if rbErr := sqlTx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return fmt.Errorf("%w (rollback also failed: %w)", err, rbErr)
		}
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}
	return nil
}

// read runs a query against the reader pool.
func (s *SQLStore) read(ctx context.Context, dest any, query string, args ...any) error {
	return s.db.Reader.SelectContext(ctx, dest, s.db.Reader.Rebind(query), args...)
}

// readOne runs a single-row query, translating "no rows" into ErrNotFound so
// that no driver error ever reaches a handler.
func (s *SQLStore) readOne(ctx context.Context, dest any, query string, args ...any) error {
	err := s.db.Reader.GetContext(ctx, dest, s.db.Reader.Rebind(query), args...)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	}
	return err
}

// translateWriteErr maps engine-specific constraint failures onto domain
// sentinels. Both engines report uniqueness violations with different codes
// and wording, so this matches on the text rather than pretending otherwise.
func translateWriteErr(err error, what string) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	switch {
	case isUniqueViolation(err):
		return fmt.Errorf("%s: %w", what, domain.ErrConflict)
	case strings.Contains(msg, "foreign key"),
		strings.Contains(msg, "violates foreign key constraint"):
		return fmt.Errorf("%s: references something that does not exist: %w", what, domain.ErrInvalid)
	case strings.Contains(msg, "check constraint"):
		return fmt.Errorf("%s: violates a database check constraint: %w", what, domain.ErrInvalid)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// isUniqueViolation reports whether err is a uniqueness-constraint failure,
// on either engine. Both report it with different codes and wording -- SQLite
// via modernc's driver text, PostgreSQL via pgx's SQLSTATE 23505 message --
// so this matches on the text rather than a single shared error type, the
// same approach translateWriteErr uses for every other constraint kind.
//
// Split out from translateWriteErr so a caller that needs to distinguish
// "this specific write lost a race" from "this write failed for some other
// reason" can ask the narrow question without also matching a foreign key or
// check-constraint failure as if it were the same case.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "unique_violation")
}

// placeholders builds "?, ?, ?" for an IN clause of n values. sqlx.In would do
// this, but it also rewrites the argument list, and several queries here mix a
// slice with scalar arguments.
// idChunkSize bounds an IN (...) list.
//
// Every bound value in a statement counts against a driver limit, and the two
// engines fail at different points: measured with the pinned drivers, SQLite
// refuses at 32,767 parameters ("too many SQL variables") and pgx at 65,536
// ("extended protocol limited to 65535 parameters"). Both surface as an opaque
// driver error with nothing an operator can act on.
//
// That was unreachable while the project footprint carried a node budget. It is
// reachable now that the footprint is complete -- correctly, since a rendering
// cap must not truncate a sum -- so the id lists it feeds are chunked instead.
// 500 is far below either limit and leaves room for the handful of other bound
// values each of these statements carries.
const idChunkSize = 500

// chunkIDs splits an id list for IN (...) use. A single chunk is returned
// unwrapped so the common case allocates nothing extra.
func chunkIDs(ids []string) [][]string {
	if len(ids) <= idChunkSize {
		return [][]string{ids}
	}
	chunks := make([][]string, 0, (len(ids)+idChunkSize-1)/idChunkSize)
	for start := 0; start < len(ids); start += idChunkSize {
		end := start + idChunkSize
		if end > len(ids) {
			end = len(ids)
		}
		chunks = append(chunks, ids[start:end])
	}
	return chunks
}

func placeholders(n int) string {
	if n <= 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// anySlice converts a []string to []any for variadic query arguments.
func anySlice(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

// requireVersion turns an UPDATE's result into a conflict when the row moved.
//
// Zero rows affected does NOT mean the row is gone. Every caller here has just
// read it, and nothing in this codebase deletes an entity -- so the clause that
// failed is `row_version = ?`, which means somebody else wrote the row between
// the form being rendered and this submission arriving. Reporting that as a 404
// would tell the operator their thing had vanished; reporting nothing at all is
// how the slower of two people silently reverts the faster one and gets the
// change_log entry with their name on it.
//
// See internal/domain/version.go for why the token is an integer.
// version points at the caller's RowVersion field, which is advanced to match
// the row this statement just wrote. Without that, a caller updating the same
// struct twice compares a stale token against a row it moved itself and gets a
// conflict against nobody. Taking a pointer rather than leaving it to each call
// site makes it impossible to forget in one of thirteen.
//
// Advanced optimistically, like UpdatedAt, which every method here also sets
// before the write: the struct is meaningful only when the error is nil.
func requireVersion(res sql.Result, entity, id string, version *int) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking the %s update: %w", entity, err)
	}
	if n == 0 {
		return fmt.Errorf("%s %s: %w", entity, id, domain.ErrStale)
	}
	*version++
	return nil
}
