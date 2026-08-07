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
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Tests for retention (docs/AUDIT.md rule 10). Both engines.
//
// Two of these -- TestChangeLogIsAppendOnly and
// TestPruneNeverRemovesDeclaredEntries -- are named in AUDIT.md's boundary test
// table. They are not behaviour tests of a feature; they are the enforcement of
// a rule whose failure mode is silent and irreversible. History that has been
// deleted cannot be shown to be missing.

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

// pruneFixture is a mixed estate: one environment in scope, one out of it, an
// asset and a placement in each. The mix is the point -- a prune that treats
// the estate uniformly passes every test written against a uniform estate.
type pruneFixture struct {
	store *SQLStore
	ctx   context.Context
	now   time.Time

	admin     *domain.AppUser
	actor     domain.Actor
	inAsset   string
	outAsset  string
	inPlace   string
	outPlace  string
	seededIDs map[string]string
}

func newPruneFixture(t *testing.T, e Engine) *pruneFixture {
	t.Helper()

	clock := newTestClock()
	s := New(e.Open(t)).WithClock(clock.Now)
	ctx := context.Background()

	inEnv := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
	outEnv := mustScopelessEnvironment(t, s, ctx, "lab")

	inAsset := mustAsset(t, s, ctx, domain.KindServer, "in-scope-01", nil, inEnv)
	outAsset := mustAsset(t, s, ctx, domain.KindServer, "out-of-scope-01", nil, outEnv)

	// An operator with a real account, because the audit row must carry an
	// opaque app_user.id and a fabricated actor would not prove that.
	admin, err := domain.NewAppUser(NewID(), "prune-operator", domain.UserSourceLocal, s.Now())
	if err != nil {
		t.Fatalf("building operator: %v", err)
	}
	if err := s.CreateUser(ctx, testActor, admin); err != nil {
		t.Fatalf("creating operator: %v", err)
	}

	f := &pruneFixture{
		store: s, ctx: ctx, now: clock.Now(),
		admin: admin, actor: domain.UserActor(admin),
		inAsset: inAsset, outAsset: outAsset,
		seededIDs: map[string]string{},
	}
	f.inPlace = f.placement(t, "orders", inEnv, inAsset)
	f.outPlace = f.placement(t, "sandbox", outEnv, outAsset)
	return f
}

// mustScopelessEnvironment creates an environment that is explicitly NOT in
// scope. mustEnvironment always sets in_scope, which is exactly the case the
// floor does not apply to.
func mustScopelessEnvironment(t *testing.T, s *SQLStore, ctx context.Context, code string) string {
	t.Helper()
	env, err := domain.NewEnvironment(NewID(), code, code, domain.EnvRoleDev, false, 1, s.Now())
	if err != nil {
		t.Fatalf("building environment %s: %v", code, err)
	}
	if err := s.CreateEnvironment(ctx, testActor, env); err != nil {
		t.Fatalf("creating environment %s: %v", code, err)
	}
	return env.ID
}

func (f *pruneFixture) placement(t *testing.T, code, envID, assetID string) string {
	t.Helper()
	svc, err := domain.NewService(NewID(), domain.ServiceSpec{
		Code: code, Name: code, Kind: domain.SvcAPI,
		EnvironmentID: envID, Availability: domain.AvailStandalone, Tier: 2,
	}, f.store.Now())
	if err != nil {
		t.Fatalf("building service %s: %v", code, err)
	}
	if err := f.store.CreateService(f.ctx, testActor, svc); err != nil {
		t.Fatalf("creating service %s: %v", code, err)
	}
	si, err := domain.NewServiceInstance(NewID(), svc.ID, assetID, domain.RuntimeSystemd, 0, f.store.Now())
	if err != nil {
		t.Fatalf("building placement for %s: %v", code, err)
	}
	if err := f.store.CreateInstance(f.ctx, testActor, si); err != nil {
		t.Fatalf("creating placement for %s: %v", code, err)
	}
	return si.ID
}

