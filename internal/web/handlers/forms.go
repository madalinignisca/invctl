// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// Form contexts.
//
// Each form partial takes one of these, both when it renders as part of a page
// and when it re-renders itself with error state after a 422. Building them in
// Go rather than assembling a map in the template means a missing field is a
// compile error instead of "<no value>" in the browser.

// Roles, Kinds, FormFactors and ClassOptions are read from their lookup
// tables, not from a domain slice. A form fed from a Go slice would offer the
// values that existed when the binary was built, so a role inserted this
// morning would be storable and unofferable -- which would make the whole point
// of the lookup tables (add a value as data, not as a release) quietly false.
type environmentFormData struct {
	Base
	Errors map[string]string
	Form   environmentForm
	Roles  []store.VocabularyTerm
}

type assetFormData struct {
	Base
	Errors       map[string]string
	Environments []domain.Environment
	Kinds        []store.VocabularyTerm
	// Lifecycles stays a domain slice: it is a behavioural enum whose value
	// selects a code path, so it is still a CHECK with a Go constant set.
	Lifecycles []string
	Parents    []store.AssetRow
	Teams      []store.TeamRow
	Roles      []store.VocabularyTerm
	// DeviceTypes is the catalogue, so an asset can be pointed at a model and
	// inherit its end-of-support date.
	DeviceTypes []store.DeviceTypeRow
	// Asset is the row being corrected, or nil when adding. One partial serves
	// both, so a field cannot be added to one and forgotten on the other --
	// which is how a form ends up quietly unable to set something.
	Asset  *store.AssetRow
	Action string
	Submit string
	Prefix string
	// Edit carries a refused submission back; nil-safe, see editState.
	Edit *editState
}

// Editing reports whether this form is correcting an existing asset.
func (f assetFormData) Editing() bool { return f.Asset != nil }

// Value is what a field should show: what the operator typed if their save was
// refused, otherwise what is stored, otherwise blank.
func (f assetFormData) Value(field string) string {
	stored := ""
	if f.Asset != nil {
		a := f.Asset.Asset
		switch field {
		case "name":
			stored = a.Name
		case "kind":
			stored = a.Kind
		case "lifecycle":
			stored = a.Lifecycle
		case "vendor":
			stored = orBlank(a.Vendor)
		case "model":
			stored = orBlank(a.Model)
		case "device_type_id":
			stored = orBlank(a.DeviceTypeID)
		case "u_height":
			stored = orBlankInt(a.UHeight)
		case "usable_depth_mm":
			stored = orBlankInt(a.UsableDepthMM)
		case "width_mm":
			stored = orBlankInt(a.WidthMM)
		case "max_load_kg":
			// Stored in grams, typed in kilograms. The form field must render
			// what it will accept back, or a save with no edit at all would
			// refuse -- "600 kg" is not a number.
			stored = orBlankKilograms(a.MaxLoadGrams)
		case "rack_position":
			stored = orBlankInt(a.RackPosition)
		case "rack_face":
			stored = orBlank(a.RackFace)
		case "serial":
			stored = orBlank(a.Serial)
		case "asset_tag":
			stored = orBlank(a.AssetTag)
		case "team_id":
			stored = orBlank(a.TeamID)
		case "manager_role":
			stored = orBlank(a.ManagerRole)
		case "eol_date":
			stored = orBlank(a.EOLDate)
		}
	}
	return f.Edit.Value(field, stored)
}

// InEnvironment reports whether the environment checkbox should be ticked.
//
// After a refusal the answer comes from the SUBMISSION, not the stored row: an
// operator who unticked an environment and failed validation elsewhere must not
// find it silently re-ticked. editState cannot answer this one -- a repeating
// checkbox is a set, not a field -- so the submitted ids are read directly.
func (f assetFormData) InEnvironment(id string) bool {
	if f.Edit != nil {
		for _, submitted := range f.Edit.Multi["environments"] {
			if submitted == id {
				return true
			}
		}
		return false
	}
	if f.Asset == nil {
		return false
	}
	for _, env := range f.Asset.Environments {
		if env.ID == id {
			return true
		}
	}
	return false
}

