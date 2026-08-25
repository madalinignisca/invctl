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

// Custom fields: definitions and the vocabulary a `select` field offers.
// docs/custom-fields-design.md is the design; values (Task 4) and the
// registry page (Task 5) build on this.

// CustomFieldRow is a definition plus what the registry needs without a
// query per row: who defined and retired it, resolved to a display name the
// way change_log.actor already is, and how many entities hold a value.
type CustomFieldRow struct {
	domain.CustomField
	CreatedByName string  `db:"created_by_name"`
	RetiredByName *string `db:"retired_by_name"`
	UsageCount    int     `db:"usage_count"`
	// The owning team, resolved the same way CreatedByName is -- a LEFT
	// JOIN, so a since-retired team still leaves the field readable. Unlike
	// the app_user join above, this one is not about a GDPR scrub: a team
	// is never scrubbed, it is only ever retired, and OwnerTeamLifecycle is
	// exactly how a reader tells "still active" from "disbanded, nobody has
	// picked this up" -- see OwnerRetired.
	OwnerTeamCode       *string `db:"owner_team_code"`
	OwnerTeamContactRef *string `db:"owner_team_contact_ref"`
	OwnerTeamLifecycle  *string `db:"owner_team_lifecycle"`
	// Options is populated by GetCustomField. ListCustomFields leaves it
	// empty on every row it returns: loading every field's options on every
	// registry row would be an N+1 query, and the registry's own list and
	// retired tables render nothing from it. The registry's options editor
	// (Ruling AF) needs it for the ONE row currently open, and gets it by a
	// separate GetCustomField call against just that row -- see
	// renderCustomFieldList -- rather than by this method ever populating it.
	Options []domain.CustomFieldOption
}

// OwnerDisplay is who to ask about this field: the team's contact_ref when
// it has one -- a group address or a channel, the actionable part -- falling
// back to the team's own code when contact_ref was left blank. Empty only
// for the handful of fields migration 00054 could not retroactively assign
// an owner to; every field created through CreateCustomField from this
// change forward always has one (domain.checkOwnerTeam).
func (r CustomFieldRow) OwnerDisplay() string {
	if r.OwnerTeamContactRef != nil && *r.OwnerTeamContactRef != "" {
		return *r.OwnerTeamContactRef
	}
	if r.OwnerTeamCode != nil {
		return *r.OwnerTeamCode
	}
	return ""
}

// OwnerRetired reports whether the team currently named as owner has since
// been disbanded. The field's own attribution is unaffected by this --
// exactly the rule docs/custom-fields-design.md §3 already applies to a
// retired `select` option: what is STORED keeps displaying, marked as
// retired; what is RETIRED is simply not offered again as a new choice. The
// symmetry between the two is not a coincidence, and OwnerOptions
// (internal/web/handlers/customfields.go) reuses it explicitly.
func (r CustomFieldRow) OwnerRetired() bool {
	return r.OwnerTeamLifecycle != nil && *r.OwnerTeamLifecycle == domain.LifecycleRetired
}

// customFieldSelect resolves both attribution columns the way
// dependencySelect resolves verified_by: a LEFT JOIN so a scrubbed account
// still leaves the row readable, just without a name to show for it.
//
// The usage count is a correlated subquery rather than a LEFT JOIN ... GROUP
// BY: teamSelect already sets that precedent for this codebase, and a GROUP
// BY here would have to repeat every column of `cf.*` in the clause for
// PostgreSQL, which SQLite does not require -- a portability wrinkle a
// subquery avoids entirely. (field_id, entity_id) is unique on
// custom_field_value, so COUNT and COUNT(DISTINCT entity_id) agree; DISTINCT
// is kept because it is what the test's name promises.
const customFieldSelect = `
	SELECT cf.*,
	       COALESCE(cu.display_name, cu.username) AS created_by_name,
	       COALESCE(ru.display_name, ru.username) AS retired_by_name,
	       (SELECT COUNT(DISTINCT cv.entity_id) FROM custom_field_value cv
	         WHERE cv.field_id = cf.id) AS usage_count,
	       ot.code       AS owner_team_code,
	       ot.contact_ref AS owner_team_contact_ref,
	       ot.lifecycle  AS owner_team_lifecycle
	FROM custom_field cf
	LEFT JOIN app_user cu ON cu.id = cf.created_by
	LEFT JOIN app_user ru ON ru.id = cf.retired_by
	LEFT JOIN team ot ON ot.id = cf.owner_team_id`

