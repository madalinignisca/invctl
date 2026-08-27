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

// Teams: who looks after part of the estate.
//
// TEAMS, NEVER PEOPLE. Nothing on these pages names an individual, and the
// contact field is documented in its own hint as a group address or a queue. The
// application cannot tell a distribution list from a person's mailbox, so the
// only enforcement available is saying so where somebody is about to type.

type teamListPage struct {
	Base
	Errors map[string]string
	Teams  []store.TeamRow
	Spec   domain.TeamSpec
	Query  string
}

type teamPage struct {
	Base
	Errors     map[string]string
	Team       *store.TeamRow
	Lifecycles []string
	Assets     []store.AssetRow
	Services   []store.ServiceRow
	Projects   []store.ProjectRow
	// What it renews. An expiring certificate is the most urgent thing a team
	// can be answerable for, so it belongs on the team's own page.
	Certificates []store.CertificateRow
}

// TeamList shows every team and the form to add one.
func (a *App) TeamList(w http.ResponseWriter, r *http.Request) {
	a.renderTeamList(w, r, http.StatusOK, nil, domain.TeamSpec{})
}

func (a *App) renderTeamList(w http.ResponseWriter, r *http.Request, status int,
	errs map[string]string, spec domain.TeamSpec) {

	query := r.URL.Query().Get("q")
	teams, err := a.Store.ListTeams(r.Context(), store.TeamFilter{Query: query})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Respond(w, r, status, "team_list", "team_list_panel", teamListPage{
		Base:   a.base(r, "Teams", "teams"),
		Errors: orEmpty(errs),
		Teams:  teams,
		Spec:   spec,
		Query:  query,
	})
}

