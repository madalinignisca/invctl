// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// counterFold renders one side of a custom_fields diff the way an entity's
// audit entry now carries it: each pair as "code@n", n being
// custom_field_value.row_version at that point, joined in the same
// "code@n,code@n" shape foldCustomValues builds. kv alternates code, version
// (version given as its string form, since row_version is a small known
// integer at each call site below and a literal reads better than a cast).
//
// Callers pass the version by hand rather than reading it back from the
// store, because every version in this file is already pinned down by
// setCustomValues' own documented contract: 1 on a field's first value, +1
// on a real change, unchanged on a no-op resubmission. Getting one of these
// wrong is exactly the kind of drift a test should catch, not paper over by
// asking the code under test what it thinks the answer is.
func counterFold(kv ...string) string {
	if len(kv)%2 != 0 {
		panic("counterFold: odd number of arguments; want code, version pairs")
	}
	parts := make([]string, 0, len(kv)/2)
	for remaining := kv; len(remaining) >= 2; remaining = remaining[2:] {
		code, version := remaining[0], remaining[1]
		parts = append(parts, code+"@"+version)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// ---------- the audit fold ----------

// TestSettingAValueIsAuditedAgainstTheEntity: the asset's own columns do not
// move when a custom value changes, so without the fold there would be no diff
// and no entry at all. The exact diff is asserted rather than a substring --
// "contains IT-42" would still pass if the old value had been dropped.
func TestSettingAValueIsAuditedAgainstTheEntity(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			mustValue(t, f, id, f.assetID, "IT-41")
			mustValue(t, f, id, f.assetID, "IT-42")

			want := fmt.Sprintf(`{"custom_fields":{"old":%q,"new":%q}}`,
				counterFold("cost_centre", "1"), counterFold("cost_centre", "2"))
			if got := lastChangeDiff(t, f, "asset", f.assetID); got != want {
				t.Fatalf("the asset's audit entry must carry both the old and the new counter\n got %s\nwant %s", got, want)
			}
		})
	}
}

// TestClearingAValueIsAuditedOnTheParent: clearing removes the row -- correct,
// because custom_field_value holds the current value of something its parent
// owns, the same shape as asset_environment. But a set replacement that
// produces no diff on the parent is exactly the failure this repo has hit three
// times, so the entry is asserted by count AND by content.
func TestClearingAValueIsAuditedOnTheParent(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			mustValue(t, f, id, f.assetID, "IT-42")
			before := changeCount(t, f, "asset", f.assetID)

			mustClearValues(t, f, f.assetID, id)

			if got := changeCount(t, f, "asset", f.assetID); got != before+1 {
				t.Fatalf("clearing a value wrote %d audit rows, want 1", got-before)
			}
			want := fmt.Sprintf(`{"custom_fields":{"old":%q,"new":""}}`, counterFold("cost_centre", "1"))
			if got := lastChangeDiff(t, f, "asset", f.assetID); got != want {
				t.Fatalf("clearing must record what was removed\n got %s\nwant %s", got, want)
			}
			n, err := f.s.countOne(f.ctx,
				`SELECT COUNT(*) FROM custom_field_value WHERE entity_id = ?`, f.assetID)
			if err != nil {
				t.Fatalf("counting: %v", err)
			}
			if n != 0 {
				t.Fatalf("clearing left %d value rows, want 0", n)
			}
		})
	}
}

// TestReorderingCustomValuesIsNotAChange: the fold sorts by code before
// joining, the same reason dependencyAudit sorts its classes. A set written in
// a different order is not a change, and an audit trail that says it is teaches
// its readers to ignore it.
func TestReorderingCustomValuesIsNotAChange(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			tagID := mustField(t, f, "asset", "asset_tag", domain.CustomFieldText)
			ccID := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)

			mustSetValues(t, f, f.assetID, map[string]string{
				tagID: "ABC-1234", ccID: "IT-42",
			})
			before := changeCount(t, f, "asset", f.assetID)
			wantFold := fmt.Sprintf(`{"custom_fields":{"old":"","new":%q}}`,
				counterFold("asset_tag", "1", "cost_centre", "1"))
			if got := lastChangeDiff(t, f, "asset", f.assetID); got != wantFold {
				t.Fatalf("the fold must be sorted by code\n got %s\nwant %s", got, wantFold)
			}

			// The same pair, written the other way round.
			mustSetValues(t, f, f.assetID, map[string]string{
				ccID: "IT-42", tagID: "ABC-1234",
			})

			if got := changeCount(t, f, "asset", f.assetID); got != before {
				t.Fatalf("writing the same values in a different order wrote %d audit rows; "+
					"a reordering is not a change", got-before)
			}
		})
	}
}

// TestAServiceValueIsAuditedAgainstTheService is the other entity type: it goes
// through serviceAudit, which did not exist before this task, so a service
// value change previously had nowhere at all to be recorded.
func TestAServiceValueIsAuditedAgainstTheService(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "service", "sla_ref", domain.CustomFieldText)
			before := changeCount(t, f, "service", f.serviceID)

			mustValue(t, f, id, f.serviceID, "SLA-7")

			if got := changeCount(t, f, "service", f.serviceID); got != before+1 {
				t.Fatalf("setting a service value wrote %d audit rows, want 1", got-before)
			}
			want := fmt.Sprintf(`{"custom_fields":{"old":"","new":%q}}`, counterFold("sla_ref", "1"))
			if got := lastChangeDiff(t, f, "service", f.serviceID); got != want {
				t.Fatalf("got %s, want %s", got, want)
			}
		})
	}
}

// TestAnAuditShapeEmbedsByValue: auditFields PANICS on an anonymous pointer
// embed, because that shape silently dropped every column from certificate
// entries for a week while still writing one. Assert the shape rather than
// trusting it.
func TestAnAuditShapeEmbedsByValue(t *testing.T) {
	cases := []struct {
		name  string
		shape reflect.Type
		inner string
	}{
		{"serviceAudit", reflect.TypeOf(serviceAudit{}), "Service"},
		{"assetAudit", reflect.TypeOf(assetAudit{}), "Asset"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			field := c.shape.Field(0)
			if !field.Anonymous {
				t.Fatalf("%s must embed domain.%s anonymously", c.name, c.inner)
			}
			if field.Type.Kind() == reflect.Pointer {
				t.Fatalf("%s embeds domain.%s by POINTER; embed by value, or every "+
					"column is silently absent from every change_log entry", c.name, c.inner)
			}
			if field.Type.Name() != c.inner {
				t.Fatalf("%s embeds %s, want domain.%s", c.name, field.Type.Name(), c.inner)
			}
		})
	}
}

