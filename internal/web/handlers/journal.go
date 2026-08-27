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
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web/middleware"
	"github.com/madalinignisca/invctl/internal/web/render"
)

// Journal entries, on whatever page somebody is standing on.
//
// ONE HANDLER FOR EVERY ENTITY, because the alternative is six of them that
// differ only in a string and drift the first time one is edited. The entity
// type comes from the path, which is what makes /assets/{id}/journal and
// /circuits/{id}/journal the same code.
//
// THE ENTITY TYPE IS ALLOW-LISTED rather than taken from the URL as given. The
// table has no foreign key -- deliberately, so the record outlives what it
// describes -- so nothing at the database layer would refuse a note attached to
// "flurble". An unchecked type turns a nice generic handler into a way to fill
// the table with rows no page will ever show.
var journalEntityTypes = map[string]string{
	"assets":     "asset",
	"services":   "service",
	"circuits":   "circuit",
	"clusters":   "cluster",
	"projects":   "project",
	"overlays":   "l2vpn",
	"redundancy": "fhrp_group",
	"vlans":      "vlan",
	"prefixes":   "prefix",
	"teams":      "team",
}

// JournalResources are the URL segments that carry a journal, for the router.
//
// EXPLICIT ROUTES RATHER THAN A {resource} WILDCARD. A top-level wildcard reads
// beautifully and conflicts with every other two-segment POST in the router --
// Go's ServeMux refuses "/{resource}/{id}/journal" against "/power/panels/{id}"
// as ambiguous, and it is right to: one of them has to win and neither
// obviously should. Registering the handful by name costs three lines in a loop
// and cannot surprise anybody.
func JournalResources() []string {
	out := make([]string, 0, len(journalEntityTypes))
	for res := range journalEntityTypes {
		out = append(out, res)
	}
	sort.Strings(out) // stable registration order
	return out
}

// journalResource is the first path segment, which the explicit routes make
// unambiguous.
func journalResource(r *http.Request) string {
	trimmed := strings.TrimPrefix(r.URL.Path, "/")
	if i := strings.IndexByte(trimmed, '/'); i >= 0 {
		return trimmed[:i]
	}
	return trimmed
}

// journalTarget resolves the path to an entity type and id.
func journalTarget(r *http.Request) (entityType, entityID string, ok bool) {
	entityType, ok = journalEntityTypes[journalResource(r)]
	return entityType, r.PathValue("id"), ok
}

// JournalCreate adds a note to whatever the path names.
func (a *App) JournalCreate(w http.ResponseWriter, r *http.Request) {
	entityType, entityID, ok := journalTarget(r)
	if !ok {
		http.NotFound(w, r)
		return
	}

	// ATTRIBUTION IS SERVER-DERIVED, never read from the form. docs/AUDIT.md
	// rule 5: an actor taken from a request payload is an actor anybody can
	// claim to be, and a note is a statement attributed to a person -- which is
	// precisely the thing worth forging.
	user := middleware.UserFrom(r.Context())
	if user == nil {
		http.Error(w, "Sign in to write a note.", http.StatusUnauthorized)
		return
	}

	entry, err := domain.NewJournalEntry(store.NewID(), entityType, entityID,
		formValue(r, "kind"), formValue(r, "body"), user.ID, a.Store.Now())
	if err == nil {
		err = a.Store.CreateJournalEntry(r.Context(), permit(r), entry)
	}
	if err != nil {
		if messages, ok := validationErrors(err); ok {
			a.journalRefused(w, r, messages)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Note added.")
	render.Redirect(w, r, journalReturnTo(r))
}

// JournalUpdate corrects a note.
func (a *App) JournalUpdate(w http.ResponseWriter, r *http.Request) {
	entityType, entityID, ok := journalTarget(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	existing, err := a.Store.GetJournalEntry(r.Context(), r.PathValue("noteID"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	// The note must belong to the entity in the path. Without this, a note id
	// from one asset could be edited through another asset's URL -- which is
	// not an authorization hole today, since write access is estate-wide, and
	// is a correctness one: the redirect and the audit would name the wrong
	// thing, and a note would appear to move between assets.
	if existing.EntityType != entityType || existing.EntityID != entityID {
		http.NotFound(w, r)
		return
	}

	updated := existing.JournalEntry
	updated.Kind = formValue(r, "kind")
	updated.Body = formValue(r, "body")
	updated.RowVersion = submittedVersion(r, updated.RowVersion)

	if err := a.Store.UpdateJournalEntry(r.Context(), permit(r), &updated); err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			switch {
			case isStale(err):
				messages = staleMessage("body")
			default:
				a.handleStoreError(w, r, err)
				return
			}
		}
		a.journalRefused(w, r, messages)
		return
	}
	a.setFlash(r, "success", "Note updated.")
	render.Redirect(w, r, journalReturnTo(r))
}

// JournalRetire withdraws a note.
func (a *App) JournalRetire(w http.ResponseWriter, r *http.Request) {
	entityType, entityID, ok := journalTarget(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	existing, err := a.Store.GetJournalEntry(r.Context(), r.PathValue("noteID"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	if existing.EntityType != entityType || existing.EntityID != entityID {
		http.NotFound(w, r)
		return
	}
	if err := a.Store.RetireJournalEntry(r.Context(), permit(r), existing.ID); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Note withdrawn. It stays in the change log.")
	render.Redirect(w, r, journalReturnTo(r))
}

// journalReturnTo is the page the note was written from.
func journalReturnTo(r *http.Request) string {
	return "/" + journalResource(r) + "/" + r.PathValue("id")
}

// journalRefused sends the operator back with the reason.
//
// A DELIBERATE AND NARROW DEPARTURE FROM THE 422-WITH-THE-FORM RULE, recorded
// here rather than left to be discovered. The house rule is that a validation
// failure re-renders the form partial with error state at 422. This panel is
// embedded in ten different pages, and re-rendering "the page it came from"
// means dispatching to ten different page renderers -- so the failure is
// carried as a flash on the page instead.
//
// It is affordable only because a note has exactly two ways to be invalid: an
// empty body, and one longer than domain.MaxJournalBody. Both are stated in the
// flash, neither loses anything the operator cannot retype in a second, and the
// textarea is the only field. If this form ever grows a third failure mode it
// should become a real partial with a real 422.
//
// The status is NOT set before the redirect: writing 422 and then a Location is
// two answers to one request, and the first draft did exactly that.
func (a *App) journalRefused(w http.ResponseWriter, r *http.Request, messages map[string]string) {
	msg := "The note was refused."
	for _, m := range messages {
		msg = m
		break
	}
	a.setFlash(r, "error", msg)
	render.Redirect(w, r, journalReturnTo(r))
}
