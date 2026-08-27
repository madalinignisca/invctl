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
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// customFieldFixture is the shared fixture for WP-A4's store tests (Tasks 3,
// 4 and 7). Modelled on newProjectFixture: one asset holding a value, one
// deliberately holding none, and a service, so a field that is null
// everywhere is never the only case a test exercises.
type customFieldFixture struct {
	s             *SQLStore
	ctx           context.Context
	actor         domain.Actor
	assetID       string
	secondAssetID string
	serviceID     string
	username      string
	// teamID is an ACTIVE team, offered to a new field's owner picker.
	// retiredTeamID starts active too and is retired by the caller when a
	// test needs a field whose owner has since disbanded -- kept separate
	// from teamID so "the field's own owner retiring" never accidentally
	// invalidates every OTHER fixture field's owner in the same test run.
	teamID        string
	retiredTeamID string
}

func newCustomFieldFixture(t *testing.T, e Engine) *customFieldFixture {
	t.Helper()
	s, ctx := newStore(t, e)

	// A real app_user row, not testActor: custom_field.created_by carries a
	// foreign key to app_user(id), and the resolved-name test needs a
	// display name distinct from the id to assert against.
	const username = "cf-admin"
	user, err := domain.NewAppUser(NewID(), username, domain.UserSourceLocal, s.Now())
	if err != nil {
		t.Fatalf("building fixture user: %v", err)
	}
	if err := s.CreateUser(ctx, testActor, user); err != nil {
		t.Fatalf("creating fixture user: %v", err)
	}
	actor := domain.UserActor(user)

	env := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
	assetID := mustAsset(t, s, ctx, domain.KindVM, "cf-vm-1", nil, env)
	secondAssetID := mustAsset(t, s, ctx, domain.KindVM, "cf-vm-2", nil, env)
	// Deliberately holds no value: a usage count that counted every asset
	// would pass a test where every asset happened to have one.
	mustAsset(t, s, ctx, domain.KindVM, "cf-vm-3", nil, env)

	svc, err := domain.NewService(NewID(), domain.ServiceSpec{
		Code: "cf-svc", Name: "cf-svc", Kind: domain.SvcAPI,
		EnvironmentID: env, Availability: domain.AvailStandalone, Tier: 2,
	}, s.Now())
	if err != nil {
		t.Fatalf("building fixture service: %v", err)
	}
	if err := s.CreateService(ctx, testPermit, svc); err != nil {
		t.Fatalf("creating fixture service: %v", err)
	}

	teamID := mustCustomFieldTeam(t, s, ctx, "cf-owner")
	retiredTeamID := mustCustomFieldTeam(t, s, ctx, "cf-owner-to-retire")

	return &customFieldFixture{
		s: s, ctx: ctx, actor: actor,
		assetID: assetID, secondAssetID: secondAssetID, serviceID: svc.ID,
		username: username, teamID: teamID, retiredTeamID: retiredTeamID,
	}
}

// mustCustomFieldTeam builds an active team, for a fixture field's own
// owner_team_id -- NewCustomField refuses an empty one, with no escape
// hatch, so every fixture field needs a real one to point at.
func mustCustomFieldTeam(t *testing.T, s *SQLStore, ctx context.Context, code string) string {
	t.Helper()
	team, err := domain.NewTeam(NewID(), domain.TeamSpec{Code: code, Name: code}, s.Now())
	if err != nil {
		t.Fatalf("building fixture team %s: %v", code, err)
	}
	if err := s.CreateTeam(ctx, testPermit, team); err != nil {
		t.Fatalf("creating fixture team %s: %v", code, err)
	}
	return team.ID
}

// mustField creates a live definition and returns its id, owned by the
// fixture's own active team.
func mustField(t *testing.T, f *customFieldFixture, entityType, code, kind string) string {
	t.Helper()
	cf, err := domain.NewCustomField(NewID(), entityType, code, code, kind,
		"a fixture field for the store test suite", f.actor.ID, f.teamID, f.s.Now())
	if err != nil {
		t.Fatalf("building custom field %s: %v", code, err)
	}
	if err := f.s.CreateCustomField(f.ctx, domain.AdministratorPermit(f.actor), cf); err != nil {
		t.Fatalf("creating custom field %s: %v", code, err)
	}
	return cf.ID
}

// mustOptions sets a select field's live option vocabulary to exactly the values
// the caller NAMES. It goes through SetCustomFieldOptions, the only writer of
// custom_field_option, so it exercises the same path a form submitting the whole
// list does.
//
// IT QUERIES FOR NOTHING. The helper it replaced, mustOption, added one option
// while "keeping whatever live options already existed" -- which it discovered
// by reading GetCustomField at submit time. That is the Ruling AA shape: a
// payload assembled from what the database happens to hold now rather than from
// what the caller, standing in for a rendered form, actually named. It could not
// destroy anything, because SetCustomFieldOptions retires only what is ABSENT
// from the list and that list was always a superset of what was live -- but
// "cannot destroy because of what the callee does with it" is exactly the
// standing Rulings Y, Z and AA rejected, so it is gone rather than annotated.
//
// The cost is that a caller wanting two options names two. That is also what the
// form does.
func mustOptions(t *testing.T, f *customFieldFixture, fieldID string, values ...string) {
	t.Helper()
	opts := make([]domain.CustomFieldOption, 0, len(values))
	for _, v := range values {
		opts = append(opts, domain.CustomFieldOption{Value: v, Label: v})
	}
	if err := f.s.SetCustomFieldOptions(f.ctx, domain.AdministratorPermit(f.actor), fieldID,
		fieldVersion(t, f, fieldID), opts); err != nil {
		t.Fatalf("setting the options of field %s to %v: %v", fieldID, values, err)
	}
}