// TestAnAssetUpdateStillCarriesItsCustomValues: UpdateAsset does not touch
// values, so the same fold goes on both sides and cancels -- the entry must
// name the field that actually moved and nothing else.
func TestAnAssetUpdateStillCarriesItsCustomValues(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			mustValue(t, f, id, f.assetID, "IT-42")

			row, err := f.s.GetAsset(f.ctx, f.assetID)
			if err != nil {
				t.Fatalf("reading the asset: %v", err)
			}
			serial := "SN-9"
			row.Serial = &serial
			if err := f.s.UpdateAsset(f.ctx, domain.AdministratorPermit(f.actor), &row.Asset, nil); err != nil {
				t.Fatalf("updating the asset: %v", err)
			}

			want := `{"serial":{"old":null,"new":"SN-9"}}`
			if got := lastChangeDiff(t, f, "asset", f.assetID); got != want {
				t.Fatalf("an unrelated edit must not report the custom values as changed\n got %s\nwant %s", got, want)
			}
		})
	}
}

// ---------- what a value is allowed to be ----------

func TestAValueForAnUnknownFieldIsRefused(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			err := f.s.SetCustomValues(f.ctx, domain.AdministratorPermit(f.actor), "asset", f.assetID,
				entityVersion(t, f, "asset", f.assetID), map[string]string{NewID(): "IT-42"})
			if err == nil {
				t.Fatal("a value for a field that does not exist must be refused")
			}
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("want ErrNotFound, got %v", err)
			}
		})
	}
}

// TestAValueForTheWrongEntityTypeIsRefused: entity_type lives on the definition
// only, so nothing in the schema would catch an asset field being written
// against a service. This check is the only thing that does.
func TestAValueForTheWrongEntityTypeIsRefused(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			assetField := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)

			err := f.s.SetCustomValues(f.ctx, domain.AdministratorPermit(f.actor), "service", f.serviceID,
				entityVersion(t, f, "service", f.serviceID), map[string]string{assetField: "IT-42"})
			if err == nil {
				t.Fatal("an asset field must not accept a value against a service")
			}
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("want ErrNotFound, got %v", err)
			}
		})
	}
}

// TestARetiredFieldTakesNoNewValue: retiring keeps every value already written
// (design.md §6) and refuses a new one, the same rule a retired select option
// follows.
func TestARetiredFieldTakesNoNewValue(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			mustValue(t, f, id, f.assetID, "IT-42")
			if err := f.s.RetireCustomField(f.ctx, domain.AdministratorPermit(f.actor), id); err != nil {
				t.Fatalf("retiring: %v", err)
			}

			err := f.s.SetCustomValues(f.ctx, domain.AdministratorPermit(f.actor), "asset", f.secondAssetID,
				entityVersion(t, f, "asset", f.secondAssetID), map[string]string{id: "IT-99"})
			if err == nil {
				t.Fatal("a retired field must take no new value")
			}
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}

			// The value written before retirement is untouched.
			values, err := f.s.CustomValuesFor(f.ctx, "asset", f.assetID)
			if err != nil {
				t.Fatalf("reading values: %v", err)
			}
			if len(values) != 1 {
				t.Fatalf("got %d values after retirement, want 1", len(values))
			}
			if values[0].ValueText != "IT-42" {
				t.Fatalf("got %q, want IT-42", values[0].ValueText)
			}
			if !values[0].Retired {
				t.Fatal("the value must report its definition as retired")
			}
		})
	}
}

// TestASelectValueMustBeALiveOption covers both halves of the option rule: a
// live option is accepted, a retired one is refused for a NEW value.
func TestASelectValueMustBeALiveOption(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "tier", domain.CustomFieldSelect)
			mustOptions(t, f, id, "gold", "silver")

			mustValue(t, f, id, f.assetID, "gold")
			values, err := f.s.CustomValuesFor(f.ctx, "asset", f.assetID)
			if err != nil {
				t.Fatalf("reading values: %v", err)
			}
			if len(values) != 1 || values[0].ValueText != "gold" {
				t.Fatalf("got %v, want a single value of gold", values)
			}

			if err := f.s.SetCustomValues(f.ctx, domain.AdministratorPermit(f.actor), "asset", f.secondAssetID,
				entityVersion(t, f, "asset", f.secondAssetID), map[string]string{id: "bronze"}); err == nil {
				t.Fatal("a value that is not an option at all must be refused")
			}

			// Retire "silver" by submitting only "gold".
			if err := f.s.SetCustomFieldOptions(f.ctx, domain.AdministratorPermit(f.actor), id, fieldVersion(t, f, id),
				[]domain.CustomFieldOption{{Value: "gold", Label: "Gold"}}); err != nil {
				t.Fatalf("retiring silver: %v", err)
			}
			if err := f.s.SetCustomValues(f.ctx, domain.AdministratorPermit(f.actor), "asset", f.secondAssetID,
				entityVersion(t, f, "asset", f.secondAssetID), map[string]string{id: "silver"}); err == nil {
				t.Fatal("a retired option must take no new value")
			}
		})
	}
}

// TestCustomValuesAreCanonicalised proves the store validates through the
// domain constructor rather than storing whatever arrived. The refusals matter
// as much as the acceptances: value_text ends up in a CSV export and in an
// audit diff, where "1,234" would be two columns.
func TestCustomValuesAreCanonicalised(t *testing.T) {
	cases := []struct {
		name    string
		kind    string
		raw     string
		want    string
		refused bool
	}{
		{"text is trimmed", domain.CustomFieldText, "  IT-42  ", "IT-42", false},
		{"a control character is refused", domain.CustomFieldText, "IT\x0042", "", true},
		{"a number keeps the operator's decimals", domain.CustomFieldNumber, "42.50", "42.50", false},
		{"a grouped number is refused", domain.CustomFieldNumber, "1,234", "", true},
		{"a date must be real", domain.CustomFieldDate, "2026-02-30", "", true},
		{"a real date is stored as typed", domain.CustomFieldDate, "2026-02-28", "2026-02-28", false},
		{"a boolean is normalised", domain.CustomFieldBoolean, "TRUE", "true", false},
		{"a non-boolean is refused", domain.CustomFieldBoolean, "yes", "", true},
	}
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					f := newCustomFieldFixture(t, e)
					id := mustField(t, f, "asset", "probe", c.kind)

					err := f.s.SetCustomValues(f.ctx, domain.AdministratorPermit(f.actor), "asset", f.assetID,
						entityVersion(t, f, "asset", f.assetID), map[string]string{id: c.raw})
					if c.refused {
						if err == nil {
							t.Fatalf("%q must be refused for a %s field", c.raw, c.kind)
						}
						return
					}
					if err != nil {
						t.Fatalf("setting %q: %v", c.raw, err)
					}
					values, err := f.s.CustomValuesFor(f.ctx, "asset", f.assetID)
					if err != nil {
						t.Fatalf("reading values: %v", err)
					}
					if len(values) != 1 {
						t.Fatalf("got %d values, want 1", len(values))
					}
					if values[0].ValueText != c.want {
						t.Fatalf("got %q, want %q", values[0].ValueText, c.want)
					}
				})
			}
		})
	}
}

