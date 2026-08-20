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
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Custom field VALUES: what one asset or service holds for the definitions
// customfields.go owns. docs/custom-fields-design.md §5 and §6 are the design.
//
// Two rules shape everything below.
//
// A VALUE CHANGE IS AUDITED AGAINST THE ASSET OR SERVICE, NEVER AGAINST THE
// FIELD, because the question a reader is asking at 03:00 is "what changed
// about vm-db-2", not "what happened to cost_centre". The mechanism is the one
// already proven for asset_environment and dependency_data_class: fold the set
// into a sorted, joined string on the parent's audited shape, so a set
// replacement that leaves every column of the parent untouched still produces a
// diff. This repo has been bitten three times by the opposite.
//
// CLEARING A VALUE REMOVES THE ROW, and that is not a violation of the
// soft-delete rule. custom_field_value holds the CURRENT value of something its
// parent owns -- "set and index tables are replaced wholesale, and that is not
// deletion". What is mandatory is that the parent's change_log entry records it,
// which is exactly what the folded string is for.

// CustomValueRow is one value with the definition it belongs to resolved, so a
// detail page renders label, kind and retirement without a query per value.
type CustomValueRow struct {
	domain.CustomFieldValue
	Code  string
	Label string
	Kind  string
	// Retired is the definition's state, not the value's -- a retired field
	// keeps every value it holds (design.md §6) and they keep displaying.
	Retired bool
}

// customValueScan is the scan shape behind CustomValueRow.
//
// Separate from it because the join carries custom_field.retired_at, and
// turning that into a bool in SQL would mean a CASE expression whose result
// type differs between the engines. Deciding it in Go costs one loop and needs
// no portability argument at all.
type customValueScan struct {
	domain.CustomFieldValue
	Code      string  `db:"code"`
	Label     string  `db:"label"`
	Kind      string  `db:"kind"`
	RetiredAt *string `db:"retired_at"`
}

// CustomValuesFor returns every value one entity holds, live fields and retired
// ones together, ordered by code so two entities read the same way.
func (s *SQLStore) CustomValuesFor(ctx context.Context, entityType, entityID string) ([]CustomValueRow, error) {
	var scanned []customValueScan
	err := s.read(ctx, &scanned, `
		SELECT cv.id, cv.field_id, cv.entity_id, cv.value_text,
		       cv.created_at, cv.updated_at, cv.row_version,
		       cf.code AS code, cf.label AS label, cf.kind AS kind,
		       cf.retired_at AS retired_at
		FROM custom_field_value cv
		JOIN custom_field cf ON cf.id = cv.field_id
		WHERE cf.entity_type = ? AND cv.entity_id = ?
		ORDER BY cf.code`, entityType, entityID)
	if err != nil {
		return nil, fmt.Errorf("listing custom values of %s %s: %w", entityType, entityID, err)
	}
	rows := make([]CustomValueRow, 0, len(scanned))
	for _, r := range scanned {
		rows = append(rows, CustomValueRow{
			CustomFieldValue: r.CustomFieldValue,
			Code:             r.Code,
			Label:            r.Label,
			Kind:             r.Kind,
			Retired:          r.RetiredAt != nil,
		})
	}
	return rows, nil
}

// customFieldDefs loads every definition for one entity type INSIDE t, keyed by
// id. Retired ones included: a value already written against a retired field
// still has to resolve to a code for the audit fold.
//
// THIS READ IS LOAD-BEARING AND IT IS NOT A CONVENIENCE. UpdateCustomField
// guards "a field's kind cannot change while values exist" with a count of
// custom_field_value inside a serializable transaction. PostgreSQL's SSI aborts
// a transaction only when it finds a CYCLE of two rw-antidependency edges. That
// count supplies ONE edge against a concurrent value insert; a value writer that
// reads nothing supplies no second edge, closes no cycle, and both transactions
// commit at any interleaving. So the value write reads custom_field inside its
// OWN serializable transaction -- here, called from inside writeSerializable.
//
// WHAT THE PROBE ACTUALLY SHOWED, because it is not what the ruling assumed.
// Against the live container, on THIS schema, even a blind INSERT into
// custom_field_value aborts with SQLSTATE 40001. The reason is
// custom_field_value.field_id REFERENCES custom_field(id): the foreign key check
// reads the parent row inside the writer's transaction, and that read is the
// second edge. Dropping the constraint and repeating the probe commits cleanly,
// no abort -- which is the behaviour the ruling described, and it is the
// constraint that makes the difference.
//
// So the guard has belt AND braces, and this read is the belt: it is the half
// that survives somebody deciding the foreign key is inconvenient, and the half
// that is visible to a reader of this package rather than to a reader of
// migration 00051. Keep it. ONE query rather than one per field, so the
// precondition lives in a single named place.
// TestAKindChangeAbortsAgainstAConcurrentValueWrite asserts the abort itself.
func customFieldDefs(ctx context.Context, t *tx, entityType string) (map[string]domain.CustomField, error) {
	var defs []domain.CustomField
	if err := t.selectAll(ctx, &defs,
		`SELECT * FROM custom_field WHERE entity_type = ?`, entityType); err != nil {
		return nil, fmt.Errorf("reading custom field definitions for %s: %w", entityType, err)
	}
	byID := make(map[string]domain.CustomField, len(defs))
	for _, d := range defs {
		byID[d.ID] = d
	}
	return byID, nil
}

