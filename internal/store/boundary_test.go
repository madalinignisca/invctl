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
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gabriel/invctl/internal/domain"
)

// The boundary tests docs/AUDIT.md requires, on both engines.
//
// These are written from the rules, not from the code. That distinction has
// already cost this project once: at M5 a scenario test recorded the buggy
// Cause attribution as its expectation, and the bug outlived the test that was
// supposed to catch it. So every assertion below quotes the rule it enforces,
// and where the tree disagrees with a rule the test says so rather than
// recording what the tree happens to do.
//
// "The boundary is tested or it does not exist" -- AUDIT.md, boundary tests.

// ---------------------------------------------------------------------------
// Live schema introspection
// ---------------------------------------------------------------------------

// liveTables lists the tables that actually exist, per engine.
//
// This is one of the few places a dialect split is right: the question is about
// the catalogue, and the catalogue is not portable. Everything the answer is
// then used for is.
func liveTables(t *testing.T, db *DB) []string {
	t.Helper()
	var query string
	switch db.Driver {
	case DriverSQLite:
		query = `SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`
	case DriverPostgres:
		query = `SELECT tablename FROM pg_tables WHERE schemaname = current_schema() ORDER BY tablename`
	default:
		t.Fatalf("no table listing for driver %q", db.Driver)
	}
	var names []string
	if err := db.Reader.Select(&names, query); err != nil {
		t.Fatalf("listing tables: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("the live schema has no tables; every assertion below would be vacuous")
	}
	return names
}

// liveColumns lists one table's columns. `SELECT * ... LIMIT 0` rather than an
// information_schema query, because that form runs unmodified on both engines
// and reports exactly what a `SELECT *` in production would bind.
func liveColumns(t *testing.T, db *DB, table string) []string {
	t.Helper()
	rows, err := db.Reader.Query(`SELECT * FROM ` + table + ` LIMIT 0`)
	if err != nil {
		t.Fatalf("reading the shape of %s: %v", table, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("listing columns of %s: %v", table, err)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("listing columns of %s: %v", table, err)
	}
	return cols
}

// ---------------------------------------------------------------------------
// TestEveryColumnIsClassified
// ---------------------------------------------------------------------------

// TestEveryColumnIsClassified catches a new column defaulting into the
// unclassified gap.
//
// docs/AUDIT.md: "A migration that adds a column classifies it in this table
// AND in domain. TestEveryColumnIsClassified reads the live schema on both
// engines and fails the build on an unclassified column -- so a new column
// cannot default into the gap."
//
// It runs both directions. A live column with no class is the gap the rule
// names. A classified column that no longer exists is the same failure read
// backwards: a census describing a schema that has moved on is one nobody can
// rely on, and it is how the first kind of gap goes unnoticed.
func TestEveryColumnIsClassified(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			db := migrated(t, e)

			// A column claimed by two classes makes ClassifyColumn return
			// whichever map it happens to check first, and the census then looks
			// complete while being wrong.
			if conflicts := domain.ClassificationConflicts(); len(conflicts) > 0 {
				t.Errorf("the classification contradicts itself:\n\t%s", strings.Join(conflicts, "\n\t"))
			}

			live := map[string][]string{}
			for _, table := range liveTables(t, db) {
				if _, exempt := domain.IsExemptTable(table); exempt {
					continue
				}
				live[table] = liveColumns(t, db, table)
			}
			if len(live) == 0 {
				t.Fatal("every live table is exempt; this test is asserting nothing")
			}

			for table, cols := range live {
				for _, col := range cols {
					class, ok := domain.ClassifyColumn(table, col)
					if !ok {
						t.Errorf("%s.%s has no class. Adding a column means three edits: the "+
							"migration, the table in docs/AUDIT.md, and internal/domain. "+
							"Naming is a hint, not the rule -- desired_state is declared intent "+
							"and verified_at is a person's attestation, so consult the table "+
							"rather than guessing.", table, col)
						continue
					}
					switch class {
					case domain.ClassDeclared, domain.ClassObserved, domain.ClassProvenance:
					default:
						t.Errorf("%s.%s is classified %q, which is not one of the three kinds of fact",
							table, col, class)
					}
				}
			}

			// The census must not describe columns the schema no longer has.
			for _, table := range domain.ClassifiedTables() {
				cols, present := live[table]
				if !present {
					if _, exempt := domain.IsExemptTable(table); exempt {
						continue
					}
					t.Errorf("the classification lists table %q, which is not in the live %s schema",
						table, e.Name)
					continue
				}
				have := map[string]bool{}
				for _, col := range cols {
					have[col] = true
				}
				for _, class := range []map[string][]string{
					domain.DeclaredColumns, domain.ObservedColumns, domain.ProvenanceColumns,
				} {
					for _, col := range class[table] {
						if !have[col] {
							t.Errorf("the classification lists %s.%s, which does not exist in the "+
								"live %s schema. A census that has drifted from the schema is how "+
								"an unclassified column goes unnoticed.", table, col, e.Name)
						}
					}
				}
			}

			// The normative table in docs/AUDIT.md, row by row. These are the
			// rows the document says are most often got wrong, and a census that
			// is internally consistent but disagrees with the document is a
			// census of the wrong thing.
			t.Run("it agrees with the normative table in AUDIT.md", func(t *testing.T) {
				cases := []struct {
					table, column string
					want          domain.ColumnClass
					why           string
				}{
					{"service_instance", "desired_state", domain.ClassDeclared,
						"intent: \"observed stopped, therefore desired stopped\" is the intent-collapse that makes drift undetectable"},
					{"service_instance", "source", domain.ClassProvenance, "rule 7"},
					{"dependency", "source", domain.ClassProvenance, "decides whether the edge is authoritative"},
					{"dependency", "verified_by", domain.ClassDeclared,
						"a person's attestation; a machine credential may never write it"},
					{"dependency", "verified_at", domain.ClassDeclared,
						"a timestamp, and still declared -- naming does not decide the class"},
					{"dependency", "first_seen", domain.ClassObserved, "written at reconciler frequency"},
					{"dependency", "last_seen", domain.ClassObserved, "written at reconciler frequency"},
					{"dependency", "confidence", domain.ClassObserved, "AUDIT.md's normative table"},
					{"dependency", "lifecycle", domain.ClassDeclared, ""},
					{"dependency", "nature", domain.ClassDeclared, ""},
					{"dependency", "identity_id", domain.ClassDeclared, ""},
					{"dependency", "firewall_rule_ref", domain.ClassDeclared, ""},
					{"app_user", "last_login_at", domain.ClassObserved,
						"rule 11: a login is telemetry about a person, not a configuration change"},
					{"asset_health", "state", domain.ClassObserved, "asset_health.* is observed"},
					{"asset_health", "state_since", domain.ClassObserved, "asset_health.* is observed"},
					{"observed_transition", "to_state", domain.ClassObserved, "observed_transition.* is observed"},
					{"asset", "lifecycle", domain.ClassDeclared, "everything else is declared"},
					{"environment", "in_scope", domain.ClassDeclared, "everything else is declared"},
					{"change_log", "actor", domain.ClassDeclared, "everything else is declared"},
					{"health_override", "asserted_state", domain.ClassDeclared,
						"rule 14: an operator overruling a monitor is a declared act"},
				}
				for _, tc := range cases {
					got, ok := domain.ClassifyColumn(tc.table, tc.column)
					if !ok {
						t.Errorf("%s.%s is unclassified; AUDIT.md classifies it %s", tc.table, tc.column, tc.want)
						continue
					}
					if got != tc.want {
						msg := fmt.Sprintf("%s.%s is classified %s, AUDIT.md says %s", tc.table, tc.column, got, tc.want)
						if tc.why != "" {
							msg += " -- " + tc.why
						}
						t.Error(msg)
					}
				}
			})

			// Rule 1 moved these out of service_instance. If they came back, the
			// classification above would happily classify them and the mixed row
			// would be legal again.
			t.Run("service_instance carries no observed column", func(t *testing.T) {
				for _, col := range live["service_instance"] {
					if col == "observed_state" || col == "observed_at" {
						t.Errorf("service_instance.%s exists again. Rule 1 moved it to asset_health "+
							"because a mixed row produces a mixed audit entry that no portable "+
							"query can classify.", col)
					}
					if class, _ := domain.ClassifyColumn("service_instance", col); class == domain.ClassObserved {
						t.Errorf("service_instance.%s is classified observed. Rule 1's separation is "+
							"by table, not by column naming.", col)
					}
				}
			})
		})
	}
}

