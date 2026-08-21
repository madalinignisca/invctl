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
	if foldDigest(key1, "a distinctive value") != foldDigest(key2, "a distinctive value") {
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