// mustValue sets ONE field's value on an entity and touches nothing else.
//
// Task 3 carried a stub here that INSERTed straight into custom_field_value,
// bypassing validation, canonicalisation and the audit fold entirely. Task 4
// REPLACED it with the real write path rather than keeping a second one
// alongside: every test in this package now exercises SetCustomValues, so a
// defect in it cannot hide behind a fixture that did the write differently.
//
// IT POSTS THE ONE FIELD AND NOTHING ELSE, which is the whole helper. Absence
// means UNTOUCHED (Ruling U), so naming one field is exactly "set this, leave
// the rest". It used to merge the entity's existing values in from
// CustomValuesFor and re-post them all, which was inert -- every re-posted value
// was byte-identical to what was stored, so each hit the unchanged-value rule
// before validation and no row was rewritten -- but inert is not the same as
// right. That merge was the Ruling Y enumeration shape: a payload built from
// CustomValuesFor, which deliberately includes retired fields' retained values
// the operator never saw. A helper that is safe only because a downstream rule
// catches it is the fragility this feature spent four review rounds on, so it is
// gone rather than annotated (Ruling Z).
func mustValue(t *testing.T, f *customFieldFixture, fieldID, entityID, raw string) {
	t.Helper()
	mustSetValues(t, f, entityID, map[string]string{fieldID: raw})
}

// mustSetValues submits one payload of custom values, exactly as it is given.
//
// It replaces nothing it is not told about: absent means untouched, an explicit
// blank means clear. It used to say it "replaces an entity's whole set in one
// call, which is what a form submission does" -- both halves repealed by Ruling
// U, and the second half is how instance 4 got written, because a helper that
// believes it is posting a complete set will happily be handed one built from
// the wrong query.
//
// SETUP AFFORDANCE, DELIBERATE AND NARROW: it reads the parent's token at submit
// time, which no form can do. That is fine for arranging a fixture and wrong for
// modelling an operator, so a test exercising a render-then-submit window
// captures its own token and calls the store directly --
// TestARestoreBetweenRenderAndSubmitDestroysNothing and
// TestAFieldCreatedBetweenRenderAndSubmitIsNotCleared both do. The payload
// itself is never queried for here: it is whatever the caller named.
func mustSetValues(t *testing.T, f *customFieldFixture, entityID string, vals map[string]string) {
	t.Helper()
	entityType := entityTypeOf(t, f, entityID)
	if err := f.s.SetCustomValues(f.ctx, domain.AdministratorPermit(f.actor), entityType, entityID,
		entityVersion(t, f, entityType, entityID), vals); err != nil {
		t.Fatalf("setting custom values on %s %s: %v", entityType, entityID, err)
	}
}

// mustClearValues clears the fields the caller NAMES, by posting an explicit
// blank for each. rendered is the field set the operator's form drew.
//
// IT TAKES THE FIELD SET FROM ITS CALLER AND QUERIES FOR NOTHING, and that is
// the whole of Ruling AA. Two earlier shapes were both wrong for the same
// reason, and each looked right until the one before it was fixed:
//
//   - Enumerating CustomValuesFor posted a blank for every VALUE held, which
//     includes retired fields' retained values a detail page renders and a form
//     never does. Rows the operator was never shown, cleared as though they had
//     asked (Ruling Y).
//   - Enumerating the live field list at SUBMIT time posted a blank for every
//     field live THEN, which includes one another administrator created after
//     the operator's form was drawn. A field the operator was never shown,
//     cleared as though they had asked (Ruling AA). Same sentence, one column
//     over.
//
// A test helper has no POST body, so the caller naming the ids IS how it models
// one honestly. Anything it looks up for itself is something the form could not
// have known, and the previous comment here already said so -- "it cannot post a
// field it did not draw" -- while the code beneath it did the opposite.
func mustClearValues(t *testing.T, f *customFieldFixture, entityID string, rendered ...string) {
	t.Helper()
	vals := make(map[string]string, len(rendered))
	for _, fieldID := range rendered {
		vals[fieldID] = ""
	}
	mustSetValues(t, f, entityID, vals)
}

// entityVersion reads the parent entity's current concurrency token, which
// SetCustomValues takes and bumps (design.md §3, as amended: the value editor's
// token is the parent's, not each value's).
//
// IT READS AT SUBMIT TIME, WHICH NO FORM CAN DO, and that is deliberate and
// narrow. The fixture sweep for "does any helper build a payload from a query"
// reached it and it is kept, for a reason worth writing down rather than
// assuming: the token is not part of the INSTRUCTION about what to change. What
// gets cleared is decided entirely by the vals map, so a helpfully-fresh token
// cannot destroy a value the caller did not name, and cannot mask a defect that
// would -- it only lets a setup write succeed. Every test that exercises
// staleness passes its own token, and every test modelling a render-then-submit
// window captures one before the window opens rather than calling this.
//
// Threading a token through the setup helpers instead would put churn on twenty
// call sites with no defect behind it. If that judgement is wrong, the thing to
// change is the helpers, not this function.
func entityVersion(t *testing.T, f *customFieldFixture, entityType, entityID string) int {
	t.Helper()
	var version int
	// Two literal statements rather than one with the table name pasted in:
	// the same rule the store side follows, for the same reason.
	var err error
	switch entityType {
	case domain.CustomFieldEntityAsset:
		err = f.s.readOne(f.ctx, &version, `SELECT row_version FROM asset WHERE id = ?`, entityID)
	case domain.CustomFieldEntityService:
		err = f.s.readOne(f.ctx, &version, `SELECT row_version FROM service WHERE id = ?`, entityID)
	default:
		t.Fatalf("no such entity type %q", entityType)
	}
	if err != nil {
		t.Fatalf("reading the row_version of %s %s: %v", entityType, entityID, err)
	}
	return version
}

