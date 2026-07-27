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

	"github.com/gabriel/invctl/internal/domain"
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
	tx    *sqlx.Tx
	db    *DB
	actor domain.Actor
	at    string
}

// rebind rewrites `?` placeholders for the target engine.
func (t *tx) rebind(query string) string { return t.db.Writer.Rebind(query) }

func (t *tx) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(ctx, t.rebind(query), args...)
}

func (t *tx) get(ctx context.Context, dest any, query string, args ...any) error {
	return t.tx.GetContext(ctx, dest, t.rebind(query), args...)
}

// log writes the audit row. Callers pass a pre-rendered JSON diff.
func (t *tx) log(ctx context.Context, entityType, entityID, action, diff string) error {
	_, err := t.exec(ctx,
		`INSERT INTO change_log (id, entity_type, entity_id, action, actor, actor_kind, diff, at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		NewID(), entityType, entityID, action, t.actor.Name, t.actor.Kind, diff, t.at)
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
	return t.log(ctx, entityType, entityID, domain.ActionCreate, snapshot)
}

// logUpdate records the field-level difference. A no-op update writes nothing:
// an audit trail full of empty entries is worse than one without them.
func (t *tx) logUpdate(ctx context.Context, entityType, entityID string, before, after any) error {
	diff, changed, err := diffJSON(before, after)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	return t.log(ctx, entityType, entityID, domain.ActionUpdate, diff)
}

// write runs fn inside a transaction on the writer pool.
//
// On SQLite the writer pool holds a single connection, so concurrent callers
// queue here rather than colliding on the database lock.
func (s *SQLStore) write(ctx context.Context, actor domain.Actor, fn func(*tx) error) error {
	sqlTx, err := s.db.Writer.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	t := &tx{tx: sqlTx, db: s.db, actor: actor, at: domain.FormatTime(s.now())}

	if err := fn(t); err != nil {
		if rbErr := sqlTx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return fmt.Errorf("%w (rollback also failed: %v)", err, rbErr)
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
	case strings.Contains(msg, "unique constraint"),
		strings.Contains(msg, "duplicate key"),
		strings.Contains(msg, "unique_violation"):
		return fmt.Errorf("%s: %w", what, domain.ErrConflict)
	case strings.Contains(msg, "foreign key"),
		strings.Contains(msg, "violates foreign key constraint"):
		return fmt.Errorf("%s: references something that does not exist: %w", what, domain.ErrInvalid)
	case strings.Contains(msg, "check constraint"):
		return fmt.Errorf("%s: violates a database check constraint: %w", what, domain.ErrInvalid)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// placeholders builds "?, ?, ?" for an IN clause of n values. sqlx.In would do
// this, but it also rewrites the argument list, and several queries here mix a
// slice with scalar arguments.
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
