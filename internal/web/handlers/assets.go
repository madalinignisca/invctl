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
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/impact"
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web/render"
)

// ---------- environments ----------

type environmentsPage struct {
	Base
	Environments []domain.Environment
	// Edit is set only when a correction was refused; see editState.
	Edit     *editState
	FormData environmentFormData
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
	roles, err := a.Store.EnvironmentRoles(r.Context())
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
			Roles:  roles,
			// The default is a value the code knows by name, which is a
			// different thing from the set of values it accepts.
			Form: environmentForm{Role: domain.EnvRoleProduction, Criticality: 3},
		},
	})
}

// EnvironmentCreate adds an environment.
func (a *App) EnvironmentCreate(w http.ResponseWriter, r *http.Request) {
	criticality, numeric := intValue(r, "criticality", 3)
	form := environmentForm{
		Code:        formValue(r, "code"),
		Name:        formValue(r, "name"),
		Role:        formValue(r, "role"),
		InScope:     checkbox(r, "in_scope"),
		Criticality: criticality,
	}
	if !numeric {
		a.renderEnvironments(w, r, http.StatusUnprocessableEntity, notANumber("criticality"), form)
		return
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

// EnvironmentUpdate corrects an environment.
//
// Every field is editable here, including the code, and that is a deliberate
// exception to "an identifier is not a description". Nothing in a request path
// resolves an environment by code -- every reference in the schema is by id,
// and GetEnvironmentByCode is used only by the seed -- so a rename moves no
// edge and orphans no row. It is still the handle other systems will quote,
// which is why the form says so rather than the code being quietly immutable.
func (a *App) EnvironmentUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := a.Store.GetEnvironment(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	updated := *existing
	updated.Code = formValue(r, "code")
	updated.Name = formValue(r, "name")
	updated.Role = formValue(r, "role")
	updated.InScope = checkbox(r, "in_scope")
	criticality, numeric := intValue(r, "criticality", existing.Criticality)
	if !numeric {
		a.renderEnvironmentsEditing(w, r, http.StatusUnprocessableEntity,
			rejected(r, id, notANumber("criticality"),
				"code", "name", "role", "criticality", "in_scope"))
		return
	}
	updated.Criticality = criticality
	updated.RowVersion = submittedVersion(r, updated.RowVersion)

	if err := a.Store.UpdateEnvironment(r.Context(), actor(r), &updated); err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			switch {
			case isStale(err):
				messages = staleMessage("name")
			case isConflict(err):
				messages = map[string]string{"code": "another environment already uses that code"}
			default:
				a.handleStoreError(w, r, err)
				return
			}
		}
		// 422 with the row reopened on what was typed, not a redirect that
		// throws the operator's input away. The house rule, and the reason for
		// it: a redirect refills the row from storage, so the field they just
		// corrected shows the old value back and nothing says whether it saved.
		a.renderEnvironmentsEditing(w, r, refusalStatus(err),
			rejected(r, id, messages, "code", "name", "role", "criticality", "in_scope"))
		return
	}

	a.setFlash(r, "success", "Environment "+updated.Code+" updated.")
	render.Redirect(w, r, "/environments")
}

// renderEnvironmentsEditing redraws the list with one row rejected.
func (a *App) renderEnvironmentsEditing(w http.ResponseWriter, r *http.Request, status int, edit *editState) {
	a.renderEnvironmentsWith(w, r, status, nil, environmentForm{}, edit)
}

func (a *App) renderEnvironments(w http.ResponseWriter, r *http.Request, status int, messages map[string]string, form environmentForm) {
	a.renderEnvironmentsWith(w, r, status, messages, form, nil)
}