// fieldVersion reads a custom_field's own current concurrency token, which
// SetCustomFieldOptions takes and bumps -- Ruling W: the options editor's
// token is the field's own, not each option's.
//
// IT READS AT SUBMIT TIME, WHICH NO FORM CAN DO, for the same reason
// entityVersion does: the token is not part of the instruction about which
// options to keep, so a helpfully-fresh token cannot mask a defect in what
// the caller named. A test exercising staleness captures its own token first
// instead of calling this.
func fieldVersion(t *testing.T, f *customFieldFixture, fieldID string) int {
	t.Helper()
	var version int
	if err := f.s.readOne(f.ctx, &version,
		`SELECT row_version FROM custom_field WHERE id = ?`, fieldID); err != nil {
		t.Fatalf("reading the row_version of custom field %s: %v", fieldID, err)
	}
	return version
}

// entityTypeOf answers "is this id an asset or a service" so the helpers above
// take the same argument list the brief's tests use. custom_field_value carries
// no entity_type of its own -- it lives on the definition -- so a test fixture
// has to decide it the same way a handler's route does.
func entityTypeOf(t *testing.T, f *customFieldFixture, entityID string) string {
	t.Helper()
	n, err := f.s.countOne(f.ctx, `SELECT COUNT(*) FROM asset WHERE id = ?`, entityID)
	if err != nil {
		t.Fatalf("deciding the entity type of %s: %v", entityID, err)
	}
	if n == 1 {
		return domain.CustomFieldEntityAsset
	}
	return domain.CustomFieldEntityService
}

// changeCount and lastChangeDiff are the two change_log helpers every WP-A4
// store test needs, kept here because this task owns the fixture.

func changeCount(t *testing.T, f *customFieldFixture, entityType, entityID string) int64 {
	t.Helper()
	n, err := f.s.countOne(f.ctx,
		`SELECT COUNT(*) FROM change_log WHERE entity_type = ? AND entity_id = ?`,
		entityType, entityID)
	if err != nil {
		t.Fatalf("counting change_log rows for %s %s: %v", entityType, entityID, err)
	}
	return n
}

func lastChangeDiff(t *testing.T, f *customFieldFixture, entityType, entityID string) string {
	t.Helper()
	var diff string
	err := f.s.readOne(f.ctx, &diff,
		`SELECT diff FROM change_log WHERE entity_type = ? AND entity_id = ? ORDER BY at DESC, id DESC LIMIT 1`,
		entityType, entityID)
	if err != nil {
		t.Fatalf("reading the last change_log diff for %s %s: %v", entityType, entityID, err)
	}
	return diff
}

// ---------- Controller ruling D: deferred from Task 1 ----------

// TestARetiredCodeCanBeUsedAgain proves migration 00051's unique index is
// partial (WHERE retired_at IS NULL). A plain UNIQUE would refuse the second
// insert and an operator could never reuse a name they had retired.
func TestARetiredCodeCanBeUsedAgain(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			first := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			if err := f.s.RetireCustomField(f.ctx, domain.AdministratorPermit(f.actor), first); err != nil {
				t.Fatalf("retiring: %v", err)
			}

			cf, err := domain.NewCustomField(NewID(), "asset", "cost_centre", "Cost Centre",
				domain.CustomFieldText, "reused after retirement", f.actor.ID, f.teamID, f.s.Now())
			if err != nil {
				t.Fatalf("building the second field: %v", err)
			}
			if err := f.s.CreateCustomField(f.ctx, domain.AdministratorPermit(f.actor), cf); err != nil {
				t.Fatalf("a retired code must be usable again: %v", err)
			}
		})
	}
}

// TestTwoLiveFieldsCannotShareACode is the other half of the same index: two
// LIVE fields with one code must be refused, retired or not notwithstanding.
func TestTwoLiveFieldsCannotShareACode(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)

			cf, err := domain.NewCustomField(NewID(), "asset", "cost_centre", "Cost Centre",
				domain.CustomFieldText, "a second, still-live attempt", f.actor.ID, f.teamID, f.s.Now())
			if err != nil {
				t.Fatalf("building the second field: %v", err)
			}
			if err := f.s.CreateCustomField(f.ctx, domain.AdministratorPermit(f.actor), cf); err == nil {
				t.Fatal("two live fields must not be able to share one code")
			}
		})
	}
}

// ---------- Task 3's own tests, verbatim from the brief ----------

func TestAFieldsKindCannotChangeWhileValuesExist(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			mustValue(t, f, id, f.assetID, "IT-42")

			got, err := f.s.GetCustomField(f.ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			got.Kind = domain.CustomFieldNumber
			err = f.s.UpdateCustomField(f.ctx, domain.AdministratorPermit(f.actor), &got.CustomField)
			if err == nil {
				t.Fatal("retyping a field that holds values must be refused: " +
					"it is a data migration wearing a form control")
			}
			if !strings.Contains(err.Error(), "1") {
				t.Errorf("the refusal should name how many values block it; got %v", err)
			}
		})
	}
}

