// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"net/http"
	"sort"
	"strings"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/web/render"
)

// Custom field VALUES on a detail page: the grouped display and the editor,
// docs/custom-fields-design.md §4, §6 and §8. Task 5 built the registry
// (customfields.go); this is the last user-facing piece.

// customFieldFormRow is one LIVE field with what this entity currently
// holds, rendered VERBATIM -- never reformatted -- and its live options
// when the kind is select. A retired field never appears here: the section
// disappears from forms and detail pages entirely (design.md §6), and this
// slice is built from a live-only field list.
type customFieldFormRow struct {
	Field domain.CustomField
	// Value is the stored value_text, or "" when the entity holds none.
	Value string
	// Options is the field's LIVE options, for a select kind only. A
	// retired option keeps displaying on the values that already chose it
	// but is never offered to a new one (design.md §3), so only live ones
	// belong in a picker.
	Options []domain.CustomFieldOption
	// Representable reports whether Value would be accepted by
	// domain.CanonicalCustomValue TODAY, under the current validator and
	// (for select) the current live options -- an empty Value always
	// counts as representable. This is the render-time half of the defence
	// select already had (see the "FINAL REVIEW B1" comment above): a
	// typed HTML widget -- <input type="number">, <input type="date"> --
	// silently BLANKS a value it cannot draw, and a blank posts back as an
	// explicit clear on the next unrelated save. Nothing in this feature
	// can produce such a value today, which is exactly why this flag has to
	// exist rather than "fix the writer": the day it stops being safe is an
	// import, a restored backup from an older build, or a future loosening
	// of the validator, none of which go through postCustomFields. When
	// this is false, the template draws a plain text input instead of the
	// typed widget so a save cannot discard what it cannot parse.
	Representable bool
	// OwnerDisplay is who to ask about this field -- store.CustomFieldRow's
	// own OwnerDisplay(), carried across because the editor's row here is
	// built from domain.CustomField alone (see below), which does not
	// resolve a team's contact_ref or code. Empty for one of the eleven
	// fields migration 00054 could not backfill; the template renders
	// nothing in that case rather than a blank "Owner:" line.
	OwnerDisplay string
	// OwnerRetired mirrors store.CustomFieldRow.OwnerRetired(): the field's
	// owner is unaffected by the team's own retirement (design.md §3's
	// rule, applied to this picker) -- this only controls whether "(retired)"
	// is appended to what OwnerDisplay already shows.
	OwnerRetired bool
}

// InputName is the one name a submission may use to touch this field:
// cf_<field id>. The handler builds its write payload from exactly the
// cf_ keys present in the POST, so this name is the whole contract between
// what the form draws and what a submission can mean.
func (row customFieldFormRow) InputName() string {
	return "cf_" + row.Field.ID
}

// customFieldsPanel is what custom_fields_show and custom_fields_form both
// render, standalone, the same shape cost_panel already uses so the asset
// and service pages can each hand it their own action URL without two
// near-identical copies drifting apart.
type customFieldsPanel struct {
	Action     string
	CSRF       string
	CanWrite   bool
	RowVersion int
	// Edit is set only when a submission against this entity's custom
	// fields was refused; see editState. Nil-safe, like every other use of
	// it in this codebase.
	Edit *editState
	Rows []customFieldFormRow
}

// customFieldsEditID names the sentinel editState.ID a custom-field-value
// refusal carries, distinct from any port, address or cost line id that
// also rides the shared ?edit= query parameter on the same detail page. It
// never collides with a real row: those are UUIDv7, this is not.
func customFieldsEditID(entityID string) string {
	return "cf-values:" + entityID
}

