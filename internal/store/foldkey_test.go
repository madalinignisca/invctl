// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAuditFoldKeyIsStableAcrossRestarts is the whole point of the fold key
// design. A session key is free to regenerate on every start -- a generated
// one only means sessions do not survive a restart. A fold key is not: a
// fresh key on every start would change every digest ever folded, and every
// entity holding a custom value would show a spurious diff on its next save,
// forever.
//
// So this test does not just call ResolveAuditFoldKey twice against one
// *DB -- that would prove nothing about persistence, only that the function
// is deterministic within a single process. It closes the database and
// opens a genuinely new connection against the SAME underlying storage, the
// way cmd/invctl does across a real restart, and asserts the second call
// reads the key back rather than generating a new one.
func TestAuditFoldKeyIsStableAcrossRestarts(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		dsn := "file:" + filepath.Join(t.TempDir(), "foldkey.db")
		testFoldKeyStableAcrossRestarts(t, DriverSQLite, dsn)
	})
	t.Run("postgres", func(t *testing.T) {
		base := os.Getenv(postgresDSNEnv)
		if base == "" {
			t.Skipf("%s not set: skipping the PostgreSQL half of this test", postgresDSNEnv)
		}
		schema := nextSchemaName(t)
		admin, err := Open(DriverPostgres, base)
		if err != nil {
			t.Fatalf("opening postgres to create the schema: %v", err)
		}
		if _, err := admin.Writer.Exec(`CREATE SCHEMA ` + schema); err != nil {
			admin.Close()
			t.Fatalf("creating schema %s: %v", schema, err)
		}
		admin.Close()
		t.Cleanup(func() {
			cleanup, err := Open(DriverPostgres, base)
			if err != nil {
				return
			}
			defer cleanup.Close()
			cleanup.Writer.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
		})
		testFoldKeyStableAcrossRestarts(t, DriverPostgres, withSearchPath(base, schema))
	})
}

func testFoldKeyStableAcrossRestarts(t *testing.T, driver, dsn string) {
	t.Helper()
	ctx := context.Background()

	// "First start": an empty, freshly migrated database.
	db1, err := Open(driver, dsn)
	if err != nil {
		t.Fatalf("opening (first start): %v", err)
	}
	if err := Migrate(ctx, db1); err != nil {
		db1.Close()
		t.Fatalf("migrating (first start): %v", err)
	}
	key1, mode1, err := New(db1).ResolveAuditFoldKey(ctx)
	if err != nil {
		db1.Close()
		t.Fatalf("resolving the fold key on first start: %v", err)
	}
	if mode1 != FoldKeyGenerated {
		t.Errorf("mode = %q on a fresh database, want %q", mode1, FoldKeyGenerated)
	}
	if len(key1) != 32 {
		t.Errorf("generated key is %d bytes, want 32", len(key1))
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("closing (first start): %v", err)
	}

	// "The restart": a brand new connection against the SAME storage --
	// not the same *DB, not the same process state, only the same bytes on
	// disk (sqlite) or the same schema (postgres).
	db2, err := Open(driver, dsn)
	if err != nil {
		t.Fatalf("opening (restart): %v", err)
	}
	defer db2.Close()
	if err := Migrate(ctx, db2); err != nil {
		t.Fatalf("migrating (restart): %v", err)
	}
	key2, mode2, err := New(db2).ResolveAuditFoldKey(ctx)
	if err != nil {
		t.Fatalf("resolving the fold key on restart: %v", err)
	}
	if mode2 != FoldKeyPersisted {
		t.Fatalf("mode = %q on a restart, want %q -- a restart must never regenerate the key",
			mode2, FoldKeyPersisted)
	}
	if !bytes.Equal(key1, key2) {
		t.Fatal("the fold key changed across a restart: every entity holding a custom value " +
			"would show a spurious diff on its next save")
	}

	// The whole point, stated as the consequence it actually protects: a
	// digest for the same value is identical across restarts.
	digest1, err := foldDigest(key1, "a distinctive value")
	if err != nil {
		t.Fatalf("folding with key1: %v", err)
	}
	digest2, err := foldDigest(key2, "a distinctive value")
	if err != nil {
		t.Fatalf("folding with key2: %v", err)
	}
	if digest1 != digest2 {
		t.Fatal("the digest for the same value differs across restarts, " +
			"even though the resolved key compared equal")
	}

	// A third "start" reads the same key back again -- this is not a
	// one-shot "generate once, then coincidentally equal twice" artefact.
	db3, err := Open(driver, dsn)
	if err != nil {
		t.Fatalf("opening (second restart): %v", err)
	}
	defer db3.Close()
	key3, mode3, err := New(db3).ResolveAuditFoldKey(ctx)
	if err != nil {
		t.Fatalf("resolving the fold key on the second restart: %v", err)
	}
	if mode3 != FoldKeyPersisted {
		t.Fatalf("mode = %q on a second restart, want %q", mode3, FoldKeyPersisted)
	}
	if !bytes.Equal(key1, key3) {
		t.Fatal("the fold key changed on a second restart")
	}
}