// ---------------------------------------------------------------------------
// TestObservedWriteLeavesDeclaredColumnsUntouched
// ---------------------------------------------------------------------------

// TestObservedWriteLeavesDeclaredColumnsUntouched snapshots the declared
// columns, observes, re-reads and asserts byte-equality including updated_at.
//
// Rule 2 states the allowlist: "an observed write may write only the observed
// columns above". The strongest reading of that is the one taken here -- not
// "the two rows this test happened to think of", but every table in the
// classification that is not an observed one, digested wholesale before and
// after. A future observed writer that reached sideways into `dependency` or
// `health_override` would be caught by this without anybody editing the test.
//
// updated_at is called out by name because it means "when a person last changed
// this row" (rule 3). A heartbeat that bumped it would rewrite the estate's
// change history into a lie that reads like activity.
func TestObservedWriteLeavesDeclaredColumnsUntouched(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)

			tables := declaredTablesInSchema(t, f.store.db)
			before := digestTables(t, f.store.db, tables)

			exerciseEveryObservedWrite(t, f)

			after := digestTables(t, f.store.db, tables)
			for _, table := range tables {
				if before[table] == after[table] {
					continue
				}
				t.Errorf("%s changed during observed writes.\n  before: %s\n"+
					"Rule 2 is an allowlist, not a blocklist: an observed write may write only "+
					"the observed columns. A reported `down` never sets lifecycle, never sets "+
					"desired_state, never deletes a placement -- only a person retires something.",
					table, firstDifference(before[table], after[table]))
			}

			// updated_at is the column rule 3 names, so assert the comparison
			// above actually covered it rather than trusting SELECT *.
			for _, probe := range []struct{ table, query, id string }{
				{"asset", `SELECT * FROM asset WHERE id = ?`, f.assetID},
				{"service_instance", `SELECT * FROM service_instance WHERE id = ?`, f.instanceID},
			} {
				row := rowSnapshot(t, f, probe.query, probe.id)
				if _, ok := row["updated_at"]; !ok {
					t.Fatalf("%s has no updated_at column, so the byte-equality above did not "+
						"cover the column rule 3 names by name", probe.table)
				}
			}

			// Observed writes put nothing in the audit trail (rule 3). The
			// digest already covers change_log as a declared table; this names
			// the count because the log is append-only and a leaked row is
			// permanent.
			if got, want := f.count(t, `SELECT COUNT(*) FROM change_log`), digestRows(before["change_log"]); got != want {
				t.Errorf("change_log has %d rows after observed writes, want %d. Rule 3: a 30-second "+
					"poll is ~2,880 rows per asset per day, and the configuration change that "+
					"caused an incident becomes unfindable underneath.", got, want)
			}

			// And the observed side did happen, or the whole comparison above is
			// a test that nothing writes anything.
			if n := f.count(t, `SELECT COUNT(*) FROM asset_health`); n == 0 {
				t.Fatal("no asset_health rows were written; this test proved nothing")
			}
			if f.transitions(t) == 0 {
				t.Fatal("no transitions were recorded; this test proved nothing")
			}
		})
	}
}