// entityCustomValues reads one entity's value rows inside t.
//
// Keyed on entity_id alone, with no join to custom_field: entity ids are
// UUIDv7 and unique across both tables, and every caller already holds the
// definitions map to filter and resolve against.
func entityCustomValues(ctx context.Context, t *tx, entityID string) ([]domain.CustomFieldValue, error) {
	var values []domain.CustomFieldValue
	if err := t.selectAll(ctx, &values,
		`SELECT * FROM custom_field_value WHERE entity_id = ?`, entityID); err != nil {
		return nil, fmt.Errorf("reading custom values of %s: %w", entityID, err)
	}
	return values, nil
}

// foldCustomValues renders an entity's values as "code=value,code=value".
//
// SORTED BY CODE BEFORE JOINING, the same reason dependencyAudit sorts its data
// classes: a set written in a different order is not a change, and an audit
// trail that reports one teaches its readers to ignore it.
//
// A value containing a comma or an equals sign renders literally, exactly as an
// environment code would. The fold is a human-readable audit rendering, not a
// wire format -- nothing parses it back.
//
// KNOWN EXPOSURE, NAMED RATHER THAN SOLVED HERE: NOTHING INSIDE THIS STRING CAN
// BE REDACTED SELECTIVELY. domain.IsRedacted takes an entity name and a COLUMN
// name, and everything folded below arrives under one opaque column,
// "custom_fields". Adding "custom_fields" to RedactedFields would redact the
// WHOLE fold wholesale -- a real lever, and worth knowing it exists -- but there
// is no name INSIDE it to key a rule on, so one field's value cannot be redacted
// while the rest stay readable. Absent that, the value_text of every custom
// field lands in the audit trail permanently and in full: change_log is
// append-only, no UPDATE and no DELETE, ever.
//
// That matters because CLAUDE.md's position is that the audit trail "carries no
// personal data and can be kept forever with no retention argument", which is
// why change_log.actor holds an opaque app_user.id rather than a username. That
// property now rests on no administrator ever defining a field like "Owner
// email" or "Contact phone" -- and letting an estate define exactly such a field
// is what this feature is FOR. Scrubbing an app_user row still answers an
// erasure request for the actor; it does nothing about PII an administrator put
// in a value.
//
// Closing it properly needs either per-code diff keys (which means changing
// auditFields, the function every entity's audit flows through and whose last
// defect was invisible for a week) or a redaction flag on custom_field and a
// rule that reads it. Redacting the whole fold is the blunt instrument
// available today at the cost of every custom value disappearing from every
// entry. Both are schema and spec changes and are the coordinator's, not
// this function's. Do not treat the silence here as a judgement that it is fine.
func foldCustomValues(defs map[string]domain.CustomField, values []domain.CustomFieldValue) string {
	pairs := make([]string, 0, len(values))
	for _, v := range values {
		def, ok := defs[v.FieldID]
		if !ok {
			// A value belonging to the other entity type. Not reachable with
			// UUID ids; skipped rather than rendered against a code this
			// entity does not have.
			continue
		}
		pairs = append(pairs, def.Code+"="+v.ValueText)
	}
	sort.Strings(pairs)
	return strings.Join(pairs, ",")
}

// customFieldsAudit renders an entity's custom values as the string that folds
// into assetAudit / serviceAudit. Empty when it holds none, which is a real
// answer and diffs correctly against a first value being set.
func customFieldsAudit(ctx context.Context, t *tx, entityType, entityID string) (string, error) {
	defs, err := customFieldDefs(ctx, t, entityType)
	if err != nil {
		return "", err
	}
	values, err := entityCustomValues(ctx, t, entityID)
	if err != nil {
		return "", err
	}
	return foldCustomValues(defs, values), nil
}