func TestTheLabelAndDescriptionStayEditableWithValues(t *testing.T) {
	// The two things an administrator actually needs to correct stay editable
	// forever. Only the kind is frozen once values exist.
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			mustValue(t, f, id, f.assetID, "IT-42")

			row, err := f.s.GetCustomField(f.ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			row.Label = "SAP Cost Centre"
			row.Description = "the code finance rebills against"
			if err := f.s.UpdateCustomField(f.ctx, domain.AdministratorPermit(f.actor), &row.CustomField); err != nil {
				t.Fatalf("renaming a field that holds values must be allowed: %v", err)
			}
			after, err := f.s.GetCustomField(f.ctx, id)
			if err != nil {
				t.Fatalf("re-reading: %v", err)
			}
			if after.Label != "SAP Cost Centre" {
				t.Fatalf("got label %q, want SAP Cost Centre", after.Label)
			}
			if after.Description != "the code finance rebills against" {
				t.Fatalf("got description %q", after.Description)
			}
		})
	}
}

// TestCodeStaysEditableWithValues is Ruling M's other half: code, like
// label and description, is a rename -- values are keyed by field_id, not by
// code, so nothing is stranded and the audit fold is simply recomputed on the
// next write rather than rewritten retroactively. Only entity_type is frozen
// even with no values at stake; code is not, and this is the test that says so.
func TestCodeStaysEditableWithValues(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			mustValue(t, f, id, f.assetID, "IT-42")

			row, err := f.s.GetCustomField(f.ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			row.Code = "cost_center_us"
			if err := f.s.UpdateCustomField(f.ctx, domain.AdministratorPermit(f.actor), &row.CustomField); err != nil {
				t.Fatalf("renaming the code of a field that holds values must be allowed: %v", err)
			}
			after, err := f.s.GetCustomField(f.ctx, id)
			if err != nil {
				t.Fatalf("re-reading: %v", err)
			}
			if after.Code != "cost_center_us" {
				t.Fatalf("got code %q, want cost_center_us", after.Code)
			}
			n, err := f.s.countOne(f.ctx,
				`SELECT COUNT(*) FROM custom_field_value WHERE field_id = ?`, id)
			if err != nil {
				t.Fatalf("counting: %v", err)
			}
			if n != 1 {
				t.Fatalf("renaming the code stranded the value: got %d, want 1", n)
			}
		})
	}
}

func TestRetiringAFieldKeepsEveryValue(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			mustValue(t, f, id, f.assetID, "IT-42")

			if err := f.s.RetireCustomField(f.ctx, domain.AdministratorPermit(f.actor), id); err != nil {
				t.Fatalf("retiring: %v", err)
			}
			// countOne(ctx, query, args...) (int64, error) -- it returns the
			// count, it does not take a destination pointer.
			n, err := f.s.countOne(f.ctx,
				`SELECT COUNT(*) FROM custom_field_value WHERE field_id = ?`, id)
			if err != nil {
				t.Fatalf("counting: %v", err)
			}
			if n != 1 {
				t.Fatalf("got %d values after retiring, want 1: retiring a field "+
					"deletes no value, or somebody rings up asking where their data went", n)
			}
		})
	}
}

func TestRestoreIsRefusedWhenALiveFieldHoldsTheCode(t *testing.T) {
	// The partial unique index frees a retired code for reuse, so restoring
	// the original would produce two live fields with one code. Refuse, and
	// say which field is in the way.
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			first := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			if err := f.s.RetireCustomField(f.ctx, domain.AdministratorPermit(f.actor), first); err != nil {
				t.Fatalf("retiring: %v", err)
			}
			mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)

			err := f.s.RestoreCustomField(f.ctx, domain.AdministratorPermit(f.actor), first)
			if err == nil {
				t.Fatal("restoring must be refused while a live field holds the code")
			}
			if !strings.Contains(err.Error(), "cost_centre") {
				t.Errorf("the refusal must name the code that is in the way; got %v", err)
			}
		})
	}
}

func TestTheRegistryResolvesTheCreatorWithoutStoringAUsername(t *testing.T) {
	// created_by holds an opaque app_user.id, exactly as change_log.actor does,
	// so scrubbing a user for an erasure request leaves the registry readable
	// and simply stops resolving a name.
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			row, err := f.s.GetCustomField(f.ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			if row.CreatedBy == f.username {
				t.Fatal("created_by holds the username; it must hold the opaque app_user.id")
			}
			if row.CreatedByName != f.username {
				t.Fatalf("got display name %q, want %q resolved by join", row.CreatedByName, f.username)
			}
		})
	}
}

func TestUsageCountsCountEntitiesNotRows(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			mustValue(t, f, id, f.assetID, "IT-42")
			mustValue(t, f, id, f.secondAssetID, "IT-99")
			// A third asset deliberately holds no value: a count that returned
			// every asset would pass a test where every asset had one.

			rows, err := f.s.ListCustomFields(f.ctx, "asset", false)
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			var got int
			for _, r := range rows {
				if r.ID == id {
					got = r.UsageCount
				}
			}
			if got != 2 {
				t.Fatalf("got usage count %d, want 2", got)
			}
		})
	}
}

