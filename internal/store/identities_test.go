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
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
)

// TestTheIdentityDateShapeCheckIsEnforcedByBothEngines is 00069's second
// statement, asserted rather than assumed. The Go constructor is the first line
// of defence and this is the second; a shape check that silently does nothing on
// one engine is the portability failure this suite exists for.
func TestTheIdentityDateShapeCheckIsEnforcedByBothEngines(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			identity, err := domain.NewIdentity(NewID(), domain.IdentitySpec{
				Kind: domain.IdentityServiceAccount, Name: "svc-shape",
			})
			if err != nil {
				t.Fatalf("building identity: %v", err)
			}
			if err := s.CreateIdentity(ctx, testPermit, identity); err != nil {
				t.Fatalf("creating identity: %v", err)
			}
			for _, bad := range []string{"2026-9-8", "08/09/2026", "2026-09-08T00:00:00Z", ""} {
				_, err := s.db.Writer.ExecContext(ctx,
					s.db.Writer.Rebind(`UPDATE identity SET last_rotated = ? WHERE id = ?`),
					bad, identity.ID)
				if err == nil {
					t.Errorf("the database accepted last_rotated = %q; the shape CHECK is not "+
						"enforcing on %s", bad, e.Name)
				}
			}
			if _, err := s.db.Writer.ExecContext(ctx,
				s.db.Writer.Rebind(`UPDATE identity SET last_rotated = ? WHERE id = ?`),
				"2026-09-08", identity.ID); err != nil {
				t.Errorf("the database refused a well-formed date: %v", err)
			}
		})
	}
}

// TestAnIdentityIsCreatedWithNoRecordedRotation pins the create half of the
// one-writer rule behaviourally, beside the AST scan that pins it structurally
// (last_rotated_source_test.go, Task 3). Declaring a credential and recording
// when it was last rotated are two acts.
func TestAnIdentityIsCreatedWithNoRecordedRotation(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			identity, err := domain.NewIdentity(NewID(), domain.IdentitySpec{
				Kind: domain.IdentityServiceAccount, Name: "svc-fresh",
			})
			if err != nil {
				t.Fatalf("building identity: %v", err)
			}
			identity.LastRotated = strPtr("2020-01-01") // deliberately set, deliberately ignored
			if err := s.CreateIdentity(ctx, testPermit, identity); err != nil {
				t.Fatalf("creating identity: %v", err)
			}
			var stored *string
			if err := s.readOne(ctx, &stored,
				`SELECT last_rotated FROM identity WHERE id = ?`, identity.ID); err != nil {
				t.Fatalf("reading it back: %v", err)
			}
			if stored != nil {
				t.Errorf("last_rotated = %q after a create; create must never write it", *stored)
			}
			var version int
			if err := s.readOne(ctx, &version,
				`SELECT row_version FROM identity WHERE id = ?`, identity.ID); err != nil {
				t.Fatalf("reading row_version: %v", err)
			}
			if version != 1 {
				t.Errorf("row_version = %d after a create, want 1", version)
			}
		})
	}
}

// TestBulkOwnershipBumpsIdentityRowVersion is the failure 00069 exists to
// prevent, from the other direction: a bulk assignment moving team_id under an
// open correction form whose token still validates, so the form's save silently
// reverts the assignment.
func TestBulkOwnershipBumpsIdentityRowVersion(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			team := f.team(t, "bump-team", "", strp("bump@example.com"))
			id := f.identity(t, "bump-identity", nil, "")

			before := f.rowVersion(t, id)
			if _, err := f.s.BulkAssignOwnership(f.ctx, testPermit, "identity",
				[]string{id}, team); err != nil {
				t.Fatalf("BulkAssignOwnership: %v", err)
			}
			if after := f.rowVersion(t, id); after != before+1 {
				t.Errorf("row_version = %d after a bulk assignment, want %d. An open "+
					"correction form's token still validates, so its save silently "+
					"reverts the assignment.", after, before+1)
			}

			// The guard is unchanged: a second assignment of a now-owned row is
			// skipped and reported, never 409'd, and does not bump again.
			mid := f.rowVersion(t, id)
			other := f.team(t, "bump-team-2", "", strp("bump2@example.com"))
			outcomes, err := f.s.BulkAssignOwnership(f.ctx, testPermit, "identity",
				[]string{id}, other)
			if err != nil {
				t.Fatalf("second BulkAssignOwnership: %v", err)
			}
			if len(outcomes) != 1 || outcomes[0].Result != AssignNoLongerUnowned {
				t.Errorf("outcome = %+v, want one %s -- the WHERE team_id IS NULL "+
					"guard is still the whole eligibility check", outcomes, AssignNoLongerUnowned)
			}
			if after := f.rowVersion(t, id); after != mid {
				t.Errorf("row_version moved on a skipped assignment: %d -> %d", mid, after)
			}
		})
	}
}

// TestReassignTeamOwnershipBumpsIdentityRowVersion is the same property for the
// team-retirement path, which uses WHERE team_id = ? rather than IS NULL.
func TestReassignTeamOwnershipBumpsIdentityRowVersion(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			from := f.team(t, "from-team", "", strp("from@example.com"))
			to := f.team(t, "to-team", "", strp("to@example.com"))
			id := f.identity(t, "reassigned-identity", &from, "")

			before := f.rowVersion(t, id)
			outcomes, err := f.s.ReassignTeamOwnership(f.ctx, testPermit, from, to)
			if err != nil {
				t.Fatalf("ReassignTeamOwnership: %v", err)
			}
			if len(outcomes) == 0 {
				t.Fatal("no outcomes at all; the reassignment found nothing to move and " +
					"the assertion below would pass on an untouched row")
			}
			if after := f.rowVersion(t, id); after != before+1 {
				t.Errorf("row_version = %d after a team reassignment, want %d. The guard "+
					"here is WHERE team_id = fromTeamID and stays that way -- the bump is "+
					"additive, so an open correction form's token stops validating once "+
					"the row moved underneath it.", after, before+1)
			}
			// The guard is untouched: reassigning again from the OLD team finds
			// nothing and reports it, rather than 409'ing or bumping.
			mid := f.rowVersion(t, id)
			again, err := f.s.ReassignTeamOwnership(f.ctx, testPermit, from, to)
			if err != nil {
				t.Fatalf("second ReassignTeamOwnership: %v", err)
			}
			for _, o := range again {
				if o.EntityType == "identity" && o.Result != ReassignStale {
					t.Errorf("outcome = %+v, want ReassignStale -- the WHERE team_id = ? "+
						"guard is still the whole eligibility check", o)
				}
			}
			if after := f.rowVersion(t, id); after != mid {
				t.Errorf("row_version moved %d -> %d on a reassignment that matched nothing",
					mid, after)
			}
		})
	}
}