// TeamCreate adds a team.
func (a *App) TeamCreate(w http.ResponseWriter, r *http.Request) {
	spec := domain.TeamSpec{
		Code:        formValue(r, "code"),
		Name:        formValue(r, "name"),
		Description: optional(formValue(r, "description")),
		ContactRef:  optional(formValue(r, "contact_ref")),
		Lifecycle:   formValue(r, "lifecycle"),
	}
	t, err := domain.NewTeam(store.NewID(), spec, a.Store.Now())
	if err == nil {
		err = a.Store.CreateTeam(r.Context(), permit(r), t)
	}
	if err != nil {
		if errs, ok := validationErrors(err); ok {
			a.renderTeamList(w, r, http.StatusUnprocessableEntity, errs, spec)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/teams/"+t.ID)
}

// TeamDetail is what a team is answerable for.
func (a *App) TeamDetail(w http.ResponseWriter, r *http.Request) {
	a.renderTeam(w, r, http.StatusOK, nil)
}

func (a *App) renderTeam(w http.ResponseWriter, r *http.Request, status int, errs map[string]string) {
	id := r.PathValue("id")
	team, err := a.Store.GetTeam(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	assets, err := a.Store.ListAssets(r.Context(), store.AssetFilter{TeamID: id})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	services, err := a.Store.ListServices(r.Context(), store.ServiceFilter{TeamID: id})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	projects, err := a.Store.ListProjects(r.Context(), store.ProjectFilter{TeamID: id})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	certificates, err := a.Store.ListCertificates(r.Context(), store.CertificateFilter{TeamID: id})
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	a.Render.Respond(w, r, status, "team_detail", "team_panel", teamPage{
		Certificates: certificates,
		Base:         a.base(r, "Team: "+team.Name, "teams"),
		Errors:       orEmpty(errs),
		Team:         team,
		Lifecycles:   domain.TeamLifecycles,
		Assets:       assets,
		Services:     services,
		Projects:     projects,
	})
}

// TeamUpdate edits a team.
func (a *App) TeamUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := a.Store.GetTeam(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	updated := existing.Team
	updated.Code = formValue(r, "code")
	updated.Name = formValue(r, "name")
	updated.Description = optional(formValue(r, "description"))
	updated.ContactRef = optional(formValue(r, "contact_ref"))
	updated.Lifecycle = formValue(r, "lifecycle")
	updated.RowVersion = submittedVersion(r, updated.RowVersion)

	if err := a.Store.UpdateTeam(r.Context(), permit(r), &updated); err != nil {
		if errs, ok := validationErrors(err); ok {
			a.renderTeam(w, r, http.StatusUnprocessableEntity, errs)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/teams/"+id)
}

// TeamRetire disbands a team.
//
// What it looked after keeps pointing at it. A retired team owning live assets
// is the estate saying "this was theirs and nobody has picked it up", which is a
// finding worth seeing; clearing the column would erase the question with the
// answer.
//
// This is "retire anyway" from the confirmation screen (TeamRetireConfirm)
// as well as the direct route -- unchanged by WP-G7 piece 2 on purpose.
// Nothing here requires the confirmation screen to have been seen, and
// nothing here reassigns anything: see team_reassignment.go's own comment on
// why RetireTeam must never call it.
func (a *App) TeamRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetireTeam(r.Context(), permit(r), r.PathValue("id")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/teams")
}

// teamRetireConfirmPage is the screen shown before a team is retired
// (docs/ownership-report-design.md §5): what it looks after, and the offer to
// move that somewhere else before the team goes. Never a block -- "retire
// anyway" stays one click away regardless of what Counts says.
type teamRetireConfirmPage struct {
	Base
	Team    *store.TeamRow
	Counts  *store.TeamOwnershipCounts
	Targets []store.TeamRow
	Errors  map[string]string
}

// TeamRetireConfirm shows what a team looks after before it is retired, and
// offers reassignment inline.
//
// GET, but registered under the admin-write bucket rather than the public
// read routes -- like /imports, this view exists purely to feed a mutation
// (TeamReassignAndRetire or TeamRetire) and a read-only user has no use for
// it. It performs no mutation itself: nothing here writes anything, so an
// operator can load this page, read the counts, and walk away having changed
// nothing.
func (a *App) TeamRetireConfirm(w http.ResponseWriter, r *http.Request) {
	a.renderTeamRetireConfirm(w, r, http.StatusOK, nil)
}

func (a *App) renderTeamRetireConfirm(w http.ResponseWriter, r *http.Request, status int, errs map[string]string) {
	id := r.PathValue("id")
	team, err := a.Store.GetTeam(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	counts, err := a.Store.TeamOwnershipCounts(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	// Only fetched when there is something to reassign: an already-empty team
	// needs no picker, and the design is explicit that a zero-count team
	// skips straight to the plain confirmation (design §5).
	var targets []store.TeamRow
	if counts.Total() > 0 {
		all, err := a.Store.TeamOptions(r.Context())
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		for _, opt := range all {
			if opt.ID != id {
				targets = append(targets, opt)
			}
		}
	}

	a.Render.Respond(w, r, status, "team_retire_confirm", "team_retire_confirm", teamRetireConfirmPage{
		Base:    a.base(r, "Retire team: "+team.Name, "teams"),
		Team:    team,
		Counts:  counts,
		Targets: targets,
		Errors:  orEmpty(errs),
	})
}

// teamReassignResultPage reports what happened, one line per entity -- never
// a bare count (design §4: "10 updated, 1 skipped tells the operator
// nothing").
type teamReassignResultPage struct {
	Base
	Team     *store.TeamRow
	Target   *store.TeamRow
	Outcomes []store.ReassignOutcome
}

// TeamReassignAndRetire moves everything the team looks after to the chosen
// target, then retires the team -- the fix offered inline on
// TeamRetireConfirm (design §5).
//
// Retirement runs REGARDLESS of the reassignment outcomes. A stale or failed
// entity is reported, not treated as a reason to abandon the retirement --
// forcing that choice is exactly what design §1 rejects: "retire anyway"
// must stay available, and an entity this call could not move simply becomes
// a report finding the same way it would if reassignment were never offered
// at all.
func (a *App) TeamReassignAndRetire(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	target := formValue(r, "target_team_id")
	if target == "" {
		a.renderTeamRetireConfirm(w, r, http.StatusUnprocessableEntity,
			map[string]string{"target_team_id": "Choose a team to reassign to."})
		return
	}

	who := permit(r)
	outcomes, err := a.Store.ReassignTeamOwnership(r.Context(), who, id, target)
	if err != nil {
		if errs, ok := validationErrors(err); ok {
			a.renderTeamRetireConfirm(w, r, http.StatusUnprocessableEntity, errs)
			return
		}
		// ReassignTeamOwnership reports an invalid target (missing, retired,
		// or the same team being retired) as domain.ErrInvalid rather than a
		// domain.ValidationError -- there is no form to re-check field by
		// field, only the one choice the operator made. Re-render the same
		// confirmation screen with that explained, rather than the bare 422
		// handleStoreError would otherwise show.
		if errors.Is(err, domain.ErrInvalid) {
			a.renderTeamRetireConfirm(w, r, http.StatusUnprocessableEntity,
				map[string]string{"target_team_id": "That team cannot be the reassignment target."})
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	if err := a.Store.RetireTeam(r.Context(), who, id); err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	team, err := a.Store.GetTeam(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	targetTeam, err := a.Store.GetTeam(r.Context(), target)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	a.Render.Respond(w, r, http.StatusOK, "team_reassign_result", "team_reassign_result", teamReassignResultPage{
		Base:     a.base(r, "Team retired: "+team.Name, "teams"),
		Team:     team,
		Target:   targetTeam,
		Outcomes: outcomes,
	})
}