func TestEveryDefinitionMutationWritesChangeLog(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			steps := []struct {
				name string
				do   func() error
			}{
				{"update", func() error {
					row, err := f.s.GetCustomField(f.ctx, id)
					if err != nil {
						return err
					}
					row.Label = "Renamed"
					return f.s.UpdateCustomField(f.ctx, domain.AdministratorPermit(f.actor), &row.CustomField)
				}},
				{"retire", func() error { return f.s.RetireCustomField(f.ctx, domain.AdministratorPermit(f.actor), id) }},
				{"restore", func() error { return f.s.RestoreCustomField(f.ctx, domain.AdministratorPermit(f.actor), id) }},
			}
			// Creation already happened inside mustField; assert it logged too.
			if n := changeCount(t, f, "custom_field", id); n != 1 {
				t.Fatalf("creating wrote %d change_log rows, want 1", n)
			}
			for i, step := range steps {
				before := changeCount(t, f, "custom_field", id)
				if err := step.do(); err != nil {
					t.Fatalf("%s: %v", step.name, err)
				}
				if got := changeCount(t, f, "custom_field", id); got != before+1 {
					t.Fatalf("%s (step %d) wrote %d rows, want 1", step.name, i, got-before)
				}
			}
		})
	}
}

// ---------- the one that is easy to get wrong ----------

// TestChangingOnlyOptionsWritesAChangeLogEntry is the test CLAUDE.md demands
// for this exact failure mode: a set replacement (SetCustomFieldOptions) that
// leaves every column of the parent struct untouched must still write a
// change_log row, because the folded Options string is the only place the
// change is visible.
func TestChangingOnlyOptionsWritesAChangeLogEntry(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "tier", domain.CustomFieldSelect)
			before := changeCount(t, f, "custom_field", id)

			mustOptions(t, f, id, "gold")

			after := changeCount(t, f, "custom_field", id)
			if after != before+1 {
				t.Fatalf("setting options with no other field change wrote %d change_log rows, want %d",
					after-before, 1)
			}
			diff := lastChangeDiff(t, f, "custom_field", id)
			if !strings.Contains(diff, "gold") {
				t.Fatalf("the diff must show the option that was added; got %s", diff)
			}
		})
	}
}

// TestReorderingCustomFieldOptionsIsNotAChange is the option-set analogue of
// dependencyAudit's sorted DataClasses: re-submitting the same live options in
// a different order must not itself produce a change_log entry.
func TestReorderingCustomFieldOptionsIsNotAChange(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "tier", domain.CustomFieldSelect)
			if err := f.s.SetCustomFieldOptions(f.ctx, domain.AdministratorPermit(f.actor), id, fieldVersion(t, f, id), []domain.CustomFieldOption{
				{Value: "gold", Label: "Gold"},
				{Value: "silver", Label: "Silver"},
			}); err != nil {
				t.Fatalf("setting initial options: %v", err)
			}
			before := changeCount(t, f, "custom_field", id)

			if err := f.s.SetCustomFieldOptions(f.ctx, domain.AdministratorPermit(f.actor), id, fieldVersion(t, f, id), []domain.CustomFieldOption{
				{Value: "silver", Label: "Silver"},
				{Value: "gold", Label: "Gold"},
			}); err != nil {
				t.Fatalf("re-submitting in a different order: %v", err)
			}

			if got := changeCount(t, f, "custom_field", id); got != before {
				t.Fatalf("reordering the same live options wrote %d change_log rows, want 0", got-before)
			}
		})
	}
}

// TestRenamingAnOptionsLabelIsAuditedEvenWithNoOtherChange is FINAL REVIEW B2:
// SetCustomFieldOptions built the audited fold from option VALUES only, so an
// option whose value and position were unchanged but whose LABEL changed
// folded identically before and after and diffJSON saw no change at all --
// custom_field_option.label is declared (docs/AUDIT.md line 63), and this is
// the fourth recurrence of the exact failure CLAUDE.md already names three
// times: a set replacement that leaves the parent struct untouched producing
// no audit entry. Deliberately the mirror image of
// TestReorderingCustomFieldOptionsIsNotAChange: that test proves the same set
// in a different order is not a change; this one proves the same set in the
// same order, with one member's label edited, IS one.
func TestRenamingAnOptionsLabelIsAuditedEvenWithNoOtherChange(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "tier", domain.CustomFieldSelect)
			if err := f.s.SetCustomFieldOptions(f.ctx, domain.AdministratorPermit(f.actor), id, fieldVersion(t, f, id), []domain.CustomFieldOption{
				{Value: "gold", Label: "Gold"},
				{Value: "silver", Label: "Silver"},
			}); err != nil {
				t.Fatalf("setting initial options: %v", err)
			}
			before := changeCount(t, f, "custom_field", id)

			// Same value, same position -- only the label moves.
			if err := f.s.SetCustomFieldOptions(f.ctx, domain.AdministratorPermit(f.actor), id, fieldVersion(t, f, id), []domain.CustomFieldOption{
				{Value: "gold", Label: "Gold Tier"},
				{Value: "silver", Label: "Silver"},
			}); err != nil {
				t.Fatalf("renaming the gold option's label: %v", err)
			}

			if got := changeCount(t, f, "custom_field", id); got != before+1 {
				t.Fatalf("a label-only rename wrote %d change_log rows, want exactly 1", got-before)
			}
			diff := lastChangeDiff(t, f, "custom_field", id)
			if !strings.Contains(diff, "gold=Gold") || !strings.Contains(diff, "gold=Gold Tier") {
				t.Fatalf("the diff must show the old and the new label, got: %s", diff)
			}
		})
	}
}