// identityFixture is an estate small enough to reason about and large enough to
// hold an edge: one environment, one host, a consumer service, a provider
// service with an endpoint, and whatever identities a test declares.
type identityFixture struct {
	s          *SQLStore
	ctx        context.Context
	consumerID string
	endpointID string
}

func newIdentityFixture(t *testing.T, e Engine) *identityFixture {
	t.Helper()
	s, ctx := newStore(t, e)
	envID := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
	mustAsset(t, s, ctx, domain.KindServer, "app-01", nil, envID)

	mkService := func(code string) string {
		svc, err := domain.NewService(NewID(), domain.ServiceSpec{
			Code: code, Name: code, Kind: domain.SvcAPI,
			EnvironmentID: envID, Availability: domain.AvailStandalone, Tier: 2,
		}, s.Now())
		if err != nil {
			t.Fatalf("building service %s: %v", code, err)
		}
		if err := s.CreateService(ctx, testPermit, svc); err != nil {
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
	if err := s.CreateEndpoint(ctx, testPermit, ep); err != nil {
		t.Fatalf("creating endpoint: %v", err)
	}
	return &identityFixture{s: s, ctx: ctx, consumerID: consumer, endpointID: ep.ID}
}

// identity declares a credential. rotationDays of 0 means no policy at all.
func (f *identityFixture) identity(t *testing.T, name string, rotationDays int) *domain.Identity {
	t.Helper()
	spec := domain.IdentitySpec{
		Kind: domain.IdentityServiceAccount, Name: name, Realm: strPtr("vault"),
		SecretRef: strPtr("kv/prod/" + name),
	}
	if rotationDays > 0 {
		spec.RotationDays = &rotationDays
	}
	i, err := domain.NewIdentity(NewID(), spec)
	if err != nil {
		t.Fatalf("building identity %s: %v", name, err)
	}
	if err := f.s.CreateIdentity(f.ctx, testPermit, i); err != nil {
		t.Fatalf("creating identity %s: %v", name, err)
	}
	return i
}

// dependency declares an edge from the consumer to the provider endpoint,
// authenticating as identityID.
func (f *identityFixture) dependency(t *testing.T, identityID string) *domain.Dependency {
	t.Helper()
	endpoint, identity := f.endpointID, identityID
	d, err := domain.NewDependency(NewID(), domain.DependencySpec{
		ConsumerServiceID:  f.consumerID,
		ProviderEndpointID: &endpoint,
		Nature:             domain.NatureHard,
		FailureMode:        "orders cannot be written",
		IdentityID:         &identity,
		AuthMethod:         strPtr("scram-sha-256"),
	}, f.s.Now())
	if err != nil {
		t.Fatalf("building dependency: %v", err)
	}
	if err := f.s.CreateDependency(f.ctx, testPermit, d, nil); err != nil {
		t.Fatalf("creating dependency: %v", err)
	}
	return d
}

// rowVersionOf and lastRotatedOf read the two columns these tests assert on,
// straight from the row rather than through GetIdentity -- a bug in the read
// path must not be able to make a write assertion pass.
func (f *identityFixture) rowVersionOf(t *testing.T, id string) int {
	t.Helper()
	var v int
	if err := f.s.readOne(f.ctx, &v, `SELECT row_version FROM identity WHERE id = ?`, id); err != nil {
		t.Fatalf("reading row_version for %s: %v", id, err)
	}
	return v
}

func (f *identityFixture) lastRotatedOf(t *testing.T, id string) *string {
	t.Helper()
	var v *string
	if err := f.s.readOne(f.ctx, &v, `SELECT last_rotated FROM identity WHERE id = ?`, id); err != nil {
		t.Fatalf("reading last_rotated for %s: %v", id, err)
	}
	return v
}

func (f *identityFixture) nameOf(t *testing.T, id string) string {
	t.Helper()
	var v string
	if err := f.s.readOne(f.ctx, &v, `SELECT name FROM identity WHERE id = ?`, id); err != nil {
		t.Fatalf("reading name for %s: %v", id, err)
	}
	return v
}

func (f *identityFixture) auditCount(t *testing.T, id string) int {
	t.Helper()
	var n int
	if err := f.s.readOne(f.ctx, &n,
		`SELECT COUNT(*) FROM change_log WHERE entity_type = ? AND entity_id = ?`,
		"identity", id); err != nil {
		t.Fatalf("counting audit entries for %s: %v", id, err)
	}
	return n
}

// TestRecordIdentityRotationWritesExactlyOneChangeLogRow is the audit shape the
// spec specifies literally: a one-field diff carrying WHEN the rotation happened
// (new), WHEN it was recorded (change_log.at) and WHO recorded it
// (change_log.actor). action stays 'update' -- VerifyDependency is the precedent
// in every respect, and adding a value to the change_log CHECK would mean
// rebuilding the table on SQLite (docs/AUDIT.md rule 10).
func TestRecordIdentityRotationWritesExactlyOneChangeLogRow(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newIdentityFixture(t, e)
			i := f.identity(t, "svc-audited", 90)
			before := f.auditCount(t, i.ID)

			const rotated = "2026-09-08"
			if err := f.s.RecordIdentityRotation(f.ctx, testPermit, i.ID, rotated); err != nil {
				t.Fatalf("recording the rotation: %v", err)
			}

			if got := f.auditCount(t, i.ID); got != before+1 {
				t.Fatalf("change_log went %d -> %d, want exactly one new entry. Every "+
					"declared mutation writes one row in the same transaction; two would "+
					"mean the write is happening twice.", before, got)
			}
			changes, err := f.s.ListChangesForEntity(f.ctx, "identity", i.ID, 50)
			if err != nil {
				t.Fatalf("reading the audit trail: %v", err)
			}
			newest := changes[0]
			if newest.Action != domain.ActionUpdate {
				t.Errorf("action = %q, want %q. The change_log CHECK allows exactly "+
					"create|update|delete|retire, and adding a value to it would mean "+
					"rebuilding the table on SQLite (docs/AUDIT.md rule 10). "+
					"VerifyDependency is the precedent: a distinct method and route that "+
					"stamp one column and log as an ordinary update.",
					newest.Action, domain.ActionUpdate)
			}
			// The one-field diff the spec specifies literally. It carries WHEN
			// the rotation happened (new), and change_log supplies when it was
			// recorded (at) and who recorded it (actor).
			const want = `{"last_rotated":{"old":null,"new":"2026-09-08"}}`
			if newest.Diff != want {
				t.Errorf("diff = %s, want %s. A wider diff means the rotation path is "+
					"writing columns it should not.", newest.Diff, want)
			}
			if newest.Actor == "" {
				t.Error("the entry has no actor, so the audit trail cannot say who " +
					"recorded the rotation -- which is half of what it is for")
			}
			if got := f.rowVersionOf(t, i.ID); got != 2 {
				t.Errorf("row_version = %d after one rotation, want 2. The rotation action "+
					"carries no token and BUMPS one, following VerifyDependency: a token "+
					"that does not move when the row changes is worse than a spurious 409.",
					got)
			}
			if got := f.lastRotatedOf(t, i.ID); got == nil || *got != rotated {
				t.Errorf("last_rotated = %v, want %q", derefOr(got, "NULL"), rotated)
			}
		})
	}
}