// exerciseEveryObservedWrite drives one entity through every branch of the
// observed write path: a first reading, a buffered repeat, a flush, a further
// transition, a stale replay, a re-delivery, a compressed episode and its
// closure, a placement's reading, and a report about something the inventory
// does not have.
//
// The point is coverage of the WRITE PATH rather than of the dispositions: a
// declared column touched by any one of these branches is the failure, and a
// test that exercised only the happy transition would miss the branch that
// closes an episode.
func exerciseEveryObservedWrite(t *testing.T, f *observedFixture) {
	t.Helper()

	f.observe(t, 0, "up")              // first observation: a transition
	f.observe(t, 30*time.Second, "up") // unchanged: buffered, no write
	if _, err := f.recorder.Flush(f.ctx); err != nil {
		t.Fatalf("flushing: %v", err)
	}
	f.observe(t, 60*time.Second, "down") // a transition

	// A replay: older than what is stored, so discarded rather than applied.
	stale := f.report("up", flapBase.Add(45*time.Second))
	if _, err := f.recorder.RecordObservation(f.ctx, f.agent, f.scope, stale); err != nil {
		t.Fatalf("recording a stale report: %v", err)
	}

	// A re-delivery: the same observation_id twice.
	dup := f.report("down", flapBase.Add(90*time.Second))
	dup.ObservationID = "redelivered-1"
	f.clock.set(flapBase.Add(90 * time.Second))
	f.record(t, dup)
	f.record(t, dup)

	// Enough churn to open a compressed episode, then quiet long enough to
	// close it (rule 9).
	for i := 4; i <= 10; i++ {
		state := "up"
		if i%2 == 1 {
			state = "down"
		}
		f.observe(t, time.Duration(i)*30*time.Second, state)
	}
	quiet := 10*30*time.Second + domain.FlapWindow + time.Minute
	f.observe(t, quiet, "up")

	// A placement, which is the other observable entity type.
	f.clock.set(flapBase.Add(quiet + time.Minute))
	f.record(t, domain.ObservationSpec{
		EntityType:      domain.ObservableServiceInstance,
		EntityID:        f.instanceID,
		State:           "degraded",
		ReportedAt:      domain.FormatTime(f.clock.Now()),
		IntervalSeconds: 60,
	})

	// Something the inventory does not have: 404, queued as drift, never
	// created (rule 6).
	ghost := f.report("down", f.clock.Now())
	ghost.EntityID = "no-such-entity"
	if _, err := f.recorder.RecordObservation(f.ctx, f.agent, f.scope, ghost); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an observation for an unknown entity returned %v, want ErrNotFound. "+
			"`INSERT ... ON CONFLICT` is the natural verb for a webhook and it is exactly "+
			"what turns a narrow monitoring token into an inventory-write vector.", err)
	}
}

// declaredTablesInSchema is every live table that holds a declared or
// provenance fact: the classification minus the observed tables and minus the
// ones the census exempts.
func declaredTablesInSchema(t *testing.T, db *DB) []string {
	t.Helper()
	var out []string
	for _, table := range liveTables(t, db) {
		if _, exempt := domain.IsExemptTable(table); exempt {
			continue
		}
		if domain.ObservedTables[table] {
			continue
		}
		out = append(out, table)
	}
	if len(out) == 0 {
		t.Fatal("no declared tables found; this comparison would be vacuous")
	}
	sort.Strings(out)
	return out
}

// digestTables renders every row of every named table as sorted text, so a
// comparison is byte-for-byte over columns nobody has to enumerate -- including
// ones added after this test was written.
func digestTables(t *testing.T, db *DB, tables []string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(tables))
	for _, table := range tables {
		out[table] = digestTable(t, db, table)
	}
	return out
}

