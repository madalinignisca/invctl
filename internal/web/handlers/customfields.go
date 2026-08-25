// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web/render"
)

// Custom fields: the registry, docs/custom-fields-design.md §4.
//
// This is the page that answers "did invctl ship this field, or did someone
// here add it?" without a phone call to the vendor -- label, code, kind,
// entity type, description, who defined it, when, how many entities hold a
// value, and its retirement state with who retired it and when.
//
// created_by and retired_by are opaque app_user.id, resolved to a display
// name for rendering exactly as change_log.actor already is (store.CustomFieldRow
// carries the resolved names). Nothing here stores or renders a username as
// the value itself, so scrubbing a user for an erasure request leaves the row
// readable -- it simply stops resolving a name.

// customFieldForm is what the operator typed, redrawn on a rejected create.
type customFieldForm struct {
	EntityType  string
	Code        string
	Label       string
	Kind        string
	Description string
	OwnerTeamID string
}

// customFieldListPage is the registry.
type customFieldListPage struct {
	Base
	Errors map[string]string
	Spec   customFieldForm
	// Grouped by entity type -- design.md §4 -- because that is the question
	// somebody opening this page is asking: "what can an asset carry" is a
	// different question from "what can a service carry".
	AssetFields   []store.CustomFieldRow
	ServiceFields []store.CustomFieldRow
	// RetiredFields holds both entity types together, in its own section,
	// with retirement date, who retired it, and the retained-value count
	// each CustomFieldRow already carries.
	RetiredFields []store.CustomFieldRow
	EntityTypes   []string
	Kinds         []string
	// Teams is the ACTIVE owner picker -- store.TeamOptions, the same
	// exclude-retired rule every other team picker in this codebase follows
	// (see its own comment): a picker offers what somebody may choose NOW.
	// Used by both the create form and, per row, OwnerOptions below.
	Teams []store.TeamRow
	// Edit is set only when a definition edit was refused; see editState.
	Edit *editState
	// EditOptions is the id of the field whose option editor is open, read
	// from ?options= -- the options editor's own token, mirroring
	// Base.EditRow's ?edit= for the definition editor. A field carries at
	// most one open editor at a time, so a second query parameter rather
	// than overloading EditRow keeps the two independent: opening the
	// definition row must not also open its options, and vice versa.
	EditOptions string
	// OptionsEdit is set only when an options submission was refused.
	OptionsEdit *editState
}

// optionFormRow is one value/label pair the options editor draws.
type optionFormRow struct {
	Value string
	Label string
}

// OptionRows builds the rows the options editor renders for one select
// field: its LIVE options, in position order, plus one blank pair to add
// another. THE PAYLOAD IS WHAT THE FORM DREW (design.md §6): a rejected
// submission redraws exactly what was typed, from OptionsEdit.Multi, rather
// than re-reading the field -- the row the operator was correcting stays on
// the screen instead of reverting to what is stored.
func (p customFieldListPage) OptionRows(row store.CustomFieldRow) []optionFormRow {
	if p.OptionsEdit != nil && p.OptionsEdit.ID == row.ID {
		values := p.OptionsEdit.Multi["option_value"]
		labels := p.OptionsEdit.Multi["option_label"]
		rows := make([]optionFormRow, len(values))
		for i, v := range values {
			var label string
			if i < len(labels) {
				label = labels[i]
			}
			rows[i] = optionFormRow{Value: v, Label: label}
		}
		return rows
	}
	rows := make([]optionFormRow, 0, len(row.Options)+1)
	for _, o := range row.Options {
		if o.RetiredAt == nil {
			rows = append(rows, optionFormRow{Value: o.Value, Label: o.Label})
		}
	}
	return append(rows, optionFormRow{})
}

// optionsEditFor returns OptionsEdit only when it belongs to this row, nil
// otherwise -- so the template never has to dereference OptionsEdit itself
// and risk a nil pointer when no options submission was rejected.
func (p customFieldListPage) optionsEditFor(rowID string) *editState {
	if p.OptionsEdit != nil && p.OptionsEdit.ID == rowID {
		return p.OptionsEdit
	}
	return nil
}