// TestRecordingTheStoredRotationDateWritesNothingAtAll is the rule the spec
// spells out and the reason is an AUTHORIZATION one, not a tidiness one:
// without the short-circuit, diffJSON returns ok=false, logUpdate skips the
// entry, and the UPDATE still moves row_version -- a declared-state write with
// no audit row, which since WP-G1 is an authorization bypass rather than merely
// an untraceable change. RetireEnvironment's "already retired returns nil" is
// the same shape, for the same reason: never claim a thing that did not happen.
func TestRecordingTheStoredRotationDateWritesNothingAtAll(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newIdentityFixture(t, e)
			i := f.identity(t, "svc-idempotent", 90)

			const rotated = "2026-09-08"
			if err := f.s.RecordIdentityRotation(f.ctx, testPermit, i.ID, rotated); err != nil {
				t.Fatalf("recording the first rotation: %v", err)
			}
			auditAfterFirst := f.auditCount(t, i.ID)
			versionAfterFirst := f.rowVersionOf(t, i.ID)

			// The same date again -- two operators recording the same rotation,
			// or one double-submitting a form.
			if err := f.s.RecordIdentityRotation(f.ctx, testPermit, i.ID, rotated); err != nil {
				t.Fatalf("re-recording the stored date must return nil, like "+
					"RetireEnvironment does for an already-retired row: %v", err)
			}

			if got := f.auditCount(t, i.ID); got != auditAfterFirst {
				t.Errorf("change_log grew %d -> %d on a no-op rotation. An audit trail "+
					"full of entries for things that did not happen is worse than one "+
					"without them.", auditAfterFirst, got)
			}
			if got := f.rowVersionOf(t, i.ID); got != versionAfterFirst {
				t.Errorf("row_version moved %d -> %d WITH NO change_log ROW. That is a "+
					"declared-state write with no audit entry, which since WP-G1 is an "+
					"authorization bypass and not merely an untraceable change -- and it "+
					"would also invalidate every open edit form for nothing.",
					versionAfterFirst, got)
			}
		})
	}
}

