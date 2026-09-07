// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"fmt"
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
	// StorageKinds is the replication vocabulary, offered only on a pool.
	StorageKinds []store.VocabularyTerm
	// Predecessors is what this asset may be recorded as replacing (WP-J1).
	//
	// A SEPARATE LIST FROM Parents, AND THE DIFFERENCE IS THE WHOLE POINT: a
	// parent must be live, because nothing is contained by a retired thing,
	// while a predecessor is USUALLY retired -- that is what being replaced
	// means. Reusing Parents here would have offered exactly the wrong set and
	// made the feature look broken to the first person who tried it.
	Predecessors []store.AssetRow
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

// ShowsCapacity reports whether to ask how big this thing is.
//
// GATED ON THE DATA COLUMN, NOT ON A LIST OF KINDS IN GO. can_host_instances
// already answers "can a workload sit here", which is exactly the population
// whose size is worth recording -- and capacity.go says why the answer belongs
// in the table: a switch compiled into Go can only speak for the kinds it was
// built with, so a kind added by INSERT would be silently unsizeable.
//
// EDIT ONLY. Adding an asset establishes what it is; measuring it is a
// separate act, usually by a different person with the machine in front of
// them. The add form already says as much -- "What it is, not where it sits".
func (f assetFormData) ShowsCapacity() bool {
	return f.Asset != nil && f.Asset.CanHostInstances()
}

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
		case "replaces_asset_id":
			stored = orBlank(a.ReplacesAssetID)
		case "id":
			// So the picker can exclude the asset being edited: an asset
			// cannot replace itself, and offering it would invite a refusal
			// the form could have prevented.
			stored = a.ID
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
		case "cpu_cores":
			stored = orBlankInt(a.CPUCores)
		case "memory_mb":
			stored = orBlankInt(a.MemoryMB)
		case "vcpu_provisioned":
			stored = orBlankInt(a.VCPUProvisioned)
		case "vcpu_allocated":
			stored = orBlankInt(a.VCPUAllocated)
		case "memory_provisioned_mb":
			stored = orBlankInt(a.MemoryProvisionedMB)
		case "memory_allocated_mb":
			stored = orBlankInt(a.MemoryAllocatedMB)
		case "storage_kind":
			stored = orBlank(a.StorageKind)
		case "raw_capacity_gb":
			stored = orBlankInt(a.RawCapacityGB)
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
	// EndpointHint and RouteHint explain an AllEndpoints/AllRoutes picker that
	// filtering left shorter than the estate actually is -- fix-b item 2.
	// Empty when no explanation is needed (the picker holds everything the
	// estate has to offer, filtered or not). See pickerHint.
	EndpointHint string
	RouteHint    string
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
	// TargetHint is Targets' empty-picker/partial-filtering explanation --
	// see pickerHint and dependencyFormData.EndpointHint.
	TargetHint string
}

