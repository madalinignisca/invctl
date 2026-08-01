package handlers

import (
	"fmt"
	"net/http"

	"github.com/gabriel/invctl/internal/domain"
	"github.com/gabriel/invctl/internal/store"
	"github.com/gabriel/invctl/internal/web/render"
)

// Projects: the business view, written for whoever signs off the budget rather
// than for whoever holds the pager.
//
// The overview answers four questions in the order a manager asks them: what
// does this project consist of, what does that imply, what is it standing on
// that somebody else owns, and what does it look like. Everything on it is
// either declared by a person or clearly labelled as derived — there is no
// third category, because a number a manager cannot trace is a number they
// will stop believing.

// projectEditForm is the data the shared project_form partial needs in edit
// mode. A struct rather than a dict so a missing field is a compile error --
// the asset form's hand-built dict silently resolved a newly added method to
// nothing and 500ed the list page.
type projectEditForm struct {
	ProjectID  string
	Spec       domain.ProjectSpec
	Lifecycle  string
	RowVersion int
	Teams      []store.TeamRow
	Lifecycles []string
	Errors     map[string]string
	CSRF       string
	Action     string
	Submit     string
	Prefix     string
	Editing    bool
	Target     string
}

type projectListPage struct {
	Base
	Errors     map[string]string
	Projects   []store.ProjectRow
	Teams      []store.TeamRow
	Spec       domain.ProjectSpec
	Lifecycles []string
}

type projectPage struct {
	Base
	Errors  map[string]string
	Project *store.ProjectRow
	// Edit is the whole-project form, present only when the operator asked for
	// it with ?edit=<the project's own id>.
	Edit       *projectEditForm
	Teams      []store.TeamRow
	Lifecycles []string

	// Declared.
	Assets   []store.ProjectAssetRow
	Services []store.ProjectServiceRow
	// Derived, and labelled as such everywhere it is shown.
	Footprint *store.ProjectFootprint
	Depends   *store.ProjectDependencyReport
	Costs     *store.ProjectCostSummary

	// The project's own cost lines -- the money attached to no box and no
	// service -- and what a form needs to add one.
	CostLines   []store.CostRow
	CostKinds   []store.VocabularyTerm
	CostPeriods []string

	// Pickers for the link forms.
	AllAssets   []store.AssetRow
	AllServices []store.ServiceRow
	Relations   []string
}

type projectMapPage struct {
	Base
	Project *store.ProjectRow
	View    diagramView
}

// ProjectList shows every project and the form to create one.
func (a *App) ProjectList(w http.ResponseWriter, r *http.Request) {
	a.renderProjectList(w, r, http.StatusOK, nil, domain.ProjectSpec{})
}

func (a *App) renderProjectList(w http.ResponseWriter, r *http.Request, status int,
	errs map[string]string, spec domain.ProjectSpec) {

	projects, err := a.Store.ListProjects(r.Context(), store.ProjectFilter{
		Query: r.URL.Query().Get("q"),
	})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	teams, _ := a.responsibilityOptions(r)
	a.Render.Respond(w, r, status, "project_list", "project_list_panel", projectListPage{
		Base:       a.base(r, "Projects", "projects"),
		Lifecycles: domain.ProjectLifecycles,
		Errors:     orEmpty(errs),
		Projects:   projects,
		Teams:      teams,
		Spec:       spec,
	})
}