func digestTable(t *testing.T, db *DB, table string) string {
	t.Helper()
	rows, err := db.Reader.Queryx(`SELECT * FROM ` + table)
	if err != nil {
		t.Fatalf("reading %s: %v", table, err)
	}
	defer rows.Close()

	var lines []string
	for rows.Next() {
		raw := map[string]any{}
		if err := rows.MapScan(raw); err != nil {
			t.Fatalf("scanning %s: %v", table, err)
		}
		cols := make([]string, 0, len(raw))
		for col := range raw {
			cols = append(cols, col)
		}
		sort.Strings(cols)
		parts := make([]string, 0, len(cols))
		for _, col := range cols {
			parts = append(parts, col+"="+renderCell(raw[col]))
		}
		lines = append(lines, strings.Join(parts, " "))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading %s: %v", table, err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// renderCell makes a value comparable across drivers within one engine. BLOBs
// are hex-rendered rather than string-formatted so the four-column IP pattern
// compares as bytes rather than as mojibake.
func renderCell(v any) string {
	switch value := v.(type) {
	case nil:
		return "<null>"
	case []byte:
		return fmt.Sprintf("0x%x", value)
	default:
		return fmt.Sprintf("%v", value)
	}
}

// digestRows is how many rows a digest describes.
func digestRows(digest string) int {
	if digest == "" {
		return 0
	}
	return len(strings.Split(digest, "\n"))
}

// firstDifference reports the first differing line of two digests, so a failure
// names a row rather than dumping a table.
func firstDifference(before, after string) string {
	b := strings.Split(before, "\n")
	a := strings.Split(after, "\n")
	for i := 0; i < len(b) && i < len(a); i++ {
		if b[i] != a[i] {
			return fmt.Sprintf("%q\n   after: %q", b[i], a[i])
		}
	}
	if len(a) > len(b) {
		return fmt.Sprintf("(%d rows)\n   after: (%d rows, first new: %q)", len(b), len(a), a[len(b)])
	}
	if len(b) > len(a) {
		return fmt.Sprintf("(%d rows, first missing: %q)\n   after: (%d rows)", len(b), b[len(a)], len(a))
	}
	return "(digests differ)"
}

// ---------------------------------------------------------------------------
// TestRepeatObservationWritesNoRow
// ---------------------------------------------------------------------------

// TestRepeatObservationWritesNoRow: 100 identical reports produce zero rows and
// leave state_since where it was.
//
// Rule 3: "Audit the transition, not the heartbeat -- and never lose the onset."
// A transition is a change in the state column alone. state_since is "down since
// when", the first question anyone asks at 03:00, and it is "never touched by a
// repeat report".
//
// Rule 4 adds the second half: a report that changes nothing "must not open a
// write transaction on the request path". On SQLite the writer pool is one
// connection, so a design where heartbeats can occupy it makes "retire this
// asset" time out at exactly the moment it matters. That is asserted here by
// reading the durable row between heartbeats: if the request path wrote, the
// disk has moved.
func TestRepeatObservationWritesNoRow(t *testing.T) {
	const heartbeats = 100

	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)

			// The first observation has no prior value, IS a transition, and is
			// logged: it is the entry an incident reviewer looks for.
			f.observe(t, 0, "down")
			onset := f.health(t)
			if f.transitions(t) != 1 {
				t.Fatalf("the first observation wrote %d transitions, want 1 -- the first "+
					"reading is a transition and is logged", f.transitions(t))
			}
			if onset.StateSince != domain.FormatTime(flapBase) {
				t.Fatalf("state_since = %q, want the server clock at the transition %q",
					onset.StateSince, domain.FormatTime(flapBase))
			}
			durableAfterFirst := f.health(t)

			for i := 1; i <= heartbeats; i++ {
				f.observe(t, time.Duration(i)*30*time.Second, "down")

				// Rule 4: nothing durable moved, because nothing was written.
				durable := f.health(t)
				if durable.LastReportAt != durableAfterFirst.LastReportAt ||
					durable.ReportedAt != durableAfterFirst.ReportedAt {
					t.Fatalf("heartbeat %d wrote through on the request path: last_report_at %q -> %q. "+
						"Rule 4 buffers no-op bumps and flushes on an interval; on SQLite the "+
						"writer pool is one connection.",
						i, durableAfterFirst.LastReportAt, durable.LastReportAt)
				}
			}

			if got := f.transitions(t); got != 1 {
				t.Errorf("%d transition rows after %d identical reports, want 1. Rule 3: a "+
					"30-second poll is ~2,880 rows per asset per day, ~1.4M/day for a "+
					"500-device estate, and the configuration change that caused an "+
					"incident becomes unfindable underneath.", got, heartbeats)
			}

			// The onset survives the flush too: the flush moves timestamps, and
			// state_since is not one of them.
			if _, err := f.recorder.Flush(f.ctx); err != nil {
				t.Fatalf("flushing: %v", err)
			}
			settled := f.health(t)
			if settled.StateSince != onset.StateSince {
				t.Errorf("state_since moved from %q to %q across %d repeat reports. "+
					"\"Down since when\" is the first question anyone asks at 03:00.",
					onset.StateSince, settled.StateSince, heartbeats)
			}
			if settled.State != onset.State {
				t.Errorf("state changed from %q to %q without a transition", onset.State, settled.State)
			}
			if settled.LastReportAt == durableAfterFirst.LastReportAt {
				t.Error("last_report_at never moved, even after a flush. Staleness is computed " +
					"from it (rule 8), so a frozen last_report_at renders a healthy estate as " +
					"unknown -- the buffer must be flushed, not discarded.")
			}
			if got := f.transitions(t); got != 1 {
				t.Errorf("the flush wrote %d transition rows, want 1 in total: a flush moves "+
					"timestamps and never state", got)
			}

			// Rule 3 again, from the other side: none of this reached the audit
			// trail.
			if n := f.count(t, `SELECT COUNT(*) FROM change_log WHERE entity_type IN (?, ?)`,
				"asset_health", "observed_transition"); n != 0 {
				t.Errorf("%d observed rows reached change_log", n)
			}

			// A byte-identical replay is a re-delivery, not new information.
			replay := f.report("down", flapBase.Add(heartbeats*30*time.Second))
			replay.ObservationID = "same-id"
			f.record(t, replay)
			transitionsBeforeReplay := f.transitions(t)
			for i := 0; i < 10; i++ {
				f.record(t, replay)
			}
			if got := f.transitions(t); got != transitionsBeforeReplay {
				t.Errorf("replaying one report ten times wrote %d extra transition rows",
					got-transitionsBeforeReplay)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestStaleObservationRejected
// ---------------------------------------------------------------------------

// TestStaleObservationRejected applies t2 and then t1: t2 survives and no
// second row is written.
//
// Rule 4: "A report whose reported_at is not newer than the stored one is
// discarded, not applied -- a retry is newer or identical, a replay is older,
// and last-write-wins on arrival order turns a delayed `down` landing after `up`
// into two transitions that never happened, three minutes late, pointing an
// incident review at the wrong cause."
//
// The states differ deliberately. A stale report carrying the same state would
// be indistinguishable from a heartbeat and would pass on the transition rule
// alone; the ordering guard is only visible when the late report disagrees.
func TestStaleObservationRejected(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)

			t1 := flapBase
			t2 := flapBase.Add(3 * time.Minute)

			// The estate goes down at t1, comes back at t2, and the collector
			// delivers them out of order.
			f.observe(t, 0, "up")
			f.clock.set(t2)
			f.record(t, f.report("down", t2))

			afterT2 := f.health(t)
			transitionsAfterT2 := f.transitions(t)
			if afterT2.State != domain.HealthDown {
				t.Fatalf("state = %q after the t2 report, want down", afterT2.State)
			}

			// The delayed t1 report arrives now.
			result, err := f.recorder.RecordObservation(f.ctx, f.agent, f.scope, f.report("up", t1))
			if err != nil {
				t.Fatalf("recording the delayed report: %v", err)
			}
			if result.Disposition != ObservationStale {
				t.Errorf("a report older than the stored one was %q, want %q -- it must be "+
					"discarded, not applied", result.Disposition, ObservationStale)
			}

			afterT1 := f.health(t)
			if afterT1.State != afterT2.State {
				t.Errorf("state = %q after a stale report, want %q. A delayed `down` landing "+
					"after `up` becomes two transitions that never happened, three minutes "+
					"late, pointing an incident review at the wrong cause.",
					afterT1.State, afterT2.State)
			}
			if afterT1.StateSince != afterT2.StateSince {
				t.Errorf("state_since moved from %q to %q on a stale report", afterT2.StateSince, afterT1.StateSince)
			}
			if afterT1.ReportedAt != afterT2.ReportedAt {
				t.Errorf("reported_at moved backwards from %q to %q. RFC3339 UTC TEXT sorts "+
					"lexicographically, so rewinding it makes every later comparison wrong.",
					afterT2.ReportedAt, afterT1.ReportedAt)
			}
			if got := f.transitions(t); got != transitionsAfterT2 {
				t.Errorf("a stale report wrote %d extra transition rows, want 0", got-transitionsAfterT2)
			}

			// A report with exactly the stored timestamp is a retry, and is also
			// not newer: "not newer" is the test, not "older".
			t.Run("a report at exactly the stored instant is not newer", func(t *testing.T) {
				res, err := f.recorder.RecordObservation(f.ctx, f.agent, f.scope, f.report("up", t2))
				if err != nil {
					t.Fatalf("recording a same-instant report: %v", err)
				}
				if res.Disposition != ObservationStale {
					t.Errorf("disposition = %q, want %q", res.Disposition, ObservationStale)
				}
				if h := f.health(t); h.State != afterT2.State {
					t.Errorf("state = %q after a same-instant disagreement, want %q", h.State, afterT2.State)
				}
			})

			// The same rule, at the other end: a clock far ahead of the server
			// wins every comparison from then on, so this entity could never be
			// updated again.
			t.Run("a report from the future is refused outright", func(t *testing.T) {
				// The rule names the number: "Reject reported_at more than 300s
				// ahead of server time". Pinned rather than read from the
				// constant, because a test that derives its input from the value
				// under test cannot notice that value changing -- and widening
				// this ceiling is the one edit that silently re-opens the whole
				// ordering hazard.
				if domain.MaxClockSkew != 300*time.Second {
					t.Errorf("domain.MaxClockSkew = %s, and rule 4 says 300s. A broken or hostile "+
						"clock poisons ordering for everything it touches: RFC3339 TEXT sorts "+
						"lexicographically, so a future date pins to the top of every ORDER BY "+
						"and no later report can beat it on monotonicity.", domain.MaxClockSkew)
				}
				future := f.report("up", f.clock.Now().Add(domain.MaxClockSkew+time.Minute))
				_, err := f.recorder.RecordObservation(f.ctx, f.agent, f.scope, future)
				if !errors.Is(err, domain.ErrInvalid) {
					t.Errorf("a report %s ahead of the server clock returned %v, want a "+
						"validation error. RFC3339 TEXT sorts lexicographically, so a future "+
						"date pins to the top of every ORDER BY.",
						domain.MaxClockSkew+time.Minute, err)
				}
				if h := f.health(t); h.State != afterT2.State {
					t.Errorf("a rejected future report still changed the state to %q", h.State)
				}
			})
		})
	}
}