type passThroughFormData struct {
	Base
	AssetID string
	Errors  map[string]string
	// Interfaces is this asset's own ports, offered as both the front and the
	// rear choice -- a pass-through is what one panel does between two of its
	// own ports (CreatePassThrough enforces "both ends on one asset").
	Interfaces []store.InterfaceRow
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
	// Same treatment as the catalogue: an unreadable vocabulary leaves the
	// picker empty rather than failing the page.
	storageKinds, err := a.Store.StorageKinds(r.Context())
	if err != nil {
		slog.Error("listing storage kinds for the asset form", "error", err, "path", r.URL.Path)
	}
	// Retired included, deliberately: see Predecessors. Same treatment on
	// failure -- an empty picker, never a failed page.
	predecessors, err := a.Store.ListAssets(r.Context(), store.AssetFilter{IncludeRetired: true})
	if err != nil {
		slog.Error("listing predecessors for the asset form", "error", err, "path", r.URL.Path)
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
		StorageKinds: storageKinds,
		Predecessors: predecessors,
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

// newDependencyForm builds the "add a dependency" panel.
//
// AllEndpoints and AllRoutes are FILTERED to what the caller could actually
// write, not offered whole and refused on submit. Task 3's ruling (2026-09-02):
// a control that looks available and isn't is the defect the two-ended-write
// sweep just removed from the row controls, and a picker is a control too.
// The filter is `Base.CanWriteEntity("service", ...)`, backed by the same
// `permit.Covers` the store's `authorizeDependencySubjects` calls at write
// time.
//
// AN EARLIER VERSION OF THIS COMMENT CLAIMED THE TWO "CANNOT DRIFT, BY
// CONSTRUCTION". They can, and a review demonstrated it. What is shared is
// `Covers` -- the question "does this permit cover this row". What is NOT
// shared is the SUBJECT RESOLUTION: which service a route's provider
// actually belongs to. The picker reads `routeSelect`'s
// `fs.id AS frontend_service_id`; the store resolves it again in
// `resolveProviderService`. Change one to the backend pool's service instead
// of the frontend endpoint's and the picker silently offers routes the store
// will refuse -- the offer-and-refuse this filtering exists to remove. That
// mutation survived the suite until the fixture was split so the two services
// differ (`TestDependencyRoutePickerIsFilteredToOwnedServices`).
//
// So the coupling is a TEST, not a construction. Keep it that way: if you
// add a third subject-resolution path, give it a case there. For an
// Administrator, whose permit covers everything, the filter removes nothing:
// this is a widening for a project owner, never a narrowing for anyone else.
func (a *App) newDependencyForm(r *http.Request, serviceID string, errs map[string]string, spec domain.DependencySpec, endpoints []store.EndpointRow, routes []store.RouteRow, identities []domain.Identity, classes []store.VocabularyTerm) dependencyFormData {
	base := a.base(r, "Services", "services")
	filteredEndpoints := writableEndpoints(base, endpoints)
	filteredRoutes := writableRoutes(base, routes)
	return dependencyFormData{
		Base:         base,
		ServiceID:    serviceID,
		Errors:       orEmpty(errs),
		Spec:         spec,
		AllEndpoints: filteredEndpoints,
		AllRoutes:    filteredRoutes,
		EndpointHint: pickerHint(len(filteredEndpoints), len(endpoints),
			"There are no live endpoints in the estate yet.",
			"There is nothing here you can depend on -- every live endpoint belongs to a service you don't own.",
			"Showing %d of %d endpoints -- the rest belong to services you don't own."),
		RouteHint: pickerHint(len(filteredRoutes), len(routes),
			"There are no routes in the estate yet.",
			"There is nothing here you can depend on -- every route's frontend belongs to a service you don't own.",
			"Showing %d of %d routes -- the rest belong to services you don't own."),
		Identities:   identities,
		Natures:      domain.Natures,
		ClassOptions: classes,
	}
}

// pickerHint explains why a create form's picker holds fewer options than it
// might, or says nothing when no explanation is needed -- fix-b item 2. It
// existed before as `{{if not .AllEndpoints}}` in the template, which fires
// on EMPTINESS rather than on FILTERING and is wrong on both counts:
//
//   - The common case filtering actually produces is PARTIAL, not empty --
//     a project owner who owns 2 of an estate's 50 endpoints saw a two-entry
//     dropdown with no explanation, reading as "the estate has two
//     endpoints".
//   - The condition also fired for an ADMINISTRATOR whenever the estate
//     genuinely had nothing to offer, telling them "everything here belongs
//     to a service you don't own" -- false for someone whose permit covers
//     the whole estate.
//
// filtered is what the caller's own scope kept; total is what the estate
// actually holds before filtering. total == 0 is the estate itself having
// nothing, true for an Administrator too, so it gets its own honest message
// rather than reusing the "belongs to someone else" one.
func pickerHint(filtered, total int, emptyEstate, allExcluded, someExcludedFmt string) string {
	switch {
	case total == 0:
		return emptyEstate
	case filtered == 0:
		return allExcluded
	case filtered < total:
		return fmt.Sprintf(someExcludedFmt, filtered, total)
	default:
		return ""
	}
}

// writableEndpoints keeps only the endpoints whose owning service the caller
// may write -- the provider side of a new dependency. See newDependencyForm.
func writableEndpoints(b Base, endpoints []store.EndpointRow) []store.EndpointRow {
	out := make([]store.EndpointRow, 0, len(endpoints))
	for _, ep := range endpoints {
		if b.CanWriteEntity("service", ep.ServiceID) {
			out = append(out, ep)
		}
	}
	return out
}

// writableRoutes keeps only the routes whose frontend service the caller may
// write. FrontendServiceID is the same service authorizeDependencySubjects
// resolves for a route provider (internal/store/deps.go), so the picker and
// the store's rule agree on the same id, not merely the same code.
func writableRoutes(b Base, routes []store.RouteRow) []store.RouteRow {
	out := make([]store.RouteRow, 0, len(routes))
	for _, rt := range routes {
		if b.CanWriteEntity("service", rt.FrontendServiceID) {
			out = append(out, rt)
		}
	}
	return out
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

// newLinkForm builds the "patch a cable" panel. Targets is FILTERED to ports
// on assets the caller may write, the same reasoning as newDependencyForm's
// AllEndpoints/AllRoutes: the far-end picker and the store's two-ended
// `authorizeLinkSubjects` check both ask `permit.Covers`, so they cannot
// disagree.
func (a *App) newLinkForm(r *http.Request, assetID string, errs map[string]string, interfaces []store.InterfaceRow, targets []store.InterfaceOption) linkFormData {
	base := a.base(r, "Assets", "assets")
	filteredTargets := writableInterfaceOptions(base, targets)
	return linkFormData{
		Base:       base,
		AssetID:    assetID,
		Errors:     orEmpty(errs),
		Interfaces: unpatchedInterfaces(interfaces),
		Targets:    filteredTargets,
		TargetHint: pickerHint(len(filteredTargets), len(targets),
			"There are no unpatched ports in the estate yet.",
			"There is nothing here you can cable to -- every unpatched port belongs to an asset you don't own.",
			"Showing %d of %d ports -- the rest belong to assets you don't own."),
	}
}

// newPassThroughForm builds the "patch through" panel. UNFILTERED, unlike
// newLinkForm's front-side picker: a front port already used by one
// pass-through and a rear port already carrying other strands are both still
// valid choices here -- the front-port unique index and the rear-position
// unique index are what refuse an actual collision, and requireUniqueRearPosition
// (cabling.go) is what turns that refusal into a message on this form rather
// than a bare 409.
func (a *App) newPassThroughForm(r *http.Request, assetID string, errs map[string]string, interfaces []store.InterfaceRow) passThroughFormData {
	return passThroughFormData{
		Base:       a.base(r, "Assets", "assets"),
		AssetID:    assetID,
		Errors:     orEmpty(errs),
		Interfaces: interfaces,
	}
}

// writableInterfaceOptions keeps only the ports on assets the caller may
// write -- the far end of a new cable. See newLinkForm.
func writableInterfaceOptions(b Base, targets []store.InterfaceOption) []store.InterfaceOption {
	out := make([]store.InterfaceOption, 0, len(targets))
	for _, opt := range targets {
		if b.CanWriteEntity("asset", opt.AssetID) {
			out = append(out, opt)
		}
	}
	return out
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
//
// CanWrite mirrors the store's own two-ended rule, not Base.CanWrite and not
// isAdmin. WP-1.1 item 1 moved "dependency" out of ScopeTopology into
// domain.ScopeSubjectDerived (the ReparentAsset two-ended trap, resolved by
// authorizeDependencySubjects in internal/store/deps.go), so a project owner
// who owns BOTH ends -- the consumer service and the provider's owning
// service -- can write a dependency at the store layer. store.DependencyRow
// already carries both subjects: ConsumerServiceID directly, and ProviderSvc
// (`db:"provider_service_id"`) resolved by dependencySelect through the same
// two LEFT JOIN chains (endpoint→service, route→frontend_endpoint→service)
// that authorizeDependencySubjects performs at write time. Nothing here
// re-derives that join or queries the store again -- it reads two columns
// the select already produced and asks the caller's own resolved permit
// (Base.CanWriteEntity, backed by permit.Covers, no extra DB round trip)
// whether it covers both. Administrator continues to cover both by
// construction, so this is a widening of what renders, never a narrowing.
//
// SecretRef stays gated on isAdmin alone -- see depRows' doc comment for why
// that is a deliberately different, narrower question than CanWrite.
type depRowData struct {
	Dep       *store.DependencyRow
	CanWrite  bool
	CSRF      string
	Direction string // "upstream" or "downstream"
	// ShowActions is set from the TABLE's depRowList.AnyWritable, not from
	// this row's own CanWrite -- fix-b item 1. A dependency's write
	// permission is two-ended, so two rows in the same table can legitimately
	// disagree on CanWrite; if the <td> itself only rendered on CanWrite (the
	// original defect this replaced), every row in a mixed table would carry
	// a different cell count than every other row, not just than the header.
	// The header decides whether the COLUMN exists (AnyWritable, per table);
	// ShowActions carries that same table-wide answer onto every row so they
	// all render the identical number of cells; CanWrite alone then decides,
	// per row, whether the cell is empty or holds the buttons.
	// TestDependencyTableHeaderMatchesItsRows pins this by walking every
	// <tr>, not just the first, in a fixture with a mixed table.
	ShowActions bool
	DataClasses []string
	// SecretRef is the identity's secret_ref, ALREADY REDACTED for anyone who
	// is not a full Administrator -- computed once here, in the handler's
	// view model, rather than left to the template. A template-side
	// {{if .IsAdmin}} around .Dep.IdentitySecretRef is one {{end}} away from
	// leaking it, and it does nothing at all for a CSV export, which never
	// passes through a template. Empty when the edge carries no identity, or
	// the identity carries no secret_ref -- both render as "—", the same as
	// every other absent field on this row.
	SecretRef string
}

// depRows decorates dependency rows for rendering. covers is the caller's
// own write predicate (Base.CanWriteEntity, or an equivalent in tests) --
// CanWrite is set only when it covers BOTH the consumer service and the
// provider's owning service, the same two-ended rule the store enforces in
// authorizeDependencySubjects. isAdmin gates SecretRef alone: that stays
// deliberately narrower than CanWrite (see depRowData's doc comment), so a
// project owner who now sees the controls still never sees the secret ref.
// depRowList is one rendered dependency table's rows.
//
// AnyWritable exists for a single reason: the trailing actions column's <th>
// must appear exactly when some row renders a trailing <td>, and CanWrite is
// now PER ROW. A dependency's write permission is two-ended -- it depends on
// the consumer service AND the provider's owning service -- so two rows in
// the same table can legitimately differ, and no page-level flag can stand in
// for them. The header used .IsAdmin while the rows moved to .CanWrite, which
// left the table one column short for precisely the people this change set
// out to serve: a project owner who owns both ends of some edge but is not an
// administrator. Found by the E2E pass, not by any assertion.
type depRowList []depRowData

// AnyWritable reports whether any row in the table has CanWrite, i.e.
// whether the actions column exists at all this render. Every row's
// ShowActions is set from this same table-wide answer (depRows), so once the
// column exists every row contributes a cell for it -- CanWrite alone then
// decides, per row, whether that cell is empty or holds the buttons.
func (rows depRowList) AnyWritable() bool {
	for _, r := range rows {
		if r.CanWrite {
			return true
		}
	}
	return false
}

func depRows(deps []store.DependencyRow, classes map[string][]string, direction, csrf string, isAdmin bool, covers func(entityType, id string) bool) []depRowData {
	out := make([]depRowData, len(deps))
	for i := range deps {
		out[i] = depRowData{
			Dep:         &deps[i],
			CanWrite:    canWriteDependency(covers, deps[i].ConsumerServiceID, deps[i].ProviderSvc),
			CSRF:        csrf,
			Direction:   direction,
			DataClasses: classes[deps[i].ID],
			SecretRef:   secretRefDisplay(deps[i].IdentitySecretRef, isAdmin),
		}
	}
	// Second pass, deliberately: ShowActions needs the WHOLE table's answer
	// (depRowList.AnyWritable), which does not exist until every row's
	// CanWrite has been set above.
	anyWritable := depRowList(out).AnyWritable()
	for i := range out {
		out[i].ShowActions = anyWritable
	}
	return out
}

// canWriteDependency is the one place a dependency's two-ended write rule is
// evaluated on the read path, shared by depRows (the table) and
// DependencyVerify (deps.go, the standalone re-render after a verify) so the
// two call sites cannot drift the way the table header and its rows once
// did (fix-b item 5). It mirrors authorizeDependencySubjects
// (internal/store/deps.go): a project owner may write a dependency only when
// their permit covers BOTH the consumer service and the provider's owning
// service. covers is the caller's own predicate (Base.CanWriteEntity, or an
// equivalent in a test) -- what is actually shared between here and the
// store is permit.Covers itself; the SUBJECT RESOLUTION (which two ids to
// ask it about) is what this function pins, and pinning it in one function
// is what makes "cannot drift" true rather than merely asserted.
func canWriteDependency(covers func(entityType, id string) bool, consumerServiceID, providerServiceID string) bool {
	return covers("service", consumerServiceID) && covers("service", providerServiceID)
}

// canWriteLink is canWriteDependency's sibling for "link": a project owner
// may retire a cable only when their permit covers the asset on BOTH ends
// (authorizeLinkSubjects, internal/store/network.go). Before fix-b item 5
// this was written out inline in asset_detail.html, with no compiler and no
// test coupling the template's `and`/`CanWriteEntity` chain to the store
// function it was meant to mirror.
func canWriteLink(covers func(entityType, id string) bool, nearAssetID, peerAssetID string) bool {
	return covers("asset", nearAssetID) && covers("asset", peerAssetID)
}

// secretRefDisplay applies the read-path redaction identity.secret_ref needs
// (spec §10): a non-administrator gets domain.Redacted, never the value,
// never merely a hidden field a client could still read out of the response.
func secretRefDisplay(ref *string, isAdmin bool) string {
	if ref == nil || *ref == "" {
		return ""
	}
	if !isAdmin {
		return domain.Redacted
	}
	return *ref
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