// TestRetiringAnOptionKeepsItSelectableOnExistingValues asserts the migration
// comment's rule directly: an option is never deleted, and one dropped from a
// SetCustomFieldOptions call is retired, not removed, so it keeps resolving.
func TestRetiringAnOptionKeepsItSelectableOnExistingValues(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "tier", domain.CustomFieldSelect)
			mustOptions(t, f, id, "gold", "silver")

			// Drop "silver" by submitting only "gold".
			if err := f.s.SetCustomFieldOptions(f.ctx, domain.AdministratorPermit(f.actor), id, fieldVersion(t, f, id), []domain.CustomFieldOption{
				{Value: "gold", Label: "Gold"},
			}); err != nil {
				t.Fatalf("dropping silver: %v", err)
			}

			row, err := f.s.GetCustomField(f.ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			var silver *domain.CustomFieldOption
			for i := range row.Options {
				if row.Options[i].Value == "silver" {
					silver = &row.Options[i]
				}
			}
			if silver == nil {
				t.Fatal("the silver option row must still exist, retired -- not deleted")
			}
			if silver.RetiredAt == nil {
				t.Fatal("the silver option must be retired once it is no longer offered")
			}
		})
	}
}

// TestSetCustomFieldOptionsRefusesADuplicateValue: a duplicate value would
// silently double-write the same row inside the loop -- last one wins, and
// nothing before this guard would have noticed.
func TestSetCustomFieldOptionsRefusesADuplicateValue(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "tier", domain.CustomFieldSelect)

			err := f.s.SetCustomFieldOptions(f.ctx, domain.AdministratorPermit(f.actor), id, fieldVersion(t, f, id), []domain.CustomFieldOption{
				{Value: "gold", Label: "Gold"},
				{Value: "gold", Label: "Gold (again)"},
			})
			if err == nil {
				t.Fatal("a duplicate option value must be refused")
			}
			if !strings.Contains(err.Error(), "gold") {
				t.Errorf("the refusal must name the offending value; got %v", err)
			}
		})
	}
}

// TestSetCustomFieldOptionsRefusesAnEmptyValueOrLabel covers the other half
// of the same guard: an option with nothing in its value or its label is not
// a value an entity could ever be asked to pick.
func TestSetCustomFieldOptionsRefusesAnEmptyValueOrLabel(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "tier", domain.CustomFieldSelect)

			if err := f.s.SetCustomFieldOptions(f.ctx, domain.AdministratorPermit(f.actor), id, fieldVersion(t, f, id), []domain.CustomFieldOption{
				{Value: "  ", Label: "Gold"},
			}); err == nil {
				t.Fatal("an empty option value must be refused")
			}
			if err := f.s.SetCustomFieldOptions(f.ctx, domain.AdministratorPermit(f.actor), id, fieldVersion(t, f, id), []domain.CustomFieldOption{
				{Value: "gold", Label: "  "},
			}); err == nil {
				t.Fatal("an empty option label must be refused")
			}
		})
	}
}

// TestSetCustomFieldOptionsBoundsValueAndLabel is FINAL REVIEW AY: an
// option's value and label are text an administrator typed, and
// CanonicalCustomValue's select branch only ever checks MEMBERSHIP, never
// bounds -- so an oversized or control-character-laden option value sailed
// straight through the moment a value selected it. Reproduces exactly what
// the review accepted: a 5,015-byte value containing an embedded NUL, which
// SQLite stores and PostgreSQL rejects with "invalid byte sequence for
// encoding UTF8" -- a portability divergence this bound closes at the Go
// layer, before either engine sees the byte.
func TestSetCustomFieldOptionsBoundsValueAndLabel(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "tier", domain.CustomFieldSelect)

			oversized := strings.Repeat("a", domain.MaxCustomTextLength+15)
			if err := f.s.SetCustomFieldOptions(f.ctx, domain.AdministratorPermit(f.actor), id, fieldVersion(t, f, id), []domain.CustomFieldOption{
				{Value: oversized, Label: "Gold"},
			}); err == nil {
				t.Fatal("an option value over the length bound must be refused")
			}

			withNUL := "gold\x00" + strings.Repeat("a", 5000)
			if err := f.s.SetCustomFieldOptions(f.ctx, domain.AdministratorPermit(f.actor), id, fieldVersion(t, f, id), []domain.CustomFieldOption{
				{Value: withNUL, Label: "Gold"},
			}); err == nil {
				t.Fatal("an option value with an embedded NUL byte must be refused")
			}

			if err := f.s.SetCustomFieldOptions(f.ctx, domain.AdministratorPermit(f.actor), id, fieldVersion(t, f, id), []domain.CustomFieldOption{
				{Value: "gold", Label: "Gold\x00"},
			}); err == nil {
				t.Fatal("an option label with an embedded NUL byte must be refused")
			}

			// Neither refusal left a partial write behind.
			row, err := f.s.GetCustomField(f.ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			if len(row.Options) != 0 {
				t.Fatalf("a refused options submission wrote %d option row(s), want 0", len(row.Options))
			}
		})
	}
}

// TestUpdateRefusesADescriptionClearedToBlank: description is not optional --
// design.md §2 and the migration's CHECK agree an administrator must always
// be able to say why a field exists, and clearing it on an edit is refused
// the same way creating it blank would be.
func TestUpdateRefusesADescriptionClearedToBlank(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			row, err := f.s.GetCustomField(f.ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			row.Description = "   "
			if err := f.s.UpdateCustomField(f.ctx, domain.AdministratorPermit(f.actor), &row.CustomField); err == nil {
				t.Fatal("clearing the description to blank must be refused")
			}
		})
	}
}