// Version is the concurrency token to render, and 0 when adding.
//
// The SUBMITTED one wins on a refusal, so a blind resubmit stays refused; see
// rejected(). Falls back to the stored value when the form is simply being
// opened.
func (f assetFormData) Version() int {
	if f.Asset == nil {
		return 0
	}
	if submitted := f.Edit.Value(domain.VersionField, ""); submitted != "" {
		if n, err := strconv.Atoi(submitted); err == nil && n > 0 {
			return n
		}
	}
	return f.Asset.RowVersion
}

type serviceFormData struct {
	Base
	Errors         map[string]string
	Spec           domain.ServiceSpec
	Environments   []domain.Environment
	Teams          []store.TeamRow
	Roles          []store.VocabularyTerm
	Kinds          []store.VocabularyTerm
	Availabilities []string
	FailoverModes  []string
	Lifecycles     []string
	// Set only when correcting an existing service. The form is already
	// Spec-driven, so edit mode is the same fields fed from the stored row.
	ServiceID  string
	Lifecycle  string
	RowVersion int
	Action     string
	Submit     string
	Prefix     string
}

// Editing reports whether this form is correcting an existing service.
func (f serviceFormData) Editing() bool { return f.ServiceID != "" }

type instanceFormData struct {
	Base
	ServiceID    string
	Errors       map[string]string
	Hosts        []store.AssetRow
	RuntimeTypes []string
}

type endpointFormData struct {
	Base
	ServiceID string
	Errors    map[string]string
	// Endpoint is the line being corrected, or nil when adding a new one. One
	// partial serves both so the two cannot drift into offering different
	// fields -- which is how a form ends up quietly unable to set something.
	Endpoint   *domain.Endpoint
	Action     string
	Submit     string
	Prefix     string
	Protos     []string
	BindScopes []string
	Exposures  []string
	TLSModes   []string
}

// Value returns what a field should be pre-filled with: the stored endpoint
// when correcting one, the zero value when adding. Called from the template so
// the two modes share every input.
func (f endpointFormData) Value(field string) string {
	if f.Endpoint == nil {
		return ""
	}
	e := f.Endpoint
	switch field {
	case "name":
		return e.Name
	case "l4_proto":
		return e.L4Proto
	case "port":
		if e.Port == nil {
			return ""
		}
		return strconv.Itoa(*e.Port)
	case "unix_path":
		return orBlank(e.UnixPath)
	case "bind_scope":
		return e.BindScope
	case "exposure":
		return e.Exposure
	case "tls_mode":
		return e.TLSMode
	}
	return ""
}