// ---------------------------------------------------------------------------
// TestSnapshotRedactsSecretRef
// ---------------------------------------------------------------------------

// TestSnapshotRedactsSecretRef: secret_ref is absent from every change_log
// diff.
//
// Rule 12: "snapshotJSON serialises every db-tagged field, so CreateIdentity's
// logCreate writes identity.secret_ref -- the full Vault path inventory -- into
// change_log.diff, which renders to every authenticated reader including
// read-only ones, and stays forever ... Record THAT secret_ref changed, never
// what to. A path is not a secret, but a complete map of secret paths readable
// by every account is a reconnaissance gift."
//
// "Every" is taken literally: the whole change_log table is read raw, not
// through ListRecentChanges, which caps at a limit and would let a leak hide
// beyond the last page. Both functions the rule names are covered -- snapshot
// via a create, diff via a direct comparison -- because redacting one and not
// the other leaks on the first update instead of the first create.
func TestSnapshotRedactsSecretRef(t *testing.T) {
	const vaultPath = "kv/prod/orders/postgres/superuser"

	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)

			identity, err := domain.NewIdentity(NewID(), domain.IdentityServiceAccount, "svc-orders")
			if err != nil {
				t.Fatalf("building identity: %v", err)
			}
			identity.SecretRef = strPtr(vaultPath)
			if err := s.CreateIdentity(ctx, testActor, identity); err != nil {
				t.Fatalf("creating identity: %v", err)
			}

			// A second identity reached through a dependency, so the leak has a
			// second route to the log if one exists.
			other, err := domain.NewIdentity(NewID(), domain.IdentityServiceAccount, "svc-billing")
			if err != nil {
				t.Fatalf("building identity: %v", err)
			}
			other.SecretRef = strPtr("kv/prod/billing/api-token")
			if err := s.CreateIdentity(ctx, testActor, other); err != nil {
				t.Fatalf("creating identity: %v", err)
			}

			var diffs []string
			if err := s.read(ctx, &diffs, `SELECT diff FROM change_log`); err != nil {
				t.Fatalf("reading every audit diff: %v", err)
			}
			if len(diffs) == 0 {
				t.Fatal("the audit trail is empty; this test proved nothing")
			}
			for _, diff := range diffs {
				for _, secret := range []string{vaultPath, "kv/prod/billing/api-token"} {
					if strings.Contains(diff, secret) {
						t.Errorf("a secret path reached the audit trail: %s", diff)
					}
				}
			}

			// The rule asks for the change to be recorded, not erased: a reader
			// must be able to tell the field exists and moved.
			var identityDiff string
			err = s.readOne(ctx, &identityDiff,
				`SELECT diff FROM change_log WHERE entity_type = ? AND entity_id = ?`,
				"identity", identity.ID)
			if err != nil {
				t.Fatalf("reading the identity's audit entry: %v", err)
			}
			if !strings.Contains(identityDiff, "secret_ref") {
				t.Errorf("the diff does not mention secret_ref at all, so a reader cannot tell "+
					"the identity has one: %s", identityDiff)
			}
			if !strings.Contains(identityDiff, domain.Redacted) {
				t.Errorf("the diff does not mark the field redacted: %s", identityDiff)
			}

			// diffJSON is the other half rule 12 names, and there is no
			// UpdateIdentity to drive it through, so it is exercised directly.
			// Redacting only the snapshot leaks on the first update instead of
			// the first create, which is a worse failure because it looks fixed.
			t.Run("the update path redacts too", func(t *testing.T) {
				before := *identity
				after := *identity
				after.SecretRef = strPtr("kv/prod/orders/postgres/rotated")
				diff, changed, err := diffJSON(&before, &after)
				if err != nil {
					t.Fatalf("diffing: %v", err)
				}
				if !changed {
					t.Fatal("a changed secret_ref produced no diff, so a rotation is invisible")
				}
				for _, secret := range []string{vaultPath, "kv/prod/orders/postgres/rotated"} {
					if strings.Contains(diff, secret) {
						t.Errorf("diffJSON leaked a secret path: %s", diff)
					}
				}
				if !strings.Contains(diff, "secret_ref") || !strings.Contains(diff, domain.Redacted) {
					t.Errorf("diffJSON did not record that secret_ref changed: %s", diff)
				}
			})

			// The same reasoning applies to the other field the rule cites as
			// precedent: "an audit trail is read by more people than the user
			// table is".
			t.Run("a password hash is redacted by the same mechanism", func(t *testing.T) {
				const hash = "$argon2id$v=19$m=65536,t=1,p=4$DISTINCTIVEHASHVALUE"
				u, err := domain.NewAppUser(NewID(), "boundary-user", domain.UserSourceLocal, s.Now())
				if err != nil {
					t.Fatalf("building user: %v", err)
				}
				u.PasswordHash = strPtr(hash)
				if err := s.CreateUser(ctx, testActor, u); err != nil {
					t.Fatalf("creating user: %v", err)
				}
				var all []string
				if err := s.read(ctx, &all, `SELECT diff FROM change_log`); err != nil {
					t.Fatalf("reading every audit diff: %v", err)
				}
				for _, diff := range all {
					if strings.Contains(diff, hash) {
						t.Errorf("a password hash reached the audit trail: %s", diff)
					}
				}
			})
		})
	}
}

