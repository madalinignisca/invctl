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

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web/render"
)

// Tags: the registry, piece 1 of WP-G4a (docs/tags-design.md). This page is
// where a tag is DEFINED. Applying a tag to an asset or a service is piece 2
// and has no handler here.
//
// created_by and retired_by are opaque app_user.id, resolved to a display
// name for rendering exactly as change_log.actor and the custom field
// registry already are (store.TagRow carries the resolved names). Nothing
// here stores or renders a username as the value itself, so scrubbing a user
// for an erasure request leaves the row readable -- it simply stops
// resolving a name.

// tagForm is what the operator typed, redrawn on a rejected create.
type tagForm struct {
	Code        string
	Label       string
	Description string
}

// tagListPage is the registry.
type tagListPage struct {
	Base
	Errors  map[string]string
	Spec    tagForm
	Tags    []store.TagRow
	Retired []store.TagRow
	// Edit is set only when an edit was refused; see editState.
	Edit *editState
}

// TagList renders the registry.
func (a *App) TagList(w http.ResponseWriter, r *http.Request) {
	a.renderTagList(w, r, http.StatusOK, "tag_list_panel", nil, tagForm{}, nil)
}

// renderTagList redraws the whole registry, full page or an HTMX partial
// depending on the request. partial names WHICH fragment an HTMX request
// receives: the table ("tag_list_panel") for everything that changes a row,
// or the create form itself ("tag_form") for a rejected create -- the
// operator submitted a form, not a table row, and the errors they need to
// see live in the form, not in the panel beside it.
func (a *App) renderTagList(w http.ResponseWriter, r *http.Request, status int, partial string,
	errs map[string]string, spec tagForm, edit *editState) {

	all, err := a.Store.ListTags(r.Context(), true)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	var live, retired []store.TagRow
	for _, t := range all {
		if t.IsRetired() {
			retired = append(retired, t)
			continue
		}
		live = append(live, t)
	}

	base := a.base(r, "Tags", "tags")
	if edit != nil {
		// The rejected row is the one that opens, whatever the query string
		// said -- the operator is looking at the form they just submitted.
		base.EditRow = edit.ID
	}

	a.Render.Respond(w, r, status, "tag_list", partial, tagListPage{
		Base:    base,
		Errors:  orEmpty(errs),
		Spec:    spec,
		Tags:    live,
		Retired: retired,
		Edit:    edit,
	})
}

// TagCreate defines a tag.
func (a *App) TagCreate(w http.ResponseWriter, r *http.Request) {
	spec := tagForm{
		Code:        formValue(r, "code"),
		Label:       formValue(r, "label"),
		Description: formValue(r, "description"),
	}

	t, err := domain.NewTag(store.NewID(), spec.Code, spec.Label, spec.Description, actor(r).ID, a.Store.Now())
	if err == nil {
		err = a.Store.CreateTag(r.Context(), actor(r), t)
	}
	if err != nil {
		if messages, ok := validationErrors(err); ok {
			a.renderTagList(w, r, http.StatusUnprocessableEntity, "tag_form", messages, spec, nil)
			return
		}
		if isConflict(err) {
			a.renderTagList(w, r, http.StatusUnprocessableEntity, "tag_form",
				map[string]string{"code": "a live tag with that code already exists"}, spec, nil)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}

	a.setFlash(r, "success", "Tag "+t.Label+" defined.")
	render.Redirect(w, r, "/tags")
}

// TagUpdate corrects a tag. Code stays editable even after a tag has been
// applied to something (piece 2) -- see docs/tags-design.md §4 and the
// domain.Tag comment on why a rename must not rewrite anything downstream.
func (a *App) TagUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := a.Store.GetTag(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	updated := existing.Tag
	updated.Code = formValue(r, "code")
	updated.Label = formValue(r, "label")
	updated.Description = formValue(r, "description")
	updated.RowVersion = submittedVersion(r, updated.RowVersion)

	if err := a.Store.UpdateTag(r.Context(), actor(r), &updated); err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			switch {
			case isStale(err):
				messages = staleMessage("description")
			case isConflict(err):
				messages = map[string]string{"code": "a live tag with that code already exists"}
			default:
				a.handleStoreError(w, r, err)
				return
			}
		}
		a.renderTagList(w, r, refusalStatus(err), "tag_list_panel", nil, tagForm{},
			rejected(r, id, messages, "code", "label", "description"))
		return
	}

	a.setFlash(r, "success", "Tag "+updated.Label+" updated.")
	render.Redirect(w, r, "/tags")
}

// TagRetire retires a tag. It deletes nothing, ever -- a retired tag keeps
// displaying on things that already carry it (piece 2) and is left alone
// rather than refused if it is already retired, the way CustomFieldRetire
// treats a second retirement.
func (a *App) TagRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetireTag(r.Context(), actor(r), r.PathValue("id")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Tag retired. Anything already carrying it keeps it.")
	render.Redirect(w, r, "/tags")
}

// TagRestore brings a retired tag back. Refused if a live tag already holds
// the same code.
func (a *App) TagRestore(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RestoreTag(r.Context(), actor(r), r.PathValue("id")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Tag restored.")
	render.Redirect(w, r, "/tags")
}