// TestABlankValueClearsThatFieldOnly: a form posts every field it renders, so
// "" is how a person says "nothing here" -- and it must not disturb the rest.
func TestABlankValueClearsThatFieldOnly(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			tagID := mustField(t, f, "asset", "asset_tag", domain.CustomFieldText)
			ccID := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			mustSetValues(t, f, f.assetID, map[string]string{tagID: "ABC-1234", ccID: "IT-42"})

			mustSetValues(t, f, f.assetID, map[string]string{tagID: "ABC-1234", ccID: "   "})

			values, err := f.s.CustomValuesFor(f.ctx, "asset", f.assetID)
			if err != nil {
				t.Fatalf("reading values: %v", err)
			}
			if len(values) != 1 {
				t.Fatalf("got %d values, want 1", len(values))
			}
			if values[0].FieldID != tagID || values[0].ValueText != "ABC-1234" {
				t.Fatalf("got %+v, want the asset_tag value intact", values[0])
			}
			want := fmt.Sprintf(`{"custom_fields":{"old":%q,"new":%q}}`,
				counterFold("asset_tag", "1", "cost_centre", "1"), counterFold("asset_tag", "1"))
			if got := lastChangeDiff(t, f, "asset", f.assetID); got != want {
				t.Fatalf("got %s, want %s", got, want)
			}
		})
	}
}

// TestOneEntitysValuesAreNotAnothers: the replacement is scoped to the entity,
// which is easy to get wrong in a DELETE and expensive to discover.
func TestOneEntitysValuesAreNotAnothers(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			mustValue(t, f, id, f.assetID, "IT-42")
			mustValue(t, f, id, f.secondAssetID, "IT-99")

			mustClearValues(t, f, f.assetID, id)

			values, err := f.s.CustomValuesFor(f.ctx, "asset", f.secondAssetID)
			if err != nil {
				t.Fatalf("reading values: %v", err)
			}
			if len(values) != 1 || values[0].ValueText != "IT-99" {
				t.Fatalf("got %v, want the second asset's value untouched", values)
			}
		})
	}
}

// TestCustomValuesForResolvesTheDefinition: a detail page renders label and
// kind beside the value without a query per row.
func TestCustomValuesForResolvesTheDefinition(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			ccID := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			tagID := mustField(t, f, "asset", "asset_tag", domain.CustomFieldText)
			mustSetValues(t, f, f.assetID, map[string]string{ccID: "IT-42", tagID: "ABC-1234"})

			values, err := f.s.CustomValuesFor(f.ctx, "asset", f.assetID)
			if err != nil {
				t.Fatalf("reading values: %v", err)
			}
			if len(values) != 2 {
				t.Fatalf("got %d values, want 2", len(values))
			}
			// Ordered by code: asset_tag before cost_centre.
			if values[0].Code != "asset_tag" || values[1].Code != "cost_centre" {
				t.Fatalf("got %q then %q, want them ordered by code", values[0].Code, values[1].Code)
			}
			if values[0].Kind != domain.CustomFieldText {
				t.Fatalf("got kind %q, want text", values[0].Kind)
			}
			if values[0].Label != "asset_tag" {
				t.Fatalf("got label %q, want asset_tag", values[0].Label)
			}
			if values[0].Retired {
				t.Fatal("a live field's value must not report as retired")
			}
		})
	}
}

// TestAValueForAnUnknownEntityIsRefused: an entity that does not exist gets a
// 404, never a created row. custom_field_value carries no foreign key to asset
// or service -- entity_type lives on the definition -- so nothing but this
// check stands between a bad id and an orphan.
func TestAValueForAnUnknownEntityIsRefused(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			ghost := NewID()

			err := f.s.SetCustomValues(f.ctx, domain.AdministratorPermit(f.actor), "asset", ghost, 1,
				map[string]string{id: "IT-42"})
			if err == nil {
				t.Fatal("a value for an asset that does not exist must be refused")
			}
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("want ErrNotFound, got %v", err)
			}
			n, err := f.s.countOne(f.ctx,
				`SELECT COUNT(*) FROM custom_field_value WHERE entity_id = ?`, ghost)
			if err != nil {
				t.Fatalf("counting: %v", err)
			}
			if n != 0 {
				t.Fatalf("the refused write left %d rows behind, want 0", n)
			}
		})
	}
}

// TestSetCustomValuesRefusesAnEntityTypeWithNoFields: assets and services only,
// per design.md §1. Widening it later is a CHECK constraint and a template
// include, not a silent acceptance here.
func TestSetCustomValuesRefusesAnUnknownEntityType(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			err := f.s.SetCustomValues(f.ctx, domain.AdministratorPermit(f.actor), "project", f.assetID, 1, nil)
			if err == nil {
				t.Fatal("only assets and services hold custom fields")
			}
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
		})
	}
}

// ---------- CONTROLLER RULING Q: the guard is only real if the value writer reads back ----------