// TestARotationMayBeBackdatedAndNeverPostDated. The asymmetry is the whole
// argument: a past date can only ever make a finding worse and can never hide
// anything, while a future date hides an overdue finding for a rotation that has
// not happened -- the one direction that turns this feature into a way of
// silencing itself.
func TestARotationMayBeBackdatedAndNeverPostDated(t *testing.T) {
	// A FIXED CLOCK, so "tomorrow" is a constant rather than something computed
	// from the wall clock at both ends -- a test that derives its input and its
	// expectation from the same moving source can pass while the boundary is
	// wrong by a day.
	fixed := time.Date(2026, 9, 15, 11, 30, 0, 0, time.UTC)

	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			for _, tc := range []struct {
				name, date string
				wantErr    bool
			}{
				{"today is fine", "2026-09-15", false},
				{"yesterday is fine -- somebody catching up on Thursday", "2026-09-14", false},
				{"long ago is fine", "2020-01-01", false},
				{"tomorrow is refused", "2026-09-16", true},
				{"next year is refused", "2027-01-01", true},
				{"a malformed date is refused before it reaches the CHECK", "2026-9-8", true},
				{"a timestamp is refused", "2026-09-15T00:00:00Z", true},
				{"an empty date is refused", "", true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					f := newIdentityFixture(t, e)
					f.s = f.s.WithClock(func() time.Time { return fixed })
					i := f.identity(t, "svc-dated", 90)

					err := f.s.RecordIdentityRotation(f.ctx, testPermit, i.ID, tc.date)

					if !tc.wantErr {
						if err != nil {
							t.Fatalf("RecordIdentityRotation(%q): %v. Refusing a backdated "+
								"rotation forces an operator to record a date they know is "+
								"wrong, which is worse than the thing the refusal was "+
								"protecting.", tc.date, err)
						}
						got := f.lastRotatedOf(t, i.ID)
						if got == nil || *got != tc.date {
							t.Errorf("last_rotated = %v, want %q",
								derefOr(got, "NULL"), tc.date)
						}
						return
					}

					// Fail loudly, then assert unconditionally.
					if err == nil {
						t.Fatalf("RecordIdentityRotation(%q) was accepted. A future date "+
							"hides an overdue finding for a rotation that has not "+
							"happened, which is the one direction that turns this "+
							"feature into a way of silencing itself.", tc.date)
					}
					ve, ok := domain.AsValidation(err)
					if !ok {
						t.Fatalf("error = %v (%T), want a *domain.ValidationError so the "+
							"handler returns 422 with the form re-rendered", err, err)
					}
					if _, named := ve.Messages()["last_rotated"]; !named {
						t.Errorf("the refusal names %v, not last_rotated -- the message has "+
							"to land on the field the operator was filling in",
							ve.Messages())
					}
					if got := f.lastRotatedOf(t, i.ID); got != nil {
						t.Errorf("last_rotated = %q after a refused rotation, want NULL", *got)
					}
				})
			}

			// NO MONOTONICITY RULE: a date EARLIER than the stored one is
			// allowed, because the stored one may simply have been wrong.
			// change_log records both values and the actor; the audit trail is
			// the control here, not a constraint. (Compare inflation_rate,
			// "corrected in place rather than superseded... a revised index for
			// 2024 was always one figure somebody had wrong.")
			t.Run("a correction may move the date backwards", func(t *testing.T) {
				f := newIdentityFixture(t, e)
				f.s = f.s.WithClock(func() time.Time { return fixed })
				i := f.identity(t, "svc-corrected", 90)

				if err := f.s.RecordIdentityRotation(f.ctx, testPermit, i.ID, "2026-09-10"); err != nil {
					t.Fatalf("recording the first date: %v", err)
				}
				if err := f.s.RecordIdentityRotation(f.ctx, testPermit, i.ID, "2026-09-01"); err != nil {
					t.Fatalf("an EARLIER correction was refused: %v. The stored date may "+
						"simply have been wrong, and a monotonicity rule would leave the "+
						"operator no way to fix it.", err)
				}
				got := f.lastRotatedOf(t, i.ID)
				if got == nil || *got != "2026-09-01" {
					t.Errorf("last_rotated = %v, want the corrected earlier date",
						derefOr(got, "NULL"))
				}
				// Both values survive in the audit trail, which is what makes
				// the permissiveness safe.
				changes, err := f.s.ListChangesForEntity(f.ctx, "identity", i.ID, 50)
				if err != nil {
					t.Fatalf("reading the audit trail: %v", err)
				}
				if len(changes) < 2 {
					t.Fatalf("got %d audit entries, want one per recorded rotation -- the "+
						"log IS the rotation history", len(changes))
				}
				if !strings.Contains(changes[0].Diff, "2026-09-10") {
					t.Errorf("the correction's diff is %s and does not carry the value it "+
						"replaced, so nobody can see what was corrected", changes[0].Diff)
				}
			})
		})
	}
}

// TestRecordIdentityRotationRefusesARetiredIdentity. Rotating a withdrawn
// credential is not a thing that happened. The message names the identity, the
// shape SetBundleMembers uses for a retired bundle.
func TestRecordIdentityRotationRefusesARetiredIdentity(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newIdentityFixture(t, e)
			i := f.identity(t, "svc-withdrawn", 90)
			if err := f.s.RetireIdentity(f.ctx, testPermit, i.ID); err != nil {
				t.Fatalf("retiring identity: %v", err)
			}
			auditBefore := f.auditCount(t, i.ID)
			versionBefore := f.rowVersionOf(t, i.ID)

			err := f.s.RecordIdentityRotation(f.ctx, testPermit, i.ID,
				domain.FormatDate(f.s.Now()))

			// Fail loudly first, then assert unconditionally. An assertion
			// nested in an `if err == nil` would pass on every error, which is
			// the shape this whole file exists to avoid.
			if err == nil {
				t.Fatal("a rotation was recorded against a withdrawn credential. " +
					"Rotating a credential nobody may use any more is not a thing that " +
					"happened, and recording it would put a false fact in change_log " +
					"permanently.")
			}
			ve, ok := domain.AsValidation(err)
			if !ok {
				t.Fatalf("error = %v (%T), want a *domain.ValidationError so the handler "+
					"can return 422 with the form re-rendered rather than a 500", err, err)
			}
			msg, named := ve.Messages()["last_rotated"]
			if !named {
				t.Fatalf("the refusal names fields %v, not last_rotated -- the message has "+
					"to land on the field the operator was filling in", ve.Messages())
			}
			if !strings.Contains(msg, i.Name) {
				t.Errorf("the message is %q and does not name %q. SetBundleMembers names "+
					"the retired bundle for the same reason: an operator with several tabs "+
					"open needs to know WHICH one was withdrawn.", msg, i.Name)
			}

			// And nothing was written. A refusal that still moved the row would
			// be worse than no refusal, because the page would look unchanged.
			if got := f.lastRotatedOf(t, i.ID); got != nil {
				t.Errorf("last_rotated = %q after a refused rotation, want NULL", *got)
			}
			if got := f.rowVersionOf(t, i.ID); got != versionBefore {
				t.Errorf("row_version moved %d -> %d on a refused rotation", versionBefore, got)
			}
			if got := f.auditCount(t, i.ID); got != auditBefore {
				t.Errorf("change_log grew %d -> %d on a refused rotation", auditBefore, got)
			}
		})
	}
}