// seedTransition writes one ledger row at an arbitrary instant.
//
// Written directly rather than through the recorder because the ages this test
// needs are measured in years, and driving the recorder back through 2025 would
// test the clock harness rather than the prune. The row still satisfies every
// CHECK on the table, so what is pruned here is a real row.
func (f *pruneFixture) seedTransition(t *testing.T, label, entityType, entityID string, at time.Time) string {
	t.Helper()
	id := NewID()
	_, err := f.store.db.Writer.Exec(f.store.db.Rebind(
		`INSERT INTO observed_transition (id, entity_type, entity_id, reporter, kind, from_state, to_state, at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
		id, entityType, entityID, "prom-a", domain.TransitionChange, nil,
		string(domain.HealthDown), domain.FormatTime(at))
	if err != nil {
		t.Fatalf("seeding transition %s: %v", label, err)
	}
	f.seededIDs[label] = id
	return id
}

func (f *pruneFixture) transitionExists(t *testing.T, label string) bool {
	t.Helper()
	var n int
	err := f.store.db.Reader.Get(&n, f.store.db.Reader.Rebind(
		`SELECT COUNT(*) FROM observed_transition WHERE id = ?`), f.seededIDs[label])
	if err != nil {
		t.Fatalf("checking transition %s: %v", label, err)
	}
	return n == 1
}

func (f *pruneFixture) changeLogCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.store.db.Reader.Get(&n, `SELECT COUNT(*) FROM change_log`); err != nil {
		t.Fatalf("counting change_log: %v", err)
	}
	return n
}

// ---------------------------------------------------------------------------
// The floor
// ---------------------------------------------------------------------------

// TestPruneRefusesAnInScopeEntityInsideAYear is rule 10's floor.
//
// "Never below 365 days for an entity resolving to an environment with
// in_scope = TRUE." The floor is per entity, not per run: a short retention
// prunes the lab and leaves production alone, rather than refusing outright or
// quietly doing what it was told. Refusing outright would mean an operator who
// wants 30 days of lab noise gone can never have it and eventually turns the
// job off; doing what it was told loses the evidence a scope validation is for.
func TestPruneRefusesAnInScopeEntityInsideAYear(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newPruneFixture(t, e)

			old := f.now.AddDate(0, 0, -400) // beyond the floor
			recent := f.now.AddDate(0, 0, -100)

			f.seedTransition(t, "in-asset-old", domain.ObservableAsset, f.inAsset, old)
			f.seedTransition(t, "in-asset-recent", domain.ObservableAsset, f.inAsset, recent)
			f.seedTransition(t, "out-asset-old", domain.ObservableAsset, f.outAsset, old)
			f.seedTransition(t, "out-asset-recent", domain.ObservableAsset, f.outAsset, recent)
			f.seedTransition(t, "in-place-old", domain.ObservableServiceInstance, f.inPlace, old)
			f.seedTransition(t, "in-place-recent", domain.ObservableServiceInstance, f.inPlace, recent)
			f.seedTransition(t, "out-place-old", domain.ObservableServiceInstance, f.outPlace, old)
			f.seedTransition(t, "out-place-recent", domain.ObservableServiceInstance, f.outPlace, recent)

			// Thirty days of retention. Well inside the floor.
			report, err := f.store.PruneObservedTransitions(f.ctx, f.actor, PruneOptions{
				Before: f.now.AddDate(0, 0, -30),
			})
			if err != nil {
				t.Fatalf("pruning: %v", err)
			}

			cases := []struct {
				label   string
				survive bool
				why     string
			}{
				{"in-asset-recent", true, "an in_scope asset's history inside the floor"},
				{"in-place-recent", true, "a placement on an in_scope host inherits the host's scope"},
				{"in-asset-old", false, "beyond the floor, so the requested cutoff applies"},
				{"in-place-old", false, "beyond the floor, so the requested cutoff applies"},
				{"out-asset-recent", false, "no in_scope environment, so no floor"},
				{"out-place-recent", false, "no in_scope environment, so no floor"},
				{"out-asset-old", false, "no in_scope environment, so no floor"},
				{"out-place-old", false, "no in_scope environment, so no floor"},
			}
			for _, tc := range cases {
				if got := f.transitionExists(t, tc.label); got != tc.survive {
					verb := "was deleted"
					if got {
						verb = "survived"
					}
					t.Errorf("%s %s; %s", tc.label, verb, tc.why)
				}
			}

			if report.Deleted != 6 {
				t.Errorf("deleted = %d, want 6", report.Deleted)
			}
			if report.Protected != 2 {
				t.Errorf("protected = %d, want 2 -- the run must report what the floor kept", report.Protected)
			}
			if !report.FloorApplied() {
				t.Error("FloorApplied() is false; an operator asking for 30 days needs to be told " +
					"the in_scope half kept a year")
			}
			if got, want := report.InScopeFloor, domain.InScopeRetentionFloor(f.now); !got.Equal(want) {
				t.Errorf("floor = %s, want %s", got, want)
			}
		})
	}
}

// TestPruneHonoursACutoffBeyondTheFloor checks the other side: the floor is a
// floor, not a fixed retention. An operator keeping five years keeps five
// years, and asking for exactly the floor removes everything older.
func TestPruneHonoursACutoffBeyondTheFloor(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newPruneFixture(t, e)

			f.seedTransition(t, "ancient", domain.ObservableAsset, f.inAsset, f.now.AddDate(0, 0, -800))
			f.seedTransition(t, "old", domain.ObservableAsset, f.inAsset, f.now.AddDate(0, 0, -400))

			// Two years of retention: the floor never pulls a generous cutoff in.
			report, err := f.store.PruneObservedTransitions(f.ctx, f.actor, PruneOptions{
				Before: f.now.AddDate(0, 0, -730),
			})
			if err != nil {
				t.Fatalf("pruning: %v", err)
			}
			if f.transitionExists(t, "ancient") {
				t.Error("a row older than a two-year cutoff survived; the floor must not widen retention")
			}
			if !f.transitionExists(t, "old") {
				t.Error("a row inside a two-year cutoff was deleted")
			}
			if report.FloorApplied() {
				t.Error("FloorApplied() is true for a cutoff already beyond the floor")
			}
			if report.Protected != 0 {
				t.Errorf("protected = %d, want 0", report.Protected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The prune touches nothing declared
// ---------------------------------------------------------------------------

// TestPruneNeverRemovesDeclaredEntries is the named boundary test, and the
// sharpest edge in rule 10.
//
// The tempting implementation is `DELETE FROM change_log WHERE actor_kind =
// 'agent'` -- it looks like it removes machine noise and keeps human decisions.
// It does not. actor_kind records WHO WROTE a row, not what kind of fact it is,
// and this repo already writes declared state under non-user kinds: the LDAP
// upsert creates accounts as `system`, the seeder writes the declared inventory
// as `system`, and a discovery agent creates dependency rows -- including the
// chd-carrying edges a firewall rule is justified by -- as `agent`. Pruning on
// that column destroys exactly the scope-validation evidence this tool exists
// to produce, and destroys it silently.
//
// So the declared rows here are written by an AGENT actor on purpose. If the
// prune ever grows an actor_kind predicate, this is what fails.
func TestPruneNeverRemovesDeclaredEntries(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newPruneFixture(t, e)

			agent, err := domain.AgentActor("discovery-1")
			if err != nil {
				t.Fatalf("building agent actor: %v", err)
			}

			// Declared state, written by a machine. Exactly the rows the naive
			// prune would take.
			discovered := mustAssetAs(t, f.store, f.ctx, agent, "discovered-01")
			f.seedTransition(t, "observed", domain.ObservableAsset, f.outAsset, f.now.AddDate(0, 0, -10))

			before := f.changeLogCount(t)
			if before == 0 {
				t.Fatal("no change_log rows to protect; the rest of this test is meaningless")
			}
			agentRows := f.countChangeLog(t, `actor_kind = ?`, domain.ActorKindAgent)
			if agentRows == 0 {
				t.Fatal("no agent-written change_log rows; the rest of this test is meaningless")
			}

			// Reach as far back as the prune is allowed to: everything.
			if _, err := f.store.PruneObservedTransitions(f.ctx, f.actor, PruneOptions{
				Before: f.now,
			}); err != nil {
				t.Fatalf("pruning: %v", err)
			}

			// Every declared entry survives, plus the prune's own.
			if got, want := f.changeLogCount(t), before+1; got != want {
				t.Errorf("change_log has %d rows, want %d (every prior row plus the prune's own entry)", got, want)
			}
			if got := f.countChangeLog(t, `actor_kind = ?`, domain.ActorKindAgent); got != agentRows {
				t.Errorf("%d agent-written change_log rows survived, want %d -- "+
					"actor_kind says who wrote a row, not what kind of fact it is", got, agentRows)
			}
			if got := f.countChangeLog(t, `entity_type = ? AND entity_id = ?`, "asset", discovered); got == 0 {
				t.Error("the discovered asset's audit history was removed")
			}

			// And the declared row itself is untouched: pruning history is not
			// retiring an entity.
			var lifecycle string
			err = f.store.db.Reader.Get(&lifecycle, f.store.db.Reader.Rebind(
				`SELECT lifecycle FROM asset WHERE id = ?`), discovered)
			if err != nil {
				t.Fatalf("re-reading the discovered asset: %v", err)
			}
			if lifecycle != domain.LifecycleActive {
				t.Errorf("asset lifecycle = %s, want active", lifecycle)
			}

			// The observed row, which is the only thing the prune may take.
			if f.transitionExists(t, "observed") {
				t.Error("the observed transition survived a prune that reached past it")
			}
		})
	}
}

