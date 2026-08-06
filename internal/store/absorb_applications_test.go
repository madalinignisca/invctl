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
	"testing"
)

// The absorb migration (00010) is the only migration in this repository that
// MOVES data rather than only reshaping it, and the only one that drops a table
// holding rows. Everything below is about the data, not the DDL: `make migrate`
// already proves the statements parse and apply.
//
// Each assertion carries its control. Proving a project appeared is worth
// little without also proving the application is gone -- a migration that
// copies and forgets to drop passes the first half and leaves two sources of
// truth behind.
const dialectVersionBeforeAbsorb = 9

func TestAbsorbApplicationsMovesEveryApplicationToAProject(t *testing.T) {
	ctx := context.Background()

	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			db := e.OpenRaw(t)
			migrateTo(t, ctx, db, dialectVersionBeforeAbsorb)

			// The world as it was: two applications, three services, one of
			// which belongs to nobody. The unowned service is the control --
			// after the absorb it must still exist and still own nothing,
			// because a migration that helpfully invents an owner for it would
			// be fabricating a declared fact.
			exec(t, db, `INSERT INTO environment (id, code, name, role, created_at, updated_at)
			             VALUES ('env1', 'prod', 'Production', 'production', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
			exec(t, db, `INSERT INTO application (id, code, name, owner_team, created_at, updated_at)
			             VALUES ('app1', 'orders', 'Orders Platform', 'commerce', '2026-01-02T00:00:00Z', '2026-01-03T00:00:00Z')`)
			exec(t, db, `INSERT INTO application (id, code, name, owner_team, created_at, updated_at)
			             VALUES ('app2', 'billing', 'Billing', NULL, '2026-01-02T00:00:00Z', '2026-01-03T00:00:00Z')`)
			for _, svc := range []struct{ id, app, code string }{
				{"svc1", "'app1'", "orders-api"},
				{"svc2", "'app1'", "orders-web"},
				{"svc3", "NULL", "loose-end"},
			} {
				exec(t, db, `INSERT INTO service
					(id, application_id, code, name, kind, environment_id, availability, tier,
					 lifecycle, attrs, created_at, updated_at)
					VALUES ('`+svc.id+`', `+svc.app+`, '`+svc.code+`', '`+svc.code+`', 'api', 'env1',
					        'standalone', 3, 'active', '{}', '2026-01-04T00:00:00Z', '2026-01-04T00:00:00Z')`)
			}
			exec(t, db, `INSERT INTO search_index (entity_type, entity_id, title, subtitle, body)
			             VALUES ('application', 'app1', 'Orders Platform', 'orders', '')`)

			if err := Migrate(ctx, db); err != nil {
				t.Fatalf("completing the migration: %v", err)
			}

			// Identity is preserved, not regenerated. change_log rows written
			// before the absorb still point at 'app1', and they resolve only
			// if the project kept the id.
			var got struct {
				Code      string `db:"code"`
				Name      string `db:"name"`
				Lifecycle string `db:"lifecycle"`
				CreatedAt string `db:"created_at"`
			}
			if err := db.Reader.Get(&got,
				`SELECT code, name, lifecycle, created_at FROM project WHERE id = 'app1'`); err != nil {
				t.Fatalf("the application did not become a project: %v", err)
			}
			if got.Code != "orders" || got.Name != "Orders Platform" || got.Lifecycle != "active" {
				t.Errorf("project app1 = %+v, want code=orders name=\"Orders Platform\" lifecycle=active", got)
			}
			// The application's own timestamps travel with it. Stamping "now"
			// would say the project was created during a deployment.
			if got.CreatedAt != "2026-01-02T00:00:00Z" {
				t.Errorf("created_at = %q, want the application's own 2026-01-02T00:00:00Z", got.CreatedAt)
			}

			// An application with no services still becomes a project. It is
			// the one an absorb driven from the service table would lose.
			if n := count(t, db, `SELECT COUNT(*) FROM project`); n != 2 {
				t.Errorf("projects = %d, want 2 (billing has no services and must survive)", n)
			}

			// Belonging became an `owns` link, for exactly the services that
			// had one.
			owned := map[string]string{}
			rows, err := db.Reader.Query(
				`SELECT service_id, project_id FROM project_service WHERE relation = 'owns' AND lifecycle = 'active'`)
			if err != nil {
				t.Fatalf("reading the links: %v", err)
			}
			defer rows.Close()
			for rows.Next() {
				var svc, project string
				if err := rows.Scan(&svc, &project); err != nil {
					t.Fatalf("scanning a link: %v", err)
				}
				owned[svc] = project
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("iterating the links: %v", err)
			}
			want := map[string]string{"svc1": "app1", "svc2": "app1"}
			if len(owned) != len(want) {
				t.Fatalf("owns links = %v, want exactly %v", owned, want)
			}
			for svc, project := range want {
				if owned[svc] != project {
					t.Errorf("owner of %s = %q, want %q", svc, owned[svc], project)
				}
			}
			// The control: a service that belonged to nothing still belongs to
			// nothing, and still exists.
			if n := count(t, db, `SELECT COUNT(*) FROM service WHERE id = 'svc3'`); n != 1 {
				t.Errorf("the unowned service did not survive the absorb")
			}

			// Search follows the rename. A stale 'application' document would
			// return a hit that leads to a 404.
			if n := count(t, db, `SELECT COUNT(*) FROM search_index WHERE entity_type = 'application'`); n != 0 {
				t.Errorf("search_index still holds %d application documents", n)
			}
			if n := count(t, db, `SELECT COUNT(*) FROM search_index WHERE entity_type = 'project'`); n != 2 {
				t.Errorf("search_index project documents = %d, want 2", n)
			}

			// And the old shape is gone on both engines -- the half that only
			// SQLite made difficult.
			if tableExists(t, db, "application") {
				t.Errorf("the application table survived the absorb")
			}
			if columnExists(t, db, "service", "application_id") {
				t.Errorf("service.application_id survived the absorb")
			}
		})
	}
}

// tableExists and columnExists ask the engine's own catalogue rather than
// probing with a SELECT, and the difference is not stylistic. Each PostgreSQL
// test runs in its own schema with search_path = <schema>,public; once the
// migration correctly drops <schema>.application, a bare `SELECT 1 FROM
// application` resolves through to a stale public.application left by some
// earlier run and reports the table as present. Name resolution cannot express
// "absent HERE", so the question has to be asked of the current schema
// explicitly -- which is why these two are the only engine-specific statements
// in the store tests.
func tableExists(t *testing.T, db *DB, table string) bool {
	t.Helper()
	switch db.Driver {
	case DriverSQLite:
		return count(t, db,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = '`+table+`'`) > 0
	case DriverPostgres:
		return count(t, db,
			`SELECT COUNT(*) FROM information_schema.tables
			 WHERE table_schema = current_schema() AND table_name = '`+table+`'`) > 0
	}
	t.Fatalf("unknown driver %q", db.Driver)
	return false
}

func columnExists(t *testing.T, db *DB, table, column string) bool {
	t.Helper()
	switch db.Driver {
	case DriverSQLite:
		return count(t, db,
			`SELECT COUNT(*) FROM pragma_table_info('`+table+`') WHERE name = '`+column+`'`) > 0
	case DriverPostgres:
		return count(t, db,
			`SELECT COUNT(*) FROM information_schema.columns
			 WHERE table_schema = current_schema() AND table_name = '`+table+`'
			   AND column_name = '`+column+`'`) > 0
	}
	t.Fatalf("unknown driver %q", db.Driver)
	return false
}

func exec(t *testing.T, db *DB, query string) {
	t.Helper()
	if _, err := db.Writer.Exec(query); err != nil {
		t.Fatalf("seeding the pre-absorb state: %v\n%s", err, query)
	}
}

func count(t *testing.T, db *DB, query string) int {
	t.Helper()
	var n int
	if err := db.Reader.Get(&n, query); err != nil {
		t.Fatalf("counting: %v\n%s", err, query)
	}
	return n
}