// TestAuditFoldKeyGenerationRacesResolveToTheSameKey covers the concurrent
// first-start case: two processes both find no row, both try to generate
// and insert. Exactly one INSERT can win the unique primary key; the loser
// must read the winner's key back rather than erroring or writing a second
// key that would silently disagree with the first.
func TestAuditFoldKeyGenerationRacesResolveToTheSameKey(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			// newStore already attaches a fold key via WithFoldKey for the
			// rest of the suite's convenience, but ResolveAuditFoldKey does
			// not read that field at all -- it goes straight to the table,
			// which this fresh database has no row in yet. That is exactly
			// the state this test wants.
			key1, mode1, err := s.ResolveAuditFoldKey(ctx)
			if err != nil {
				t.Fatalf("resolving the fold key: %v", err)
			}
			if mode1 != FoldKeyGenerated {
				t.Fatalf("mode = %q on a fresh table, want %q", mode1, FoldKeyGenerated)
			}

			key2, mode2, err := s.ResolveAuditFoldKey(ctx)
			if err != nil {
				t.Fatalf("resolving the fold key a second time: %v", err)
			}
			if mode2 != FoldKeyPersisted {
				t.Fatalf("mode = %q on the second call, want %q", mode2, FoldKeyPersisted)
			}
			if !bytes.Equal(key1, key2) {
				t.Fatal("two resolutions against the same row-holding table returned different keys")
			}
		})
	}
}

// TestAuditFoldKeyInsertConflictIsRecognisedAsALostRace reaches the branch
// neither of the two tests above does: ResolveAuditFoldKey always re-reads
// before it inserts, so a sequential "call it twice" never drives its own
// INSERT into the unique-violation arm -- the second call finds the row on
// its read and returns before touching insertFoldKey at all. The only way
// that arm is ever exercised in production is a genuine race between two
// processes' inserts, which is not reproducible deterministically.
//
// So this drives the lower-level call directly, the way a security review of
// this exact function did: insert once (it must succeed, on an empty table),
// then insert again with a DIFFERENT key for the same fixed id (it must fail,
// and isUniqueViolation must recognise the failure on both engines -- SQLite's
// driver text and PostgreSQL's SQLSTATE 23505 are worded differently). Then
// confirm the table still holds only the FIRST key: a lost race must adopt
// the winner's key, never overwrite it.
func TestAuditFoldKeyInsertConflictIsRecognisedAsALostRace(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			first := bytes.Repeat([]byte{0x11}, 32)
			if err := s.insertFoldKey(ctx, first); err != nil {
				t.Fatalf("first insert (empty table): %v", err)
			}

			second := bytes.Repeat([]byte{0x22}, 32)
			err := s.insertFoldKey(ctx, second)
			if err == nil {
				t.Fatal("second insert for the same id succeeded, want a uniqueness conflict")
			}
			if !isUniqueViolation(err) {
				t.Fatalf("isUniqueViolation(%v) = false, want true", err)
			}

			winner, ok, err := s.readFoldKey(ctx)
			if err != nil {
				t.Fatalf("reading back the persisted key: %v", err)
			}
			if !ok {
				t.Fatal("no key persisted after a winning insert")
			}
			if !bytes.Equal(winner, first) {
				t.Fatal("the losing insert changed the persisted key; " +
					"the first writer's key must be the one every later reader sees")
			}
		})
	}
}