// loadCustomFieldsPanel builds the display and edit data for one entity's
// custom fields: every LIVE field defined for its entity type, the value
// this entity holds for each (verbatim), and live options for a select
// kind. Read fresh on every render -- this is what the page SHOWS, never
// what a submission is built from; see postCustomFields for that half of
// the contract.
func (a *App) loadCustomFieldsPanel(r *http.Request, entityType, entityID string,
	rowVersion int, action string, csrf string, canWrite bool, edit *editState) (customFieldsPanel, error) {

	defs, err := a.Store.ListCustomFields(r.Context(), entityType, false)
	if err != nil {
		return customFieldsPanel{}, err
	}
	values, err := a.Store.CustomValuesFor(r.Context(), entityType, entityID)
	if err != nil {
		return customFieldsPanel{}, err
	}
	byField := make(map[string]string, len(values))
	for _, v := range values {
		byField[v.FieldID] = v.ValueText
	}

	rows := make([]customFieldFormRow, 0, len(defs))
	for _, d := range defs {
		row := customFieldFormRow{
			Field:        d.CustomField,
			Value:        byField[d.ID],
			OwnerDisplay: d.OwnerDisplay(),
			OwnerRetired: d.OwnerRetired(),
		}
		if d.Kind == domain.CustomFieldSelect {
			full, err := a.Store.GetCustomField(r.Context(), d.ID)
			if err != nil {
				return customFieldsPanel{}, err
			}
			for _, o := range full.Options {
				if o.RetiredAt == nil {
					row.Options = append(row.Options, o)
				}
			}
			// FINAL REVIEW B1: the operator must be shown everything the
			// submission will decide (design.md §6, the converse half of the
			// contract). A value naming an option retired since it was set
			// keeps displaying (§3) -- which means the widget that draws it
			// must offer it too, or the browser falls back to its own blank
			// "not set" choice and the next unrelated save posts that blank
			// back as an explicit clear. Appended once, only when it is the
			// value THIS entity holds: a retired option that is not the
			// current value stays unofferable to a new selection.
			if row.Value != "" {
				current := false
				for _, o := range row.Options {
					if o.Value == row.Value {
						current = true
						break
					}
				}
				if !current {
					for _, o := range full.Options {
						if o.Value == row.Value && o.RetiredAt != nil {
							row.Options = append(row.Options, o)
							break
						}
					}
				}
			}
		}
		// Representability is checked against the SAME options the widget
		// would offer (row.Options, which for a select already carries the
		// appended retired option above), never a fresh membership list --
		// otherwise this check would fight the fix directly above it and
		// mark a select's own retained value as unrepresentable.
		row.Representable = row.Value == ""
		if !row.Representable {
			optValues := make([]string, 0, len(row.Options))
			for _, o := range row.Options {
				optValues = append(optValues, o.Value)
			}
			if _, err := domain.CanonicalCustomValue(d.Kind, row.Value, optValues); err == nil {
				row.Representable = true
			}
		}
		rows = append(rows, row)
	}

	return customFieldsPanel{
		Action:     action,
		CSRF:       csrf,
		CanWrite:   canWrite,
		RowVersion: rowVersion,
		Edit:       edit,
		Rows:       rows,
	}, nil
}