// TestRetireIdentityRefusesNothingAndRewritesNothing is migration 00003's case
// carried forward: "the natural response to a compromised credential is to
// retire it and create its replacement under the same name". Blocking retirement
// until every dependency has been re-pointed would mean, at exactly the wrong
// moment, that the compromised credential cannot be withdrawn.
func TestRetireIdentityRefusesNothingAndRewritesNothing(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newIdentityFixture(t, e)
			// identity named by a live dependency and by an rt_windows logon
			// account. The Windows half is not built here -- newIdentityFixture
			// carries no service_instance/rt_windows fixture, and building one
			// by hand would be a second, weaker copy of the real one the seeder
			// already carries (see TestIdentityUsageNamesWhatWouldNotice's own
			// comment on this).
			i := f.identity(t, "svc-compromised", 90)
			live := f.dependency(t, i.ID)
			depVersionBefore := live.RowVersion

			if err := f.s.RetireIdentity(f.ctx, testPermit, i.ID); err != nil {
				t.Fatalf("retirement was refused while a live dependency named it: %v", err)
			}

			// The dependency still names it, and its row_version did NOT move:
			// nothing was rewritten, so nothing was misattributed to whoever
			// clicked withdraw.
			var storedIdentityID *string
			if err := f.s.readOne(f.ctx, &storedIdentityID,
				`SELECT identity_id FROM dependency WHERE id = ?`, live.ID); err != nil {
				t.Fatalf("reading the dependency's identity_id: %v", err)
			}
			if storedIdentityID == nil || *storedIdentityID != i.ID {
				t.Errorf("the dependency no longer names %s after retirement; "+
					"RetireIdentity must rewrite nothing", i.ID)
			}
			var depVersionAfter int
			if err := f.s.readOne(f.ctx, &depVersionAfter,
				`SELECT row_version FROM dependency WHERE id = ?`, live.ID); err != nil {
				t.Fatalf("reading the dependency's row_version: %v", err)
			}
			if depVersionAfter != depVersionBefore {
				t.Errorf("dependency row_version moved %d -> %d on an identity retirement; "+
					"that would misattribute a dependency change to whoever clicked withdraw",
					depVersionBefore, depVersionAfter)
			}

			// And the (realm, name) pair is immediately reusable -- the
			// live-scoped unique index is what makes a restore path
			// unnecessary.
			replacement, err := domain.NewIdentity(NewID(), domain.IdentitySpec{
				Kind: domain.IdentityServiceAccount, Name: i.Name, Realm: i.Realm,
				SecretRef: strPtr("kv/prod/" + i.Name + "-v2"),
			})
			if err != nil {
				t.Fatalf("building the replacement: %v", err)
			}
			if err := f.s.CreateIdentity(f.ctx, testPermit, replacement); err != nil {
				t.Errorf("the withdrawn name is not reusable, so an operator is stranded: %v", err)
			}

			// A second retire returns nil and writes nothing further.
			auditBefore := f.auditCount(t, i.ID)
			if err := f.s.RetireIdentity(f.ctx, testPermit, i.ID); err != nil {
				t.Fatalf("retiring an already-retired identity must return nil: %v", err)
			}
			if got := f.auditCount(t, i.ID); got != auditBefore {
				t.Errorf("change_log grew %d -> %d on a second retire of the same identity",
					auditBefore, got)
			}
		})
	}
}

// TestIdentityUsageNamesWhatWouldNotice is what makes withdrawal an informed act
// rather than a blind one -- the shape TeamOwnershipCounts feeds the
// team-retirement screen.
func TestIdentityUsageNamesWhatWouldNotice(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newIdentityFixture(t, e)
			named := f.identity(t, "svc-named", 90)
			unused := f.identity(t, "svc-unused", 90)

			live := f.dependency(t, named.ID)
			withdrawn := f.dependency(t, named.ID)
			if err := f.s.RetireDependency(f.ctx, testPermit, withdrawn.ID); err != nil {
				t.Fatalf("retiring the second dependency: %v", err)
			}

			usage, err := f.s.IdentityUsage(f.ctx, named.ID)
			if err != nil {
				t.Fatalf("IdentityUsage: %v", err)
			}
			if len(usage.Dependencies) != 1 {
				t.Fatalf("IdentityUsage returned %d dependencies, want 1. A RETIRED edge is "+
					"history; the question the withdrawal screen asks is what is still "+
					"running, and counting history would overstate the blast radius.",
					len(usage.Dependencies))
			}
			got := usage.Dependencies[0]
			if got.DependencyID != live.ID {
				t.Errorf("the surviving dependency is %s, want the live one %s",
					got.DependencyID, live.ID)
			}
			// The panel has to say something an operator can act on, not an id.
			if got.ConsumerCode != "orders" {
				t.Errorf("consumer code = %q, want %q -- the panel names WHO would notice",
					got.ConsumerCode, "orders")
			}
			if got.ProviderName != "sql" {
				t.Errorf("provider name = %q, want %q", got.ProviderName, "sql")
			}
			if got.AuthMethod != "scram-sha-256" {
				t.Errorf("auth method = %q, want %q", got.AuthMethod, "scram-sha-256")
			}

			// A credential nothing names must report nothing, or the panel says
			// "this is in use" about every credential in the estate.
			empty, err := f.s.IdentityUsage(f.ctx, unused.ID)
			if err != nil {
				t.Fatalf("IdentityUsage for an unused credential: %v", err)
			}
			if len(empty.Dependencies) != 0 || len(empty.Windows) != 0 {
				t.Errorf("an unused credential reports %d dependencies and %d windows "+
					"services, want none of either",
					len(empty.Dependencies), len(empty.Windows))
			}

			// THE rt_windows HALF IS COVERED IN internal/web, NOT HERE, and the
			// reason is that the seeded estate already has the real thing:
			// seed_services.go:260 makes svc-backup$ the logon account of a
			// Windows service, because "the run-as account is the usual reason a
			// Windows service dies after a credential rotation". Rebuilding a
			// service_instance and an rt_windows row by hand here to assert the
			// same join would be a second, weaker fixture of something the
			// fixture suite already carries -- see
			// TestTheIdentityDetailPageNamesWhatWouldNotice in Task 4.
		})
	}
}