// ProjectCreate stores a new project.
func (a *App) ProjectCreate(w http.ResponseWriter, r *http.Request) {
	spec := domain.ProjectSpec{
		Code:        formValue(r, "code"),
		Name:        formValue(r, "name"),
		Description: optional(formValue(r, "description")),
		TeamID:      optional(formValue(r, "team_id")),
		Lifecycle:   formValue(r, "lifecycle"),
	}
	p, err := domain.NewProject(store.NewID(), spec, a.Store.Now())
	if err != nil {
		if errs, ok := validationErrors(err); ok {
			a.renderProjectList(w, r, http.StatusUnprocessableEntity, errs, spec)
			return
		}
		a.serverError(w, r, err)
		return
	}
	if err := a.Store.CreateProject(r.Context(), actor(r), p); err != nil {
		if errs, ok := validationErrors(err); ok {
			a.renderProjectList(w, r, http.StatusUnprocessableEntity, errs, spec)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/projects/"+p.ID)
}

// ProjectOverview is the page a product owner or a CTO opens.
func (a *App) ProjectOverview(w http.ResponseWriter, r *http.Request) {
	a.renderProject(w, r, http.StatusOK, nil)
}

// renderProject draws the page. submitted carries a refused save, so the form
// redraws on what the operator typed rather than on what is stored.
func (a *App) renderProject(w http.ResponseWriter, r *http.Request, status int, errs map[string]string) {
	a.renderProjectWith(w, r, status, errs, nil)
}

func (a *App) renderProjectWith(w http.ResponseWriter, r *http.Request, status int,
	errs map[string]string, submitted *domain.ProjectSpec) {
	id := r.PathValue("id")
	project, err := a.Store.GetProject(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	assets, err := a.Store.ListProjectAssets(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	services, err := a.Store.ListProjectServices(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	footprint, err := a.Store.ProjectFootprint(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	depends, err := a.Store.ProjectExternalDependencies(r.Context(), footprint)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	costs, err := a.Store.ProjectCosts(r.Context(), footprint, a.Store.Now())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	costLines, err := a.Store.ListProjectCosts(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	costKinds, err := a.Store.CostKinds(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	allAssets, err := a.Store.ListAssets(r.Context(), store.AssetFilter{})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	allServices, err := a.Store.ListServices(r.Context(), store.ServiceFilter{})
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	base := a.base(r, "Project: "+project.Name, "projects")
	teams, err := a.Store.TeamOptions(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	var edit *projectEditForm
	if base.CanWrite && base.EditRow == project.ID {
		spec := domain.ProjectSpec{
			Code: project.Code, Name: project.Name,
			Description: project.Description, TeamID: project.TeamID,
			Lifecycle: project.Lifecycle,
		}
		if submitted != nil {
			spec = *submitted
		}
		edit = &projectEditForm{
			ProjectID: project.ID, Spec: spec, Lifecycle: spec.Lifecycle,
			RowVersion: project.RowVersion, Teams: teams,
			Lifecycles: domain.ProjectLifecycles, Errors: orEmpty(errs),
			CSRF: base.CSRF, Editing: true,
			Action: "/projects/" + project.ID, Submit: "Save project",
			Prefix: "p-" + project.ID, Target: "#project-edit",
		}
	}

	a.Render.Respond(w, r, status, "project_detail", "project_panel", projectPage{
		Base:        base,
		Edit:        edit,
		Teams:       teams,
		Lifecycles:  domain.ProjectLifecycles,
		Errors:      orEmpty(errs),
		Project:     project,
		Assets:      assets,
		Services:    services,
		Footprint:   footprint,
		Depends:     depends,
		Costs:       costs,
		CostLines:   costLines,
		CostKinds:   costKinds,
		CostPeriods: domain.CostPeriods,
		AllAssets:   allAssets,
		AllServices: allServices,
		Relations:   domain.ProjectRelations,
	})
}

// ProjectUpdate edits a project's own fields.
func (a *App) ProjectUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := a.Store.GetProject(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	spec := domain.ProjectSpec{
		Code:        formValue(r, "code"),
		Name:        formValue(r, "name"),
		Description: optional(formValue(r, "description")),
		TeamID:      optional(formValue(r, "team_id")),
		Lifecycle:   formValue(r, "lifecycle"),
	}
	updated, err := domain.NewProject(id, spec, a.Store.Now())
	if err != nil {
		if errs, ok := validationErrors(err); ok {
			a.renderProject(w, r, http.StatusUnprocessableEntity, errs)
			return
		}
		a.serverError(w, r, err)
		return
	}
	updated.CreatedAt = existing.CreatedAt
	// NewProject builds a fresh struct, so the version has to be restored
	// explicitly -- a zero here would conflict with every row on earth.
	updated.RowVersion = submittedVersion(r, existing.RowVersion)
	if err := a.Store.UpdateProject(r.Context(), actor(r), updated); err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			switch {
			case isStale(err):
				messages = staleMessage("code")
			case isConflict(err):
				messages = map[string]string{"code": "a project with that code already exists"}
			default:
				a.handleStoreError(w, r, err)
				return
			}
		}
		a.renderProjectWith(w, r, refusalStatus(err), messages, &spec)
		return
	}
	render.Redirect(w, r, "/projects/"+id)
}

// ProjectRetire soft-deletes a project and releases its links.
func (a *App) ProjectRetire(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.Store.RetireProject(r.Context(), actor(r), id); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/projects")
}

// ProjectAssetLink links an asset, or changes how it is linked.
//
// A conflict here is a 422 rather than a 409, and the distinction is not
// pedantry: the operator picked a valid asset and a valid relation, and what
// is wrong is the combination — which is a field-level problem they can fix on
// the form in front of them. The store's error already names the current owner.
func (a *App) ProjectAssetLink(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	link, err := domain.NewProjectAssetLink(id, formValue(r, "asset_id"),
		formValue(r, "relation"), optional(formValue(r, "note")), a.Store.Now())
	if err != nil {
		a.linkFailed(w, r, err, "asset_id")
		return
	}
	if err := a.Store.LinkProjectAsset(r.Context(), actor(r), link); err != nil {
		a.linkFailed(w, r, err, "asset_id")
		return
	}
	render.Redirect(w, r, "/projects/"+id)
}

// ProjectServiceLink links a service, or changes how it is linked.
func (a *App) ProjectServiceLink(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	link, err := domain.NewProjectServiceLink(id, formValue(r, "service_id"),
		formValue(r, "relation"), optional(formValue(r, "note")), a.Store.Now())
	if err != nil {
		a.linkFailed(w, r, err, "service_id")
		return
	}
	if err := a.Store.LinkProjectService(r.Context(), actor(r), link); err != nil {
		a.linkFailed(w, r, err, "service_id")
		return
	}
	render.Redirect(w, r, "/projects/"+id)
}

// linkFailed re-renders the overview with the problem against the field the
// operator can act on.
func (a *App) linkFailed(w http.ResponseWriter, r *http.Request, err error, field string) {
	if errs, ok := validationErrors(err); ok {
		a.renderProject(w, r, http.StatusUnprocessableEntity, errs)
		return
	}
	if isConflict(err) {
		a.renderProject(w, r, http.StatusUnprocessableEntity, map[string]string{
			field: err.Error(),
		})
		return
	}
	a.handleStoreError(w, r, err)
}

// ProjectAssetRetire releases one asset link.
func (a *App) ProjectAssetRetire(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.Store.RetireProjectAsset(r.Context(), actor(r), id, r.PathValue("assetID")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/projects/"+id)
}

// ProjectServiceRetire releases one service link.
func (a *App) ProjectServiceRetire(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.Store.RetireProjectService(r.Context(), actor(r), id, r.PathValue("serviceID")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/projects/"+id)
}

// ProjectMap draws the project's asset surface, through the same pipeline as
// the environment map.
func (a *App) ProjectMap(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	project, err := a.Store.GetProject(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	g, err := a.Store.ProjectMap(r.Context(), id, 0)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	assetIDs := make([]string, len(g.Assets))
	for i, n := range g.Assets {
		assetIDs[i] = n.ID
	}
	assetHealth, err := a.Store.EntityHealthFor(r.Context(), domain.ObservableAsset, assetIDs)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	instanceIDs := make([]string, len(g.Placements))
	for i, p := range g.Placements {
		instanceIDs[i] = p.InstanceID
	}
	instanceHealth, err := a.Store.EntityHealthFor(r.Context(), domain.ObservableServiceInstance, instanceIDs)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	view, err := buildEnvMapView(g, selectedLayers(r, nil), assetHealth, instanceHealth)
	if err != nil {
		a.serverError(w, r, fmt.Errorf("laying out the %s map: %w", project.Code, err))
		return
	}
	view.Action = "/projects/" + id + "/map"
	view.Heading = "What " + project.Name + " consists of"
	view.Note = "Everything the project declared, plus what that implies — not a walk, " +
		"so somebody else's estate is absent however it is cabled."
	// The node budget lives here now rather than in the footprint, so this is
	// the only place a cut can happen -- and a cut nobody is told about is
	// indistinguishable from a fact that does not exist. The totals on the
	// overview are computed over the WHOLE footprint and are unaffected.
	if g.AssetsElided > 0 {
		view.Note += fmt.Sprintf(" %s not drawn, because a picture with that many boxes "+
			"is not a picture; the costs and counts on the overview still cover them.",
			pluraliseCount(g.AssetsElided, "further asset is", "further assets are"))
	}

	a.Render.Respond(w, r, http.StatusOK, "project_map", "project_map_panel", projectMapPage{
		Base:    a.base(r, "Map: "+project.Code, "projects"),
		Project: project,
		View:    view,
	})
}

// optional turns an empty form field into a nil rather than a pointer to "".
// A blank description is the absence of one, not the presence of nothing.
func optional(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