// TestAKindChangeAbortsAgainstAConcurrentValueWrite is the test that proves
// Task 3's writeSerializable guard on UpdateCustomField is not decorative.
//
// WHY IT IS SHAPED LIKE THIS. PostgreSQL's SSI aborts a transaction only when
// it finds a CYCLE of two rw-antidependency edges. UpdateCustomField's count of
// custom_field_value supplies ONE edge against a concurrent insert; a writer
// that reads nothing supplies no second edge and both transactions commit, at
// any interleaving. So this is not about timing at all, and a goroutine race
// would prove nothing (it was tried: 0 failures in 700 trials against genuinely
// broken code, because there was no dangerous structure to find).
//
// ON THIS SCHEMA THE SECOND EDGE HAS TWO SOURCES, which a probe established
// rather than assumed. customFieldDefs reads custom_field inside the writer's
// serializable transaction, and so does the foreign key check behind
// custom_field_value.field_id REFERENCES custom_field(id). Dropping that
// constraint and repeating the probe with a blind insert commits with no abort;
// with it, even a blind insert aborts. This test therefore asserts the OUTCOME
// -- the kind change cannot commit -- rather than pinning which of the two
// supplies it, because either one disappearing is a regression and either one
// remaining keeps the invariant true.
//
// Two manually sequenced transactions under explicit control flow instead. T1
// is UpdateCustomField's guard, written out by hand because the real method
// cannot be paused mid-transaction. T2 is the REAL value writer, which is the
// whole point: it is SetCustomValues that has to supply the second edge, and it
// does so only because customFieldDefs reads custom_field inside its own
// serializable transaction.
//
// PostgreSQL only. SQLite serialises writes on a single connection by
// construction and has no SSI to exercise; holding T1 open there would simply
// deadlock against the writer pool.
func TestAKindChangeAbortsAgainstAConcurrentValueWrite(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			if e.Name != DriverPostgres {
				t.Skip("SQLite serialises writes on a single connection by construction " +
					"and has no SSI to exercise; holding T1 open here would deadlock " +
					"against the writer pool rather than test anything")
			}
			f := newCustomFieldFixture(t, e)
			fieldID := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			raw := f.s.DB().Writer
			rebind := raw.Rebind

			// T1: UpdateCustomField's guard. Begin, read the value count,
			// and HOLD.
			t1, err := raw.BeginTx(f.ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
			if err != nil {
				t.Fatalf("beginning T1: %v", err)
			}
			defer t1.Rollback()
			var n int
			if err := t1.QueryRowContext(f.ctx, rebind(
				`SELECT COUNT(*) FROM custom_field_value WHERE field_id = ?`),
				fieldID).Scan(&n); err != nil {
				t.Fatalf("T1 counting values: %v", err)
			}
			if n != 0 {
				t.Fatalf("T1 saw %d values, want 0: the guard would refuse for the wrong reason", n)
			}

			// T2: the real value writer, start to finish, committed.
			mustValue(t, f, fieldID, f.assetID, "IT-42")

			// T1 resumes and tries to commit the kind change its stale count
			// said was safe.
			_, err = t1.ExecContext(f.ctx, rebind(
				`UPDATE custom_field SET kind = ?, row_version = row_version + 1 WHERE id = ?`),
				domain.CustomFieldNumber, fieldID)
			if err == nil {
				err = t1.Commit()
			}
			if err == nil {
				t.Fatal("the kind change committed against a value written concurrently: " +
					"Task 3's serializable guard is INERT, because the value writer supplied " +
					"no rw-antidependency edge back. SetCustomValues must read custom_field " +
					"INSIDE its own serializable transaction -- see customFieldDefs.")
			}
			if !isSerializationFailure(err) {
				t.Fatalf("want a serialization failure (SQLSTATE 40001), got %v", err)
			}

			// And the value survived: T2 is the transaction that committed.
			values, err := f.s.CustomValuesFor(f.ctx, "asset", f.assetID)
			if err != nil {
				t.Fatalf("reading values: %v", err)
			}
			if len(values) != 1 || values[0].ValueText != "IT-42" {
				t.Fatalf("got %v, want the concurrently written value to survive", values)
			}
		})
	}
}

// ---------- retention: what a wholesale replacement must NOT destroy ----------

// TestARetiredFieldsValueSurvivesALaterEdit is the Critical this task shipped
// and a review caught.
//
// The delete list was built from customFieldDefs, which deliberately includes
// RETIRED fields so the fold can resolve their codes. So the next unrelated edit
// wiped every retained value of every retired field, and the insert loop could
// never put them back because a retired field takes no new value. Measured
// before the fix: the retired field's value rows went to 0 and the audit entry
// read as an operator clearing a value, which nobody did.
//
// docs/custom-fields-design.md §6 is unambiguous -- "Retiring a field deletes no
// value, ever" -- and Restore is supposed to bring the field AND every value
// back, not an empty field.
func TestARetiredFieldsValueSurvivesALaterEdit(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			tagID := mustField(t, f, "asset", "asset_tag", domain.CustomFieldText)
			ccID := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			mustSetValues(t, f, f.assetID, map[string]string{tagID: "ABC-1", ccID: "IT-42"})

			if err := f.s.RetireCustomField(f.ctx, domain.AdministratorPermit(f.actor), ccID); err != nil {
				t.Fatalf("retiring cost_centre: %v", err)
			}

			// The operator edits asset_tag only. The retired field is not on
			// the form at all, so it is not in the map.
			mustSetValues(t, f, f.assetID, map[string]string{tagID: "ABC-2"})

			values, err := f.s.CustomValuesFor(f.ctx, "asset", f.assetID)
			if err != nil {
				t.Fatalf("reading values: %v", err)
			}
			if len(values) != 2 {
				t.Fatalf("got %d values, want 2: the retired field's value was destroyed "+
					"by an edit to a different field", len(values))
			}
			if values[0].Code != "asset_tag" || values[0].ValueText != "ABC-2" {
				t.Fatalf("got %s=%s, want asset_tag=ABC-2", values[0].Code, values[0].ValueText)
			}
			if values[1].Code != "cost_centre" || values[1].ValueText != "IT-42" {
				t.Fatalf("got %s=%s, want cost_centre=IT-42 retained", values[1].Code, values[1].ValueText)
			}
			if !values[1].Retired {
				t.Fatal("the retained value's field must report as retired")
			}

			// And the audit entry says the retired value did not move.
			want := fmt.Sprintf(`{"custom_fields":{"old":%q,"new":%q}}`,
				counterFold("asset_tag", "1", "cost_centre", "1"), counterFold("asset_tag", "2", "cost_centre", "1"))
			if got := lastChangeDiff(t, f, "asset", f.assetID); got != want {
				t.Fatalf("the entry must show only what moved\n got %s\nwant %s", got, want)
			}

			// Restore brings back a field that still holds its value.
			if err := f.s.RestoreCustomField(f.ctx, domain.AdministratorPermit(f.actor), ccID); err != nil {
				t.Fatalf("restoring: %v", err)
			}
			restored, err := f.s.GetCustomField(f.ctx, ccID)
			if err != nil {
				t.Fatalf("reading the restored field: %v", err)
			}
			if restored.UsageCount != 1 {
				t.Fatalf("the restored field holds %d values, want 1", restored.UsageCount)
			}
		})
	}
}