func (f *pruneFixture) countChangeLog(t *testing.T, where string, args ...any) int {
	t.Helper()
	var n int
	err := f.store.db.Reader.Get(&n, f.store.db.Reader.Rebind(
		`SELECT COUNT(*) FROM change_log WHERE `+where), args...)
	if err != nil {
		t.Fatalf("counting change_log where %s: %v", where, err)
	}
	return n
}

func mustAssetAs(t *testing.T, s *SQLStore, ctx context.Context, actor domain.Actor, name string) string {
	t.Helper()
	a, err := domain.NewAsset(NewID(), domain.KindServer, name, nil, s.Now())
	if err != nil {
		t.Fatalf("building asset %s: %v", name, err)
	}
	if err := s.CreateAsset(ctx, actor, a, nil); err != nil {
		t.Fatalf("creating asset %s: %v", name, err)
	}
	return a.ID
}

// ---------------------------------------------------------------------------
// The run records itself
// ---------------------------------------------------------------------------

// TestPruneWritesItsOwnAuditRow covers rule 10's last requirement: each run
// writes one change_log row recording the window, the row count and the actor.
//
// A deletion that leaves no trace of having happened is the one gap an audit
// trail cannot recover from -- a reader finding a hole in the history has no
// way to tell a prune from a failure, and the difference decides whether
// anybody investigates.
func TestPruneWritesItsOwnAuditRow(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newPruneFixture(t, e)
			f.seedTransition(t, "old", domain.ObservableAsset, f.outAsset, f.now.AddDate(0, 0, -400))

			before := f.now.AddDate(0, 0, -30)
			report, err := f.store.PruneObservedTransitions(f.ctx, f.actor, PruneOptions{Before: before})
			if err != nil {
				t.Fatalf("pruning: %v", err)
			}

			var rows []domain.ChangeLog
			err = f.store.read(f.ctx, &rows,
				`SELECT cl.*, '' AS actor_name FROM change_log cl WHERE cl.entity_type = ?`,
				"observed_transition")
			if err != nil {
				t.Fatalf("reading the prune's audit row: %v", err)
			}
			if len(rows) != 1 {
				t.Fatalf("%d audit rows for the prune, want exactly 1", len(rows))
			}
			row := rows[0]

			if row.Action != domain.ActionDelete {
				t.Errorf("action = %q, want %q -- the CHECK on change_log.action has no 'prune', "+
					"and altering a CHECK in SQLite means rebuilding the table", row.Action, domain.ActionDelete)
			}
			if row.EntityID != report.RunID {
				t.Errorf("entity_id = %q, want the run id %q", row.EntityID, report.RunID)
			}

			// Rule 5: an opaque id, never a username. The audit trail carries no
			// personal data, so it can be kept forever with no retention
			// argument, and scrubbing an account answers an erasure request
			// while the log keeps its integrity.
			if row.Actor != f.admin.ID {
				t.Errorf("actor = %q, want the opaque app_user id %q", row.Actor, f.admin.ID)
			}
			if strings.Contains(row.Actor, f.admin.Username) {
				t.Errorf("actor = %q contains the username; change_log.actor holds an id", row.Actor)
			}
			if row.ActorKind != domain.ActorKindUser {
				t.Errorf("actor_kind = %q, want %q", row.ActorKind, domain.ActorKindUser)
			}

			// The window and the count.
			var payload struct {
				Pruned struct {
					Table            string `json:"table"`
					Before           string `json:"before"`
					InScopeFloor     string `json:"in_scope_floor"`
					InScopeMinDays   int    `json:"in_scope_minimum_days"`
					Deleted          int64  `json:"deleted"`
					ProtectedInScope int64  `json:"protected_in_scope"`
				} `json:"pruned"`
			}
			if err := json.Unmarshal([]byte(row.Diff), &payload); err != nil {
				t.Fatalf("parsing the prune diff %q: %v", row.Diff, err)
			}
			if payload.Pruned.Table != "observed_transition" {
				t.Errorf("diff names table %q", payload.Pruned.Table)
			}
			if payload.Pruned.Before != domain.FormatTime(before) {
				t.Errorf("diff before = %q, want %q", payload.Pruned.Before, domain.FormatTime(before))
			}
			if payload.Pruned.InScopeFloor != domain.FormatTime(domain.InScopeRetentionFloor(f.now)) {
				t.Errorf("diff floor = %q, want the in_scope floor", payload.Pruned.InScopeFloor)
			}
			if payload.Pruned.InScopeMinDays != domain.MinInScopeRetentionDays {
				t.Errorf("diff minimum days = %d, want %d", payload.Pruned.InScopeMinDays, domain.MinInScopeRetentionDays)
			}
			if payload.Pruned.Deleted != 1 {
				t.Errorf("diff deleted = %d, want 1", payload.Pruned.Deleted)
			}

			// A second run that finds nothing still records that it ran. "The
			// prune found nothing" and "the prune did not run" are different
			// facts and the second is the one worth noticing.
			if _, err := f.store.PruneObservedTransitions(f.ctx, f.actor, PruneOptions{Before: before}); err != nil {
				t.Fatalf("second prune: %v", err)
			}
			if got := f.countChangeLog(t, `entity_type = ?`, "observed_transition"); got != 2 {
				t.Errorf("%d prune audit rows after two runs, want 2", got)
			}
		})
	}
}