func (a *App) renderEnvironmentsWith(w http.ResponseWriter, r *http.Request, status int,
	messages map[string]string, form environmentForm, edit *editState) {
	envs, err := a.Store.ListEnvironments(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	roles, err := a.Store.EnvironmentRoles(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	formData := environmentFormData{
		Base:   a.base(r, "Environments", "environments"),
		Errors: orEmpty(messages),
		Roles:  roles,
		Form:   form,
	}
	if render.IsHTMX(r) {
		a.Render.Partial(w, status, "environment_form", formData)
		return
	}
	base := a.base(r, "Environments", "environments")
	if edit != nil {
		// The rejected row is the one that opens, whatever the query string
		// said: the operator is looking at the form they just submitted.
		base.EditRow = edit.ID
	}
	a.Render.Page(w, status, "environment_list", environmentsPage{
		Base:         base,
		Environments: envs,
		Edit:         edit,
		FormData:     formData,
	})
}

// ---------- assets ----------

type assetListPage struct {
	Base
	Assets       []store.AssetRow
	Environments []domain.Environment
	Kinds        []store.VocabularyTerm
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
		DeviceTypeID:   q.Get("device_type_id"),
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
	kinds, err := a.Store.AssetKinds(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	// The same rows the page would have shown, as a file. SHARED HANDLER AND
	// SHARED FILTER, deliberately: a separate export route would parse the
	// query a second time, and the day the two readings diverge is the day
	// somebody exports a filtered list and quietly gets everything.
	if render.WantsCSV(r) {
		table, err := a.Store.ExportAssets(r.Context(), assets)
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		render.CSV(w, r, table, a.Store.Now())
		return
	}

	data := assetListPage{
		// Derived, not literal: /assets?kind=firewall is the rail's Firewalls
		// entry under Network, and telling the rail "assets" would open Estate
		// instead. See AssetListNav.
		Base:         a.base(r, "Assets", AssetListNav(filter.Kind)),
		Assets:       assets,
		Environments: envs,
		Kinds:        kinds,
		Filter:       filter,
		FormData:     a.newAssetForm(r, nil, envs, kinds, assets),
	}
	// Filtering swaps only the table, so typing in the filter box does not
	// rebuild the page around it.
	a.Render.Respond(w, r, http.StatusOK, "asset_list", "asset_table", data)
}

type assetDetailPage struct {
	Base
	Asset *store.AssetRow
	// What TLS is on it. The incident question runs this way round: the
	// certificate page already says where it is deployed.
	Certificates []store.DeployedCertificate
	// Costs are the lines attached to this asset, retired ones included so the
	// page can show them struck through. Totalling excludes them.
	Costs       []store.CostRow
	CostTotals  domain.CostTotals
	CostKinds   []store.VocabularyTerm
	CostPeriods []string
	Ancestors   []domain.Asset
	Children    []store.AssetRow
	Interfaces  []store.InterfaceRow
	Instances   []store.InstanceRow
	// Health is what the estate reports about this asset, with staleness
	// applied and any operator override alongside it -- never merged into it.
	Health *store.EntityHealth
	// InstanceHealth is the same for each workload placed here, keyed by
	// instance id. Every placement has an entry, including the ones nothing
	// watches: a missing key and an unobserved entity render identically and
	// mean completely different things.
	InstanceHealth map[string]store.EntityHealth
	// Timeline folds this asset's declared history with its one-hop declared
	// neighbours' and with the observed transitions for the same rows. "What
	// changed just before this broke" is the 03:00 question and it is not
	// answerable from one entity's history.
	Timeline     []store.TimelineEntry
	Environments []domain.Environment
	Kinds        []store.VocabularyTerm
	Lifecycles   []string
	// Edit is set only when a port or address correction was refused.
	Edit *editState
	// AssetEdit is the whole-asset form, present only when the operator asked
	// for it with ?edit=<the asset's own id>.
	AssetEdit     *assetFormData
	InterfaceForm interfaceFormData
	IPAddressForm ipAddressFormData
	LinkForm      linkFormData
	OverrideForm  overrideFormData
	// Where it takes power from, and the feeds it could take it from.
	PowerInputs []store.PowerInputRow
	PowerFeeds  []store.PowerFeedRow
	// Elevation is set only for a rack: what is mounted in it and where.
	Elevation *store.RackElevation
	// Fit is set only for a rack: whether what is in it physically fits, what
	// it weighs and whether it can breathe.
	Fit *store.RackFitReport
	// Replacement is what this asset took over from, or nil. WP-J1.
	Replacement *store.ReplacementComparison
	// Movement is how each of its cost kinds has moved. WP-J2.
	Movement []store.PriceSeries
	// PassThroughs is what this panel does between its own ports.
	PassThroughs []store.PassThroughRow
	// Notes somebody wrote about this asset, and what the panel posts to.
	Journal         []store.JournalRow
	JournalResource string
	JournalID       string
}

// AssetDetail renders one asset with its containment, ports and workloads.
func (a *App) AssetDetail(w http.ResponseWriter, r *http.Request) {
	a.renderAssetDetail(w, r, http.StatusOK, r.PathValue("id"), nil)
}

// renderAssetDetail draws the page at any status, so a refused inline edit can
// come back as 422 with the row reopened on what was typed -- the same shape
// renderServiceDetail has, and for the same reason: the ports and addresses
// editors live on this page, and answering a form post with a bare fragment
// leaves a browser with no layout and no way back.
func (a *App) renderAssetDetail(w http.ResponseWriter, r *http.Request, status int,
	id string, edit *editState) {

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
	health, err := a.Store.GetEntityHealth(r.Context(), domain.ObservableAsset, id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	instanceHealth, err := a.Store.EntityHealthFor(r.Context(), domain.ObservableServiceInstance,
		instanceIDs(instances))
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	timeline, _, err := a.Store.TimelineForEntityAndNeighbours(r.Context(), "asset", id, timelineLimit)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	targets, err := a.assetOverrideTargets(r.Context(), asset, instances)
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
	linkTargets, err := a.Store.ListAvailableInterfaces(r.Context(), "")
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	kinds, err := a.Store.AssetKinds(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	formFactors, err := a.Store.InterfaceFormFactors(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	ipRoles, err := a.Store.IPAddressRoles(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	certificates, err := a.Store.CertificatesOnAsset(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	costs, err := a.Store.ListAssetCosts(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	costKinds, err := a.Store.CostKinds(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	assetBase := a.base(r, asset.Name, "assets")
	if edit != nil {
		// The refused row opens, whatever the query string said.
		assetBase.EditRow = edit.ID
	}
	var assetEdit *assetFormData
	if assetBase.CanWrite && assetBase.EditRow == asset.ID {
		f := a.newAssetEditForm(r, asset, nil, envs, kinds, nil)
		assetEdit = &f
	}
	if edit != nil && edit.ID == asset.ID {
		// A refused save of the asset itself, rather than of a row on it.
		f := a.newAssetEditForm(r, asset, edit.Errors, envs, kinds, edit)
		assetEdit = &f
	}
	// Power, and the feeds this asset could be plugged into. A failure to read
	// either leaves the section empty rather than failing the page: an asset
	// page is what somebody opens during an incident, and it must not 500
	// because a subsystem they were not asking about is unhappy.
	powerInputs, err := a.Store.PowerInputsFor(r.Context(), id)
	if err != nil {
		slog.Error("listing power inputs", "error", err, "asset", id)
	}
	powerFeeds, err := a.Store.ListPowerFeeds(r.Context(), store.PowerFeedFilter{})
	if err != nil {
		slog.Error("listing power feeds", "error", err, "asset", id)
	}

	// The elevation, for racks only. A failure leaves the section absent rather
	// than failing the page: an asset page is what somebody opens during an
	// incident.
	var elevation *store.RackElevation
	var fit *store.RackFitReport
	if asset.Kind == domain.KindRack {
		if e, err := a.Store.Elevation(r.Context(), id); err != nil {
			slog.Error("resolving the rack elevation", "error", err, "asset", id)
		} else {
			elevation = e
		}
		// Same treatment as the elevation: logged and absent rather than fatal.
		// A physical-fit panel is worth having and is not worth taking an asset
		// page down for during an incident.
		if f, err := a.Store.RackFit(r.Context(), id); err != nil {
			slog.Error("resolving the rack fit", "error", err, "asset", id)
		} else {
			fit = f
		}
	}

	// What this box patches through, if anything. Read for every asset rather
	// than only for panels: an estate can and does patch through things that
	// are not called patch panels.
	passThroughs, err := a.Store.PassThroughsFor(r.Context(), id)
	if err != nil {
		slog.Error("listing pass-throughs", "error", err, "asset", id)
	}

	// What this replaced, when it replaced anything (WP-J1). Same treatment as
	// the elevation: logged and absent rather than fatal.
	replacement, err := a.Store.ReplacementFor(r.Context(), id, a.Store.Now())
	if err != nil {
		slog.Error("resolving the replacement", "error", err, "asset", id)
	}

	// How its prices moved (WP-J2). Logged and absent rather than fatal.
	movement, err := a.Store.PriceMovementForAsset(r.Context(), id)
	if err != nil {
		slog.Error("resolving price movement", "error", err, "asset", id)
	}

	// Notes. Logged and absent rather than fatal, like the elevation: an asset
	// page is what somebody opens during an incident, and a panel is not worth
	// taking it down for.
	notes, err := a.Store.ListJournal(r.Context(), "asset", id)
	if err != nil {
		slog.Error("listing journal", "error", err, "asset", id)
	}

	a.Render.Page(w, status, "asset_detail", assetDetailPage{
		Base:            assetBase,
		Journal:         notes,
		JournalResource: "assets",
		JournalID:       id,
		Elevation:       elevation,
		Fit:             fit,
		Replacement:     replacement,
		Movement:        movement,
		PassThroughs:    passThroughs,
		PowerInputs:     powerInputs,
		PowerFeeds:      powerFeeds,
		Edit:            edit,
		AssetEdit:       assetEdit,
		Asset:           asset,
		Certificates:    certificates,
		Costs:           costs,
		CostTotals:      store.TotalCosts(costs, domain.FormatDate(a.Store.Now())),
		CostKinds:       costKinds,
		CostPeriods:     domain.CostPeriods,
		Ancestors:       ancestors,
		Children:        children,
		Interfaces:      interfaces,
		Instances:       instances,
		Health:          health,
		InstanceHealth:  instanceHealth,
		Timeline:        timeline,
		Environments:    envs,
		Kinds:           kinds,
		Lifecycles:      domain.AssetLifecycles,
		InterfaceForm:   a.newInterfaceForm(r, id, nil, formFactors),
		IPAddressForm:   a.newIPAddressForm(r, id, nil, interfaces, ipRoles),
		LinkForm:        a.newLinkForm(r, id, nil, interfaces, linkTargets),
		OverrideForm:    a.newOverrideForm(r, targets, nil, overrideForm{}),
	})
}

// timelineLimit is one screen of folded history. It is larger than the old
// per-entity change list because the timeline covers the neighbourhood as well,
// and a page that shows the neighbours but truncates before reaching them would
// be worse than not folding at all.
const timelineLimit = 60

func instanceIDs(rows []store.InstanceRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return out
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
		asset.DeviceTypeID = optionalString(r, "device_type_id")
		asset.UHeight, _ = optionalInt(r, "u_height")
		asset.RackPosition, _ = optionalInt(r, "rack_position")
		asset.RackFace = optionalString(r, "rack_face")
		// The cabinet's own measurements. Only a rack's form renders these, and
		// domain.Validate refuses a nonsense value on any kind, so a stray
		// width on a switch cannot arrive quietly.
		nums := optionalNumbers(r)
		asset.UsableDepthMM = nums.opt("usable_depth_mm")
		asset.WidthMM = nums.opt("width_mm")
		asset.MaxLoadGrams = nums.kilos("max_load_kg")
		if msgs := nums.messages(); msgs != nil {
			a.renderAssetFormError(w, r, msgs)
			return
		}
		asset.TeamID = optionalString(r, "team_id")
		asset.ManagerRole = optionalString(r, "manager_role")
		asset.EOLDate = optionalString(r, "eol_date")
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
	// submittedString, not optionalString: a picker that failed to render must
	// not read as an operator clearing the field. See its doc comment. The
	// catalogue picker is the same shape, and getting it wrong would silently
	// drop an asset's model -- taking its inherited end-of-support date with it.
	updated.DeviceTypeID = submittedString(r, "device_type_id", updated.DeviceTypeID)
	// Same treatment for the lineage: a form that did not carry the field must
	// not read as an operator clearing it, and a form that did carry it empty
	// must clear it. submittedString is the one helper that tells those apart.
	updated.ReplacesAssetID = submittedString(r, "replaces_asset_id", updated.ReplacesAssetID)
	// Refused, not silently dropped: a rack height that quietly became nothing
	// would take every capacity answer with it, and a position that vanished
	// would move a box off the diagram without saying so.
	height, heightOK := optionalInt(r, "u_height")
	position, positionOK := optionalInt(r, "rack_position")
	if !heightOK || !positionOK {
		field := "u_height"
		if heightOK {
			field = "rack_position"
		}
		a.renderAssetFormError(w, r, notANumber(field))
		return
	}
	updated.UHeight, updated.RackPosition = height, position
	// Same rule as the height above: a measurement that quietly became nothing
	// would turn every fit answer for this cabinet into "not recorded", which
	// reads as reassurance rather than as the loss it is.
	nums := optionalNumbers(r)
	depth, width, load := nums.opt("usable_depth_mm"), nums.opt("width_mm"), nums.kilos("max_load_kg")
	if msgs := nums.messages(); msgs != nil {
		a.renderAssetFormError(w, r, msgs)
		return
	}
	updated.UsableDepthMM, updated.WidthMM, updated.MaxLoadGrams = depth, width, load
	updated.RackFace = submittedString(r, "rack_face", updated.RackFace)
	updated.TeamID = submittedString(r, "team_id", updated.TeamID)
	updated.ManagerRole = submittedString(r, "manager_role", updated.ManagerRole)
	updated.EOLDate = optionalString(r, "eol_date")
	updated.RowVersion = submittedVersion(r, updated.RowVersion)

	if err := a.Store.UpdateAsset(r.Context(), actor(r), &updated, submittedEnvironments(r)); err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			switch {
			case isStale(err):
				messages = staleMessage("name")
			case isConflict(err):
				messages = map[string]string{"name": "another asset already has that name here"}
			default:
				a.handleStoreError(w, r, err)
				return
			}
		}
		// The whole page at the right status, with the form reopened on what
		// was typed. renderAssetFormError answers a form post with a bare
		// fragment -- no layout, no way back -- which is only survivable
		// because HTMX swaps it into a page that is already there.
		a.renderAssetDetail(w, r, refusalStatus(err), id,
			rejected(r, id, messages, "name", "kind", "lifecycle", "vendor", "model",
				"serial", "asset_tag", "team_id", "manager_role", "eol_date").
				withMulti("environments", submittedEnvironments(r)))
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
	kinds, err := a.Store.AssetKinds(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Partial(w, http.StatusUnprocessableEntity, "asset_form",
		a.newAssetForm(r, messages, envs, kinds, parents))
}

// ---------- impact simulation ----------

// maxOutageAssets bounds the simulated set.
//
// Every id becomes a placeholder in the closure and instance queries, so an
// unbounded repeated parameter is a cheap way for any signed-in reader to make
// the server build an enormous statement. Twenty-five is far more than an
// honest question needs: the biggest real outage is a rack or a site, and that
// is one id because containment expands it.
const maxOutageAssets = 25

// impactAsset is one member of the simulated outage set, with the link that
// takes it back out again.
type impactAsset struct {
	Asset *store.AssetRow
	// RemoveURL is empty for the last remaining member. Simulating the loss of
	// nothing answers nothing, and an operator who removed the final entry
	// would land on a page whose question had silently changed underneath the
	// answer.
	RemoveURL string
}

type impactPage struct {
	Base
	// Asset is the first member of the set -- the one named in the URL path.
	// Every link the page builds hangs off it, which is what keeps a
	// single-asset simulation byte-for-byte what it was before sets existed.
	Asset *store.AssetRow
	// Assets is the whole set, in the order it was built. The page renders
	// every one of them, always: an impact answer read against the wrong
	// question is worse than no answer, and the only defence is putting the
	// question next to it.
	Assets []impactAsset
	// Extra carries the non-primary ids so both toolbars can round-trip the
	// set through hidden fields. Without it, changing the outage window would
	// quietly narrow the simulation back to the one asset in the path and
	// answer a question nobody asked.
	Extra []string
	// Candidates are the assets not already in the set, for the picker that
	// widens it.
	Candidates []store.AssetRow
	Multiple   bool
	Result     impact.Result
	Window     int
	Windows    []windowOption
	// HasImpact is true when a service is affected. HasNetworkFinding is true
	// when the network has something to say that no service status carries --
	// an isolated asset, a partitioned edge, a group left without redundancy.
	// They are separate because "Nothing breaks" printed above a list of
	// isolated assets is the exact contradiction this feature was built to
	// remove: an operator who reads the headline and stops is being told the
	// opposite of what the page below it says.
	HasImpact         bool
	HasNetworkFinding bool
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

// AssetImpact simulates losing one or more assets, and everything they contain.
//
// The path id is the primary member; every repeated ?asset= parameter widens
// the set. Several at once is not a convenience: a redundant pair only tells
// the truth when both halves can be taken away in the same run, so "what
// happens once redundancy is exhausted" -- the one question a pair exists to
// answer -- is unaskable one asset at a time.
//
// The engine needed nothing for this. impact.Request.DownAssetIDs has always
// been a set, and SubtreeIDs already expands several ancestors in one closure
// query with overlapping subtrees collapsing on their own.
func (a *App) AssetImpact(w http.ResponseWriter, r *http.Request) {
	// The path id goes first, so a bare /assets/{id}/impact is exactly what it
	// always was, and a ?asset= repeating the path id collapses into it rather
	// than naming one asset twice.
	ids := dedupeStrings(append([]string{r.PathValue("id")}, queryStrings(r, "asset")...))
	if len(ids) > maxOutageAssets {
		http.Error(w, fmt.Sprintf("An outage simulation covers at most %d assets at once.", maxOutageAssets),
			http.StatusUnprocessableEntity)
		return
	}
	window := queryInt(r, "window", 180, 1, 3650)

	assets := make([]impactAsset, 0, len(ids))
	for i, id := range ids {
		asset, err := a.Store.GetAsset(r.Context(), id)
		if err != nil {
			// An id that resolves to nothing is a 404, never a quietly dropped
			// parameter. Ignoring it would report the impact of a smaller
			// outage than the operator asked about, under a heading naming the
			// outage they wanted -- "nothing breaks" about a scenario nobody
			// simulated is the most dangerous answer this tool can give.
			a.handleStoreError(w, r, err)
			return
		}
		assets = append(assets, impactAsset{
			Asset:     asset,
			RemoveURL: impactURL(withoutIndex(ids, i), window),
		})
	}

	result, err := a.Store.Simulate(r.Context(), impact.Request{
		DownAssetIDs:  ids,
		WindowSeconds: window,
	})
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	candidates, err := a.Store.ListAssets(r.Context(), store.AssetFilter{})
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	data := impactPage{
		Base:       a.base(r, impactTitle(assets), "assets"),
		Asset:      assets[0].Asset,
		Assets:     assets,
		Extra:      ids[1:],
		Candidates: excludeAssets(candidates, ids),
		Multiple:   len(assets) > 1,
		Result:     result,
		Window:     window,
		Windows:    windows,
		HasImpact:  len(result.Services) > 0 || len(result.WontRestart) > 0,
		HasNetworkFinding: len(result.Isolated) > 0 || len(result.Partitions) > 0 ||
			len(result.Unreachable) > 0 || len(result.RedundancyLost) > 0,
	}
	a.Render.Respond(w, r, http.StatusOK, "impact", "impact_result", data)
}

// impactURL builds this page's own address for a given outage set: the first
// id takes the path, the rest ride as repeated parameters, and the window
// comes along so changing one half of the question never resets the other.
func impactURL(ids []string, window int) string {
	if len(ids) == 0 {
		return ""
	}
	q := url.Values{"window": {strconv.Itoa(window)}}
	for _, id := range ids[1:] {
		q.Add("asset", id)
	}
	return "/assets/" + url.PathEscape(ids[0]) + "/impact?" + q.Encode()
}

// withoutIndex returns ids with the element at i removed, leaving the original
// untouched. A one-element set yields nil, which is what suppresses the remove
// link on the last member.
func withoutIndex(ids []string, i int) []string {
	if len(ids) < 2 {
		return nil
	}
	out := make([]string, 0, len(ids)-1)
	out = append(out, ids[:i]...)
	return append(out, ids[i+1:]...)
}

// excludeAssets drops the ones already being simulated, so the picker only
// offers something that would actually change the answer.
func excludeAssets(rows []store.AssetRow, ids []string) []store.AssetRow {
	chosen := make(map[string]bool, len(ids))
	for _, id := range ids {
		chosen[id] = true
	}
	out := make([]store.AssetRow, 0, len(rows))
	for _, row := range rows {
		if !chosen[row.ID] {
			out = append(out, row)
		}
	}
	return out
}

// impactTitle names the set in the browser tab without letting a long one run
// away with the title bar. The page itself always names every member.
func impactTitle(assets []impactAsset) string {
	if len(assets) == 1 {
		return "Impact: " + assets[0].Asset.Name
	}
	return fmt.Sprintf("Impact: %s + %d more", assets[0].Asset.Name, len(assets)-1)
}

func isConflict(err error) bool {
	return errors.Is(err, domain.ErrConflict)
}

// isStale separates "somebody else got here first" from "that name is taken".
// Both are conflicts; only one is fixed by choosing a different value.
func isStale(err error) bool {
	return errors.Is(err, domain.ErrStale)
}

// refusalStatus separates the two reasons a save comes back.
//
// 422 says "what you typed is wrong"; 409 says "what you typed was fine and
// somebody else got there first". They call for different things from whoever
// is on the other end -- a person retypes a field for one and re-reads the row
// for the other, and a script should retry only the second. Answering both with
// 422 tells them to fix input that has nothing wrong with it.
func refusalStatus(err error) int {
	if isStale(err) {
		return http.StatusConflict
	}
	return http.StatusUnprocessableEntity
}

// staleMessage is what an operator is told when their form lost the race. It
// names the field they were most likely editing so the message lands next to
// the work rather than floating at the top of the page.
func staleMessage(field string) map[string]string {
	return map[string]string{
		field: "somebody else changed this since you opened the form. " +
			"Your text is still here — reopen the row to see what they wrote, then re-apply it.",
	}
}
