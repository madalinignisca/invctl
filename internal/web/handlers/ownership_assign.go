// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// WP-G7 piece 3: bulk assignment of the Unowned finding
// (docs/ownership-report-design.md §4, §6). Two admin-only, CSRF-protected
// routes, unlike OwnershipReport itself:
//
//   - OwnershipCandidates (GET) re-renders ONE entity-type group's row list,
//     narrowed by whatever filter the operator chose -- reusing assetFilterFrom
//     (assets.go) and serviceFilterFrom (services.go) rather than a third
//     filter parser, exactly as design §6 asks. This is what "narrow by
//     project or site BEFORE selecting" actually is: an HTMX swap of the
//     group's checkbox list, never a client-side re-filter of rows already in
//     the DOM.
//   - OwnershipAssign (POST) is the mutation: it trusts nothing but the ids the
//     request actually named -- including a "select all" click, which is a
//     client-side convenience over whatever OwnershipCandidates most recently
//     rendered, never a signal read here as "all unowned X" (design §4's
//     submission contract, unchanged from piece 2).
//
// Registered under the admin-write bucket in routes.go -- GET included, the
// same way /teams/{id}/retire is: this GET exists purely to feed the POST and
// a read-only user has no use for it.

// unownedCandidates dispatches to the right store query for entityType,
// reusing assetFilterFrom/serviceFilterFrom for the two entity types that
// have a project-or-site dimension to narrow by (design §6); the other three
// take a free-text query only, matching UnownedProjectCandidates,
// UnownedIdentityCandidates and UnownedCustomFieldCandidates in
// bulk_ownership.go.
func (a *App) unownedCandidates(ctx context.Context, entityType string, q url.Values) ([]store.OwnershipCandidate, error) {
	switch entityType {
	case "asset":
		f := assetFilterFrom(q)
		return a.Store.UnownedAssetCandidates(ctx, f)
	case "service":
		// Tier is deliberately not exposed on this screen -- the tier picker
		// is a service-list-page concept, and reusing serviceFilterFrom with a
		// fixed 0 (its own "no filter" value) still parses environment, kind,
		// project and query exactly as /services does.
		return a.Store.UnownedServiceCandidates(ctx, serviceFilterFrom(q, 0))
	case "project":
		return a.Store.UnownedProjectCandidates(ctx, q.Get("q"))
	case "identity":
		return a.Store.UnownedIdentityCandidates(ctx, q.Get("q"))
	case "custom_field":
		return a.Store.UnownedCustomFieldCandidates(ctx, q.Get("q"))
	default:
		return nil, fmt.Errorf("unknown entity type %q: %w", entityType, domain.ErrInvalid)
	}
}

// ownershipAssignGroupPage is the group partial: one entity type's filtered
// candidate list, the checkboxes that go with it, and the team picker.
// Renderable standalone -- CLAUDE.md's rule for every partial -- since it
// carries CSRF, the entity type and the target list itself rather than
// assuming a parent already rendered them.
type ownershipAssignGroupPage struct {
	Base
	EntityType  string
	EntityLabel string
	Rows        []store.OwnershipCandidate
	Targets     []store.TeamRow
	// Error is set only when a POST to OwnershipAssign is re-rendering this
	// group after a refusal (no ids selected, no target chosen, or an
	// invalid/retired target) -- the 422 path, form partial re-rendered with
	// error state (CLAUDE.md's HTTP/HTMX conventions).
	Error string
}