// TestAValueOnARetiredOptionSurvivesALaterEdit is the same root cause wearing a
// different face: the field is LIVE, so scoping the replacement to live fields
// does nothing for it.
//
// §3 says a retired select option "keeps displaying" on the values that already
// chose it while no NEW value may select it. This test proves the STORE half of
// that: setCustomValues must accept an unchanged resubmission of a value naming
// a retired option, alongside an edit to a field the operator actually touched,
// rather than either rejecting the whole submission over a field nobody meant to
// change, or silently dropping the retained value.
//
// PREMISE CORRECTED, FINAL REVIEW B1: this comment used to say "the form renders
// the retained value and posts it back unchanged" as an established fact. It was
// not -- no form could reach this path at all, because the value editor built its
// picker from LIVE options only, so a value naming a retired one matched no
// <option>, the browser fell back to its own blank "not set" choice, and the very
// next unrelated save posted that blank back as an explicit clear. This store
// method was already correct; nothing above it could hand it the case it handles.
// The form fix (loadCustomFieldsPanel appending the current value's retired
// option, marked as retired) is what makes the sentence above true today, and
// TestEveryStoredCustomValueSurvivesARoundTripThroughItsWidget
// (internal/web/customfield_roundtrip_test.go) is what proves it at the widget.
// This test stays: it is the store-level guarantee the form fix depends on.
func TestAValueOnARetiredOptionSurvivesALaterEdit(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			tierID := mustField(t, f, "asset", "tier", domain.CustomFieldSelect)
			mustOptions(t, f, tierID, "gold", "silver")
			tagID := mustField(t, f, "asset", "asset_tag", domain.CustomFieldText)
			mustSetValues(t, f, f.assetID, map[string]string{tierID: "silver", tagID: "ABC-1"})

			// Retire "silver" by offering only "gold".
			if err := f.s.SetCustomFieldOptions(f.ctx, domain.AdministratorPermit(f.actor), tierID, fieldVersion(t, f, tierID),
				[]domain.CustomFieldOption{{Value: "gold", Label: "Gold"}}); err != nil {
				t.Fatalf("retiring the silver option: %v", err)
			}

			// The form renders the retained value and posts it back unchanged
			// alongside the field the operator actually edited.
			mustSetValues(t, f, f.assetID, map[string]string{tierID: "silver", tagID: "ABC-2"})

			values, err := f.s.CustomValuesFor(f.ctx, "asset", f.assetID)
			if err != nil {
				t.Fatalf("reading values: %v", err)
			}
			if len(values) != 2 {
				t.Fatalf("got %d values, want 2", len(values))
			}
			if values[1].Code != "tier" || values[1].ValueText != "silver" {
				t.Fatalf("got %s=%s, want tier=silver retained", values[1].Code, values[1].ValueText)
			}

			// A genuinely NEW value on the retired option is still refused.
			if err := f.s.SetCustomValues(f.ctx, domain.AdministratorPermit(f.actor), "asset", f.secondAssetID,
				entityVersion(t, f, "asset", f.secondAssetID),
				map[string]string{tierID: "silver"}); err == nil {
				t.Fatal("a NEW value selecting a retired option must still be refused")
			}
		})
	}
}

// TestAnUnchangedValueKeepsItsRowIdentity: the row is not churned. Same id, same
// created_at, same row_version -- "when was this first set" is a fact about the
// value and a replacement is a mechanism, not a new beginning.
func TestAnUnchangedValueKeepsItsRowIdentity(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			mustValue(t, f, id, f.assetID, "IT-42")
			first, err := f.s.CustomValuesFor(f.ctx, "asset", f.assetID)
			if err != nil {
				t.Fatalf("reading values: %v", err)
			}

			mustValue(t, f, id, f.assetID, "IT-42")
			again, err := f.s.CustomValuesFor(f.ctx, "asset", f.assetID)
			if err != nil {
				t.Fatalf("re-reading values: %v", err)
			}
			if again[0].ID != first[0].ID {
				t.Fatalf("the id changed on a no-op save: %s then %s", first[0].ID, again[0].ID)
			}
			if again[0].CreatedAt != first[0].CreatedAt {
				t.Fatalf("created_at changed on a no-op save: %s then %s",
					first[0].CreatedAt, again[0].CreatedAt)
			}
			if again[0].RowVersion != first[0].RowVersion {
				t.Fatalf("row_version moved on a no-op save: %d then %d",
					first[0].RowVersion, again[0].RowVersion)
			}

			// A real change carries the id and created_at across the
			// replacement and advances the version.
			mustValue(t, f, id, f.assetID, "IT-99")
			changed, err := f.s.CustomValuesFor(f.ctx, "asset", f.assetID)
			if err != nil {
				t.Fatalf("re-reading values: %v", err)
			}
			if changed[0].ID != first[0].ID {
				t.Fatalf("a changed value got a new id: %s then %s", first[0].ID, changed[0].ID)
			}
			if changed[0].CreatedAt != first[0].CreatedAt {
				t.Fatalf("a changed value lost its created_at: %s then %s",
					first[0].CreatedAt, changed[0].CreatedAt)
			}
			if changed[0].RowVersion != first[0].RowVersion+1 {
				t.Fatalf("got row_version %d, want %d", changed[0].RowVersion, first[0].RowVersion+1)
			}
		})
	}
}

// ---------- RULING R: the token is the parent entity's ----------

