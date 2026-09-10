// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"fmt"
	"os"
	osexec "os/exec"
	"testing"
)

// TestTheTemplateSweepSparesALiveSiblingsTemplate guards the one dangerous
// line in sweepDeadTemplates.
//
// `go test ./...` runs every package as its own process, so at any moment
// several invctl_tmpl_<pid> databases can be in use at once. A sweep that
// dropped every template it found would delete a sibling package's database
// out from under it, mid-run -- and it would present as a random Postgres
// failure in a package that had changed nothing, which is close to the worst
// failure signature a test harness can produce.
//
// The liveness check is what prevents that. This test names a template after a
// process that is definitely alive (this one) and asserts the sweep leaves it
// where it is.
func TestTheTemplateSweepSparesALiveSiblingsTemplate(t *testing.T) {
	dsn := os.Getenv(postgresDSNEnv)
	if dsn == "" {
		t.Skip("INV_TEST_POSTGRES_DSN not set: this guard is Postgres-only")
	}
	admin, err := Open(DriverPostgres, dsn)
	if err != nil {
		t.Fatalf("opening postgres: %v", err)
	}
	defer admin.Close()

	// A REAL CHILD PROCESS, not this one.
	//
	// The first version of this test used os.Getpid(), which is precisely the
	// name pgTemplateDB gives the live template -- so it dropped the suite's
	// own template and replaced it with an empty database, and every test that
	// cloned afterwards failed with `relation "app_user" does not exist`. It
	// passed in isolation, which is how it got written. A guard that damages
	// the thing it guards is worse than no guard.
	//
	// A child we spawned is unambiguously alive, is not the template's name,
	// and is ours -- so signal 0 returns nil rather than EPERM.
	child := osexec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatalf("starting a stand-in process: %v", err)
	}
	defer func() {
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
	}()
	live := fmt.Sprintf("invctl_tmpl_%d", child.Process.Pid)
	if _, err := admin.Writer.Exec(`DROP DATABASE IF EXISTS ` + live + ` WITH (FORCE)`); err != nil {
		t.Fatalf("clearing before: %v", err)
	}
	if _, err := admin.Writer.Exec(`CREATE DATABASE ` + live); err != nil {
		t.Fatalf("creating the stand-in template: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Writer.Exec(`DROP DATABASE IF EXISTS ` + live + ` WITH (FORCE)`)
	})

	// A name the sweep must NOT treat as its own, so `live` is judged purely
	// on whether its pid is running.
	sweepDeadTemplates(admin, "invctl_tmpl_0")

	var n int
	if err := admin.Reader.Get(&n,
		admin.Reader.Rebind(`SELECT COUNT(*) FROM pg_database WHERE datname = ?`), live); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if n != 1 {
		t.Error("the sweep dropped a template whose owning process is still running. " +
			"Under `go test ./...` that is a sibling package's database going away " +
			"mid-run, surfacing as a random Postgres failure in a package this " +
			"change never touched.")
	}
}
