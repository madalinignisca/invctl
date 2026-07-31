package handlers

import (
	"net/http"

	"github.com/gabriel/invctl/internal/domain"
	"github.com/gabriel/invctl/internal/store"
	"github.com/gabriel/invctl/internal/web/render"
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
		err = a.Store.CreateTeam(r.Context(), actor(r), t)
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

	if err := a.Store.UpdateTeam(r.Context(), actor(r), &updated); err != nil {
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
func (a *App) TeamRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetireTeam(r.Context(), actor(r), r.PathValue("id")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/teams")
}