// TestEntityTypeCannotChangeEvenAtZeroValues is CONTROLLER RULING M:
// entity_type is stricter than Kind. Kind is refused only once values exist,
// because a retype is harmless with none. entity_type is refused even with
// NO values, because it is half the field's identity and half the partial
// unique index -- flipping it would strand every future value against the
// wrong entity kind, and custom_field_value has nothing that would catch the
// mismatch.
func TestEntityTypeCannotChangeEvenAtZeroValues(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)

			row, err := f.s.GetCustomField(f.ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			// No mustValue call here on purpose: this field holds ZERO
			// values. Refusing anyway is the entire point of this test --
			// it is what tells entity_type apart from Kind.
			row.EntityType = "service"
			err = f.s.UpdateCustomField(f.ctx, domain.AdministratorPermit(f.actor), &row.CustomField)
			if err == nil {
				t.Fatal("changing entity_type must be refused, even when the field holds no values")
			}
			if !strings.Contains(err.Error(), "asset") {
				t.Errorf("the refusal should name the field's real entity_type; got %v", err)
			}
		})
	}
}

// ---------- Ruling W: the options editor needs a token, in both directions ----------

// TestAStaleOptionsSubmissionDoesNotUnretireAnOption is the first of the two
// directions Ruling W closes: a stale option list still names an option
// another administrator just retired, and applying it anyway would silently
// UN-RETIRE that option, reversing the exact decision retirement exists to
// protect.
func TestAStaleOptionsSubmissionDoesNotUnretireAnOption(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "tier", domain.CustomFieldSelect)
			mustOptions(t, f, id, "gold", "silver")

			// The stale administrator's form was rendered before either edit
			// below and still names both options live.
			stale := fieldVersion(t, f, id)

			// A second administrator retires "silver".
			if err := f.s.SetCustomFieldOptions(f.ctx, domain.AdministratorPermit(f.actor), id, fieldVersion(t, f, id),
				[]domain.CustomFieldOption{{Value: "gold", Label: "Gold"}}); err != nil {
				t.Fatalf("retiring silver: %v", err)
			}

			// The stale submission, naming both options as though nothing had
			// happened, must be refused rather than un-retiring silver.
			err := f.s.SetCustomFieldOptions(f.ctx, domain.AdministratorPermit(f.actor), id, stale, []domain.CustomFieldOption{
				{Value: "gold", Label: "Gold"},
				{Value: "silver", Label: "Silver"},
			})
			if !errors.Is(err, domain.ErrStale) {
				t.Fatalf("a stale options submission returned %v, want domain.ErrStale", err)
			}

			row, err := f.s.GetCustomField(f.ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			for _, o := range row.Options {
				if o.Value == "silver" && o.RetiredAt == nil {
					t.Fatal("the stale submission un-retired silver")
				}
			}
		})
	}
}

// TestAStaleOptionsSubmissionDoesNotRetireAnOptionJustAdded is the second
// direction: an option present in the live set but absent from a stale list
// would be retired, silently undoing an addition the submitting administrator
// never saw.
func TestAStaleOptionsSubmissionDoesNotRetireAnOptionJustAdded(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "tier", domain.CustomFieldSelect)
			mustOptions(t, f, id, "gold")

			// The stale administrator's form was rendered with only "gold" live.
			stale := fieldVersion(t, f, id)

			// A second administrator adds "silver".
			if err := f.s.SetCustomFieldOptions(f.ctx, domain.AdministratorPermit(f.actor), id, fieldVersion(t, f, id),
				[]domain.CustomFieldOption{
					{Value: "gold", Label: "Gold"},
					{Value: "silver", Label: "Silver"},
				}); err != nil {
				t.Fatalf("adding silver: %v", err)
			}

			// The stale submission, naming only "gold" as though "silver" did
			// not exist, must be refused rather than retiring it.
			err := f.s.SetCustomFieldOptions(f.ctx, domain.AdministratorPermit(f.actor), id, stale,
				[]domain.CustomFieldOption{{Value: "gold", Label: "Gold"}})
			if !errors.Is(err, domain.ErrStale) {
				t.Fatalf("a stale options submission returned %v, want domain.ErrStale", err)
			}

			row, err := f.s.GetCustomField(f.ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			for _, o := range row.Options {
				if o.Value == "silver" && o.RetiredAt != nil {
					t.Fatal("the stale submission retired silver, which the operator never saw added")
				}
			}
		})
	}
}

// ---------- owner team (WP-A4 follow-up): "who do I ask" ----------
//
// created_by answers "who defined this field", which is the wrong answer to
// "who do I ask" the moment that person leaves -- a senior review's own
// finding. owner_team_id is required with no escape hatch, editable forever
// after, and follows the exact rule already applied to a retired `select`
// option: what is STORED keeps displaying, what is RETIRED is not newly
// selectable.

// TestCreateRefusesANonexistentOwnerTeam: the friendly refusal
// requireActiveOwnerTeam exists for, rather than a bare foreign-key failure
// with no column translateWriteErr can name.
func TestCreateRefusesANonexistentOwnerTeam(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			cf, err := domain.NewCustomField(NewID(), "asset", "cost_centre", "Cost Centre",
				domain.CustomFieldText, "an owner that does not exist", f.actor.ID, NewID(), f.s.Now())
			if err != nil {
				t.Fatalf("building: %v", err)
			}
			if err := f.s.CreateCustomField(f.ctx, domain.AdministratorPermit(f.actor), cf); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("got %v, want domain.ErrInvalid for a nonexistent owner team", err)
			}
		})
	}
}

