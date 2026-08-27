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

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web/render"
)

// Applying tags to an asset, service or project, from that entity's own page
// -- piece 2 of WP-G4a (docs/tags-design.md §4a). Piece 1 (tags.go) built the
// registry, where a tag is DEFINED; this is where it is APPLIED, and where a
// missing one can be created without leaving the page (§4a: "a tag is
// created once per audit ... the friction lands far more often" than a
// custom field's one-time definition).
//
// SAME SHAPE AS THE CUSTOM-FIELD EDITOR BESIDE IT (loadCustomFieldsPanel /
// postCustomFields in customvalues.go): a panel built fresh for every render,
// a shared submit handler parameterised over entity type, and a refusal that
// re-renders the whole detail page at 422 or 409 rather than a bare
// fragment.
//
// UNLIKE CUSTOM VALUES, THE SUBMISSION IS THE WHOLE SET, not a delta. A tag
// picker draws a checkbox per offered tag; what the browser posts back is
// therefore "everything ticked", not "everything that changed" -- there is
// no per-tag "clear" signal to invent because a tag has no value to clear,
// only membership. store.SetEntityTags's contract matches: tagIDs is the
// desired set.

// entityTagsPanel is what entity_tags_show and entity_tags_form both render.
type entityTagsPanel struct {
	Action     string
	CSRF       string
	CanWrite   bool
	RowVersion int
	// Applied is every tag this entity carries, live and retired together,
	// ordered by code -- a retired tag keeps displaying (design.md §2).
	Applied []domain.Tag
	// Offerable is every LIVE tag not already applied: what a new tick can
	// add. A retired tag never appears here even if it is not yet applied --
	// design.md §2 and §4a, "not offered for new application".
	Offerable []domain.Tag
	// Edit is set only when a submission against this entity's tags was
	// refused; see editState. Nil-safe, like every other use of it here.
	Edit *editState
}

// Ticked reports whether a tag's checkbox should be checked.
//
// After a refusal the answer comes from the SUBMISSION, not from Applied --
// the identical reasoning assetFormData.InEnvironment gives: an operator who
// unticked a tag and was refused over the new-tag fields must not find it
// silently re-ticked. editState.Value cannot answer this because a
// repeating checkbox is a set, not a single field, so the submitted ids are
// read from Edit.Multi directly.
func (p entityTagsPanel) Ticked(id string) bool {
	if p.Edit != nil {
		for _, submitted := range p.Edit.Multi["tag_id"] {
			if submitted == id {
				return true
			}
		}
		return false
	}
	for _, t := range p.Applied {
		if t.ID == id {
			return true
		}
	}
	return false
}

// entityTagsEditID names the sentinel editState.ID a tag-submission refusal
// carries, distinct from any port, address, cost line or custom-field-values
// sentinel riding the same shared ?edit= query parameter on the same detail
// page. Never a real row id: those are UUIDv7, this is not.
func entityTagsEditID(entityID string) string {
	return "tags:" + entityID
}

// loadEntityTagsPanel builds the display and edit data for one entity's
// tags: everything it carries (Applied) and every live tag it does not yet
// carry (Offerable) -- read fresh on every render, the same rule
// loadCustomFieldsPanel follows and for the same reason: this is what the
// page SHOWS, never what a submission is built from.
func (a *App) loadEntityTagsPanel(r *http.Request, entityType, entityID string,
	rowVersion int, action, csrf string, canWrite bool, edit *editState) (entityTagsPanel, error) {

	applied, err := a.Store.EntityTagsFor(r.Context(), entityType, entityID)
	if err != nil {
		return entityTagsPanel{}, err
	}
	live, err := a.Store.ListTags(r.Context(), false)
	if err != nil {
		return entityTagsPanel{}, err
	}
	held := make(map[string]bool, len(applied))
	for _, t := range applied {
		held[t.ID] = true
	}
	offerable := make([]domain.Tag, 0, len(live))
	for _, row := range live {
		if !held[row.ID] {
			offerable = append(offerable, row.Tag)
		}
	}

	return entityTagsPanel{
		Action:     action,
		CSRF:       csrf,
		CanWrite:   canWrite,
		RowVersion: rowVersion,
		Applied:    applied,
		Offerable:  offerable,
		Edit:       edit,
	}, nil
}