// setCustomValues applies one entity's submitted custom values INSIDE t.
//
// It never opens its own transaction: the parent's change_log entry has to
// cover this write, so the caller owns both. SetCustomValues below is the only
// caller today and does exactly that.
//
// THE CONTRACT, AND TASKS 5 AND 6 BUILD AGAINST EXACTLY THIS:
//
//	ABSENT from vals        -> UNTOUCHED. Whatever is stored stays stored.
//	present, blank or space -> CLEARED. The row is removed.
//	present with a value    -> set, once canonicalised and validated.
//
// vals maps custom_field.id to the RAW text an operator submitted. A form that
// wants to clear a field must post an empty string for it; leaving it out says
// "I am not editing this", not "delete it".
//
// ABSENCE USED TO MEAN "CLEAR" AND THAT WAS A DATA-DESTROYING BUG, TWICE. The
// old rule rested on "a form posts every field it renders", which holds only if
// the form's field set equals this writer's field set. It does not, because the
// two are computed at different moments:
//
//   - A field retired between render and submit is absent from the form, so its
//     retained value was deleted on the next unrelated edit -- against §6's
//     "retiring a field deletes no value, ever", leaving Restore to bring the
//     field back empty.
//   - A field RESTORED in that same window is live at write time and still
//     absent from the operator's map, so it was doomed for the same reason. The
//     parent's row_version cannot close this: RestoreCustomField bumps
//     custom_field.row_version, not the asset's, so the operator's token is
//     still current and the write is accepted.
//
// Requiring an explicit blank closes both, and it closes them structurally
// rather than by scoping the delete to whatever the field's state happens to be
// at write time. The retired-field protection then falls out for free: a retired
// field is absent from forms (§6), so it is absent from vals, so it is
// untouched. An explicit blank still clears one -- that is an operator's own
// deliberate act, which §6 says does remove the row, and no form can reach it by
// accident because no form renders a retired field.
//
// AN UNCHANGED VALUE IS NOT A NEW VALUE, so it is left exactly where it is --
// same row, same id, same created_at, same row_version -- and never rewritten.
// §3 says a retired select option "keeps displaying" on the values that already
// chose it while no NEW value may select it; without this rule a form that
// renders such a value and posts it back was rejected outright over a field the
// operator never touched. The comparison is made twice, on the trimmed text
// before validation and on the canonical form after it, because canonicalisation
// can collapse "TRUE" onto a stored "true".
//
// THE RETIRED REFUSAL COMES LAST, after canonicalising and after both equality
// checks. Above them it made that second comparison unreachable on the retired
// path, so a retired boolean holding "true", re-posted as "TRUE", refused the
// whole submission over a field the operator never touched and could not clear.
// Only a value that is genuinely NEW is refused for a retired field.
func setCustomValues(ctx context.Context, t *tx, entityType, entityID string, vals map[string]string) error {
	defs, err := customFieldDefs(ctx, t, entityType)
	if err != nil {
		return err
	}
	existing, err := entityCustomValues(ctx, t, entityID)
	if err != nil {
		return err
	}
	stored := make(map[string]domain.CustomFieldValue, len(existing))
	for _, v := range existing {
		if _, ok := defs[v.FieldID]; ok {
			stored[v.FieldID] = v
		}
	}

	// Sorted so that a map with two bad entries always names the same one
	// first: an error message that varies run to run is one nobody trusts.
	fieldIDs := make([]string, 0, len(vals))
	for id := range vals {
		fieldIDs = append(fieldIDs, id)
	}
	sort.Strings(fieldIDs)

	// cleared: the row goes. changed: the row is written again. Anything not in
	// either -- including every field absent from vals -- is left alone.
	cleared := make(map[string]bool, len(fieldIDs))
	changed := make(map[string]string, len(fieldIDs))
	for _, fieldID := range fieldIDs {
		def, ok := defs[fieldID]
		if !ok {
			// Deliberately not distinguishable from "belongs to a service":
			// either way this entity has no such field, and 404 rather than an
			// upsert is the rule -- ON CONFLICT here would turn a narrow write
			// into an inventory-creation vector.
			return fmt.Errorf("setting custom values of %s %s: no such field %s for a %s: %w",
				entityType, entityID, fieldID, entityType, domain.ErrNotFound)
		}
		raw := strings.TrimSpace(vals[fieldID])
		if raw == "" {
			cleared[fieldID] = true
			continue
		}
		prev, held := stored[fieldID]
		if held && prev.ValueText == raw {
			continue // unchanged, verbatim
		}
		var options []string
		if def.Kind == domain.CustomFieldSelect {
			options, err = liveOptionValues(ctx, t, fieldID)
			if err != nil {
				return err
			}
		}
		value, err := domain.CanonicalCustomValue(def.Kind, raw, options)
		if err != nil {
			return fmt.Errorf("custom field %q: %w", def.Code, err)
		}
		if held && prev.ValueText == value {
			continue // unchanged once canonicalised
		}
		if def.IsRetired() {
			return fmt.Errorf("setting custom values of %s %s: field %q is retired and takes no new value: %w",
				entityType, entityID, def.Code, domain.ErrInvalid)
		}
		changed[fieldID] = value
	}

	// What goes: only rows this submission explicitly cleared or is about to
	// write again. Nothing is deleted on the strength of being absent.
	//
	// Scoped by explicit id rather than by a
	// `field_id IN (SELECT id FROM custom_field WHERE ...)` subquery, and that
	// is deliberate twice over. It cannot reach another entity type's fields,
	// and the subquery would read custom_field too -- ALSO supplying the
	// rw-antidependency edge customFieldDefs exists to supply, which is fine
	// until somebody rewrites this DELETE and silently takes Task 3's kind
	// guard with it. One read, in one named place, with a test pointed at it,
	// beats an invariant that happens to hold twice.
	doomed := make([]string, 0, len(cleared)+len(changed))
	for fieldID := range stored {
		if !cleared[fieldID] {
			if _, rewriting := changed[fieldID]; !rewriting {
				continue
			}
		}
		doomed = append(doomed, fieldID)
	}
	sort.Strings(doomed)
	if len(doomed) > 0 {
		args := append([]any{entityID}, anySlice(doomed)...)
		if _, err := t.exec(ctx,
			`DELETE FROM custom_field_value WHERE entity_id = ? AND field_id IN (`+
				placeholders(len(doomed))+`)`, args...); err != nil {
			return translateWriteErr(err, "clearing custom values")
		}
	}

	for _, fieldID := range fieldIDs {
		value, ok := changed[fieldID]
		if !ok {
			continue
		}
		// The id, created_at and row_version are carried across the
		// replacement rather than reset. "When was this first set" is a fact
		// about the value, the primary key is a handle somebody may already
		// hold, and the replacement is a mechanism rather than a new
		// beginning.
		id, createdAt, version := NewID(), t.at, 1
		if prev, held := stored[fieldID]; held {
			id, createdAt, version = prev.ID, prev.CreatedAt, prev.RowVersion+1
		}
		if _, err := t.exec(ctx, `
			INSERT INTO custom_field_value (id, field_id, entity_id, value_text,
			                                created_at, updated_at, row_version)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, fieldID, entityID, value, createdAt, t.at, version); err != nil {
			return translateWriteErr(err, "setting custom value")
		}
	}
	return nil
}

// liveOptionValues reads a select field's currently offered options inside t. A
// retired option keeps displaying on the values that already chose it and is
// refused for a new one -- design.md §3, the same rule as a retired field.
func liveOptionValues(ctx context.Context, t *tx, fieldID string) ([]string, error) {
	var values []string
	if err := t.selectAll(ctx, &values,
		`SELECT value FROM custom_field_option
		 WHERE field_id = ? AND retired_at IS NULL ORDER BY position`, fieldID); err != nil {
		return nil, fmt.Errorf("reading live options of custom field %s: %w", fieldID, err)
	}
	return values, nil
}

// SetCustomValues replaces every custom value one asset or service holds and
// audits the result against that entity.
//
// writeSerializable, not write, and the reason is Task 3's kind guard rather
// than anything this method asserts for itself: see customFieldDefs. The retry
// writeSerializable already carries turns the resulting abort into "you raced,
// try again" instead of an error an operator has to interpret.
//
// THE CONCURRENCY TOKEN IS THE PARENT ENTITY'S row_version, NOT EACH VALUE'S,
// and expected is the one the form was rendered with. design.md §3, as amended:
// values are replaced wholesale per entity, so a per-value token would need one
// hidden token per rendered field and would still not describe what the operator
// is editing -- they are editing vm-db-2, not vm-db-2's cost centre. The audit
// entry is written against the entity for the same reason and every other editor
// in this repo is per-entity.
//
// The bump is UNCONDITIONAL, including on a save that changes nothing. That is
// what invariant 4 means by "refuses a second save from one token", and it is
// exactly what TestEveryEditorRefusesASecondSaveFromOneToken submits: the same
// form twice. A bump conditional on a diff would let the second save through and
// the guard would read as present while being inert. It writes no change_log row
// on its own -- auditFields skips row_version and updated_at, and the audited
// shape is built from the row as it was read, so a no-op save moves the token
// and records nothing, which is correct on both counts.
//
// The caller's token is not advanced in place; a handler re-reads the entity to
// re-render it and gets the new one from there.
func (s *SQLStore) SetCustomValues(ctx context.Context, actor domain.Actor, entityType, entityID string, expected int, vals map[string]string) error {
	switch entityType {
	case domain.CustomFieldEntityAsset, domain.CustomFieldEntityService:
	default:
		return fmt.Errorf("setting custom values: %q holds no custom fields: %w",
			entityType, domain.ErrInvalid)
	}

	return s.writeSerializable(ctx, actor, func(t *tx) error {
		before, err := customFieldsAudit(ctx, t, entityType, entityID)
		if err != nil {
			return err
		}
		if err := setCustomValues(ctx, t, entityType, entityID, vals); err != nil {
			return err
		}
		after, err := customFieldsAudit(ctx, t, entityType, entityID)
		if err != nil {
			return err
		}
		if entityType == domain.CustomFieldEntityAsset {
			return logAssetCustomValues(ctx, t, entityID, expected, before, after)
		}
		return logServiceCustomValues(ctx, t, entityID, expected, before, after)
	})
}

// bumpedParentVersion turns the parent bump's result into a stale-token
// refusal: domain.ErrStale, which handlers map to 409.
//
// It takes the RESULT rather than building the statement, so that each caller
// writes its own UPDATE with a LITERAL table name. Assembling the target at run
// time is refused by TestNoAssembledWriteReachesChangeLog, and rightly: a
// statement that names its own table can eventually be pointed at change_log,
// which is append-only. Two five-line statements beat one clever one.
//
// Zero rows affected does NOT mean the entity is gone -- the caller has just
// read it inside this same transaction -- so the clause that failed is
// `row_version = ?`: somebody else wrote the entity between the form being
// rendered and this submission arriving. requireVersion says the rest.
func bumpedParentVersion(res sql.Result, err error, entity, entityID string, expected int) error {
	if err != nil {
		return translateWriteErr(err, "bumping the "+entity+" version")
	}
	version := expected
	return requireVersion(res, entity, entityID, &version)
}

// logAssetCustomValues writes the asset's audit entry for a value replacement.
//
// The asset row and its environments are read inside the transaction and used
// on BOTH sides of the diff, so they cancel and the entry names the one thing
// that actually moved. Without the surrounding audited shape there would be no
// entry at all -- the asset row itself is untouched by this write, which is the
// exact failure the fold exists to prevent.
func logAssetCustomValues(ctx context.Context, t *tx, assetID string, expected int, before, after string) error {
	var a domain.Asset
	if err := t.get(ctx, &a, `SELECT * FROM asset WHERE id = ?`, assetID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("setting custom values: asset %s: %w", assetID, domain.ErrNotFound)
		}
		return fmt.Errorf("reading asset %s for its audit entry: %w", assetID, err)
	}
	var codes []string
	if err := t.selectAll(ctx, &codes, `
		SELECT e.code FROM asset_environment ae
		JOIN environment e ON e.id = ae.environment_id
		WHERE ae.asset_id = ?`, assetID); err != nil {
		return fmt.Errorf("reading environments of asset %s for its audit entry: %w", assetID, err)
	}
	res, err := t.exec(ctx,
		`UPDATE asset SET row_version = row_version + 1, updated_at = ?
		 WHERE id = ? AND row_version = ?`, t.at, assetID, expected)
	if err := bumpedParentVersion(res, err, "asset", assetID, expected); err != nil {
		return err
	}
	return t.logUpdate(ctx, "asset", assetID,
		auditedAsset(&a, codes, before), auditedAsset(&a, codes, after))
}

// logServiceCustomValues is the service half of the same thing.
func logServiceCustomValues(ctx context.Context, t *tx, serviceID string, expected int, before, after string) error {
	var svc domain.Service
	if err := t.get(ctx, &svc, `SELECT * FROM service WHERE id = ?`, serviceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("setting custom values: service %s: %w", serviceID, domain.ErrNotFound)
		}
		return fmt.Errorf("reading service %s for its audit entry: %w", serviceID, err)
	}
	res, err := t.exec(ctx,
		`UPDATE service SET row_version = row_version + 1, updated_at = ?
		 WHERE id = ? AND row_version = ?`, t.at, serviceID, expected)
	if err := bumpedParentVersion(res, err, "service", serviceID, expected); err != nil {
		return err
	}
	return t.logUpdate(ctx, "service", serviceID,
		auditedService(&svc, before), auditedService(&svc, after))
}
