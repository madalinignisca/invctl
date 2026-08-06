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
	"io/fs"
	"testing"

	"github.com/pressly/goose/v3"
)

// migrateTo applies migrations only as far as a given dialect version.
//
// This exists for exactly one reason, and it is a prerequisite rather than a
// convenience: without it the absorb migration is UNTESTABLE BY CONSTRUCTION.
// A real test of it has to reach the state before the absorb, put an
// application and a service pointing at it into that schema, and only then
// migrate the rest of the way. Migrate() has no partial entry point, and
// TestMigrationsPreserveDataOnUpgrade re-runs Migrate over an already-migrated
// database -- which for the newest migration is a no-op.
//
// So the highest-risk statement in the change would have had no coverage at
// all, and would have looked covered. That is the same shape as a flag
// defaulting off: it defers the test, not just the risk.
func migrateTo(t *testing.T, ctx context.Context, db *DB, dialectVersion int64) {
	t.Helper()

	// The shared set in full: nothing in it depends on the dialect version,
	// and the absorb lives in the dialect directory.
	if err := migrateDir(ctx, db, "migrations/shared", sharedVersionTable); err != nil {
		t.Fatalf("applying shared migrations: %v", err)
	}

	sub, err := fs.Sub(migrationFS, "migrations/"+db.Driver)
	if err != nil {
		t.Fatalf("opening the dialect migration dir: %v", err)
	}
	provider, err := goose.NewProvider(gooseDialect(db.Driver), db.SQLDB(), sub,
		goose.WithTableName(dialectVersionTable),
		goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatalf("creating the goose provider: %v", err)
	}
	if _, err := provider.UpTo(ctx, dialectVersion); err != nil {
		t.Fatalf("migrating to dialect version %d: %v", dialectVersion, err)
	}
}