// TestASecondSaveFromOneTokenIsRefused is invariant 4 for this editor.
//
// design.md §3 as amended: the value editor's token is the parent asset or
// service's row_version, not each value's. Two operators with the same asset
// page open must not silently overwrite each other's whole set, and
// TestEveryEditorRefusesASecondSaveFromOneToken submits every editor's form
// twice from one token and requires the second to be refused -- so the bump is
// unconditional, including on a save that changes nothing.
func TestASecondSaveFromOneTokenIsRefused(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			token := entityVersion(t, f, "asset", f.assetID)

			if err := f.s.SetCustomValues(f.ctx, domain.AdministratorPermit(f.actor), "asset", f.assetID, token,
				map[string]string{id: "IT-42"}); err != nil {
				t.Fatalf("the first save from a fresh token must succeed: %v", err)
			}
			if got := entityVersion(t, f, "asset", f.assetID); got != token+1 {
				t.Fatalf("got version %d after one save, want %d", got, token+1)
			}

			err := f.s.SetCustomValues(f.ctx, domain.AdministratorPermit(f.actor), "asset", f.assetID, token,
				map[string]string{id: "IT-99"})
			if err == nil {
				t.Fatal("a second save from one token must be refused")
			}
			if !errors.Is(err, domain.ErrStale) {
				t.Fatalf("want ErrStale, got %v", err)
			}
			values, err := f.s.CustomValuesFor(f.ctx, "asset", f.assetID)
			if err != nil {
				t.Fatalf("reading values: %v", err)
			}
			if len(values) != 1 || values[0].ValueText != "IT-42" {
				t.Fatalf("got %v, want the stale write to have changed nothing", values)
			}
		})
	}
}

// TestASaveThatChangesNothingStillMovesTheToken: the bump is unconditional.
// Conditional on a diff, the second identical submission of one form would be
// accepted and the guard would read as present while being inert -- which is
// precisely the defect TestEveryEditorRefusesASecondSaveFromOneToken exists to
// catch, and it submits the same form twice.
func TestASaveThatChangesNothingStillMovesTheToken(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			mustValue(t, f, id, f.assetID, "IT-42")

			token := entityVersion(t, f, "asset", f.assetID)
			changes := changeCount(t, f, "asset", f.assetID)

			if err := f.s.SetCustomValues(f.ctx, domain.AdministratorPermit(f.actor), "asset", f.assetID, token,
				map[string]string{id: "IT-42"}); err != nil {
				t.Fatalf("re-saving the same value: %v", err)
			}

			if got := entityVersion(t, f, "asset", f.assetID); got != token+1 {
				t.Fatalf("got version %d, want %d: a no-op save must still move the token", got, token+1)
			}
			if got := changeCount(t, f, "asset", f.assetID); got != changes {
				t.Fatalf("a no-op save wrote %d change_log rows, want 0", got-changes)
			}
			if err := f.s.SetCustomValues(f.ctx, domain.AdministratorPermit(f.actor), "asset", f.assetID, token,
				map[string]string{id: "IT-42"}); !errors.Is(err, domain.ErrStale) {
				t.Fatalf("the same token twice must be refused, got %v", err)
			}
		})
	}
}

// TestAServiceValueEditCarriesTheSameToken: both entity types, one rule.
func TestAServiceValueEditCarriesTheSameToken(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "service", "sla_ref", domain.CustomFieldText)
			token := entityVersion(t, f, "service", f.serviceID)

			if err := f.s.SetCustomValues(f.ctx, domain.AdministratorPermit(f.actor), "service", f.serviceID, token,
				map[string]string{id: "SLA-7"}); err != nil {
				t.Fatalf("first save: %v", err)
			}
			if got := entityVersion(t, f, "service", f.serviceID); got != token+1 {
				t.Fatalf("got version %d, want %d", got, token+1)
			}
			if err := f.s.SetCustomValues(f.ctx, domain.AdministratorPermit(f.actor), "service", f.serviceID, token,
				map[string]string{id: "SLA-8"}); !errors.Is(err, domain.ErrStale) {
				t.Fatalf("want ErrStale from a stale token, got %v", err)
			}
		})
	}
}

// TestARestoreBetweenRenderAndSubmitDestroysNothing is RULING U, and it is the
// second instance of the shape the Critical was about.
//
// The old contract inferred a clear from ABSENCE, which rests on "a form posts
// every field it renders". That holds only if the form's field set equals the
// writer's field set, and the two are computed at different moments. §6 says a
// retired field is absent from forms, so it cannot be in the operator's map --
// and if an administrator RESTORES it between render and submit, the field is
// live at write time, absent from the map, and was therefore deleted.
//
// The parent's token cannot close this, which is what makes it structural:
// RestoreCustomField bumps custom_field.row_version, not the asset's, so the
// operator's token is still current and the write is accepted. Measured before
// the fix: submit succeeded, cost_centre=IT-42 was hard-deleted, and the audit
// entry attributed the clearing to an operator who never saw the field.
func TestARestoreBetweenRenderAndSubmitDestroysNothing(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			tagID := mustField(t, f, "asset", "asset_tag", domain.CustomFieldText)
			ccID := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			mustSetValues(t, f, f.assetID, map[string]string{tagID: "ABC-1", ccID: "IT-42"})

			if err := f.s.RetireCustomField(f.ctx, domain.AdministratorPermit(f.actor), ccID); err != nil {
				t.Fatalf("retiring cost_centre: %v", err)
			}

			// RENDER: the form is built now. cost_centre is retired, so §6 says
			// it is not on it, so it is not in what the operator will post.
			submitted := map[string]string{tagID: "ABC-2"}
			token := entityVersion(t, f, "asset", f.assetID)

			// An administrator restores the field in the window.
			if err := f.s.RestoreCustomField(f.ctx, domain.AdministratorPermit(f.actor), ccID); err != nil {
				t.Fatalf("restoring cost_centre: %v", err)
			}
			// The operator's token is STILL CURRENT -- the restore moved
			// custom_field.row_version, not the asset's. This is the assertion
			// that says the token cannot be the thing protecting the value.
			if got := entityVersion(t, f, "asset", f.assetID); got != token {
				t.Fatalf("the asset's version moved to %d during the restore; this test "+
					"is no longer exercising the window it was written for", got)
			}

			// SUBMIT.
			if err := f.s.SetCustomValues(f.ctx, domain.AdministratorPermit(f.actor), "asset", f.assetID, token, submitted); err != nil {
				t.Fatalf("submitting: %v", err)
			}

			values, err := f.s.CustomValuesFor(f.ctx, "asset", f.assetID)
			if err != nil {
				t.Fatalf("reading values: %v", err)
			}
			if len(values) != 2 {
				t.Fatalf("got %d values, want 2: a field restored between render and "+
					"submit had its value destroyed by an edit to a different field", len(values))
			}
			if values[0].Code != "asset_tag" || values[0].ValueText != "ABC-2" {
				t.Fatalf("got %s=%s, want asset_tag=ABC-2", values[0].Code, values[0].ValueText)
			}
			if values[1].Code != "cost_centre" || values[1].ValueText != "IT-42" {
				t.Fatalf("got %s=%s, want cost_centre=IT-42 intact", values[1].Code, values[1].ValueText)
			}
			want := fmt.Sprintf(`{"custom_fields":{"old":%q,"new":%q}}`,
				counterFold("asset_tag", "1", "cost_centre", "1"), counterFold("asset_tag", "2", "cost_centre", "1"))
			if got := lastChangeDiff(t, f, "asset", f.assetID); got != want {
				t.Fatalf("the entry must show only what the operator moved\n got %s\nwant %s", got, want)
			}
		})
	}
}

