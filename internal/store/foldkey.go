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
