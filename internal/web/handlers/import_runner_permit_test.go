// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// WP-G1 Task 9: this is the ONE test in the package that actually drives
// importRunner.run, rather than calling store.ImportAssetsBatched directly
// the way internal/web/import_test.go's Task 9 tests do. Those prove the
// STORE method's contract; this proves the RUNNER wires importWork.permit
// into that call rather than reaching for something wider on its own --
// which is exactly the seam a mutation at the runner's call site (rather
// than in the store method) would otherwise slip past untested.
//
// Mutation: change run()'s ImportAssetsBatched call to
// domain.AdministratorPermit(w.actor) instead of w.permit -- this must fail,
// because the scoped permit below authorizes nothing and every row would
// then succeed regardless.
func newRunnerTestStore(t *testing.T) *store.SQLStore {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(store.DriverSQLite, "file:"+filepath.Join(dir, "runner.db"))
	if err != nil {
		t.Fatalf("opening test store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	return store.New(db)
}

func TestTheImportRunnerUsesTheCapturedPermitNotSomethingWiderMintedFromTheActor(t *testing.T) {
	s := newRunnerTestStore(t)
	r := newImportRunner(s)

	submitter := domain.Actor{ID: "runner-submitter", Name: "runner-submitter", Kind: domain.ActorKindUser}
	// Authorizes nothing: entities is nil, and ScopedPermit cannot yet cover
	// a not-yet-existing row's id regardless (see internal/domain/role.go's
	// ScopedPermit doc comment). Every row submitted under it must be
	// refused, never created.
	permit := domain.ScopedPermit(submitter, []string{"P"}, nil)

	ctx := context.Background()
	job := store.ImportJob{
		ID: store.NewID(), Kind: store.ImportKindAssets, Filename: "runner-scope-test.csv",
		Actor: submitter.ID, ActorKind: submitter.Kind, Status: store.ImportQueued,
		RowsTotal: 1, CreatedAt: domain.FormatTime(s.Now()),
	}
	if err := s.CreateImportJob(ctx, &job); err != nil {
		t.Fatalf("CreateImportJob: %v", err)
	}

	r.submit(importWork{
		job:    job,
		assets: []store.AssetImportRow{{Line: 2, Name: "runner-scope-asset", Kind: "site"}},
		actor:  submitter,
		permit: permit,
	})

	deadline := time.Now().Add(10 * time.Second)
	var final *store.ImportJob
	for time.Now().Before(deadline) {
		j, err := s.GetImportJob(ctx, job.ID)
		if err != nil {
			t.Fatalf("GetImportJob: %v", err)
		}
		if j.Status != store.ImportQueued && j.Status != store.ImportRunning {
			final = j
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if final == nil {
		t.Fatal("import job did not finish within 10s")
	}

	var n int
	reader := s.DB().Reader
	if err := reader.Get(&n, reader.Rebind(
		`SELECT COUNT(*) FROM asset WHERE name = 'runner-scope-asset'`)); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if n != 0 {
		t.Errorf("the runner created %d asset(s) under a permit that authorizes nothing -- "+
			"it must be using importWork.permit, not a wider permit minted from the actor",
			n)
	}
}