// TestListIdentitiesFilters covers each filter and, specifically, that a retired
// identity is findable: "what is stored keeps displaying".
func TestListIdentitiesFilters(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newIdentityFixture(t, e)

			// Five rows spanning every axis the page filters on.
			orders := f.identity(t, "svc-orders", 90)     // within window, once rotated
			sso := f.identity(t, "svc-sso", 90)           // overdue
			backup := f.identity(t, "svc-backup", 90)     // never recorded
			metrics := f.identity(t, "metrics-scrape", 0) // unmanaged
			legacy := f.identity(t, "svc-legacy", 90)     // retired

			now := f.s.Now()
			if err := f.s.RecordIdentityRotation(f.ctx, testPermit, orders.ID,
				domain.FormatDate(now.AddDate(0, 0, -30))); err != nil {
				t.Fatalf("rotating svc-orders: %v", err)
			}
			if err := f.s.RecordIdentityRotation(f.ctx, testPermit, sso.ID,
				domain.FormatDate(now.AddDate(0, 0, -200))); err != nil {
				t.Fatalf("rotating svc-sso: %v", err)
			}
			// metrics-scrape is an api_token so the kind filter has something to
			// separate; correcting it here rather than widening the fixture.
			mk := *metrics
			mk.Kind = domain.IdentityAPIToken
			if err := f.s.UpdateIdentity(f.ctx, testPermit, &mk); err != nil {
				t.Fatalf("setting the kind on metrics-scrape: %v", err)
			}
			if err := f.s.RetireIdentity(f.ctx, testPermit, legacy.ID); err != nil {
				t.Fatalf("retiring svc-legacy: %v", err)
			}

			names := func(rows []IdentityRow) []string {
				out := make([]string, 0, len(rows))
				for _, r := range rows {
					out = append(out, r.Name)
				}
				sort.Strings(out)
				return out
			}

			for _, tc := range []struct {
				name   string
				filter IdentityFilter
				want   []string
			}{
				{
					// The default omits the retired one, because the default is
					// what a picker and a list both start from.
					"the default is live rows only",
					IdentityFilter{},
					[]string{"metrics-scrape", "svc-backup", "svc-orders", "svc-sso"},
				},
				{
					// "so a retired credential can be found" -- what is stored
					// keeps displaying; it just stops being newly selectable.
					"IncludeRetired finds the withdrawn one",
					IdentityFilter{IncludeRetired: true},
					[]string{"metrics-scrape", "svc-backup", "svc-legacy", "svc-orders", "svc-sso"},
				},
				{"kind", IdentityFilter{Kind: domain.IdentityAPIToken}, []string{"metrics-scrape"}},
				{"name substring", IdentityFilter{Query: "orders"}, []string{"svc-orders"}},
				{"realm substring", IdentityFilter{Query: "vault"},
					[]string{"metrics-scrape", "svc-backup", "svc-orders", "svc-sso"}},
				{"query is case-insensitive", IdentityFilter{Query: "ORDERS"}, []string{"svc-orders"}},
				{"rotation: overdue", IdentityFilter{Rotation: domain.RotationOverdue},
					[]string{"svc-sso"}},
				{"rotation: never recorded", IdentityFilter{Rotation: domain.RotationNeverRecorded},
					[]string{"svc-backup"}},
				{"rotation: within window", IdentityFilter{Rotation: domain.RotationWithinWindow},
					[]string{"svc-orders"}},
				{"rotation: unmanaged", IdentityFilter{Rotation: domain.RotationUnmanaged},
					[]string{"metrics-scrape"}},
				{
					// Two filters compose rather than one winning. The findings
					// page links in with a rotation state and the operator then
					// narrows by name, which is this case.
					"rotation and query compose",
					IdentityFilter{Rotation: domain.RotationNeverRecorded, Query: "backup"},
					[]string{"svc-backup"},
				},
				{"a filter matching nothing returns nothing, not everything",
					IdentityFilter{Query: "no-such-credential"}, []string{}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					rows, err := f.s.ListIdentities(f.ctx, tc.filter)
					if err != nil {
						t.Fatalf("ListIdentities(%+v): %v", tc.filter, err)
					}
					if got := names(rows); !slices.Equal(got, tc.want) {
						t.Errorf("ListIdentities(%+v) = %v, want %v", tc.filter, got, tc.want)
					}
				})
			}

			// The team filter needs a team, which the fixture has none of --
			// built here so the assertion is about the filter rather than about
			// the fixture's shape.
			t.Run("team", func(t *testing.T) {
				team, err := domain.NewTeam(NewID(), domain.TeamSpec{
					Code: "platform", Name: "Platform",
					ContactRef: strPtr("platform@example.com"),
				}, f.s.Now())
				if err != nil {
					t.Fatalf("building team: %v", err)
				}
				if err := f.s.CreateTeam(f.ctx, testPermit, team); err != nil {
					t.Fatalf("creating team: %v", err)
				}
				owned := *backup
				owned.TeamID = &team.ID
				if err := f.s.UpdateIdentity(f.ctx, testPermit, &owned); err != nil {
					t.Fatalf("assigning the team: %v", err)
				}
				rows, err := f.s.ListIdentities(f.ctx, IdentityFilter{TeamID: team.ID})
				if err != nil {
					t.Fatalf("ListIdentities by team: %v", err)
				}
				if got := names(rows); !slices.Equal(got, []string{"svc-backup"}) {
					t.Errorf("ListIdentities by team = %v, want [svc-backup]", got)
				}
			})
		})
	}
}