// TestPruneDryRunChangesNothing: a dry run must not delete and must not write
// an audit row. An audit trail that records intentions is one where a reader
// can no longer tell which entries are events.
func TestPruneDryRunChangesNothing(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newPruneFixture(t, e)
			f.seedTransition(t, "out-old", domain.ObservableAsset, f.outAsset, f.now.AddDate(0, 0, -400))
			f.seedTransition(t, "in-recent", domain.ObservableAsset, f.inAsset, f.now.AddDate(0, 0, -100))

			before := f.changeLogCount(t)
			report, err := f.store.PruneObservedTransitions(f.ctx, f.actor, PruneOptions{
				Before: f.now.AddDate(0, 0, -30),
				DryRun: true,
			})
			if err != nil {
				t.Fatalf("dry run: %v", err)
			}

			if !f.transitionExists(t, "out-old") {
				t.Error("a dry run deleted a row")
			}
			if got := f.changeLogCount(t); got != before {
				t.Errorf("change_log grew by %d during a dry run", got-before)
			}
			// The counts must predict the real run exactly, or the dry run
			// lies and nobody trusts it twice.
			if report.Deleted != 1 || report.Protected != 1 {
				t.Errorf("dry run reported deleted=%d protected=%d, want 1 and 1", report.Deleted, report.Protected)
			}

			real, err := f.store.PruneObservedTransitions(f.ctx, f.actor, PruneOptions{
				Before: f.now.AddDate(0, 0, -30),
			})
			if err != nil {
				t.Fatalf("prune after dry run: %v", err)
			}
			if real.Deleted != report.Deleted || real.Protected != report.Protected {
				t.Errorf("the real run deleted=%d protected=%d after a dry run predicted %d and %d",
					real.Deleted, real.Protected, report.Deleted, report.Protected)
			}
		})
	}
}