// ---------------------------------------------------------------------------
// TestOnlyUserActorWritesDeclaredSource
// ---------------------------------------------------------------------------

// TestOnlyUserActorWritesDeclaredSource is rule 7: provenance laundering.
//
// "Only a `user` actor may write the provenance value `declared` ... A machine
// credential may set only discovered-subset values. No machine may assert that
// a fact was hand-declared -- that laundering is how a fabricated workload
// inside an in_scope environment renders to an operator as hand-asserted fact
// and never reaches the conflict queue."
//
// And from the amended "Never do this": "Let an observed writer touch a
// declared column, or a machine actor write verified_by, verified_at, or
// source = 'declared'."
//
// The store is where this has to hold. A handler that happens not to expose the
// field is a handler, not a boundary -- rule 1's argument applies here word for
// word, and post-M5 discovery agents are named in rule 10 as writers of
// dependency rows.
//
// The seeder is out of scope by construction: rule 10 names SystemActor as a
// writer of the declared inventory, and domain.CheckProvenanceWrite documents
// the carve-out. What may not happen is an AGENT asserting `declared`.
func TestOnlyUserActorWritesDeclaredSource(t *testing.T) {
	agent, err := domain.AgentActor("discovery-1")
	if err != nil {
		t.Fatalf("building agent actor: %v", err)
	}

	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newProvenanceFixture(t, e)

			t.Run("an agent may not create a dependency claiming it was declared", func(t *testing.T) {
				dep := f.dependency(t, domain.SourceDeclared)
				err := f.store.CreateDependency(f.ctx, agent, dep, nil)
				if err == nil {
					t.Errorf("a machine credential (%s actor) created a dependency with source = %q. "+
						"No machine may assert that a fact was hand-declared: that laundering is "+
						"how a fabricated edge renders to an operator as hand-asserted fact and "+
						"never reaches the conflict queue. domain.CheckProvenanceWrite exists for "+
						"exactly this and no store method calls it.", agent.Kind, domain.SourceDeclared)
				} else if !errors.Is(err, domain.ErrInvalid) {
					t.Errorf("error = %v, want a validation error", err)
				}
			})

			t.Run("an agent may create a dependency it discovered", func(t *testing.T) {
				dep := f.dependency(t, domain.SourceDiscoveredNetstat)
				if err := f.store.CreateDependency(f.ctx, agent, dep, nil); err != nil {
					t.Errorf("a machine credential could not record what it discovered: %v. "+
						"Rule 7 restricts the value, not the writer.", err)
				}
			})

			t.Run("an operator may declare", func(t *testing.T) {
				dep := f.dependency(t, domain.SourceDeclared)
				if err := f.store.CreateDependency(f.ctx, testActor, dep, nil); err != nil {
					t.Errorf("a signed-in operator could not declare a dependency: %v", err)
				}
			})

			t.Run("an agent may not launder a discovered edge into a declared one", func(t *testing.T) {
				dep := f.dependency(t, domain.SourceDiscoveredNetstat)
				if err := f.store.CreateDependency(f.ctx, agent, dep, nil); err != nil {
					t.Fatalf("seeding a discovered edge: %v", err)
				}
				row, err := f.store.GetDependency(f.ctx, dep.ID)
				if err != nil {
					t.Fatalf("re-reading the edge: %v", err)
				}
				laundered := row.Dependency
				laundered.Source = domain.SourceDeclared
				if err := f.store.UpdateDependency(f.ctx, agent, &laundered, nil); err == nil {
					t.Error("a machine flipped an edge from discovered to declared. Rule 7: " +
						"\"Flipping an edge between declared and discovered_* is an operator " +
						"act with a change_log row.\"")
				}
				after, err := f.store.GetDependency(f.ctx, dep.ID)
				if err != nil {
					t.Fatalf("re-reading the edge: %v", err)
				}
				if after.Source == domain.SourceDeclared {
					t.Errorf("the edge's source is now %q", after.Source)
				}
			})

			t.Run("an agent may not create a placement claiming it was declared", func(t *testing.T) {
				si, err := domain.NewServiceInstance(NewID(), f.serviceID, f.assetID, domain.RuntimeSystemd, 7, f.store.Now())
				if err != nil {
					t.Fatalf("building placement: %v", err)
				}
				si.Source = domain.SourceDeclared
				if err := f.store.CreateInstance(f.ctx, agent, si); err == nil {
					t.Errorf("a machine credential (%s actor) created a service_instance with "+
						"source = %q. A fabricated workload inside an in_scope environment must "+
						"not render to an operator as hand-asserted fact. The CHECK added by "+
						"00008 constrains the VALUE; rule 7 also constrains the WRITER, and "+
						"domain.CheckProvenanceWrite is the uncalled function that would.",
						agent.Kind, domain.SourceDeclared)
				}
			})

			t.Run("an agent may not sign off an edge as verified", func(t *testing.T) {
				dep := f.dependency(t, domain.SourceDiscoveredNetstat)
				if err := f.store.CreateDependency(f.ctx, agent, dep, nil); err != nil {
					t.Fatalf("seeding a discovered edge: %v", err)
				}
				if err := f.store.VerifyDependency(f.ctx, agent, dep.ID); err == nil {
					t.Error("a machine credential verified a dependency. verified_by/verified_at " +
						"are a PERSON's attestation that an edge is legitimate; a machine " +
						"writing them is a rubber stamp on an undocumented chd edge and on the " +
						"firewall rule justified by it.")
				}
				row, err := f.store.GetDependency(f.ctx, dep.ID)
				if err != nil {
					t.Fatalf("re-reading the edge: %v", err)
				}
				if row.VerifiedBy != nil || row.VerifiedAt != nil {
					t.Errorf("verified_by = %v, verified_at = %v after a machine verification",
						derefText(row.VerifiedBy), derefText(row.VerifiedAt))
				}
			})

			t.Run("confidence is not taken from the payload", func(t *testing.T) {
				// Rule 7's closing sentence: "`confidence` is likewise set by the
				// store from the credential, never from the payload:
				// self-attested confidence is not a control."
				dep := f.dependency(t, domain.SourceDiscoveredNetstat)
				claimed := 1.0
				dep.Confidence = &claimed
				if err := f.store.CreateDependency(f.ctx, agent, dep, nil); err != nil {
					// Refusing the field outright also satisfies the rule.
					return
				}
				row, err := f.store.GetDependency(f.ctx, dep.ID)
				if err != nil {
					t.Fatalf("re-reading the edge: %v", err)
				}
				if row.Confidence != nil && *row.Confidence == claimed {
					t.Errorf("a machine's self-declared confidence of %v was stored verbatim. "+
						"Self-attested confidence is not a control.", claimed)
				}
			})
		})
	}
}

