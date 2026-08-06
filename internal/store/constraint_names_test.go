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
	"regexp"
	"sort"
	"strings"
	"testing"
)

// anonymousEnumCheck matches a CHECK (col IN (...)) that carries no CONSTRAINT
// name in front of it.
var anonymousEnumCheck = regexp.MustCompile(`(?s)(CONSTRAINT\s+\w+\s+)?CHECK\s*\(\s*(\w+)\s+IN\s*\(`)

// TestEveryEnumConstraintIsNamed.
//
// Migration 00005 exists solely to give every enum CHECK a name, and until this
// test nothing asserted the result. Stripping every CONSTRAINT token from that
// migration -- reducing it to a pure no-op rebuild of sixteen tables -- left the
// whole suite green, measured. A deliverable no test can see is a deliverable
// that can be reverted by accident.
//
// The point of the name is that SQLite can only DROP a constraint that has one,
// so an anonymous CHECK is permanently unwidenable there. After POC sign-off
// that means a rebuild plus a recorded decision to add one value to one
// vocabulary.
//
// Reads the LIVE schema rather than the migration files, because what matters
// is the shape a fresh install actually has.
func TestEveryEnumConstraintIsNamed(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			db := e.Open(t)
			ctx := context.Background()
			if err := Migrate(ctx, db); err != nil {
				t.Fatalf("migrating: %v", err)
			}

			var anonymous []string
			switch db.Driver {
			case DriverSQLite:
				var tables []struct {
					Name string `db:"name"`
					SQL  string `db:"sql"`
				}
				if err := db.Reader.SelectContext(ctx, &tables,
					`SELECT name, sql FROM sqlite_master
					 WHERE type='table' AND sql IS NOT NULL AND name NOT LIKE 'sqlite_%'
					   AND name NOT LIKE 'goose_%' AND name NOT LIKE 'search_index%'`); err != nil {
					t.Fatalf("reading schema: %v", err)
				}
				for _, tbl := range tables {
					for _, m := range anonymousEnumCheck.FindAllStringSubmatch(tbl.SQL, -1) {
						if m[1] == "" {
							anonymous = append(anonymous, tbl.Name+"."+m[2])
						}
					}
				}
			case DriverPostgres:
				// PostgreSQL names every constraint itself, so there is nothing
				// anonymous to find. What matters there is that the name is the
				// one SQLite uses too, which TestConstraintNamesMatchAcrossEngines
				// covers.
				t.Skip("PostgreSQL auto-names constraints; see the cross-engine test")
			}

			if len(anonymous) > 0 {
				sort.Strings(anonymous)
				t.Errorf("%d enum CHECK constraint(s) have no name, so they can never be widened "+
					"on SQLite without rebuilding the table:\n  %s",
					len(anonymous), strings.Join(anonymous, "\n  "))
			}
		})
	}
}

// TestConstraintNamesMatchAcrossEngines.
//
// A constraint named differently on each engine is named but still frozen for a
// dual-engine migration: widening it cannot be written as one ALTER pair, which
// is the entire benefit the naming was for. Two did disagree -- both on
// service_instance, both scars from earlier migrations -- and nothing noticed.
func TestConstraintNamesMatchAcrossEngines(t *testing.T) {
	engines := Engines(t)
	if len(engines) < 2 {
		t.Skip("needs both engines; set INV_TEST_POSTGRES_DSN")
	}

	names := map[string]map[string]bool{}
	for _, e := range engines {
		db := e.Open(t)
		ctx := context.Background()
		if err := Migrate(ctx, db); err != nil {
			t.Fatalf("migrating %s: %v", e.Name, err)
		}
		found := map[string]bool{}
		switch db.Driver {
		case DriverSQLite:
			var sqls []string
			if err := db.Reader.SelectContext(ctx, &sqls,
				`SELECT sql FROM sqlite_master WHERE type='table' AND sql IS NOT NULL`); err != nil {
				t.Fatalf("reading schema: %v", err)
			}
			re := regexp.MustCompile(`CONSTRAINT\s+(\w+)\s+CHECK`)
			for _, s := range sqls {
				for _, m := range re.FindAllStringSubmatch(s, -1) {
					found[m[1]] = true
				}
			}
		case DriverPostgres:
			var conNames []string
			if err := db.Reader.SelectContext(ctx, &conNames,
				`SELECT conname FROM pg_constraint c
				 JOIN pg_class t ON t.oid = c.conrelid
				 JOIN pg_namespace n ON n.oid = t.relnamespace
				 WHERE c.contype = 'c' AND n.nspname = current_schema()`); err != nil {
				t.Fatalf("reading constraints: %v", err)
			}
			for _, n := range conNames {
				// PostgreSQL adds a NOT NULL check per column on some versions;
				// only the named enum ones are comparable.
				if strings.HasSuffix(n, "_check") {
					found[n] = true
				}
			}
		}
		names[e.Name] = found
	}

	only := func(a, b string) []string {
		var out []string
		for n := range names[a] {
			if !names[b][n] {
				out = append(out, n)
			}
		}
		sort.Strings(out)
		return out
	}
	if extra := only("sqlite", "postgres"); len(extra) > 0 {
		t.Errorf("named on SQLite and not on PostgreSQL, so widening needs two different "+
			"statements:\n  %s", strings.Join(extra, "\n  "))
	}
	if extra := only("postgres", "sqlite"); len(extra) > 0 {
		t.Errorf("named on PostgreSQL and not on SQLite:\n  %s", strings.Join(extra, "\n  "))
	}
}