// OptionsError is the message against a rejected options submission, or
// empty when this row's options were not the one refused.
func (p customFieldListPage) OptionsError(row store.CustomFieldRow) string {
	if e := p.optionsEditFor(row.ID); e != nil {
		return e.Err("options")
	}
	return ""
}

// OptionsRowVersion is the token the options form must carry: the stale one
// it was just refused with, or the field's current one on a fresh open --
// never re-read from the field on a refusal, which is exactly what Ruling W
// exists to prevent.
func (p customFieldListPage) OptionsRowVersion(row store.CustomFieldRow) string {
	if e := p.optionsEditFor(row.ID); e != nil {
		return e.Value("row_version", strconv.Itoa(row.RowVersion))
	}
	return strconv.Itoa(row.RowVersion)
}

// OwnerOptions is the owner picker for one row's edit form: the active
// teams (p.Teams) plus, if row's CURRENT owner has since been retired, that
// one team too, appended and rendered "(retired)".
//
// The reasoning is the exact symmetry docs/custom-fields-design.md §3 and
// §4 both name explicitly: what is STORED must keep displaying, what is
// RETIRED must not be newly selectable. TeamOptions already enforces the
// second half by excluding retired teams outright; this function is the
// first half, restoring the one retired team a row is actually pinned to
// so the select can draw back what custom_field.owner_team_id already
// holds (the same round-trip requirement a `select` field's own retired
// option answers in loadCustomFieldsPanel) -- without it the browser would
// silently fall back to the FIRST active team in the list, and the next
// unrelated save on this row would reassign the field's owner as a side
// effect of correcting its label.
func (p customFieldListPage) OwnerOptions(row store.CustomFieldRow) []store.TeamRow {
	if row.OwnerTeamID == nil {
		return p.Teams
	}
	for _, t := range p.Teams {
		if t.ID == *row.OwnerTeamID {
			return p.Teams // already active and already in the list
		}
	}
	if row.OwnerTeamCode == nil {
		return p.Teams // nothing to reconstruct a display row from
	}
	retired := store.TeamRow{}
	retired.ID = *row.OwnerTeamID
	retired.Code = *row.OwnerTeamCode
	retired.Lifecycle = domain.LifecycleRetired
	out := make([]store.TeamRow, 0, len(p.Teams)+1)
	out = append(out, p.Teams...)
	return append(out, retired)
}

// enrichOptions fills in Options on the one row matching id, in place. A
// no-op if id names no row in rows -- the field being edited lives in the
// other slice (asset vs service), and this is called against both.
func enrichOptions(rows []store.CustomFieldRow, id string, opts []domain.CustomFieldOption) {
	for i := range rows {
		if rows[i].ID == id {
			rows[i].Options = opts
			return
		}
	}
}

// CustomFieldList renders the registry.
func (a *App) CustomFieldList(w http.ResponseWriter, r *http.Request) {
	a.renderCustomFieldList(w, r, http.StatusOK, "custom_field_list_panel", nil,
		customFieldForm{EntityType: domain.CustomFieldEntityAsset, Kind: domain.CustomFieldText}, nil, nil)
}

