package handlers

import (
	"errors"
	"net/http"

	"github.com/gabriel/invctl/internal/domain"
	"github.com/gabriel/invctl/internal/impact"
	"github.com/gabriel/invctl/internal/store"
	"github.com/gabriel/invctl/internal/web/render"
)

// ---------- environments ----------

type environmentsPage struct {
	Base
	Environments []domain.Environment
	FormData     environmentFormData
}

type environmentForm struct {
	Code        string
	Name        string
	Role        string
	InScope     bool
	Criticality int
}

// EnvironmentList renders the environments page.
func (a *App) EnvironmentList(w http.ResponseWriter, r *http.Request) {
	envs, err := a.Store.ListEnvironments(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Page(w, http.StatusOK, "environment_list", environmentsPage{
		Base:         a.base(r, "Environments", "environments"),
		Environments: envs,
		FormData: environmentFormData{
			Base:   a.base(r, "Environments", "environments"),
			Errors: map[string]string{},
			Roles:  domain.EnvRoles,
			Form:   environmentForm{Role: domain.EnvRoleProduction, Criticality: 3},
		},
	})
}

// EnvironmentCreate adds an environment.
func (a *App) EnvironmentCreate(w http.ResponseWriter, r *http.Request) {
	form := environmentForm{
		Code:        formValue(r, "code"),
		Name:        formValue(r, "name"),
		Role:        formValue(r, "role"),
		InScope:     checkbox(r, "in_scope"),
		Criticality: intValue(r, "criticality", 3),
	}

	env, err := domain.NewEnvironment(store.NewID(), form.Code, form.Name, form.Role,
		form.InScope, form.Criticality, a.Store.Now())
	if err == nil {
		err = a.Store.CreateEnvironment(r.Context(), actor(r), env)
	}
	if err != nil {
		// A validation failure re-renders the form with error state and
		// returns 422 -- never a 200 with the message buried in the body.
		if messages, ok := validationErrors(err); ok {
			a.renderEnvironments(w, r, http.StatusUnprocessableEntity, messages, form)
			return
		}
		if isConflict(err) {
			a.renderEnvironments(w, r, http.StatusUnprocessableEntity,
				map[string]string{"code": "an environment with that code already exists"}, form)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}

	a.setFlash(r, "success", "Environment "+env.Code+" created.")
	render.Redirect(w, r, "/environments")
}

func (a *App) renderEnvironments(w http.ResponseWriter, r *http.Request, status int, messages map[string]string, form environmentForm) {
	envs, err := a.Store.ListEnvironments(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	formData := environmentFormData{
		Base:   a.base(r, "Environments", "environments"),
		Errors: orEmpty(messages),
		Roles:  domain.EnvRoles,
		Form:   form,
	}
	if render.IsHTMX(r) {
		a.Render.Partial(w, status, "environment_form", formData)
		return
	}
	a.Render.Page(w, status, "environment_list", environmentsPage{
		Base:         a.base(r, "Environments", "environments"),
		Environments: envs,
		FormData:     formData,
	})
}

// ---------- assets ----------

type assetListPage struct {
	Base
	Assets       []store.AssetRow
	Environments []domain.Environment
	Kinds        []string
	Filter       store.AssetFilter
	FormData     assetFormData
}

// AssetList renders the asset inventory with filters.
func (a *App) AssetList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := store.AssetFilter{
		Kind:           q.Get("kind"),
		EnvironmentID:  q.Get("environment"),
		Lifecycle:      q.Get("lifecycle"),
		Query:          q.Get("q"),
		IncludeRetired: q.Get("retired") == "1",
	}

	assets, err := a.Store.ListAssets(r.Context(), filter)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	envs, err := a.Store.ListEnvironments(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	data := assetListPage{
		Base:         a.base(r, "Assets", "assets"),
		Assets:       assets,
		Environments: envs,
		Kinds:        domain.AssetKinds,
		Filter:       filter,
		FormData:     a.newAssetForm(r, nil, envs, assets),
	}
	// Filtering swaps only the table, so typing in the filter box does not
	// rebuild the page around it.
	a.Render.Respond(w, r, http.StatusOK, "asset_list", "asset_table", data)
}

type assetDetailPage struct {
	Base
	Asset         *store.AssetRow
	Ancestors     []domain.Asset
	Children      []store.AssetRow
	Interfaces    []store.InterfaceRow
	Instances     []store.InstanceRow
	Changes       []domain.ChangeLog
	Environments  []domain.Environment
	Kinds         []string
	Lifecycles    []string
	InterfaceForm interfaceFormData
	IPAddressForm ipAddressFormData
	LinkForm      linkFormData
}

// AssetDetail renders one asset with its containment, ports and workloads.
func (a *App) AssetDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	asset, err := a.Store.GetAsset(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	ancestors, err := a.Store.Ancestors(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	children, err := a.Store.Children(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	interfaces, err := a.Store.ListInterfaces(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	instances, err := a.Store.ListInstancesByHost(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	changes, err := a.Store.ListChangesForEntity(r.Context(), "asset", id, 20)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	envs, err := a.Store.ListEnvironments(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	// Candidates for the "patch to" dropdown -- every unpatched interface in
	// the estate, this asset's own ports included. Excluding nothing by asset
	// keeps the query simple; CreateLink's uniqueness check is what actually
	// prevents a bad cable.
	targets, err := a.Store.ListAvailableInterfaces(r.Context(), "")
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	a.Render.Page(w, http.StatusOK, "asset_detail", assetDetailPage{
		Base:          a.base(r, asset.Name, "assets"),
		Asset:         asset,
		Ancestors:     ancestors,
		Children:      children,
		Interfaces:    interfaces,
		Instances:     instances,
		Changes:       changes,
		Environments:  envs,
		Kinds:         domain.AssetKinds,
		Lifecycles:    domain.AssetLifecycles,
		InterfaceForm: a.newInterfaceForm(r, id, nil),
		IPAddressForm: a.newIPAddressForm(r, id, nil, interfaces),
		LinkForm:      a.newLinkForm(r, id, nil, interfaces, targets),
	})
}

// AssetCreate adds an asset.
func (a *App) AssetCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	parentID := optionalString(r, "parent_id")

	asset, err := domain.NewAsset(store.NewID(), formValue(r, "kind"), formValue(r, "name"),
		parentID, a.Store.Now())
	if err == nil {
		asset.Serial = optionalString(r, "serial")
		asset.AssetTag = optionalString(r, "asset_tag")
		asset.Vendor = optionalString(r, "vendor")
		asset.Model = optionalString(r, "model")
		asset.OwnerTeam = optionalString(r, "owner_team")
		err = a.Store.CreateAsset(r.Context(), actor(r), asset, submittedEnvironments(r))
	}
	if err != nil {
		if messages, ok := validationErrors(err); ok {
			a.renderAssetFormError(w, r, messages)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}

	a.setFlash(r, "success", "Asset "+asset.Name+" created.")
	render.Redirect(w, r, "/assets/"+asset.ID)
}

// AssetUpdate saves field changes.
func (a *App) AssetUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	existing, err := a.Store.GetAsset(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	updated := existing.Asset
	updated.Name = formValue(r, "name")
	updated.Kind = formValue(r, "kind")
	updated.Lifecycle = formValue(r, "lifecycle")
	updated.Serial = optionalString(r, "serial")
	updated.AssetTag = optionalString(r, "asset_tag")
	updated.Vendor = optionalString(r, "vendor")
	updated.Model = optionalString(r, "model")
	updated.OwnerTeam = optionalString(r, "owner_team")

	if err := a.Store.UpdateAsset(r.Context(), actor(r), &updated, submittedEnvironments(r)); err != nil {
		if messages, ok := validationErrors(err); ok {
			a.renderAssetFormError(w, r, messages)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}

	a.setFlash(r, "success", "Asset updated.")
	render.Redirect(w, r, "/assets/"+id)
}

// AssetRetire soft-deletes an asset. There is no hard delete anywhere.
func (a *App) AssetRetire(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.Store.RetireAsset(r.Context(), actor(r), id); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Asset retired. Its history is kept.")
	render.Redirect(w, r, "/assets/"+id)
}

// AssetReparent moves an asset in the containment tree, rebuilding the closure
// rows for its subtree.
func (a *App) AssetReparent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	parentID := optionalString(r, "parent_id")

	if err := a.Store.ReparentAsset(r.Context(), actor(r), id, parentID); err != nil {
		if messages, ok := validationErrors(err); ok {
			text := "That move is not allowed."
			for _, m := range messages {
				text = m
				break
			}
			a.setFlash(r, "error", text)
			render.Redirect(w, r, "/assets/"+id)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Asset moved.")
	render.Redirect(w, r, "/assets/"+id)
}

func (a *App) renderAssetFormError(w http.ResponseWriter, r *http.Request, messages map[string]string) {
	envs, err := a.Store.ListEnvironments(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	parents, err := a.Store.ListAssets(r.Context(), store.AssetFilter{})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Partial(w, http.StatusUnprocessableEntity, "asset_form",
		a.newAssetForm(r, messages, envs, parents))
}

// ---------- impact simulation ----------

type impactPage struct {
	Base
	Asset     *store.AssetRow
	Result    impact.Result
	Window    int
	Windows   []windowOption
	Subtree   []string
	HasImpact bool
}

type windowOption struct {
	Seconds int
	Label   string
}

// windows are the outage lengths worth distinguishing. They exist because an
// async dependency with a 300-second buffer behaves differently across them,
// and that difference is invisible without offering the choice.
var windows = []windowOption{
	{Seconds: 180, Label: "3 min (quick reboot)"},
	{Seconds: 900, Label: "15 min (patch and reboot)"},
	{Seconds: 2700, Label: "45 min (hardware swap)"},
	{Seconds: 28800, Label: "8 h (extended outage)"},
}

// AssetImpact simulates losing an asset and everything it contains.
func (a *App) AssetImpact(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	asset, err := a.Store.GetAsset(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	window := queryInt(r, "window", 180)
	result, err := a.Store.Simulate(r.Context(), impact.Request{
		DownAssetIDs:  []string{id},
		WindowSeconds: window,
	})
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	data := impactPage{
		Base:      a.base(r, "Impact: "+asset.Name, "assets"),
		Asset:     asset,
		Result:    result,
		Window:    window,
		Windows:   windows,
		HasImpact: len(result.Services) > 0 || len(result.WontRestart) > 0,
	}
	a.Render.Respond(w, r, http.StatusOK, "impact", "impact_result", data)
}

func isConflict(err error) bool {
	return errors.Is(err, domain.ErrConflict)
}
