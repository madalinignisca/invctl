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
	"crypto/hmac"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Fold key resolution modes, returned by ResolveAuditFoldKey so cmd/invctl
// can log which one is in force without ever logging the key itself.
const (
	// FoldKeyGenerated means no key existed yet: 32 random bytes were
	// generated and persisted just now, on THIS start.
	FoldKeyGenerated = "generated"
	// FoldKeyPersisted means a key generated on an earlier start was read
	// back unchanged. This is the common case after the first start of a
	// deployment that has never set INV_AUDIT_FOLD_KEY.
	FoldKeyPersisted = "persisted"
)

const auditFoldKeyRowID = "default"

const insertAuditFoldKey = `INSERT INTO audit_fold_key (id, key_b64, created_at) VALUES (?, ?, ?)`

// ResolveAuditFoldKey resolves the persisted fallback for INV_AUDIT_FOLD_KEY:
// the key used to fold a custom field's value into the audit trail as a
// digest (foldDigest in customvalues.go) when the operator has not set the
// environment variable.
//
// CALL THIS ONLY WHEN INV_AUDIT_FOLD_KEY IS UNSET. When it is set,
// config.Load already validated and decoded it -- same base64, same
// minimum-32-bytes rule as INV_SESSION_KEY -- and cmd/invctl uses that value
// directly; there is no reason to touch this table at all, and every start
// with the variable set leaves it untouched.
//
// THE KEY MUST NEVER CHANGE UNDER A RUNNING DEPLOYMENT'S DATA. Unlike
// config.sessionKey, which is free to generate a fresh key on every start
// because a generated session key only means sessions do not survive a
// restart, a fold key regenerated at startup changes every digest that was
// ever folded, and every entity holding a custom value would show a
// spurious diff on its very next save -- forever, because change_log is
// append-only. So a key is generated exactly ONCE, the first time this ever
// runs against a given database, and every later call reads that same key
// back rather than generating a new one. TestAuditFoldKeyIsStableAcrossRestarts
// is the test that proves this holds across a real close-and-reopen of the
// same database.
//
// A race between two processes starting against an empty table at once is
// resolved by re-reading after a failed insert: whichever process's INSERT
// commits first wins, and the loser adopts the winner's key rather than
// erroring -- both processes are trying to reach the same outcome, "a key
// exists", and only one of them needs to be the one that created it.
//
// ONLY A UNIQUENESS CONFLICT ON THE FIXED id='default' ROW IS TREATED AS A
// LOST RACE. A disk error or a permissions problem raised by the same INSERT
// must not be swallowed the same way -- re-reading after THAT kind of failure
// would either find nothing (and report the original failure honestly, which
// this still does) or, worse on a partially-broken connection, appear to
// succeed and mask a real fault. isUniqueViolation (store.go) is the same
// text match translateWriteErr already uses for every other constraint kind
// in this package, so both engines' different wording for a conflict --
// SQLite's driver text, PostgreSQL's SQLSTATE 23505 -- are recognised the
// same way here as everywhere else this package inspects a write error.
func (s *SQLStore) ResolveAuditFoldKey(ctx context.Context) ([]byte, string, error) {
	if existing, ok, err := s.readFoldKey(ctx); err != nil {
		return nil, "", err
	} else if ok {
		return existing, FoldKeyPersisted, nil
	}

	generated := make([]byte, 32)
	if _, err := rand.Read(generated); err != nil {
		return nil, "", fmt.Errorf("generating an audit fold key: %w", err)
	}

	if err := s.insertFoldKey(ctx, generated); err != nil {
		if !isUniqueViolation(err) {
			return nil, "", fmt.Errorf("persisting a generated audit fold key: %w", err)
		}
		// Lost the race: some other process's INSERT landed first. Read back
		// what it wrote rather than treating this as a failure -- the goal
		// was "a key exists", and one now does.
		if winner, ok, readErr := s.readFoldKey(ctx); readErr == nil && ok {
			return winner, FoldKeyPersisted, nil
		}
		return nil, "", fmt.Errorf("persisting a generated audit fold key: %w", err)
	}
	return generated, FoldKeyGenerated, nil
}