// customFieldAudit is the audited shape of a definition: the row itself plus
// the LIVE option values it currently offers, folded in the same way
// dependencyAudit folds its data classes.
//
// Without this, SetCustomFieldOptions replacing the option set produces no
// diff on the CustomField struct itself and therefore no change_log entry at
// all -- the exact failure this repo has been bitten by three times
// (CLAUDE.md, "the one that is easy to get wrong").
type customFieldAudit struct {
	domain.CustomField
	// Sorted and joined so re-submitting the same set in a different order
	// is never reported as a change.
	Options string `db:"options"`
}

func auditedCustomField(f *domain.CustomField, optionValues []string) *customFieldAudit {
	sorted := append([]string(nil), optionValues...)
	sort.Strings(sorted)
	return &customFieldAudit{CustomField: *f, Options: strings.Join(sorted, ",")}
}

// optionValues extracts the LIVE options as "value=label" pairs, for folding
// into the audited shape. A retired option keeps displaying on existing
// values but is no longer part of what the field currently offers.
//
// FINAL REVIEW B2: the label is folded in alongside the value, not the value
// alone. Before this fix an option whose value and position were unchanged
// but whose LABEL changed folded identically before and after -- diffJSON
// saw no change on CustomField itself (SetCustomFieldOptions never touches
// it) and none in this fold either, so a rename wrote no change_log row at
// all. custom_field_option.label is classified declared (docs/AUDIT.md line
// 63): CLAUDE.md's "every mutation of declared state writes a change_log
// row, no exceptions" admits none for a rename any more than for a value.
// This is the fourth recurrence of the exact failure CLAUDE.md already names
// three times -- a set replacement that leaves the parent struct untouched
// producing no audit entry -- because the earlier three folds all fixed the
// SET (which value is live) without also folding what changed ABOUT a
// member that stays in the set. auditedCustomField still sorts and joins the
// resulting "value=label" strings exactly as before, so
// TestReorderingCustomFieldOptionsIsNotAChange keeps holding: the set is
// still unordered, only what each member carries changed.
func optionValues(opts []domain.CustomFieldOption) []string {
	var pairs []string
	for _, o := range opts {
		if o.RetiredAt == nil {
			pairs = append(pairs, o.Value+"="+o.Label)
		}
	}
	return pairs
}

// GetCustomField loads one definition with its full option list, live and
// retired together -- a retired option keeps displaying on the values that
// already chose it, so the registry and any value-editing form need to see it.
func (s *SQLStore) GetCustomField(ctx context.Context, id string) (*CustomFieldRow, error) {
	var row CustomFieldRow
	if err := s.readOne(ctx, &row, customFieldSelect+` WHERE cf.id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting custom field %s: %w", id, err)
	}
	var opts []domain.CustomFieldOption
	if err := s.read(ctx, &opts,
		`SELECT * FROM custom_field_option WHERE field_id = ? ORDER BY position`, id); err != nil {
		return nil, fmt.Errorf("getting options of custom field %s: %w", id, err)
	}
	row.Options = opts
	return &row, nil
}

// ListCustomFields lists definitions for one entity type, live only unless
// includeRetired is set. The registry (Task 5) is the only caller expected to
// ask for both.
func (s *SQLStore) ListCustomFields(ctx context.Context, entityType string, includeRetired bool) ([]CustomFieldRow, error) {
	var where []string
	var args []any
	if entityType != "" {
		where = append(where, `cf.entity_type = ?`)
		args = append(args, entityType)
	}
	if !includeRetired {
		where = append(where, `cf.retired_at IS NULL`)
	}
	query := customFieldSelect
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	query += ` ORDER BY cf.entity_type, cf.code`

	var rows []CustomFieldRow
	if err := s.read(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("listing custom fields: %w", err)
	}
	return rows, nil
}

// CreateCustomField inserts a definition. Validation of the shape -- a known
// entity type and kind, a non-empty description -- is the domain
// constructor's job (NewCustomField); the CHECK constraints in migration
// 00051 are the second line of defence, not the first.
func (s *SQLStore) CreateCustomField(ctx context.Context, actor domain.Actor, f *domain.CustomField) error {
	f.RowVersion = 1
	return s.write(ctx, actor, func(t *tx) error {
		// f.OwnerTeamID is never nil here: domain.checkOwnerTeam refuses an
		// empty one before this method is ever reached. Checked inside the
		// same transaction as the insert, not before s.write is called,
		// because there is no "unchanged owner" exception on a create the
		// way UpdateCustomField needs one -- every owner named on a brand
		// new field is, by definition, newly chosen.
		if err := t.requireActiveOwnerTeam(ctx, *f.OwnerTeamID); err != nil {
			return err
		}
		_, err := t.exec(ctx, `
			INSERT INTO custom_field (id, entity_type, code, label, kind, description,
			                           created_by, created_at, owner_team_id, row_version)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			f.ID, f.EntityType, f.Code, f.Label, f.Kind, f.Description,
			f.CreatedBy, f.CreatedAt, f.OwnerTeamID, f.RowVersion)
		if err != nil {
			return translateWriteErr(err, "creating custom field")
		}
		// No option exists yet at creation time -- SetCustomFieldOptions is
		// the only writer of custom_field_option.
		return t.logCreate(ctx, "custom_field", f.ID, auditedCustomField(f, nil))
	})
}