// TestAnAbsentFieldIsUntouchedAndABlankOneIsCleared states the contract Tasks 5
// and 6 build against, as a test rather than only as a doc comment: absent means
// untouched, an explicit blank means clear. Both live fields here, so neither
// retirement nor restoration is doing the work.
func TestAnAbsentFieldIsUntouchedAndABlankOneIsCleared(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			tagID := mustField(t, f, "asset", "asset_tag", domain.CustomFieldText)
			ccID := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			mustSetValues(t, f, f.assetID, map[string]string{tagID: "ABC-1", ccID: "IT-42"})

			// Absent: untouched.
			mustSetValues(t, f, f.assetID, map[string]string{tagID: "ABC-2"})
			values, err := f.s.CustomValuesFor(f.ctx, "asset", f.assetID)
			if err != nil {
				t.Fatalf("reading values: %v", err)
			}
			if len(values) != 2 {
				t.Fatalf("got %d values, want 2: an absent field must be untouched", len(values))
			}
			if values[1].ValueText != "IT-42" {
				t.Fatalf("got cost_centre=%q, want IT-42", values[1].ValueText)
			}

			// Explicitly blank: cleared.
			mustSetValues(t, f, f.assetID, map[string]string{ccID: ""})
			values, err = f.s.CustomValuesFor(f.ctx, "asset", f.assetID)
			if err != nil {
				t.Fatalf("re-reading values: %v", err)
			}
			if len(values) != 1 {
				t.Fatalf("got %d values, want 1: an explicit blank must clear", len(values))
			}
			if values[0].Code != "asset_tag" || values[0].ValueText != "ABC-2" {
				t.Fatalf("got %s=%s, want asset_tag=ABC-2 left alone", values[0].Code, values[0].ValueText)
			}
		})
	}
}

// TestARetiredValueRePostedUnchangedInADifferentCaseIsAccepted is RULING V.
//
// The retired refusal sat above the post-canonicalisation equality check, so
// that comparison was unreachable on the retired path: a retired boolean holding
// "true", re-posted as "TRUE", refused the WHOLE submission over a field the
// operator never touched and could not clear. It contradicted the rule the same
// function states -- an unchanged value is not a new value.
func TestARetiredValueRePostedUnchangedInADifferentCaseIsAccepted(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			flagID := mustField(t, f, "asset", "pci_scope", domain.CustomFieldBoolean)
			tagID := mustField(t, f, "asset", "asset_tag", domain.CustomFieldText)
			mustSetValues(t, f, f.assetID, map[string]string{flagID: "true", tagID: "ABC-1"})
			if err := f.s.RetireCustomField(f.ctx, domain.AdministratorPermit(f.actor), flagID); err != nil {
				t.Fatalf("retiring pci_scope: %v", err)
			}

			// Canonicalisation collapses "TRUE" onto the stored "true", so this
			// is not a new value and must not be refused.
			if err := f.s.SetCustomValues(f.ctx, domain.AdministratorPermit(f.actor), "asset", f.assetID,
				entityVersion(t, f, "asset", f.assetID),
				map[string]string{flagID: "TRUE", tagID: "ABC-2"}); err != nil {
				t.Fatalf("re-posting an unchanged retired value in a different case: %v", err)
			}

			values, err := f.s.CustomValuesFor(f.ctx, "asset", f.assetID)
			if err != nil {
				t.Fatalf("reading values: %v", err)
			}
			if len(values) != 2 {
				t.Fatalf("got %d values, want 2", len(values))
			}
			if values[1].Code != "pci_scope" || values[1].ValueText != "true" {
				t.Fatalf("got %s=%s, want pci_scope=true retained", values[1].Code, values[1].ValueText)
			}

			// A genuinely different value on the retired field is still refused.
			if err := f.s.SetCustomValues(f.ctx, domain.AdministratorPermit(f.actor), "asset", f.assetID,
				entityVersion(t, f, "asset", f.assetID),
				map[string]string{flagID: "false"}); err == nil {
				t.Fatal("a NEW value for a retired field must still be refused")
			}
		})
	}
}

// TestAClearAllLeavesARetiredFieldsRetainedValue is RULING Y, and it is the
// fourth instance of the shape -- this one in the RECIPE the previous fix
// installed rather than in the store.
//
// setCustomValues is correct in isolation: it deletes only what the submission
// explicitly cleared. So the destruction moved one layer up, into how a caller
// builds that submission. Enumerating CustomValuesFor -- which deliberately
// returns retired fields' retained values alongside live ones, because a detail
// page has to render them -- posts an explicit blank for rows the operator was
// never shown and cannot decline to clear. The enumeration step launders "I did
// not see this" into "I instructed you to delete this", and the store then
// obeys, correctly, an instruction nobody gave.
//
// Measured with the payload built that way: 0 values remained, and the diff read
// `{"custom_fields":{"old":"asset_tag=ABC-1,cost_centre=IT-42","new":""}}` --
// the Critical verbatim.
//
// A clear-all must enumerate the LIVE FIELD LIST, which is also all a real form
// can do: it renders the live fields and posts what it rendered.
func TestAClearAllLeavesARetiredFieldsRetainedValue(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			tagID := mustField(t, f, "asset", "asset_tag", domain.CustomFieldText)
			ccID := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			mustSetValues(t, f, f.assetID, map[string]string{tagID: "ABC-1", ccID: "IT-42"})

			if err := f.s.RetireCustomField(f.ctx, domain.AdministratorPermit(f.actor), ccID); err != nil {
				t.Fatalf("retiring cost_centre: %v", err)
			}

			// The operator clears everything they can see. cost_centre is
			// retired, so it was not on their screen and is not in their post.
			mustClearValues(t, f, f.assetID, tagID)

			values, err := f.s.CustomValuesFor(f.ctx, "asset", f.assetID)
			if err != nil {
				t.Fatalf("reading values: %v", err)
			}
			if len(values) != 1 {
				t.Fatalf("got %d values, want 1: a clear-all built from the entity's "+
					"existing values destroyed a retired field's retained value, which "+
					"the operator was never shown", len(values))
			}
			if values[0].Code != "cost_centre" || values[0].ValueText != "IT-42" {
				t.Fatalf("got %s=%s, want cost_centre=IT-42 retained",
					values[0].Code, values[0].ValueText)
			}
			if !values[0].Retired {
				t.Fatal("the retained value's field must report as retired")
			}

			want := fmt.Sprintf(`{"custom_fields":{"old":%q,"new":%q}}`,
				counterFold("asset_tag", "1", "cost_centre", "1"), counterFold("cost_centre", "1"))
			if got := lastChangeDiff(t, f, "asset", f.assetID); got != want {
				t.Fatalf("the entry must show only the value the operator cleared\n got %s\nwant %s", got, want)
			}
		})
	}
}

