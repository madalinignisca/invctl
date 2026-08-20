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

// setCustomValues replaces one entity's custom values wholesale INSIDE t.
//
// It never opens its own transaction: the parent's change_log entry has to
// cover this write, so the caller owns both. SetCustomValues below is the only
// caller today and does exactly that.
//
// vals maps custom_field.id to the RAW value an operator submitted. A blank or
// whitespace-only entry, and any field absent from the map, CLEARS that field --
// a form posts every field it renders, and "" is how a person says "nothing
// here". Every remaining value is canonicalised through
// domain.CanonicalCustomValue against its field's kind and, for select, its LIVE
// options. A value for an unknown field, a field belonging to another entity
// type, or a retired field is refused.
func setCustomValues(ctx context.Context, t *tx, entityType, entityID string, vals map[string]string) error {
	defs, err := customFieldDefs(ctx, t, entityType)
	if err != nil {
		return err
	}

	// Sorted so that a map with two bad entries always names the same one
	// first: an error message that varies run to run is one nobody trusts.
	fieldIDs := make([]string, 0, len(vals))
	for id := range vals {
		fieldIDs = append(fieldIDs, id)
	}
	sort.Strings(fieldIDs)

	canonical := make(map[string]string, len(fieldIDs))
	for _, fieldID := range fieldIDs {
		raw := vals[fieldID]
		if strings.TrimSpace(raw) == "" {
			continue // cleared
		}
		def, ok := defs[fieldID]
		if !ok {
			// Deliberately not distinguishable from "belongs to a service":
			// either way this entity has no such field, and 404 rather than an
			// upsert is the rule -- ON CONFLICT here would turn a narrow write
			// into an inventory-creation vector.
			return fmt.Errorf("setting custom values of %s %s: no such field %s for a %s: %w",
				entityType, entityID, fieldID, entityType, domain.ErrNotFound)
		}
		if def.IsRetired() {
			return fmt.Errorf("setting custom values of %s %s: field %q is retired and takes no new value: %w",
				entityType, entityID, def.Code, domain.ErrInvalid)
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
		canonical[fieldID] = value
	}

	// created_at and row_version are carried across the replacement rather than
	// reset: "when was this first set" is a fact about the value, and the
	// replacement is a mechanism, not a new beginning.
	existing, err := entityCustomValues(ctx, t, entityID)
	if err != nil {
		return err
	}
	previous := make(map[string]domain.CustomFieldValue, len(existing))
	for _, v := range existing {
		if _, ok := defs[v.FieldID]; ok {
			previous[v.FieldID] = v
		}
	}

	// Scoped to THIS entity type's fields, so setting a service's values could
	// never clear an asset's even if the two ids somehow coincided.
	//
	// The id list comes from the defs map rather than from a
	// `field_id IN (SELECT id FROM custom_field WHERE entity_type = ?)`
	// subquery, and that is deliberate. The subquery reads custom_field too, so
	// it ALSO supplies the rw-antidependency edge customFieldDefs exists to
	// supply -- which is fine until somebody rewrites this DELETE and silently
	// takes Task 3's kind guard with it. One read, in one named place, with a
	// test pointed at it, beats an invariant that happens to hold twice.
	if len(defs) > 0 {
		ids := make([]string, 0, len(defs))
		for id := range defs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		args := append([]any{entityID}, anySlice(ids)...)
		if _, err := t.exec(ctx,
			`DELETE FROM custom_field_value WHERE entity_id = ? AND field_id IN (`+
				placeholders(len(ids))+`)`, args...); err != nil {
			return translateWriteErr(err, "clearing custom values")
		}
	}

	for _, fieldID := range fieldIDs {
		value, ok := canonical[fieldID]
		if !ok {
			continue
		}
		createdAt, version := t.at, 1
		if prev, ok := previous[fieldID]; ok {
			createdAt, version = prev.CreatedAt, prev.RowVersion+1
		}
		if _, err := t.exec(ctx, `
			INSERT INTO custom_field_value (id, field_id, entity_id, value_text,
			                                created_at, updated_at, row_version)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			NewID(), fieldID, entityID, value, createdAt, t.at, version); err != nil {
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
func (s *SQLStore) SetCustomValues(ctx context.Context, actor domain.Actor, entityType, entityID string, vals map[string]string) error {
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
			return logAssetCustomValues(ctx, t, entityID, before, after)
		}
		return logServiceCustomValues(ctx, t, entityID, before, after)
	})
}

// logAssetCustomValues writes the asset's audit entry for a value replacement.
//
// The asset row and its environments are read inside the transaction and used
// on BOTH sides of the diff, so they cancel and the entry names the one thing
// that actually moved. Without the surrounding audited shape there would be no
// entry at all -- the asset row itself is untouched by this write, which is the
// exact failure the fold exists to prevent.
func logAssetCustomValues(ctx context.Context, t *tx, assetID, before, after string) error {
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
	return t.logUpdate(ctx, "asset", assetID,
		auditedAsset(&a, codes, before), auditedAsset(&a, codes, after))
}

// logServiceCustomValues is the service half of the same thing.
func logServiceCustomValues(ctx context.Context, t *tx, serviceID, before, after string) error {
	var svc domain.Service
	if err := t.get(ctx, &svc, `SELECT * FROM service WHERE id = ?`, serviceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("setting custom values: service %s: %w", serviceID, domain.ErrNotFound)
		}
		return fmt.Errorf("reading service %s for its audit entry: %w", serviceID, err)
	}
	return t.logUpdate(ctx, "service", serviceID,
		auditedService(&svc, before), auditedService(&svc, after))
}