// requireActiveOwnerTeam checks that a custom field's owner names a LIVE
// team before the write reaches the foreign key -- the same reasoning
// requireRole (teams.go) already gives for manager_role: a naked FK failure
// reports no column SQLite's engine can name, so translateWriteErr can only
// turn it into an unstyled 422 with the form contents lost. Also refuses a
// team that exists but has since been RETIRED, because a picker only ever
// offers what somebody may choose NOW (TeamOptions's own rule) -- a retired
// team is not a legitimate NEW choice, only a legitimate EXISTING one, which
// is why callers editing an unchanged owner must skip this check entirely
// rather than pass it a laxer version of it.
func (t *tx) requireActiveOwnerTeam(ctx context.Context, ownerTeamID string) error {
	var lifecycle string
	if err := t.get(ctx, &lifecycle, `SELECT lifecycle FROM team WHERE id = ?`, ownerTeamID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("owner team %s does not exist: %w", ownerTeamID, domain.ErrInvalid)
		}
		return fmt.Errorf("checking owner team %s: %w", ownerTeamID, err)
	}
	if lifecycle == domain.LifecycleRetired {
		return fmt.Errorf("owner team %s is retired: choose an active team: %w", ownerTeamID, domain.ErrInvalid)
	}
	return nil
}

// UpdateCustomField persists field changes. The label and the description
// stay editable forever -- the two things an administrator actually needs to
// correct -- but a Kind change is refused once any value exists: retyping a
// field that holds values is a data migration wearing a form control, and the
// refusal names how many values are in the way.
func (s *SQLStore) UpdateCustomField(ctx context.Context, actor domain.Actor, f *domain.CustomField) error {
	before, err := s.GetCustomField(ctx, f.ID)
	if err != nil {
		return err
	}

	if err := f.Validate(); err != nil {
		return err
	}

	// entity_type is immutable, always, even at zero values -- stricter than
	// the rule for Kind below. It is half the field's identity and half the
	// partial unique index (entity_type, code); flipping it strands every
	// existing value against the wrong entity kind, because
	// custom_field_value carries no entity_type of its own to catch the
	// mismatch. An operator who meant a service field created an asset field,
	// which is one form, not an edit of this one.
	//
	// No race to guard here: entity_type can only ever change through this
	// same check, on this same row, so `before.EntityType` read a moment ago
	// cannot have gone stale in a way that flips the outcome -- unlike the
	// Kind guard below, which depends on a table this method does not own.
	if f.EntityType != before.EntityType {
		return fmt.Errorf(
			"custom field %s belongs to %s: a field for %s is a new field, not an edit: %w",
			before.Code, before.EntityType, f.EntityType, domain.ErrInvalid)
	}

	// Attribution and retirement are not this method's business -- Retire and
	// Restore own those columns.
	f.CreatedAt = before.CreatedAt
	f.CreatedBy = before.CreatedBy
	f.RetiredAt = before.RetiredAt
	f.RetiredBy = before.RetiredBy

	// writeSerializable, not write: the Kind guard below is check-then-act --
	// "does this field hold no values" -- against a table this method does
	// not own, with no unique index or FK backing the invariant the way
	// RestoreCustomField's code collision is backed by the partial unique
	// index. Read-committed lets a concurrent value write commit between the
	// count and this transaction's own commit; only serializable isolation
	// (with the retry writeSerializable already provides) actually closes it.
	// It is unreachable today, before Task 4 adds the value writer that could
	// race it -- and "unreachable today" is exactly the reasoning this repo
	// has already paid for once.
	//
	// THIS GUARD PROTECTS THE INVARIANT ONLY IF THE VALUE WRITER ALSO READS
	// custom_field.kind INSIDE ITS OWN SERIALIZABLE TRANSACTION. PostgreSQL's
	// SSI detects a serialization failure only when it finds a CYCLE of two
	// rw-antidependency edges between concurrent serializable transactions.
	// This transaction reading custom_field_value against a concurrent value
	// insert supplies ONE edge. A writer that reads nothing back supplies no
	// second edge and therefore closes no cycle, and "the kind change commits,
	// then the insert runs" is a perfectly valid serial order Postgres cannot
	// rule out.
	//
	// CORRECTED BY A LATER PROBE, and the earlier wording here said the
	// opposite as settled fact: on THIS schema even a blind INSERT into
	// custom_field_value aborts with SQLSTATE 40001. The second edge comes
	// from custom_field_value.field_id REFERENCES custom_field(id) -- the
	// referential-integrity check reads the parent row inside the inserting
	// transaction. Drop that constraint in a scratch schema and repeat the
	// probe and the blind insert commits cleanly, no abort, which is the
	// behaviour originally described. So the foreign key is load-bearing for
	// this guard, and removing it would quietly make this decorative.
	//
	// Task 4's value writer reads custom_field inside its own
	// writeSerializable transaction anyway (see customFieldDefs in
	// customvalues.go): belt as well as braces, at no extra cost, because it
	// needs the kind, the retirement state and the codes regardless.
	// TestAKindChangeAbortsAgainstAConcurrentValueWrite asserts the outcome.
	return s.writeSerializable(ctx, actor, func(t *tx) error {
		if f.Kind != before.Kind {
			n, err := t.countOne(ctx,
				`SELECT COUNT(*) FROM custom_field_value WHERE field_id = ?`, f.ID)
			if err != nil {
				return fmt.Errorf("counting values of custom field %s: %w", f.ID, err)
			}
			if n > 0 {
				return fmt.Errorf(
					"custom field %s holds %d value(s): kind cannot change while values exist, create a new field instead: %w",
					before.Code, n, domain.ErrInvalid)
			}
		}

		// requireActiveOwnerTeam runs ONLY when the owner is actually
		// CHANGING. f.OwnerTeamID is never nil by this point -- Validate
		// already refused an empty one -- but before.OwnerTeamID can be, for
		// one of the eleven fields migration 00054 could not backfill: an
		// administrator correcting that field's label for the first time
		// must be allowed to set an owner in the same edit, which is exactly
		// "changing" and goes through the check like any other change. What
		// must NOT go through it is a field whose owner already names a team
		// that has since retired -- the exact "keeps displaying, is not
		// newly selectable" rule from design.md §3, applied to this picker:
		// retirement must not retroactively invalidate every field that
		// team already owned, only stop it being chosen again.
		ownerChanged := before.OwnerTeamID == nil || *before.OwnerTeamID != *f.OwnerTeamID
		if ownerChanged {
			if err := t.requireActiveOwnerTeam(ctx, *f.OwnerTeamID); err != nil {
				return err
			}
		}

		res, err := t.exec(ctx, `
			UPDATE custom_field SET entity_type = ?, code = ?, label = ?, kind = ?,
			                         description = ?, owner_team_id = ?, row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			f.EntityType, f.Code, f.Label, f.Kind, f.Description, f.OwnerTeamID, f.ID, f.RowVersion)
		if err != nil {
			return translateWriteErr(err, "updating custom field")
		}
		if err := requireVersion(res, "custom field", f.ID, &f.RowVersion); err != nil {
			return err
		}
		values := optionValues(before.Options)
		beforeAudit := auditedCustomField(&before.CustomField, values)
		afterAudit := auditedCustomField(f, values)
		return t.logUpdate(ctx, "custom_field", f.ID, beforeAudit, afterAudit)
	})
}

// RetireCustomField soft-deletes a definition. It deletes no value, ever --
// see docs/custom-fields-design.md §6. A field already retired is left alone
// rather than refused, the way RetireTeam treats a second retirement.
func (s *SQLStore) RetireCustomField(ctx context.Context, actor domain.Actor, id string) error {
	before, err := s.GetCustomField(ctx, id)
	if err != nil {
		return err
	}
	if before.RetiredAt != nil {
		return nil
	}
	at := domain.FormatTime(s.now())
	return s.write(ctx, actor, func(t *tx) error {
		res, err := t.exec(ctx, `
			UPDATE custom_field SET retired_at = ?, retired_by = ?, row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			at, actor.ID, id, before.RowVersion)
		if err != nil {
			return translateWriteErr(err, "retiring custom field")
		}
		v := before.RowVersion
		if err := requireVersion(res, "custom field", id, &v); err != nil {
			return err
		}
		diff := fmt.Sprintf(`{"retired_at":{"old":null,"new":%q},"retired_by":{"old":null,"new":%q}}`,
			at, actor.ID)
		return t.log(ctx, "custom_field", id, domain.ActionRetire, diff, "")
	})
}