// TestAFieldCreatedBetweenRenderAndSubmitIsNotCleared is RULING AA, and it is
// the sixth instance of the shape -- in the recipe again, one column over from
// the fifth.
//
// Ruling Y moved the clear-all off "every value held" and onto "every live
// field". That swapped rows the operator never saw for FIELDS the operator never
// saw: a field another administrator creates between render and submit is live
// at submit time, so a submit-time enumeration posts an explicit blank for it,
// and the store correctly obeys an instruction nobody gave. Measured with the
// payload built that way: 0 values remained, and the diff read
// `{"custom_fields":{"old":"asset_tag=ABC-1,cost_centre=IT-42","new":""}}` --
// the Critical verbatim, for the fourth time.
//
// The submission here therefore names the field set captured at RENDER, and the
// token captured at render with it, which is all a real form can post.
func TestAFieldCreatedBetweenRenderAndSubmitIsNotCleared(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			tagID := mustField(t, f, "asset", "asset_tag", domain.CustomFieldText)
			mustSetValues(t, f, f.assetID, map[string]string{tagID: "ABC-1"})

			// RENDER. The form draws one field and captures the token with it.
			rendered := []string{tagID}
			token := entityVersion(t, f, "asset", f.assetID)

			// Another administrator defines a second field, and somebody gives
			// the asset a value for it, entirely outside this operator's window.
			ccID := mustField(t, f, "asset", "cost_centre", domain.CustomFieldText)
			mustValue(t, f, ccID, f.assetID, "IT-42")

			// SUBMIT: a clear-all of what was drawn. The token has moved,
			// because setting cost_centre bumped the asset -- so this write is
			// refused, which is the outer guard doing its job.
			vals := map[string]string{}
			for _, id := range rendered {
				vals[id] = ""
			}
			err := f.s.SetCustomValues(f.ctx, domain.AdministratorPermit(f.actor), "asset", f.assetID, token, vals)
			if !errors.Is(err, domain.ErrStale) {
				t.Fatalf("want ErrStale from a token that predates the concurrent value, got %v", err)
			}

			// And with a CURRENT token -- the operator reloads and resubmits the
			// same form, which still draws only what it drew -- the field they
			// never saw is untouched. This is the assertion that does not depend
			// on the token guard, and it is the one that matters: the payload
			// names only what was rendered.
			mustClearValues(t, f, f.assetID, rendered...)

			values, err := f.s.CustomValuesFor(f.ctx, "asset", f.assetID)
			if err != nil {
				t.Fatalf("reading values: %v", err)
			}
			if len(values) != 1 {
				t.Fatalf("got %d values, want 1: a field created between render and submit "+
					"was cleared by a submission that never named it", len(values))
			}
			if values[0].Code != "cost_centre" || values[0].ValueText != "IT-42" {
				t.Fatalf("got %s=%s, want cost_centre=IT-42 untouched",
					values[0].Code, values[0].ValueText)
			}
			want := fmt.Sprintf(`{"custom_fields":{"old":%q,"new":%q}}`,
				counterFold("asset_tag", "1", "cost_centre", "1"), counterFold("cost_centre", "1"))
			if got := lastChangeDiff(t, f, "asset", f.assetID); got != want {
				t.Fatalf("the entry must show only the value the operator cleared\n got %s\nwant %s", got, want)
			}
		})
	}
}

// ---------- GDPR: a value's text never reaches change_log ----------

// TestACustomValuesPlaintextNeverReachesChangeLog is the test that proves the
// feature does what it claims. A distinctive value is set through the normal
// write path, and the assertion is not "the fold looks like a counter" -- it
// is a direct search of the raw stored diff text for the string an operator
// typed. This test predates the counter fold (it held, unchanged, against the
// keyed-digest fold this replaced) and it holds now for a stronger reason: a
// counter carries no information about the value at all, so there is nothing
// left that COULD coincidentally reproduce operator text.
func TestACustomValuesPlaintextNeverReachesChangeLog(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newCustomFieldFixture(t, e)
			id := mustField(t, f, "asset", "owner_email", domain.CustomFieldText)

			const distinctive = "gabriel.the-operator@example-corp.test"
			mustValue(t, f, id, f.assetID, distinctive)

			got := lastChangeDiff(t, f, "asset", f.assetID)
			if strings.Contains(got, distinctive) {
				t.Fatalf("the raw value reached change_log verbatim: %s", got)
			}
			// And the counter form is exactly what is there instead -- this
			// half rules out the fold having silently dropped the field
			// rather than having folded it safely.
			want := fmt.Sprintf(`{"custom_fields":{"old":"","new":%q}}`, counterFold("owner_email", "1"))
			if got != want {
				t.Fatalf("got %s, want %s", got, want)
			}

			// A second, different value must not collide with the first --
			// the counter advances on any real change regardless of what the
			// two values are.
			mustValue(t, f, id, f.assetID, "someone.else@example-corp.test")
			second := lastChangeDiff(t, f, "asset", f.assetID)
			if strings.Contains(second, "someone.else@example-corp.test") {
				t.Fatalf("the second raw value reached change_log verbatim: %s", second)
			}
			if second == got {
				t.Fatal("two different values folded to the same change_log entry")
			}
		})
	}
}