// renderCustomFieldList redraws the whole registry, full page or an HTMX
// partial depending on the request. partial names WHICH fragment an HTMX
// request receives: the list/retired tables ("custom_field_list_panel") for
// everything that changes a row, or the create form itself
// ("custom_field_form") for a rejected create -- the operator submitted a
// form, not a table row, and the errors they need to see live in the form,
// not in the panel beside it.
func (a *App) renderCustomFieldList(w http.ResponseWriter, r *http.Request, status int, partial string,
	errs map[string]string, spec customFieldForm, edit *editState, optionsEdit *editState) {

	all, err := a.Store.ListCustomFields(r.Context(), "", true)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	teams, err := a.Store.TeamOptions(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	var assetFields, serviceFields, retired []store.CustomFieldRow
	for _, f := range all {
		if f.IsRetired() {
			retired = append(retired, f)
			continue
		}
		switch f.EntityType {
		case domain.CustomFieldEntityAsset:
			assetFields = append(assetFields, f)
		case domain.CustomFieldEntityService:
			serviceFields = append(serviceFields, f)
		}
	}

	base := a.base(r, "Custom fields", "custom-fields")
	if edit != nil {
		// The rejected row is the one that opens, whatever the query string
		// said -- the operator is looking at the form they just submitted.
		base.EditRow = edit.ID
	}
	editOptions := r.URL.Query().Get("options")
	if optionsEdit != nil {
		editOptions = optionsEdit.ID
	}

	// Ruling AF. ListCustomFields leaves Options empty on every row -- fine
	// for the table, which renders nothing from it, but the options editor
	// (OptionRows) reads it directly on a fresh open. Left unenriched, that
	// editor drew a blank row over every option a field already had: honest
	// about what the submission named and wrong about what it meant, because
	// the writer commits the empty draw faithfully (design.md §6, the
	// converse half of the submission contract). Enriched here rather than
	// left to OptionRows, and only for the one row being edited -- the store
	// comment's N+1 concern still holds for the list itself.
	//
	// Skipped when optionsEdit is set: a rejected submission redraws from
	// OptionsEdit.Multi, never from row.Options, so the extra read would be
	// wasted. The definition editor (?edit=) needs no equivalent enrichment
	// -- it draws Code/Label/Kind/Description straight off the row already
	// in AssetFields/ServiceFields, never off Options.
	if editOptions != "" && optionsEdit == nil {
		if full, err := a.Store.GetCustomField(r.Context(), editOptions); err == nil {
			enrichOptions(assetFields, editOptions, full.Options)
			enrichOptions(serviceFields, editOptions, full.Options)
		}
		// A lookup failure here (id gone, wrong type) is not fatal: the row
		// simply will not match EditOptions in the template and the editor
		// will not open, the same as any other stale link.
	}

	a.Render.Respond(w, r, status, "custom_field_list", partial, customFieldListPage{
		Base:          base,
		Errors:        orEmpty(errs),
		Spec:          spec,
		AssetFields:   assetFields,
		ServiceFields: serviceFields,
		RetiredFields: retired,
		EntityTypes:   domain.CustomFieldEntityTypes,
		Kinds:         domain.CustomFieldKinds,
		Teams:         teams,
		Edit:          edit,
		EditOptions:   editOptions,
		OptionsEdit:   optionsEdit,
	})
}

// CustomFieldCreate defines a field.
func (a *App) CustomFieldCreate(w http.ResponseWriter, r *http.Request) {
	spec := customFieldForm{
		EntityType:  formValue(r, "entity_type"),
		Code:        formValue(r, "code"),
		Label:       formValue(r, "label"),
		Kind:        formValue(r, "kind"),
		Description: formValue(r, "description"),
		OwnerTeamID: formValue(r, "owner_team_id"),
	}

	// THE SPECIFIC, NAMED REFUSAL: an estate with no active team cannot name
	// one no matter what it submits, and domain.checkOwnerTeam's generic
	// "is required" would tell that operator to do something the form
	// cannot let them do. Checked before NewCustomField is even called, and
	// worded like migration 00014's own comment ("create the teams first
	// and set team_id") rather than inventing new phrasing for the same
	// situation.
	teams, err := a.Store.TeamOptions(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if len(teams) == 0 {
		a.renderCustomFieldList(w, r, http.StatusUnprocessableEntity, "custom_field_form",
			map[string]string{"owner_team_id": "no active teams exist: create the teams first and set an owner before defining a custom field"},
			spec, nil, nil)
		return
	}

	f, err := domain.NewCustomField(store.NewID(), spec.EntityType, spec.Code, spec.Label,
		spec.Kind, spec.Description, actor(r).ID, spec.OwnerTeamID, a.Store.Now())
	if err == nil {
		err = a.Store.CreateCustomField(r.Context(), actor(r), f)
	}
	if err != nil {
		if messages, ok := validationErrors(err); ok {
			a.renderCustomFieldList(w, r, http.StatusUnprocessableEntity, "custom_field_form", messages, spec, nil, nil)
			return
		}
		if isConflict(err) {
			a.renderCustomFieldList(w, r, http.StatusUnprocessableEntity, "custom_field_form",
				map[string]string{"code": "a live field with that code already exists for this entity type"},
				spec, nil, nil)
			return
		}
		// requireActiveOwnerTeam's own refusals -- a nonexistent or a
		// retired team named as owner -- wrap domain.ErrInvalid rather than
		// ValidationError, since the check runs against the database inside
		// the write transaction, not against the shape NewCustomField alone
		// can see. Attributed to "owner_team_id" specifically: it is the
		// only field this refusal can ever be about.
		if errors.Is(err, domain.ErrInvalid) {
			a.renderCustomFieldList(w, r, http.StatusUnprocessableEntity, "custom_field_form",
				map[string]string{"owner_team_id": trimSentinel(err, domain.ErrInvalid)}, spec, nil, nil)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}

	a.setFlash(r, "success", "Custom field "+f.Label+" defined.")
	render.Redirect(w, r, "/custom-fields")
}

// CustomFieldUpdate corrects a definition. entity_type is not read from the
// form and cannot change here -- it is half the field's identity (design.md
// §2) and offering it would let one form silently strand every value the
// field already holds against the wrong entity kind.
func (a *App) CustomFieldUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := a.Store.GetCustomField(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	updated := existing.CustomField
	updated.Code = formValue(r, "code")
	updated.Label = formValue(r, "label")
	updated.Kind = formValue(r, "kind")
	updated.Description = formValue(r, "description")
	updated.OwnerTeamID = submittedString(r, "owner_team_id", updated.OwnerTeamID)
	updated.RowVersion = submittedVersion(r, updated.RowVersion)

	if err := a.Store.UpdateCustomField(r.Context(), actor(r), &updated); err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			switch {
			case isStale(err):
				messages = staleMessage("description")
			case isConflict(err):
				messages = map[string]string{"code": "a live field with that code already exists for this entity type"}
			case errors.Is(err, domain.ErrInvalid):
				// Three distinct store refusals share this sentinel: the
				// kind-frozen-while-values-exist guard, which belongs
				// against "kind"; the entity_type-immutable guard --
				// unreachable from this handler today because the form
				// never offers entity_type, but a change elsewhere in the
				// call chain should not silently mislabel it as a kind
				// problem if it ever does become reachable; and
				// requireActiveOwnerTeam's own refusal, reachable whenever
				// an operator changes the owner to a team that no longer
				// exists or has since retired. Told apart by which guard
				// actually wrote the message, not guessed.
				field := "kind"
				switch {
				case strings.Contains(err.Error(), "belongs to"):
					field = "entity_type"
				case strings.Contains(err.Error(), "owner team"):
					field = "owner_team_id"
				}
				messages = map[string]string{field: trimSentinel(err, domain.ErrInvalid)}
			default:
				a.handleStoreError(w, r, err)
				return
			}
		}
		a.renderCustomFieldList(w, r, refusalStatus(err), "custom_field_list_panel", nil, customFieldForm{},
			rejected(r, id, messages, "code", "label", "kind", "description", "owner_team_id"), nil)
		return
	}

	a.setFlash(r, "success", "Custom field "+updated.Label+" updated.")
	render.Redirect(w, r, "/custom-fields")
}

// CustomFieldRetire retires a definition. It deletes no value, ever -- see
// docs/custom-fields-design.md §6 -- and a field already retired is left
// alone rather than refused, the way TeamRetire treats a second retirement.
func (a *App) CustomFieldRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetireCustomField(r.Context(), actor(r), r.PathValue("id")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Custom field retired. Every value it already holds is kept.")
	render.Redirect(w, r, "/custom-fields")
}

// CustomFieldRestore brings a retired field and every value it still retains
// back. Refused if a live field already holds the same (entity_type, code).
func (a *App) CustomFieldRestore(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RestoreCustomField(r.Context(), actor(r), r.PathValue("id")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Custom field restored, with every value it retained.")
	render.Redirect(w, r, "/custom-fields")
}

// CustomFieldOptions replaces a select field's live option vocabulary.
//
// THE PAYLOAD IS WHAT THE FORM DREW, never a list re-read at submit time
// (design.md §6, "a submission may only name what the operator was shown").
// The registry renders one input pair per LIVE option plus one blank pair to
// add a new one; that blank pair, submitted untouched, is dropped here rather
// than turned into an empty option no entity could ever select.
//
// row_version is the FIELD's own token, the one the form was rendered with --
// Ruling W. A stale submission is refused outright (409) rather than
// reasoned about, because it is wrong in both directions at once: it would
// un-retire an option somebody else just retired, and it would retire an
// option somebody else just added that the stale form never showed.
func (a *App) CustomFieldOptions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := a.Store.GetCustomField(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	values := r.PostForm["option_value"]
	labels := r.PostForm["option_label"]
	opts := make([]domain.CustomFieldOption, 0, len(values))
	for i, v := range values {
		v = strings.TrimSpace(v)
		var label string
		if i < len(labels) {
			label = strings.TrimSpace(labels[i])
		}
		if v == "" && label == "" {
			// The blank row the form always renders to add one more option,
			// submitted untouched. Naming nothing is not naming an option.
			continue
		}
		opts = append(opts, domain.CustomFieldOption{Value: v, Label: label})
	}

	expected := submittedVersion(r, existing.RowVersion)
	if err := a.Store.SetCustomFieldOptions(r.Context(), actor(r), id, expected, opts); err != nil {
		// FINAL REVIEW AY: err.Error() must never reach the browser verbatim
		// -- CLAUDE.md, "never return a raw driver error to the HTTP layer".
		// Only a domain-recognised refusal gets its own styled per-field
		// message here: a stale token, a validation failure on an option's
		// value or label (domain.ValidateCustomFieldOptionText, bounded and
		// control-character-checked the same way a text custom value is),
		// or another domain.ErrInvalid this handler already knows how to
		// word (a duplicate option value). Anything else -- including
		// whatever translateWriteErr's own catch-all wrapped a driver said
		// -- goes through handleStoreError, which maps to a status and a
		// message it authored, never one it received.
		var messages map[string]string
		switch {
		case isStale(err):
			messages = staleMessage("options")
		default:
			if ve, ok := domain.AsValidation(err); ok && len(ve.Fields) > 0 {
				messages = map[string]string{"options": ve.Fields[0].Message}
			} else if errors.Is(err, domain.ErrInvalid) {
				messages = map[string]string{"options": trimSentinel(err, domain.ErrInvalid)}
			} else {
				a.handleStoreError(w, r, err)
				return
			}
		}
		// The Multi values, not Values -- an option list is a set of pairs,
		// and editState.Value only ever holds one string per field. The row
		// the operator typed reopens exactly as they left it.
		edit := rejected(r, id, messages).withMulti("option_value", values).withMulti("option_label", labels)
		a.renderCustomFieldList(w, r, refusalStatus(err), "custom_field_list_panel", nil, customFieldForm{}, nil, edit)
		return
	}

	a.setFlash(r, "success", "Options updated.")
	render.Redirect(w, r, "/custom-fields")
}

// trimSentinel strips a store error's wrapped sentinel suffix for display,
// so an operator sees "kind cannot change while values exist" rather than
// "...: invalid" tacked onto the end of it.
func trimSentinel(err error, sentinel error) string {
	return strings.TrimSuffix(err.Error(), ": "+sentinel.Error())
}