// RestoreCustomField clears retirement and brings the field and every value
// it already holds back. Refused while a live field holds the same
// (entity_type, code) -- the partial unique index freed that code for reuse
// the moment this field was retired, and restoring the original would
// produce two live fields with one code.
func (s *SQLStore) RestoreCustomField(ctx context.Context, actor domain.Actor, id string) error {
	before, err := s.GetCustomField(ctx, id)
	if err != nil {
		return err
	}
	if before.RetiredAt == nil {
		return nil
	}
	n, err := s.countOne(ctx, `
		SELECT COUNT(*) FROM custom_field
		WHERE entity_type = ? AND code = ? AND retired_at IS NULL AND id <> ?`,
		before.EntityType, before.Code, id)
	if err != nil {
		return fmt.Errorf("checking for a live field holding code %s: %w", before.Code, err)
	}
	if n > 0 {
		return fmt.Errorf("restoring custom field: a live field already holds the code %q: %w",
			before.Code, domain.ErrConflict)
	}

	return s.write(ctx, actor, func(t *tx) error {
		res, err := t.exec(ctx, `
			UPDATE custom_field SET retired_at = NULL, retired_by = NULL, row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			id, before.RowVersion)
		if err != nil {
			return translateWriteErr(err, "restoring custom field")
		}
		v := before.RowVersion
		if err := requireVersion(res, "custom field", id, &v); err != nil {
			return err
		}
		retiredAt := ""
		if before.RetiredAt != nil {
			retiredAt = *before.RetiredAt
		}
		// "restore" is not in the change_log.action vocabulary (migration
		// 00009_change_log_constraints.sql) and this codebase's first
		// restorable entity is not a reason to widen it -- it is logged as
		// the update it is: two columns moving back to their zero value.
		diff := fmt.Sprintf(`{"retired_at":{"old":%q,"new":null},"retired_by":{"old":%q,"new":null}}`,
			retiredAt, actorOrEmpty(before.RetiredBy))
		return t.log(ctx, "custom_field", id, domain.ActionUpdate, diff, "")
	})
}

// actorOrEmpty is a small helper for building the restore diff above: a
// retired-by column is nullable, and %q on a nil *string would panic rather
// than render "null".
func actorOrEmpty(id *string) string {
	if id == nil {
		return ""
	}
	return *id
}

// SetCustomFieldOptions replaces a field's option vocabulary wholesale, in
// the field's own transaction, the same shape as asset_environment and
// dependency_data_class -- except an option is never deleted, only retired
// (migration 00051's comment on custom_field_option), because existing
// values that chose it must keep resolving and keep displaying.
//
// A value already in opts is un-retired and its label/position brought up to
// date; a live value missing from opts is retired; a value new to opts is
// inserted. The field's change_log entry folds the resulting live option set
// in as a second field (customFieldAudit), so a set replacement that leaves
// the CustomField struct itself untouched still writes an audit row.
//
// expected IS THE FIELD'S OWN row_version, THE ONE THE FORM WAS RENDERED
// WITH -- Ruling W. Without it two administrators editing one field's options
// never learn they collided, and the outcome is worse than an ordinary lost
// update in both directions: an option present in a stale list is un-retired,
// silently reversing a retirement another administrator just made; an option
// absent from a stale list is retired, silently undoing an addition the
// submitting administrator never saw. A stale token is refused outright with
// domain.ErrStale (409), which closes both directions at once rather than
// reasoning about either.
func (s *SQLStore) SetCustomFieldOptions(ctx context.Context, actor domain.Actor, fieldID string, expected int, opts []domain.CustomFieldOption) error {
	before, err := s.GetCustomField(ctx, fieldID)
	if err != nil {
		return err
	}
	beforeValues := optionValues(before.Options)

	// A duplicate value would silently double-write the same row -- last one
	// wins, and nothing above notices -- so both it and an empty value or
	// label are refused up front, naming the offending value.
	//
	// FINAL REVIEW AY: value and label are bounded and control-character-
	// checked here, the same rules a `text` custom value obeys
	// (domain.ValidateCustomFieldOptionText) -- an option's value is text an
	// administrator typed, and CanonicalCustomValue's select branch only
	// ever checks MEMBERSHIP, never bounds, so an oversized or control-
	// character-laden option value sailed straight through the moment a
	// value selected it. opts[i] is rewritten in place with the trimmed,
	// validated text, the same way the text kind stores the trimmed original
	// rather than the raw submission.
	desired := make(map[string]bool, len(opts))
	for i := range opts {
		value, err := domain.ValidateCustomFieldOptionText("value", opts[i].Value)
		if err != nil {
			return fmt.Errorf("setting options of custom field %s: option value %w", fieldID, err)
		}
		label, err := domain.ValidateCustomFieldOptionText("label", opts[i].Label)
		if err != nil {
			return fmt.Errorf("setting options of custom field %s: option %q label %w", fieldID, value, err)
		}
		opts[i].Value = value
		opts[i].Label = label
		if desired[value] {
			return fmt.Errorf("setting options of custom field %s: value %q is listed more than once: %w",
				fieldID, value, domain.ErrInvalid)
		}
		desired[value] = true
	}
	existingByValue := make(map[string]domain.CustomFieldOption, len(before.Options))
	for _, o := range before.Options {
		existingByValue[o.Value] = o
	}

	at := domain.FormatTime(s.now())
	return s.write(ctx, actor, func(t *tx) error {
		// The stale-token guard, first and alone: refused outright, before a
		// single option row is touched, so a collision never leaves a partial
		// write behind for the transaction to roll back around.
		res, err := t.exec(ctx,
			`UPDATE custom_field SET row_version = row_version + 1 WHERE id = ? AND row_version = ?`,
			fieldID, expected)
		if err != nil {
			return translateWriteErr(err, "bumping the custom field version")
		}
		version := expected
		if err := requireVersion(res, "custom field", fieldID, &version); err != nil {
			return err
		}

		for i, o := range opts {
			if cur, ok := existingByValue[o.Value]; ok {
				if _, err := t.exec(ctx, `
					UPDATE custom_field_option SET label = ?, position = ?, retired_at = NULL
					WHERE id = ?`,
					o.Label, i, cur.ID); err != nil {
					return translateWriteErr(err, "updating custom field option")
				}
				continue
			}
			if _, err := t.exec(ctx, `
				INSERT INTO custom_field_option (id, field_id, value, label, position, retired_at)
				VALUES (?, ?, ?, ?, ?, NULL)`,
				NewID(), fieldID, o.Value, o.Label, i); err != nil {
				return translateWriteErr(err, "creating custom field option")
			}
		}
		for _, cur := range before.Options {
			if desired[cur.Value] || cur.RetiredAt != nil {
				continue
			}
			if _, err := t.exec(ctx,
				`UPDATE custom_field_option SET retired_at = ? WHERE id = ?`, at, cur.ID); err != nil {
				return translateWriteErr(err, "retiring custom field option")
			}
		}

		// FINAL REVIEW B2: "value=label", the same shape optionValues folds
		// beforeValues into, so a label-only rename (value and position
		// unchanged) still produces a diff -- opts is what the submission
		// named, not a re-read, but it carries every live option's label
		// exactly as it will be written above.
		afterValues := make([]string, 0, len(opts))
		for _, o := range opts {
			afterValues = append(afterValues, o.Value+"="+o.Label)
		}
		beforeAudit := auditedCustomField(&before.CustomField, beforeValues)
		afterAudit := auditedCustomField(&before.CustomField, afterValues)
		return t.logUpdate(ctx, "custom_field", fieldID, beforeAudit, afterAudit)
	})
}