// AssetCustomFields sets an asset's custom field values.
func (a *App) AssetCustomFields(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	asset, err := a.Store.GetAsset(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.postCustomFields(w, r, domain.CustomFieldEntityAsset, id, asset.RowVersion,
		func(status int, cfEdit *editState) { a.renderAssetDetail(w, r, status, id, cfEdit) },
		func() {
			a.setFlash(r, "success", "Custom fields updated.")
			render.Redirect(w, r, "/assets/"+id)
		})
}

// ServiceCustomFields sets a service's custom field values.
func (a *App) ServiceCustomFields(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	svc, err := a.Store.GetService(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.postCustomFields(w, r, domain.CustomFieldEntityService, id, svc.RowVersion,
		func(status int, cfEdit *editState) {
			a.renderServiceDetail(w, r, status, id, endpointFormState{}, cfEdit)
		},
		func() {
			a.setFlash(r, "success", "Custom fields updated.")
			render.Redirect(w, r, "/services/"+id)
		})
}

// postCustomFields applies one submission of custom values against entityID,
// shared between the asset and service routes. rerender redraws the whole
// detail page at the given status with the refusal folded in; onSuccess
// runs once the write has gone in.
//
// THE PAYLOAD IS BUILT FROM WHAT THE FORM DREW, docs/custom-fields-design.md
// §6 -- every cf_<field id> key present in the POST, and nothing else.
// Never a fresh a.Store.ListCustomFields, never a.Store.CustomValuesFor: both
// are payloads assembled from a query rather than from a rendering, and both
// would destroy a value the operator never saw. A retired field renders no
// input (loadCustomFieldsPanel is built from a live-only list), so it can
// never arrive here as a key -- there is nothing to accidentally clear.
func (a *App) postCustomFields(w http.ResponseWriter, r *http.Request, entityType, entityID string,
	currentVersion int, rerender func(status int, cfEdit *editState), onSuccess func()) {

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}

	// The live field definitions, needed to know each submitted field's
	// kind and (for a select) its live options, so a bad value can be
	// pre-validated and attributed to the field that produced it before
	// anything is written -- SetCustomValues stops at the first bad field
	// and reports it by CODE, not by id, which is not what a per-field
	// form error needs.
	defs, err := a.Store.ListCustomFields(r.Context(), entityType, false)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	byID := make(map[string]domain.CustomField, len(defs))
	for _, d := range defs {
		byID[d.ID] = d.CustomField
	}

	// FINAL REVIEW B1: what this entity already holds, so an UNCHANGED
	// resubmission never re-enters kind validation. setCustomValues itself
	// already treats an unchanged value as untouched regardless of the
	// field's or (for select) the option's retirement -- see the comment on
	// "AN UNCHANGED VALUE IS NOT A NEW VALUE" there. Without the same
	// exception here, this handler's OWN pre-validation -- which exists only
	// to attribute a bad value to its field before anything is written --
	// re-checked a value the store would have accepted unchanged, and for a
	// select field naming a since-retired option that re-check fails: its
	// membership test only ever sees LIVE options. A form that correctly
	// draws the retained value (see loadCustomFieldsPanel) would then be
	// refused with 422 on the very next unrelated save.
	current, err := a.Store.CustomValuesFor(r.Context(), entityType, entityID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	storedByID := make(map[string]string, len(current))
	for _, v := range current {
		storedByID[v.FieldID] = v.ValueText
	}

	vals := map[string]string{}
	fieldNames := make([]string, 0, len(r.PostForm))
	for key, v := range r.PostForm {
		id, ok := strings.CutPrefix(key, "cf_")
		if !ok || len(v) == 0 {
			continue
		}
		vals[id] = v[0]
		fieldNames = append(fieldNames, key)
	}

	ids := make([]string, 0, len(vals))
	for id := range vals {
		ids = append(ids, id)
	}
	// Sorted so a form with two bad fields always names the same one
	// first -- the same reasoning setCustomValues itself gives for
	// sorting its own field ids.
	sort.Strings(ids)

	errs := map[string]string{}
	for _, id := range ids {
		def, ok := byID[id]
		if !ok {
			// Not a field this entity type has live -- either a stale form
			// naming a field retired since it was opened, or a hand-built
			// request. RULING AJ: dropped from the payload rather than
			// forwarded, which is the first half of the submission contract
			// applied to the race -- a submission may only name what the
			// operator was shown, and this operator was not shown a field
			// that retired out from under them. Forwarding it left
			// SetCustomValues to refuse it correctly but via a wrapped
			// ErrInvalid that handleStoreError renders as a generic error
			// page instead of the styled per-field form every sibling
			// refusal in this handler produces.
			delete(vals, id)
			continue
		}
		raw := strings.TrimSpace(vals[id])
		if raw == "" {
			continue // a blank clears; clearing needs no validation
		}
		if stored, held := storedByID[id]; held && stored == raw {
			continue // unchanged, verbatim -- the store leaves it exactly where it is
		}
		var options []string
		if def.Kind == domain.CustomFieldSelect {
			full, err := a.Store.GetCustomField(r.Context(), id)
			if err != nil {
				a.serverError(w, r, err)
				return
			}
			for _, o := range full.Options {
				if o.RetiredAt == nil {
					options = append(options, o.Value)
				}
			}
		}
		if _, err := domain.CanonicalCustomValue(def.Kind, raw, options); err != nil {
			errs["cf_"+id] = customFieldErrorMessage(err, vals[id])
		}
	}

	if len(errs) > 0 {
		rerender(http.StatusUnprocessableEntity, rejected(r, customFieldsEditID(entityID), errs, fieldNames...))
		return
	}

	// THE TOKEN IS THE PARENT ENTITY'S row_version, the one the form was
	// rendered with -- design.md §3. currentVersion is only the fallback
	// for a form that carried none at all, the same rule submittedVersion
	// documents for every other editor.
	expected := submittedVersion(r, currentVersion)
	if err := a.Store.SetCustomValues(r.Context(), actor(r), entityType, entityID, expected, vals); err != nil {
		if isStale(err) {
			rerender(http.StatusConflict, rejected(r, customFieldsEditID(entityID), staleMessage("_form"), fieldNames...))
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	onSuccess()
}

// customFieldErrorMessage extracts the message a rejected value should show.
//
// CARRIED FROM TASK 2: the control-character refusal is the same generic
// message for an embedded NUL and an embedded newline, because the domain
// layer cannot tell an operator's intent apart from the character class. An
// operator who pasted a two-line value is told about the line break, not
// about "control characters" -- special-cased here, where the error is
// rendered, on the raw text this handler still has and the domain layer
// does not keep.
func customFieldErrorMessage(err error, raw string) string {
	msg := err.Error()
	if ve, ok := domain.AsValidation(err); ok && len(ve.Fields) > 0 {
		msg = ve.Fields[0].Message
	}
	if strings.Contains(msg, "control characters") && strings.ContainsAny(raw, "\n\r") {
		return "must be a single line"
	}
	return msg
}
