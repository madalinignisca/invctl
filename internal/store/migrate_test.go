package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/gabriel/invctl/internal/domain"
)

// TestMigrate proves the schema applies cleanly on every available engine and
// that re-running is a no-op.
func TestMigrate(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			db := migrated(t, e)

			if err := Migrate(context.Background(), db); err != nil {
				t.Fatalf("migrating twice: %v", err)
			}

			tables := []string{
				"environment", "asset", "asset_closure", "asset_environment",
				"interface", "link", "prefix", "ip_address",
				"application", "identity", "service", "service_instance",
				"rt_systemd", "rt_windows", "rt_container", "rt_k8s",
				"endpoint", "backend_pool", "backend_member", "route",
				"dependency", "dependency_data_class",
				"change_log", "app_user", "sessions", "search_index",
				"net_group", "net_group_member", "net_uplink",
				"net_attachment", "net_attachment_member", "net_anchor",
			}
			for _, table := range tables {
				var n int
				if err := db.Reader.Get(&n, "SELECT COUNT(*) FROM "+table); err != nil {
					t.Errorf("selecting from %s: %v", table, err)
				}
			}
		})
	}
}

// TestByteRangeContainment proves the four-column IP pattern on every engine:
// a BYTEA-declared column round-trips raw bytes, and bytewise range comparison
// finds the most specific containing prefix. The whole IPAM side rests on
// this, and it is exactly the kind of assumption that holds on one engine and
// not the other.
func TestByteRangeContainment(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			assertByteRangeContainment(t, migrated(t, e))
		})
	}
}

// TestSQLiteForeignKeysEnforced is the canary for the pragma that is off by
// default. Without foreign_keys=ON every REFERENCES clause silently does
// nothing and no other test would notice.
func TestSQLiteForeignKeysEnforced(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "fk.db")
	db, err := Open(DriverSQLite, dsn)
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	_, err = db.Writer.Exec(
		`INSERT INTO asset_environment (asset_id, environment_id) VALUES (?, ?)`,
		"no-such-asset", "no-such-env")
	if err == nil {
		t.Fatal("inserted a row with dangling foreign keys; the foreign_keys pragma is not in effect")
	}

	var journal string
	if err := db.Writer.Get(&journal, "PRAGMA journal_mode"); err != nil {
		t.Fatalf("reading journal_mode: %v", err)
	}
	if journal != "wal" {
		t.Errorf("journal_mode = %q, want wal", journal)
	}
}

// TestInMemoryDatabaseIsUsable. An in-memory SQLite database lives inside its
// connection, so the two-pool layout gave the reader a different, empty
// database: migrations applied to the writer, startup logged success, and the
// first read failed with "no such table". A silent split like that is worse
// than a refusal to start.
func TestInMemoryDatabaseIsUsable(t *testing.T) {
	for _, dsn := range []string{":memory:", "file::memory:", "file:test?mode=memory"} {
		t.Run(dsn, func(t *testing.T) {
			db, err := Open(DriverSQLite, dsn)
			if err != nil {
				t.Fatalf("opening %q: %v", dsn, err)
			}
			t.Cleanup(func() { db.Close() })

			if err := Migrate(context.Background(), db); err != nil {
				t.Fatalf("migrating: %v", err)
			}

			// The reader must see what the writer migrated.
			var n int
			if err := db.Reader.Get(&n, "SELECT COUNT(*) FROM environment"); err != nil {
				t.Fatalf("reading through the reader pool: %v", err)
			}

			// And a write must be visible to a subsequent read.
			s := New(db)
			ctx := context.Background()
			env, err := domain.NewEnvironment(NewID(), "prod", "Production",
				domain.EnvRoleProduction, true, 1, s.Now())
			if err != nil {
				t.Fatalf("building environment: %v", err)
			}
			if err := s.CreateEnvironment(ctx, domain.SystemActor, env); err != nil {
				t.Fatalf("creating environment: %v", err)
			}
			if _, err := s.GetEnvironmentByCode(ctx, "prod"); err != nil {
				t.Fatalf("reading back a written row: %v", err)
			}
		})
	}
}