// TestUpdateIdentityRefusesAStaleToken.
func TestUpdateIdentityRefusesAStaleToken(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newIdentityFixture(t, e)
			i := f.identity(t, "svc-contended", 90)

			// TWO READS OF THE SAME ROW, which is what two operators with the
			// form open at the same time are holding.
			first, err := f.s.GetIdentity(f.ctx, i.ID)
			if err != nil {
				t.Fatalf("first read: %v", err)
			}
			second, err := f.s.GetIdentity(f.ctx, i.ID)
			if err != nil {
				t.Fatalf("second read: %v", err)
			}

			a := first.Identity
			a.Name = "svc-contended-first"
			if err := f.s.UpdateIdentity(f.ctx, testPermit, &a); err != nil {
				t.Fatalf("the first write must succeed, or the second is not stale and "+
					"this test proves nothing: %v", err)
			}

			b := second.Identity
			b.Name = "svc-contended-second"
			err = f.s.UpdateIdentity(f.ctx, testPermit, &b)

			if err == nil {
				t.Fatal("the second write succeeded against a token from before the first. " +
					"That is the silent revert row_version exists to prevent, and " +
					"change_log would record it as a deliberate act by whoever was slower.")
			}
			if !errors.Is(err, domain.ErrStale) {
				t.Fatalf("error = %v, want domain.ErrStale. The handler maps ErrStale to "+
					"409 and everything else to 422 or 500 -- a plain ErrConflict here "+
					"would tell the operator to choose a different name, which is not "+
					"the problem.", err)
			}
			// Never a 404: the row is there, it simply moved.
			if errors.Is(err, domain.ErrNotFound) {
				t.Error("a stale write reported ErrNotFound, which would tell the operator " +
					"their credential had vanished")
			}

			stored := f.nameOf(t, i.ID)
			if stored != "svc-contended-first" {
				t.Errorf("stored name = %q, want the first writer's", stored)
			}

			// And the caller's own token advanced on the write that DID land, so
			// a second save from the same struct is not a conflict against
			// nobody (requireVersion's doc comment).
			if a.RowVersion != first.RowVersion+1 {
				t.Errorf("the successful writer's RowVersion is %d, want %d -- a caller "+
					"updating the same struct twice would otherwise compare a stale token "+
					"against a row it moved itself", a.RowVersion, first.RowVersion+1)
			}
		})
	}
}

// TestUpdateIdentityNeverWritesLifecycleOrLastRotated. UpdateEnvironment pins
// lifecycle for exactly this reason: a correction path that can also withdraw is
// a second withdrawal path with none of RetireIdentity's audit shape.
func TestUpdateIdentityNeverWritesLifecycleOrLastRotated(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newIdentityFixture(t, e)
			i := f.identity(t, "svc-pinned", 90)

			// A REAL rotation first, so last_rotated is non-NULL and an attempt
			// to overwrite it has something to overwrite. Pinning a NULL against
			// a NULL proves nothing.
			const rotated = "2026-06-01"
			if err := f.s.RecordIdentityRotation(f.ctx, testPermit, i.ID, rotated); err != nil {
				t.Fatalf("recording the first rotation: %v", err)
			}
			stored, err := f.s.GetIdentity(f.ctx, i.ID)
			if err != nil {
				t.Fatalf("reading it back: %v", err)
			}

			// NOW ATTEMPT THE FORBIDDEN THING. A test that simply never submits
			// these fields passes whether or not the pinning exists, which makes
			// it decoration. This submission carries a changed lifecycle, a
			// changed last_rotated AND a changed name -- the name so that the
			// update genuinely happens, because a write that was rejected
			// wholesale would leave both pinned fields unmoved for the wrong
			// reason and the test would pass on a no-op.
			attempt := stored.Identity
			attempt.Name = "svc-pinned-renamed"
			attempt.Lifecycle = domain.LifecycleRetired
			attempt.LastRotated = strPtr("2020-01-01")

			if err := f.s.UpdateIdentity(f.ctx, testPermit, &attempt); err != nil {
				t.Fatalf("UpdateIdentity: %v", err)
			}

			after, err := f.s.GetIdentity(f.ctx, i.ID)
			if err != nil {
				t.Fatalf("reading after the update: %v", err)
			}

			// The legitimate change landed, so the update was not a no-op.
			if after.Name != "svc-pinned-renamed" {
				t.Fatalf("name = %q, want the corrected one -- the update did not happen at "+
					"all, so the two assertions below would pass for the wrong reason",
					after.Name)
			}
			// lifecycle is pinned from the stored row. UpdateEnvironment and
			// UpdateInterface pin theirs for the identical reason: this method
			// would otherwise be a SECOND WITHDRAWAL PATH with none of
			// RetireIdentity's audit shape -- it would log a plain field diff
			// rather than a withdrawal, and the change_log entry an auditor
			// looks for would not be there.
			if after.Lifecycle != domain.LifecycleActive {
				t.Errorf("lifecycle = %q after a correction submitting 'retired'. A "+
					"correction form must not be able to withdraw a credential: that is "+
					"RetireIdentity's job and it writes a different audit entry.",
					after.Lifecycle)
			}
			// last_rotated is pinned because it has exactly one writer and this
			// is not it. A correction form that could also stamp the date would
			// let somebody silence an overdue finding through the edit screen,
			// with the change buried in a multi-field diff.
			if after.LastRotated == nil || *after.LastRotated != rotated {
				t.Errorf("last_rotated = %v after a correction submitting 2020-01-01, want %q. "+
					"RecordIdentityRotation is the only writer (see "+
					"internal/store/last_rotated_source_test.go).",
					derefOr(after.LastRotated, "NULL"), rotated)
			}

			// And the audit entry for the correction mentions neither pinned
			// column, so a reader of change_log is not told about a change that
			// did not happen.
			changes, err := f.s.ListChangesForEntity(f.ctx, "identity", i.ID, 10)
			if err != nil {
				t.Fatalf("reading the audit trail: %v", err)
			}
			if len(changes) == 0 {
				t.Fatal("no audit entries at all; this assertion is checking nothing")
			}
			newest := changes[0].Diff
			if strings.Contains(newest, "lifecycle") || strings.Contains(newest, "last_rotated") {
				t.Errorf("the correction's diff is %s and names a pinned column. The diff "+
					"must describe what actually changed, or the audit trail reports a "+
					"withdrawal or a rotation that never happened.", newest)
			}
		})
	}
}

// TestAnIdentityIsFindable is the behavioural half of the search package.
//
// TestEveryCreatableEntityIsIndexedOrArgued proves the CALL exists. It cannot
// prove the call works, and the distinction is not academic: an indexEntity
// with the wrong entity_type, an empty Title, or a document written outside the
// transaction would all satisfy the census and leave search exactly as broken
// as it was. WP-J9 recorded this rule after four fixes reintroduced the defect
// they were fixing -- prove the guard against the SPECIFIC bug, not its shape.
func TestAnIdentityIsFindable(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newIdentityFixture(t, e)
			id := f.identity(t, "svc-payments-gateway", 90)

			results, err := f.s.Search(f.ctx, "svc-payments-gateway", 25)
			if err != nil {
				t.Fatalf("searching: %v", err)
			}
			if !hasResult(results, "identity", id.ID) {
				t.Fatalf("an identity declared through CreateIdentity is not findable "+
					"by its own name. This is the WP-J8 defect verbatim: the whole "+
					"surface works and the credential is invisible to search.\n"+
					"got: %+v", results)
			}
		})
	}
}