// OwnershipCandidates re-renders one entity-type group's candidate list under
// a new filter -- the HTMX swap behind "narrow by project or site BEFORE
// selecting" (design §6). It is a GET with no side effect at all, but sits in
// the admin-write bucket because its only purpose is feeding
// OwnershipAssign, the same reasoning TeamRetireConfirm already documents for
// its own GET.
func (a *App) OwnershipCandidates(w http.ResponseWriter, r *http.Request) {
	entityType := r.URL.Query().Get("entity_type")
	rows, err := a.unownedCandidates(r.Context(), entityType, r.URL.Query())
	if err != nil {
		if errors.Is(err, domain.ErrInvalid) {
			http.Error(w, "unknown entity type", http.StatusBadRequest)
			return
		}
		a.serverError(w, r, err)
		return
	}
	targets, err := a.Store.TeamOptions(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Partial(w, http.StatusOK, "ownership_assign_group", ownershipAssignGroupPage{
		Base:        a.base(r, "Ownership", "ownership-report"),
		EntityType:  entityType,
		EntityLabel: ownershipEntityLabels[entityType],
		Rows:        rows,
		Targets:     targets,
	})
}

// OwnershipAssign is the mutation: it moves exactly the ids the request
// named to the chosen team, and reports a per-item outcome, never a bare
// count (design §4).
//
// A REFUSAL RE-RENDERS THE SAME GROUP, 422, WITH AN ERROR -- never a bare
// 422 with nothing to look at, and never a 500 for what is an operator
// mistake (nothing selected, no team chosen, or a retired/unknown target).
// The refused request's own filter query string is reused to rebuild the
// group exactly as the operator was looking at it when they submitted,
// carried through as hidden fields on the form (see the template).
func (a *App) OwnershipAssign(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		a.serverError(w, r, fmt.Errorf("parsing bulk-assign form: %w", err))
		return
	}
	entityType := formValue(r, "entity_type")
	teamID := formValue(r, "team_id")
	ids := r.PostForm["ids"]

	outcomes, err := a.Store.BulkAssignOwnership(r.Context(), a.permit(r), entityType, ids, teamID)
	if err != nil {
		if errors.Is(err, domain.ErrInvalid) {
			a.renderOwnershipAssignRefusal(w, r, entityType, refusalMessage(entityType, ids, teamID))
			return
		}
		a.handleStoreError(w, r, err)
		return
	}

	a.Render.Partial(w, http.StatusOK, "ownership_assign_result", ownershipAssignResultPage{
		Base:        a.base(r, "Ownership", "ownership-report"),
		EntityType:  entityType,
		EntityLabel: ownershipEntityLabels[entityType],
		Outcomes:    outcomes,
	})
}

// refusalMessage explains BulkAssignOwnership's domain.ErrInvalid without
// re-parsing its wrapped text -- there is no per-field form to check here,
// only the handful of ways a bulk-assign submission can be malformed.
func refusalMessage(entityType string, ids []string, teamID string) string {
	if len(ids) == 0 {
		return "Select at least one " + singularEntityLabel(entityType) + " first."
	}
	if teamID == "" {
		return "Choose a team to assign to."
	}
	return "That team cannot be the assignment target."
}

func singularEntityLabel(entityType string) string {
	switch entityType {
	case "custom_field":
		return "custom field"
	default:
		return entityType
	}
}

// renderOwnershipAssignRefusal re-renders the group with the current (still
// eligible) candidates and an error, 422 -- CLAUDE.md's rule for a validation
// failure. Best-effort on the candidate refetch: an entity type this codebase
// does not recognise renders no rows rather than failing the refusal itself.
func (a *App) renderOwnershipAssignRefusal(w http.ResponseWriter, r *http.Request, entityType, message string) {
	rows, _ := a.unownedCandidates(r.Context(), entityType, url.Values{})
	targets, err := a.Store.TeamOptions(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Partial(w, http.StatusUnprocessableEntity, "ownership_assign_group", ownershipAssignGroupPage{
		Base:        a.base(r, "Ownership", "ownership-report"),
		EntityType:  entityType,
		EntityLabel: ownershipEntityLabels[entityType],
		Rows:        rows,
		Targets:     targets,
		Error:       message,
	})
}

// ownershipAssignResultPage reports what happened, one line per entity --
// never a bare count (design §4: "10 updated, 1 skipped tells the operator
// nothing").
type ownershipAssignResultPage struct {
	Base
	EntityType  string
	EntityLabel string
	Outcomes    []store.ReassignOutcome
}