// TestCheckAuditFoldKeyDivergence covers the three outcomes cmd/invctl's
// attachFoldKey branches on when INV_AUDIT_FOLD_KEY is set: nothing persisted
// yet (first start with the variable already set -- nothing to diverge
// from), a match (the common, unremarkable case), and a genuine mismatch
// (the operational trap this function exists to catch).
func TestCheckAuditFoldKeyDivergence(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			operatorKey := bytes.Repeat([]byte{0x33}, 32)

			hasPersisted, differs, err := s.CheckAuditFoldKeyDivergence(ctx, operatorKey)
			if err != nil {
				t.Fatalf("divergence check against an empty table: %v", err)
			}
			if hasPersisted {
				t.Error("hasPersisted = true against an empty table, want false")
			}
			if differs {
				t.Error("differs = true against an empty table, want false: nothing to diverge from")
			}

			if err := s.insertFoldKey(ctx, operatorKey); err != nil {
				t.Fatalf("persisting the operator's own key: %v", err)
			}
			hasPersisted, differs, err = s.CheckAuditFoldKeyDivergence(ctx, operatorKey)
			if err != nil {
				t.Fatalf("divergence check on a match: %v", err)
			}
			if !hasPersisted {
				t.Error("hasPersisted = false with a row present, want true")
			}
			if differs {
				t.Error("differs = true when the operator key matches the persisted key")
			}

			otherKey := bytes.Repeat([]byte{0x44}, 32)
			hasPersisted, differs, err = s.CheckAuditFoldKeyDivergence(ctx, otherKey)
			if err != nil {
				t.Fatalf("divergence check on a mismatch: %v", err)
			}
			if !hasPersisted {
				t.Error("hasPersisted = false with a row present, want true")
			}
			if !differs {
				t.Error("differs = false for a key that does not match what is persisted, want true")
			}
		})
	}
}

// TestAuditFoldKeyInsertFailureThatIsNotARaceIsReported proves the failed
// INSERT is only ever read as "lost the race" when the failure is actually a
// uniqueness conflict on the fixed id='default' row. A different failure --
// modelled here by breaking the writer pool after the first (empty) read --
// must be reported, not swallowed as if some other process had already
// written the key.
//
// SQLite only: this needs the writer and reader pools to be genuinely
// separate connections, which only holds for a file-backed database (see
// db.go's isMemoryDSN branch and openTestSQLite's comment on using a file for
// exactly this reason). PostgreSQL's Reader and Writer point at the same pool
// in this package, so closing one closes both and the read half this test
// depends on succeeding would fail too, testing nothing.
func TestAuditFoldKeyInsertFailureThatIsNotARaceIsReported(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "foldkey-insert-fail.db")
	db, err := Open(DriverSQLite, dsn)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	defer db.Close()
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	// Break the writer pool only. The reader pool stays open, so the first
	// read inside ResolveAuditFoldKey still finds no row -- the state this
	// test needs -- and the INSERT that follows fails for a reason that has
	// nothing to do with a concurrent writer.
	if err := db.Writer.Close(); err != nil {
		t.Fatalf("closing the writer pool: %v", err)
	}

	_, _, err = New(db).ResolveAuditFoldKey(context.Background())
	if err == nil {
		t.Fatal("resolving against a broken writer pool must fail, not silently succeed")
	}
	if strings.Contains(strings.ToLower(err.Error()), "unique") {
		t.Fatalf("a broken writer pool was reported as a uniqueness conflict: %v", err)
	}
}