// provenanceFixture is the smallest estate a dependency needs: a consumer, a
// provider and something to depend on.
type provenanceFixture struct {
	store     *SQLStore
	ctx       context.Context
	serviceID string
	assetID   string

	consumerID string
	endpointID string
}

func (f *provenanceFixture) dependency(t *testing.T, source string) *domain.Dependency {
	t.Helper()
	endpoint := f.endpointID
	dep, err := domain.NewDependency(NewID(), domain.DependencySpec{
		ConsumerServiceID:  f.consumerID,
		ProviderEndpointID: &endpoint,
		Nature:             domain.NatureHard,
		FailureMode:        "orders cannot be written",
		Source:             source,
	}, f.store.Now())
	if err != nil {
		t.Fatalf("building dependency with source %q: %v", source, err)
	}
	return dep
}

func newProvenanceFixture(t *testing.T, e Engine) *provenanceFixture {
	t.Helper()
	s, ctx := newStore(t, e)

	envID := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
	assetID := mustAsset(t, s, ctx, domain.KindServer, "app-01", nil, envID)

	mkService := func(code string) string {
		svc, err := domain.NewService(NewID(), domain.ServiceSpec{
			Code: code, Name: code, Kind: domain.SvcAPI,
			EnvironmentID: envID, Availability: domain.AvailStandalone, Tier: 2,
		}, s.Now())
		if err != nil {
			t.Fatalf("building service %s: %v", code, err)
		}
		if err := s.CreateService(ctx, testActor, svc); err != nil {
			t.Fatalf("creating service %s: %v", code, err)
		}
		return svc.ID
	}
	consumer, provider := mkService("orders"), mkService("orders-db")

	port := 5432
	ep, err := domain.NewEndpoint(NewID(), provider, "sql", domain.ProtoTCP, &port, domain.BindHost)
	if err != nil {
		t.Fatalf("building endpoint: %v", err)
	}
	if err := s.CreateEndpoint(ctx, testActor, ep); err != nil {
		t.Fatalf("creating endpoint: %v", err)
	}

	return &provenanceFixture{
		store: s, ctx: ctx, serviceID: provider, assetID: assetID,
		consumerID: consumer, endpointID: ep.ID,
	}
}