// TestCreateRefusesARetiredOwnerTeam is the half of the symmetry that
// matters most: a retired team is not offered to a NEW field -- a picker
// only ever offers what somebody may choose NOW, TeamOptions's own rule,
// enforced here at the write path too rather than trusted to the form alone.
func TestCreateRefusesARetiredOwnerTeam(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			if err := f.s.RetireTeam(f.ctx, domain.AdministratorPermit(f.actor), f.retiredTeamID); err != nil {
				t.Fatalf("retiring the fixture team: %v", err)
			}
			cf, err := domain.NewCustomField(NewID(), "asset", "cost_centre", "Cost Centre",
				domain.CustomFieldText, "a retired owner", f.actor.ID, f.retiredTeamID, f.s.Now())
			if err != nil {
				t.Fatalf("building: %v", err)
			}
			if err := f.s.CreateCustomField(f.ctx, domain.AdministratorPermit(f.actor), cf); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("got %v, want domain.ErrInvalid for a retired owner team", err)
			}
		})
	}
}

// TestAFieldAlreadyOwnedByASinceRetiredTeamStillShowsThatTeam is the other
// half: what is STORED keeps displaying. A team retiring after it was
// assigned must not blank the field's attribution -- it is a finding
// ("nobody has picked this up"), not an error to hide.
func TestAFieldAlreadyOwnedByASinceRetiredTeamStillShowsThatTeam(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			cf, err := domain.NewCustomField(NewID(), "asset", "cost_centre", "Cost Centre",
				domain.CustomFieldText, "an owner about to retire", f.actor.ID, f.retiredTeamID, f.s.Now())
			if err != nil {
				t.Fatalf("building: %v", err)
			}
			if err := f.s.CreateCustomField(f.ctx, domain.AdministratorPermit(f.actor), cf); err != nil {
				t.Fatalf("creating with an active owner: %v", err)
			}

			if err := f.s.RetireTeam(f.ctx, domain.AdministratorPermit(f.actor), f.retiredTeamID); err != nil {
				t.Fatalf("retiring the owner team: %v", err)
			}

			row, err := f.s.GetCustomField(f.ctx, cf.ID)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			if row.OwnerTeamID == nil || *row.OwnerTeamID != f.retiredTeamID {
				t.Fatalf("the field's owner must survive the team's own retirement, got %v", row.OwnerTeamID)
			}
			if !row.OwnerRetired() {
				t.Fatal("OwnerRetired must report true once the owning team is retired")
			}
			if row.OwnerDisplay() == "" {
				t.Fatal("OwnerDisplay must still resolve something to show, not go blank")
			}

			// An UNRELATED edit -- correcting the label -- must still be
			// allowed with the owner left untouched: this is the
			// "unchanged owner" exception requireActiveOwnerTeam's own
			// comment describes, and without it a team retiring would
			// retroactively freeze every OTHER field of that team's too.
			row.Label = "Corrected Label"
			if err := f.s.UpdateCustomField(f.ctx, domain.AdministratorPermit(f.actor), &row.CustomField); err != nil {
				t.Fatalf("an unrelated edit with the owner unchanged must be allowed even though "+
					"the owner has since retired: %v", err)
			}
		})
	}
}

// TestUpdateRefusesAssigningANewlyRetiredTeamAsOwner: an operator MAY NOT
// newly choose a retired team as a field's owner, even one that already owns
// a different field -- retirement blocks being newly picked, it does not
// merely block being picked for the first time ever.
func TestUpdateRefusesAssigningANewlyRetiredTeamAsOwner(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			if err := f.s.RetireTeam(f.ctx, domain.AdministratorPermit(f.actor), f.retiredTeamID); err != nil {
				t.Fatalf("retiring the fixture team: %v", err)
			}

			row, err := f.s.GetCustomField(f.ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			retired := f.retiredTeamID
			row.OwnerTeamID = &retired
			if err := f.s.UpdateCustomField(f.ctx, domain.AdministratorPermit(f.actor), &row.CustomField); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("got %v, want domain.ErrInvalid for newly choosing a retired team", err)
			}
		})
	}
}

// TestChangingTheOwnerWritesExactlyOneChangeLogRowWhoseDiffShowsOldAndNew is
// the audit obligation CLAUDE.md states plainly: every mutation of declared
// state writes a change_log row in the same transaction, no exceptions.
// owner_team_id is a plain column on customFieldAudit's embedded
// domain.CustomField, not a folded set, so diffJSON already walks it --
// this proves that rather than assuming it.
func TestChangingTheOwnerWritesExactlyOneChangeLogRowWhoseDiffShowsOldAndNew(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			newOwner := mustCustomFieldTeam(t, f.s, f.ctx, "cf-owner-2")

			before := changeCount(t, f, "custom_field", id)

			row, err := f.s.GetCustomField(f.ctx, id)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			oldOwner := *row.OwnerTeamID
			row.OwnerTeamID = &newOwner
			if err := f.s.UpdateCustomField(f.ctx, domain.AdministratorPermit(f.actor), &row.CustomField); err != nil {
				t.Fatalf("changing the owner: %v", err)
			}

			after := changeCount(t, f, "custom_field", id)
			if after != before+1 {
				t.Fatalf("changing the owner wrote %d change_log rows, want exactly %d", after-before, 1)
			}
			diff := lastChangeDiff(t, f, "custom_field", id)
			if !strings.Contains(diff, oldOwner) || !strings.Contains(diff, newOwner) {
				t.Fatalf("the diff must show both the old and the new owner; got %s", diff)
			}
		})
	}
}