// TestPruneRefusesANonUserActor: a prune is an operator's act.
//
// A machine credential that can prune the transition ledger can erase the
// record of what it reported, which is the one thing a compromised collector
// most wants. `system` is refused too: rule 10 requires the run to record its
// actor, and recording a placeholder satisfies the letter of that and none of
// the point.
func TestPruneRefusesANonUserActor(t *testing.T) {
	agent, err := domain.AgentActor("prom-a")
	if err != nil {
		t.Fatalf("building agent actor: %v", err)
	}

	cases := []struct {
		name  string
		actor domain.Actor
		opts  PruneOptions
	}{
		{"an agent may not prune its own history", agent, PruneOptions{}},
		{"a system actor is not an operator", domain.SystemActor, PruneOptions{}},
	}

	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newPruneFixture(t, e)
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					opts := tc.opts
					opts.Before = f.now.AddDate(0, 0, -30)
					_, err := f.store.PruneObservedTransitions(f.ctx, tc.actor, opts)
					if !errors.Is(err, domain.ErrInvalid) {
						t.Errorf("error = %v, want a validation error", err)
					}
				})
			}

			// And a cutoff in the future, which would otherwise let a run reach
			// rows that have not happened yet the moment the clock moves.
			_, err := f.store.PruneObservedTransitions(f.ctx, f.actor, PruneOptions{
				Before: f.now.Add(time.Hour),
			})
			if !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("a future cutoff was accepted: %v", err)
			}
			// A zero cutoff is a truncate wearing a prune's name.
			_, err = f.store.PruneObservedTransitions(f.ctx, f.actor, PruneOptions{})
			if !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("an empty cutoff was accepted: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Structural properties: what the code may not contain
// ---------------------------------------------------------------------------

// TestChangeLogIsAppendOnly is the named boundary test for rule 10's first
// sentence: no UPDATE change_log, no DELETE FROM change_log, anywhere, ever.
//
// Correcting a wrong entry means writing a new one. This is enforced by reading
// the source rather than by a runtime check because there is no runtime moment
// at which it could be caught: by the time a row is gone, the evidence that it
// existed is the thing that went.
//
// Go source is parsed rather than grepped, so a comment explaining the rule is
// not mistaken for a violation of it -- several files in this package
// necessarily name the statements they forbid. SQL files are read as text with
// their comments stripped, for the same reason. The forbidden phrases are built
// by concatenation so this file does not match itself and need no exemption;
// an exemption is the loophole every such test eventually acquires.
func TestChangeLogIsAppendOnly(t *testing.T) {
	// `DROP TABLE change_log` is deliberately absent: 00005's `-- +goose Down`
	// drops the table it created, which is a schema rollback rather than an
	// edit to the trail. Removing a row and unmaking the table are different
	// acts, and only the first is what rule 10 forbids. TRUNCATE has no such
	// reading and stays on the list.
	forbidden := []string{
		"update " + "change_log",
		"delete from " + "change_log",
		"truncate " + "change_log",
		"truncate table " + "change_log",
	}

	root := repoRoot(t)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}

		var haystacks []string
		switch filepath.Ext(path) {
		case ".go":
			haystacks = goStringLiterals(t, path)
		case ".sql":
			// Migrations are the other place a statement could hide, and goose
			// runs them with nobody reading.
			haystacks = []string{stripSQLComments(readFile(t, path))}
		default:
			return nil
		}

		rel, _ := filepath.Rel(root, path)
		for _, h := range haystacks {
			normalised := strings.ToLower(strings.Join(strings.Fields(h), " "))
			for _, bad := range forbidden {
				if strings.Contains(normalised, bad) {
					t.Errorf("%s contains %q. change_log is append-only: correct a wrong entry "+
						"by writing a new one. The rule 10 prune targets observed_transition only.",
						rel, bad)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}
}

// TestTheOnlyFactDeletingStatementIsThePrune keeps rule 10's closing claim
// true: "this prune is the ONLY DELETE FROM permitted in this codebase".
//
// The four pre-existing ones are set replacements inside the owning parent's
// transaction -- asset_closure, asset_environment, dependency_data_class and
// the SQLite search index -- which hold the current value of something the
// parent owns and are not deletion in the sense that matters. They are listed
// individually, with the table each one targets, so that adding a fifth is a
// deliberate edit to this list and a conversation, rather than a diff nobody
// reads.
func TestTheOnlyFactDeletingStatementIsThePrune(t *testing.T) {
	allowed := map[string]string{
		"internal/store/assets.go": "asset_closure and asset_environment: sets the asset owns, replaced wholesale",
		"internal/store/deps.go":   "dependency_data_class: a set the dependency owns, replaced wholesale",
		"internal/store/search.go": "search_index: a derived index, rebuilt not authored",
		"internal/store/prune.go":  "the rule 10 retention prune, the only statement here that removes a fact",
		// certificate_san holds the CURRENT set of names a certificate covers,
		// which the certificate owns. Replaced wholesale inside the parent's
		// transaction, and certificateAudit folds the names into the audited
		// value so the replacement cannot produce an empty diff -- the failure
		// this codebase made three times before with set tables.
		//
		// The DEPLOYMENTS are not deleted: certificate_asset and
		// certificate_service are soft-retired like every other link.
		"internal/store/certificates.go": "certificate_san: the set of names a certificate owns, replaced wholesale",
		// interface_vlan holds the CURRENT set of VLANs a port is in, which the
		// port owns. Replaced wholesale inside the interface's transaction, and
		// foldVLANValue puts the VIDs into the audited value so the replacement
		// cannot produce an empty diff -- a port moving from VLAN 10 to VLAN 20
		// with no entry in change_log would be the fourth time this codebase
		// made that exact mistake.
		//
		// The VLANs themselves are soft-retired like every other entity, and
		// RetireVLAN refuses while any prefix or port still names one.
		"internal/store/vlans.go": "interface_vlan: the set of VLANs a port is in, replaced wholesale",
	}

	root := repoRoot(t)
	needle := "delete " + "from "
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for _, lit := range goStringLiterals(t, path) {
			normalised := strings.ToLower(strings.Join(strings.Fields(lit), " "))
			if !strings.Contains(normalised, needle) {
				continue
			}
			if _, ok := allowed[rel]; !ok {
				t.Errorf("%s deletes rows. Soft delete is the rule for entities, and the only "+
					"permitted removal of a fact is the observed_transition prune in "+
					"internal/store/prune.go. If this is a set replacement, add it to the list "+
					"in this test and say which set it owns.", rel)
			}
			return nil
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}
}

// TestPruneIsNotReachableFromAHandler enforces "admin-invoked cmd/invctl
// subcommand, never handler code, never an automatic side effect of a write
// path".
//
// A retention job wired into a request path is one that runs at whatever rate
// requests arrive, attributed to whoever happened to be browsing.
func TestPruneIsNotReachableFromAHandler(t *testing.T) {
	method := "Prune" + "ObservedTransitions"
	root := repoRoot(t)

	for _, dir := range []string{"internal/web", "internal/seed", "internal/auth", "internal/impact"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || filepath.Ext(path) != ".go" {
				return nil
			}
			if strings.Contains(readFile(t, path), method) {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s references %s. Pruning is admin-invoked from cmd/invctl and is never "+
					"a side effect of serving a request.", filepath.ToSlash(rel), method)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", dir, err)
		}
	}
}

// TestPruneTakesNoTableName is rule 10's "its store method takes no table
// name", asserted rather than asserted-in-a-comment.
//
// A prune that can be pointed at a table is one that will eventually be pointed
// at change_log. There is no string anywhere in the call signature, so there is
// nothing to point.
func TestPruneTakesNoTableName(t *testing.T) {
	method, ok := reflect.TypeOf(&SQLStore{}).MethodByName("PruneObservedTransitions")
	if !ok {
		t.Fatal("PruneObservedTransitions is not a method on *SQLStore")
	}
	for i := 0; i < method.Type.NumIn(); i++ {
		if method.Type.In(i).Kind() == reflect.String {
			t.Errorf("parameter %d is a string; a prune must not be able to name its own table", i)
		}
	}
	opts := reflect.TypeOf(PruneOptions{})
	for i := 0; i < opts.NumField(); i++ {
		if opts.Field(i).Type.Kind() == reflect.String {
			t.Errorf("PruneOptions.%s is a string; the only dial is how far back to reach",
				opts.Field(i).Name)
		}
	}

	// And nothing in the statements selects rows by who wrote them.
	for name, stmt := range map[string]string{
		"delete":    pruneDelete(inScopeSubquery[domain.ObservableAsset]),
		"protected": pruneProtectedCount(inScopeSubquery[domain.ObservableAsset]),
		"deletable": pruneDeletableCount(inScopeSubquery[domain.ObservableAsset]),
	} {
		if strings.Contains(stmt, "actor_kind") {
			t.Errorf("the %s statement mentions actor_kind; that column says who wrote a row, "+
				"not what kind of fact it is", name)
		}
		if !strings.Contains(stmt, prunableTable) {
			t.Errorf("the %s statement does not name %s", name, prunableTable)
		}
	}
}

// ---------------------------------------------------------------------------
// Source-reading helpers
// ---------------------------------------------------------------------------

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolving the repository root: %v", err)
	}
	return root
}

func skipDir(name string) bool {
	switch name {
	case ".git", "bin", "node_modules", "vendor", "testdata":
		return true
	}
	return false
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

// goStringLiterals returns every string literal in a Go file.
//
// Literals only: a comment that names a forbidden statement in order to forbid
// it is not a violation, and a test that cannot tell the difference is one
// somebody eventually disables.
func goStringLiterals(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		// Raw string literals hold the SQL in this codebase; Unquote handles
		// both forms and simply fails on anything exotic, which cannot be SQL.
		if unquoted, err := strconv.Unquote(lit.Value); err == nil {
			out = append(out, unquoted)
		}
		return true
	})
	return out
}

// stripSQLComments removes comments so a migration explaining a rule is not
// read as breaking it.
//
// Both forms. `--` is what this codebase writes, but a rule explained in a
// `/* */` block is doing the same job, and a guard that flagged the explanation
// is a guard somebody switches off.
func stripSQLComments(sql string) string {
	sql = sqlBlockComment.ReplaceAllString(sql, " ")
	lines := strings.Split(sql, "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "--"); idx >= 0 {
			lines[i] = line[:idx]
		}
	}
	return strings.Join(lines, "\n")
}