// ---------------------------------------------------------------------------
// TestObservedStateNotIndexed
// ---------------------------------------------------------------------------

// TestObservedStateNotIndexed keeps search_index free of observed values.
//
// Rule 13: "search_index is built from declared columns only; filtering by
// health is a query against the entity table, not a search. Otherwise a decoy
// row carrying a real hostname surfaces to a responder pasting that hostname
// mid-incident."
//
// The reporter id is the probe rather than the state words: `up` and `down`
// occur legitimately in declared text, and an assertion that fires on those
// would be turned off the first time someone names a rack `up-1`. A reporter id
// appears nowhere in declared data, so a hit is unambiguous.
//
// The second half of the same rule is here too -- "never free text". The enum
// is what stops an external credential authoring the string an operator reads
// mid-incident, so it is asserted against the live CHECK on both engines rather
// than against the Go constant set, which is the half that cannot be bypassed
// by a raw write.
func TestObservedStateNotIndexed(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newObservedFixture(t, e)

			before := f.count(t, `SELECT COUNT(*) FROM search_index`)
			if before == 0 {
				t.Fatal("search_index is empty before any observation; every assertion below " +
					"would be vacuous")
			}

			exerciseEveryObservedWrite(t, f)

			if after := f.count(t, `SELECT COUNT(*) FROM search_index`); after != before {
				t.Errorf("search_index went from %d rows to %d during observed writes. Rule 3: "+
					"reindexing on every heartbeat rewrites the FTS table thousands of times "+
					"a day against a single SQLite writer.", before, after)
			}

			type doc struct {
				EntityType string `db:"entity_type"`
				EntityID   string `db:"entity_id"`
				Title      string `db:"title"`
				Subtitle   string `db:"subtitle"`
				Body       string `db:"body"`
			}
			var docs []doc
			err := f.store.db.Reader.Select(&docs,
				`SELECT entity_type, entity_id, title, subtitle, body FROM search_index`)
			if err != nil {
				t.Fatalf("reading search_index: %v", err)
			}
			if len(docs) == 0 {
				t.Fatal("search_index is empty; this test proved nothing")
			}

			for _, d := range docs {
				if domain.ObservedTables[d.EntityType] {
					t.Errorf("search_index carries a %s document (%s). Filtering by health is a "+
						"query against the entity table, not a search.", d.EntityType, d.EntityID)
				}
				text := strings.ToLower(d.Title + " " + d.Subtitle + " " + d.Body)
				for _, needle := range []string{"prom-a", domain.AgentActorPrefix, "asset_health", "observed_transition"} {
					if strings.Contains(text, strings.ToLower(needle)) {
						t.Errorf("search_index contains %q, which no declared column holds: %+v", needle, d)
					}
				}
			}

			// And through the query path, which is where a decoy row would
			// actually reach a responder.
			for _, query := range []string{"prom-a", "monitor:prom-a"} {
				results, err := f.store.Search(f.ctx, query, 20)
				if err != nil {
					t.Fatalf("searching for %q: %v", query, err)
				}
				if len(results) != 0 {
					t.Errorf("searching for %q returned %d results; an observed value is not "+
						"searchable", query, len(results))
				}
			}

			// Rule 13, second half. A raw write is the right probe: the Go
			// constant set is the first line of defence and the CHECK is the
			// second, and only the second survives a writer that forgets to ask.
			t.Run("state is an enum in the database, not free text", func(t *testing.T) {
				cases := []struct{ table, stmt string }{
					{"asset_health", `INSERT INTO asset_health
						(entity_type, entity_id, reporter, state, state_since, reported_at, last_report_at)
						VALUES (?, ?, ?, ?, ?, ?, ?)`},
				}
				for _, tc := range cases {
					for _, vendorWord := range []string{"firing", "NotReady", "2", "running", ""} {
						_, err := f.store.db.Writer.Exec(f.store.db.Rebind(tc.stmt),
							domain.ObservableAsset, NewID(), "prom-b", vendorWord,
							domain.FormatTime(f.clock.Now()), domain.FormatTime(f.clock.Now()),
							domain.FormatTime(f.clock.Now()))
						if err == nil {
							t.Errorf("%s.state accepted %q. Vendor vocabularies are mapped at the "+
								"adapter per reporter; an unmapped value is 422 with the offending "+
								"value echoed, never coerced, never stored raw.", tc.table, vendorWord)
						}
					}
				}

				// The Go constant set and the CHECK must agree, or one of them
				// is decorative.
				for _, state := range domain.HealthStates {
					_, err := f.store.db.Writer.Exec(f.store.db.Rebind(cases[0].stmt),
						domain.ObservableAsset, NewID(), "prom-b", string(state),
						domain.FormatTime(f.clock.Now()), domain.FormatTime(f.clock.Now()),
						domain.FormatTime(f.clock.Now()))
					if err != nil {
						t.Errorf("the CHECK rejected %q, which is in domain.HealthStates: %v", state, err)
					}
				}
			})
		})
	}
}