func orBlank(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

type dependencyFormData struct {
	Base
	ServiceID    string
	Errors       map[string]string
	Spec         domain.DependencySpec
	AllEndpoints []store.EndpointRow
	AllRoutes    []store.RouteRow
	Identities   []domain.Identity
	Natures      []string
	ClassOptions []store.VocabularyTerm
}

type interfaceFormData struct {
	Base
	AssetID     string
	Errors      map[string]string
	FormFactors []store.VocabularyTerm
}

type ipAddressFormData struct {
	Base
	AssetID    string
	Errors     map[string]string
	Interfaces []store.InterfaceRow
	Roles      []store.VocabularyTerm
}

type linkFormData struct {
	Base
	AssetID    string
	Errors     map[string]string
	Interfaces []store.InterfaceRow    // this asset's unpatched ports, the "from" side
	Targets    []store.InterfaceOption // candidates across the estate, the "to" side
}

type prefixFormData struct {
	Base
	Errors       map[string]string
	Environments []domain.Environment
	// VLANs the prefix can be put on. A reference now, not a typed number:
	// the two coexisted for one release and disagreed the first time anybody
	// edited a prefix.
	VLANs []store.VLANRow
}

// newAssetForm builds the asset form context. Parents is the same list the
// table shows, so an asset can be filed under anything already on screen.
func (a *App) newAssetForm(r *http.Request, errs map[string]string, envs []domain.Environment, kinds []store.VocabularyTerm, parents []store.AssetRow) assetFormData {
	teams, roles := a.responsibilityOptions(r)
	// A catalogue that cannot be read leaves the picker empty rather than
	// failing the page: the form still works, the model is simply not offered.
	// The empty-picker failure has bitten this codebase before, so the count is
	// asserted in a test rather than trusted here.
	deviceTypes, err := a.Store.ListDeviceTypes(r.Context(), store.DeviceTypeFilter{})
	if err != nil {
		slog.Error("listing device types for the asset form", "error", err, "path", r.URL.Path)
	}
	return assetFormData{
		Base:         a.base(r, "Assets", "assets"),
		Errors:       orEmpty(errs),
		Environments: envs,
		Kinds:        kinds,
		Lifecycles:   domain.AssetLifecycles,
		Parents:      parents,
		Teams:        teams,
		Roles:        roles,
		DeviceTypes:  deviceTypes,
		Action:       "/assets",
		Submit:       "Add asset",
		Prefix:       "asset-new",
	}
}

// newAssetEditForm is the same form pointed at an existing asset.
//
// PARENT IS NOT AMONG THE FIELDS, and AssetUpdate does not read one. Moving an
// asset rewrites asset_closure and therefore every containment answer, every
// impact simulation and every environment span; it has its own flow. The form
// says so rather than leaving a reader to wonder where the field went.
func (a *App) newAssetEditForm(r *http.Request, row *store.AssetRow, errs map[string]string,
	envs []domain.Environment, kinds []store.VocabularyTerm, edit *editState) assetFormData {

	f := a.newAssetForm(r, errs, envs, kinds, nil)
	f.Asset = row
	f.Action = "/assets/" + row.ID
	f.Submit = "Save asset"
	f.Prefix = "asset-" + row.ID
	f.Edit = edit
	return f
}

// responsibilityOptions loads the two pickers every entity form carries.
//
// Loaded here rather than threaded through each caller's signature: they are two
// small tables that every form wants and no caller varies. A failure degrades to
// empty pickers rather than a 500, because a page that refuses to render over a
// field nobody is required to fill in is worse than one that renders without it.
//
// BUT IT SAYS SO. An empty picker and "no teams exist yet" look identical, and a
// review pointed out that the worst moment for that ambiguity is the 422
// re-render — where the rule most likely to have just fired is the one about
// these very fields. So the failure raises a flash as well as a log line. The
// dangerous half of this degradation, a blank picker reading as an operator
// clearing the field, is closed separately by submittedString.
func (a *App) responsibilityOptions(r *http.Request) ([]store.TeamRow, []store.VocabularyTerm) {
	teams, err := a.Store.TeamOptions(r.Context())
	if err != nil {
		slog.Error("loading teams for a form", "error", err)
		a.setFlash(r, "error", "The team list could not be loaded, so that picker is empty. "+
			"It is not that no teams exist.")
		teams = nil
	}
	roles, err := a.Store.ResponsibilityRoles(r.Context())
	if err != nil {
		slog.Error("loading responsibility roles for a form", "error", err)
		a.setFlash(r, "error", "The role list could not be loaded, so that picker is empty.")
		roles = nil
	}
	return teams, roles
}

// A service form has no project picker. Ownership is a link with a relation on
// it, and the place to say "this project owns that service" is the project
// overview, where the choice between owns and uses is visible. Offering only
// half of it here would make `owns` look like the only kind of belonging.
func (a *App) newServiceForm(r *http.Request, errs map[string]string, spec domain.ServiceSpec, envs []domain.Environment, kinds []store.VocabularyTerm) serviceFormData {
	if spec.Tier == 0 {
		spec.Tier = 3
	}
	teams, roles := a.responsibilityOptions(r)
	return serviceFormData{
		Base:           a.base(r, "Services", "services"),
		Teams:          teams,
		Roles:          roles,
		Errors:         orEmpty(errs),
		Spec:           spec,
		Environments:   envs,
		Kinds:          kinds,
		Availabilities: domain.Availabilities,
		FailoverModes:  domain.FailoverModes,
		Lifecycles:     domain.ServiceLifecycles,
		Action:         "/services",
		Submit:         "Add service",
		Prefix:         "svc-new",
	}
}

// newServiceEditForm is the same form fed from a stored service.
//
// The ENVIRONMENT stays editable here, unlike an asset's parent: a service
// belongs to exactly one environment and that is a classification somebody
// assigns, not a structural edge -- the same reasoning that makes a prefix's
// environment editable. Its PLACEMENTS are what tie it to hardware, and they
// have their own rows and their own editor.
func (a *App) newServiceEditForm(r *http.Request, svc *store.ServiceRow, errs map[string]string,
	envs []domain.Environment, kinds []store.VocabularyTerm, spec *domain.ServiceSpec) serviceFormData {

	current := domain.ServiceSpec{
		Code: svc.Code, Name: svc.Name, Kind: svc.Kind,
		EnvironmentID: svc.EnvironmentID, Availability: svc.Availability,
		Tier: svc.Tier, MinHealthy: svc.MinHealthy, FailoverMode: svc.FailoverMode,
		RTOMinutes: svc.RTOMinutes, RPOMinutes: svc.RPOMinutes,
		TeamID: svc.TeamID, ManagerRole: svc.ManagerRole, EOLDate: svc.EOLDate,
	}
	if spec != nil {
		// A refused save: show what was typed, not what is stored.
		current = *spec
	}
	f := a.newServiceForm(r, errs, current, envs, kinds)
	f.ServiceID = svc.ID
	f.Lifecycle = svc.Lifecycle
	f.RowVersion = svc.RowVersion
	f.Action = "/services/" + svc.ID
	f.Submit = "Save service"
	f.Prefix = "svc-" + svc.ID
	return f
}

func (a *App) newInstanceForm(r *http.Request, serviceID string, errs map[string]string, hosts []store.AssetRow) instanceFormData {
	return instanceFormData{
		Base:         a.base(r, "Services", "services"),
		ServiceID:    serviceID,
		Errors:       orEmpty(errs),
		Hosts:        hosts,
		RuntimeTypes: domain.RuntimeTypes,
	}
}

func (a *App) newEndpointForm(r *http.Request, serviceID string, errs map[string]string) endpointFormData {
	return endpointFormData{
		Base:       a.base(r, "Services", "services"),
		ServiceID:  serviceID,
		Errors:     orEmpty(errs),
		Action:     "/services/" + serviceID + "/endpoints",
		Submit:     "Add endpoint",
		Prefix:     "ep-new",
		Protos:     domain.L4Protos,
		BindScopes: domain.BindScopes,
		Exposures:  domain.Exposures,
		TLSModes:   domain.TLSModes,
	}
}

// newEndpointEditForm is the same form pointed at an existing socket. The
// service it belongs to is NOT among the fields: moving an endpoint to another
// service moves every dependency that resolves through it, which is a different
// act with different consequences and does not belong behind a Save button
// labelled the same way.
func (a *App) newEndpointEditForm(r *http.Request, e *domain.Endpoint, errs map[string]string) endpointFormData {
	f := a.newEndpointForm(r, e.ServiceID, errs)
	f.Endpoint = e
	f.Action = "/endpoints/" + e.ID
	f.Submit = "Save endpoint"
	f.Prefix = "ep-" + e.ID
	return f
}

func (a *App) newDependencyForm(r *http.Request, serviceID string, errs map[string]string, spec domain.DependencySpec, endpoints []store.EndpointRow, routes []store.RouteRow, identities []domain.Identity, classes []store.VocabularyTerm) dependencyFormData {
	return dependencyFormData{
		Base:         a.base(r, "Services", "services"),
		ServiceID:    serviceID,
		Errors:       orEmpty(errs),
		Spec:         spec,
		AllEndpoints: endpoints,
		AllRoutes:    routes,
		Identities:   identities,
		Natures:      domain.Natures,
		ClassOptions: classes,
	}
}

func (a *App) newInterfaceForm(r *http.Request, assetID string, errs map[string]string, formFactors []store.VocabularyTerm) interfaceFormData {
	return interfaceFormData{
		Base:        a.base(r, "Assets", "assets"),
		AssetID:     assetID,
		Errors:      orEmpty(errs),
		FormFactors: formFactors,
	}
}

func (a *App) newIPAddressForm(r *http.Request, assetID string, errs map[string]string, interfaces []store.InterfaceRow, roles []store.VocabularyTerm) ipAddressFormData {
	return ipAddressFormData{
		Base:       a.base(r, "Assets", "assets"),
		AssetID:    assetID,
		Errors:     orEmpty(errs),
		Interfaces: interfaces,
		Roles:      roles,
	}
}

func (a *App) newLinkForm(r *http.Request, assetID string, errs map[string]string, interfaces []store.InterfaceRow, targets []store.InterfaceOption) linkFormData {
	return linkFormData{
		Base:       a.base(r, "Assets", "assets"),
		AssetID:    assetID,
		Errors:     orEmpty(errs),
		Interfaces: unpatchedInterfaces(interfaces),
		Targets:    targets,
	}
}

func (a *App) newPrefixForm(r *http.Request, errs map[string]string, envs []domain.Environment,
	vlans []store.VLANRow) prefixFormData {

	return prefixFormData{
		Base:         a.base(r, "Prefixes", "prefixes"),
		Errors:       orEmpty(errs),
		Environments: envs,
		VLANs:        vlans,
	}
}

// unpatchedInterfaces filters to ports with no active cable -- the only
// sensible "from" side of a new patch.
func unpatchedInterfaces(interfaces []store.InterfaceRow) []store.InterfaceRow {
	out := make([]store.InterfaceRow, 0, len(interfaces))
	for _, i := range interfaces {
		if !i.IsPatched() {
			out = append(out, i)
		}
	}
	return out
}

// orEmpty guarantees a non-nil map so a template can index it without a guard.
func orEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// hostableAssets filters to the kinds that can actually run a workload.
// Offering a patch panel as a host is how bad placement data gets entered.
func hostableAssets(assets []store.AssetRow) []store.AssetRow {
	out := make([]store.AssetRow, 0, len(assets))
	for _, a := range assets {
		if a.CanHostInstances() {
			out = append(out, a)
		}
	}
	return out
}

// depRowData is one dependency row. The row partial is rendered both inside
// the service page and on its own after a verify, so it gets a real type
// rather than a map assembled in the template.
type depRowData struct {
	Dep         *store.DependencyRow
	CanWrite    bool
	CSRF        string
	Direction   string // "upstream" or "downstream"
	DataClasses []string
}

// depRows decorates dependency rows for rendering.
func depRows(deps []store.DependencyRow, classes map[string][]string, direction, csrf string, canWrite bool) []depRowData {
	out := make([]depRowData, len(deps))
	for i := range deps {
		out[i] = depRowData{
			Dep:         &deps[i],
			CanWrite:    canWrite,
			CSRF:        csrf,
			Direction:   direction,
			DataClasses: classes[deps[i].ID],
		}
	}
	return out
}

// submittedEnvironments reads the environment checkbox group.
//
// An unticked checkbox group is absent from the form entirely, so
// r.Form["environments"] is nil rather than empty -- and nil means "do not
// manage membership" to the store, which made it impossible to remove an
// asset's last environment through the UI. Both asset forms always render the
// group, so a submission from them always intends to set membership: normalise
// nil to an empty slice.
func submittedEnvironments(r *http.Request) []string {
	if values := r.Form["environments"]; values != nil {
		return values
	}
	return []string{}
}

// orBlankInt renders an optional number for a form input, empty when unset --
// so "not recorded" comes back as an empty box rather than as a zero somebody
// then saves.
func orBlankInt(n *int) string {
	if n == nil {
		return ""
	}
	return strconv.Itoa(*n)
}

// orBlankKilograms renders grams as the kilograms the form asks for, with no
// unit suffix -- the field has to give back something it will accept.
func orBlankKilograms(g *int) string {
	if g == nil {
		return ""
	}
	whole, frac := *g/1000, (*g%1000)/100
	if frac == 0 {
		return strconv.Itoa(whole)
	}
	return strconv.Itoa(whole) + "." + strconv.Itoa(frac)
}