// AssetTags sets an asset's tag set.
func (a *App) AssetTags(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	asset, err := a.Store.GetAsset(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.postEntityTags(w, r, domain.TagEntityAsset, id, asset.RowVersion,
		func(status int, tagsEdit *editState) { a.renderAssetDetail(w, r, status, id, tagsEdit) },
		func() {
			a.setFlash(r, "success", "Tags updated.")
			render.Redirect(w, r, "/assets/"+id)
		})
}

// ServiceTags sets a service's tag set.
func (a *App) ServiceTags(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	svc, err := a.Store.GetService(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.postEntityTags(w, r, domain.TagEntityService, id, svc.RowVersion,
		func(status int, tagsEdit *editState) {
			a.renderServiceDetail(w, r, status, id, endpointFormState{}, tagsEdit)
		},
		func() {
			a.setFlash(r, "success", "Tags updated.")
			render.Redirect(w, r, "/services/"+id)
		})
}

// ProjectTags sets a project's tag set.
func (a *App) ProjectTags(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	project, err := a.Store.GetProject(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.postEntityTags(w, r, domain.TagEntityProject, id, project.RowVersion,
		func(status int, tagsEdit *editState) { a.renderProjectWith(w, r, status, nil, nil, tagsEdit) },
		func() {
			a.setFlash(r, "success", "Tags updated.")
			render.Redirect(w, r, "/projects/"+id)
		})
}

// postEntityTags applies one submission of a tag set against entityID,
// shared between the asset, service and project routes. rerender redraws the
// whole detail page at the given status with the refusal folded in;
// onSuccess runs once the write has gone in.
//
// TWO THINGS RIDE ONE SUBMISSION: the ticked set (tag_id, repeating -- an
// unticked box submits nothing, so a wholly-unticked picker is a real "clear
// everything" and not "field absent") and, optionally, a brand-new tag typed
// into the same form (design.md §4a: create-and-apply reachable from the
// page the operator is already on). The new tag is created FIRST, as its own
// write with its own change_log entry against `tag` -- creation is always
// audited (design.md §4) regardless of whether the entity-tag submission
// that carried it goes on to succeed -- and its id is then folded into the
// submitted set as if it had been ticked.
func (a *App) postEntityTags(w http.ResponseWriter, r *http.Request, entityType, entityID string,
	currentVersion int, rerender func(status int, tagsEdit *editState), onSuccess func()) {

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}

	tagIDs := append([]string(nil), r.PostForm["tag_id"]...)
	// tag_id is NOT in fieldNames: rejected() captures a field with
	// formValue, which reads only the FIRST value of a repeating key.
	// Redrawing what was actually ticked goes through withMulti below
	// instead -- the same split assets.go's environment checkboxes use.
	fieldNames := []string{"new_tag_code", "new_tag_label", "new_tag_description"}

	newCode := formValue(r, "new_tag_code")
	if newCode != "" {
		newTag, err := domain.NewTag(store.NewID(), newCode, formValue(r, "new_tag_label"),
			formValue(r, "new_tag_description"), actor(r).ID, a.Store.Now())
		if err == nil {
			err = a.Store.CreateTag(r.Context(), permit(r), newTag)
		}
		if err != nil {
			var msgs map[string]string
			switch {
			case isConflict(err):
				msgs = map[string]string{"new_tag_code": "a live tag with that code already exists"}
			default:
				var ok bool
				if msgs, ok = validationErrors(err); !ok {
					a.handleStoreError(w, r, err)
					return
				}
				// validationErrors keys by domain field name (code/label/
				// description); the form's inputs are prefixed new_tag_ so
				// the error lands on the box the operator actually typed
				// into, not on a field this form does not render.
				prefixed := make(map[string]string, len(msgs))
				for k, v := range msgs {
					prefixed["new_tag_"+k] = v
				}
				msgs = prefixed
			}
			rerender(http.StatusUnprocessableEntity,
				rejected(r, entityTagsEditID(entityID), msgs, fieldNames...).withMulti("tag_id", tagIDs))
			return
		}
		tagIDs = append(tagIDs, newTag.ID)
	}

	expected := submittedVersion(r, currentVersion)
	if err := a.Store.SetEntityTags(r.Context(), permit(r), entityType, entityID, expected, tagIDs); err != nil {
		switch {
		case isStale(err):
			rerender(http.StatusConflict,
				rejected(r, entityTagsEditID(entityID), staleMessage("_form"), fieldNames...).withMulti("tag_id", tagIDs))
		case errors.Is(err, domain.ErrInvalid):
			rerender(http.StatusUnprocessableEntity,
				rejected(r, entityTagsEditID(entityID), map[string]string{"_form": err.Error()}, fieldNames...).
					withMulti("tag_id", tagIDs))
		default:
			a.handleStoreError(w, r, err)
		}
		return
	}
	onSuccess()
}