// insertFoldKey writes a freshly generated key. Split out from
// ResolveAuditFoldKey so this is the only place in the function that touches
// the writer pool -- generated never passed through anything but
// crypto/rand.Read and a base64 encoding, and isolating the INSERT keeps that
// obvious rather than requiring a reader to trace it back through the read
// path above.
func (s *SQLStore) insertFoldKey(ctx context.Context, generated []byte) error {
	encoded := base64.StdEncoding.EncodeToString(generated)
	// gosec's G701 flags this line, and only this one in the whole
	// codebase, over a whole-program taint path rather than anything local:
	// its call-graph analysis traces INV_AUDIT_FOLD_KEY (an operator-set
	// environment value, hence "external") through SQLStore.foldKey into
	// foldDigest and from there into a *different* parameterized INSERT
	// (change_log.diff, store.go's tx.log) and attributes the finding to a
	// sink elsewhere in the same package rather than to that actual one --
	// confirmed by removing this Exec call in isolation, which makes the
	// finding vanish rather than move. Every value on this line and the one
	// gosec is really describing reaches the database exclusively through a
	// `?` placeholder bind argument, never string-built into the query text
	// (CLAUDE.md's placeholder rule, enforced everywhere in this package) --
	// which is the actual defence against SQL injection, and taint alone
	// does not defeat it. There is no G701 exclusion in .golangci.yml
	// because the false positive is this one call site, not the rule in
	// general, and a package-wide exclusion would be broader than the
	// problem it is solving.
	//nolint:gosec // G701: parameterized query, see comment above
	_, err := s.db.Writer.ExecContext(ctx, s.db.Rebind(insertAuditFoldKey),
		auditFoldKeyRowID, encoded, domain.FormatTime(s.Now()))
	return err
}

// CheckAuditFoldKeyDivergence compares an operator-supplied
// INV_AUDIT_FOLD_KEY against the key already persisted in audit_fold_key, if
// a row exists yet. It exists so cmd/invctl can warn -- loudly, but without
// refusing to start -- when the two disagree, rather than silently folding
// every custom value under a key that does not match what earlier writes
// used. A divergence is not always a mistake: a deliberate rotation is a
// legitimate operator decision. But it is never SAFE to be silent about,
// because it changes every digest ever folded and puts a spurious diff on
// every entity holding a custom value on its next save, permanently, since
// change_log is append-only and nothing rewrites a stored diff.
//
// Neither key is ever returned, logged, or otherwise exposed by this
// function or its caller -- only whether they match.
//
// hasPersisted is false when no row exists yet (the first start of a
// database that already has INV_AUDIT_FOLD_KEY set): there is nothing to
// diverge from, since this start's key is exactly what would be persisted if
// the operator ever unset the variable and fell back to the database. The
// caller should not warn in that case.
func (s *SQLStore) CheckAuditFoldKeyDivergence(ctx context.Context, operatorKey []byte) (hasPersisted, differs bool, err error) {
	persisted, ok, err := s.readFoldKey(ctx)
	if err != nil {
		return false, false, err
	}
	if !ok {
		return false, false, nil
	}
	return true, !hmac.Equal(persisted, operatorKey), nil
}

// readFoldKey reads the persisted key, if a row exists yet.
func (s *SQLStore) readFoldKey(ctx context.Context) (key []byte, ok bool, err error) {
	var encoded string
	err = s.db.Reader.GetContext(ctx, &encoded,
		s.db.Rebind(`SELECT key_b64 FROM audit_fold_key WHERE id = ?`), auditFoldKeyRowID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("reading the persisted audit fold key: %w", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, false, fmt.Errorf("reading the persisted audit fold key: "+
			"the stored value is not valid base64: %w", err)
	}
	return decoded, true, nil
}