// TestAnIdentityRenameReachesTheIndex covers the update path separately.
//
// A create-only fix passes the census and still leaves a corrected name
// answering with the typo forever, with nothing on screen to say so.
func TestAnIdentityRenameReachesTheIndex(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newIdentityFixture(t, e)
			id := f.identity(t, "svc-typoo", 0)

			row, err := f.s.GetIdentity(f.ctx, id.ID)
			if err != nil {
				t.Fatalf("getting identity: %v", err)
			}
			corrected := row.Identity
			corrected.Name = "svc-corrected"
			if err := f.s.UpdateIdentity(f.ctx, testPermit, &corrected); err != nil {
				t.Fatalf("updating identity: %v", err)
			}

			results, err := f.s.Search(f.ctx, "svc-corrected", 25)
			if err != nil {
				t.Fatalf("searching: %v", err)
			}
			if !hasResult(results, "identity", id.ID) {
				t.Errorf("a corrected identity name did not reach the index: %+v", results)
			}

			stale, err := f.s.Search(f.ctx, "svc-typoo", 25)
			if err != nil {
				t.Fatalf("searching: %v", err)
			}
			if hasResult(stale, "identity", id.ID) {
				t.Errorf("the OLD name still resolves. indexEntity is an upsert for "+
					"exactly this reason; a stale document means the delete-then-insert "+
					"half did not run.\ngot: %+v", stale)
			}
		})
	}
}

// TestSearchNeverDisclosesASecretRef is the security assertion of this package.
//
// secret_ref holds a PATH, never material, so this is not about leaking a
// credential -- it is about not publishing a map of where the estate keeps
// them. Search is the widest read surface in the product: no project scope, no
// cost gate, every authenticated reader. CLAUDE.md keeps secret_ref out of the
// audit trail; the index is wider than the audit trail, so it stays out of here
// too. Asserted behaviourally rather than by reading indexIdentity, because the
// field could arrive in the document from Title, Subtitle or Body and only a
// query proves all three.
func TestSearchNeverDisclosesASecretRef(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newIdentityFixture(t, e)
			id := f.identity(t, "svc-vaulted", 90)

			// The exact value newIdentityFixture stores. The FTS5 tokenizer
			// declares '/' a token character (migration 00001), so this is one
			// token and a match would be a real disclosure rather than a
			// coincidental word hit.
			for _, probe := range []string{"kv/prod/svc-vaulted", "kv", "vault"} {
				results, err := f.s.Search(f.ctx, probe, 25)
				if err != nil {
					t.Fatalf("searching %q: %v", probe, err)
				}
				if probe == "vault" {
					// realm IS indexed and is deliberately findable; this probe
					// exists to prove the query shape can match at all, so the
					// two negative probes below mean something.
					if !hasResult(results, "identity", id.ID) {
						t.Errorf("realm %q did not resolve, so the negative probes "+
							"in this test prove nothing", probe)
					}
					continue
				}
				if hasResult(results, "identity", id.ID) {
					t.Errorf("searching %q returned the identity: secret_ref reached "+
						"the search index. It holds the location of a credential and "+
						"search is the widest read surface there is.\ngot: %+v",
						probe, results)
				}
			}
		})
	}
}

// TestReindexIdentitiesRecoversAPreFixEstate simulates exactly what the demo
// and any upgraded deployment look like: identity rows that exist and have no
// search document, because they were written before CreateIdentity indexed.
//
// The simulation is a direct DELETE from search_index rather than a mocked
// store, so what is being tested is the real recovery path against the real
// index on both engines.
func TestReindexIdentitiesRecoversAPreFixEstate(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newIdentityFixture(t, e)
			live := f.identity(t, "svc-legacy-live", 90)
			retired := f.identity(t, "svc-legacy-retired", 0)
			if err := f.s.RetireIdentity(f.ctx, testPermit, retired.ID); err != nil {
				t.Fatalf("retiring identity: %v", err)
			}

			// Back to the pre-fix world.
			exec(t, f.s.db, `DELETE FROM search_index WHERE entity_type = 'identity'`)
			gone, err := f.s.Search(f.ctx, "svc-legacy-live", 25)
			if err != nil {
				t.Fatalf("searching: %v", err)
			}
			if hasResult(gone, "identity", live.ID) {
				t.Fatal("the setup did not actually clear the index, so this test " +
					"would pass without the reindex doing anything")
			}

			written, err := f.s.ReindexIdentities(f.ctx, domain.SystemPermit("test"))
			if err != nil {
				t.Fatalf("reindexing: %v", err)
			}
			if written != 2 {
				t.Errorf("reindexed %d identities, want 2", written)
			}

			back, err := f.s.Search(f.ctx, "svc-legacy-live", 25)
			if err != nil {
				t.Fatalf("searching: %v", err)
			}
			if !hasResult(back, "identity", live.ID) {
				t.Errorf("a live identity is still unfindable after a reindex: %+v", back)
			}

			// Retired ones too: RetireIdentity leaves the document in place, so a
			// backfill that skipped them would make a retired credential findable
			// only by accident of when it was written.
			retiredHits, err := f.s.Search(f.ctx, "svc-legacy-retired", 25)
			if err != nil {
				t.Fatalf("searching: %v", err)
			}
			if !hasResult(retiredHits, "identity", retired.ID) {
				t.Errorf("a retired identity was skipped by the reindex. "+
					"RetireIdentity deliberately leaves the document in place, so "+
					"the backfill has to agree with it.\ngot: %+v", retiredHits)
			}
		})
	}
}
