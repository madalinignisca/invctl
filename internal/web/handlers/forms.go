package handlers

import (
	"log/slog"
	"net/http"

	"github.com/gabriel/invctl/internal/domain"
	"github.com/gabriel/invctl/internal/store"
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
}

type instanceFormData struct {
	Base
	ServiceID    string
	Errors       map[string]string
	Hosts        []store.AssetRow
	RuntimeTypes []string
}

type endpointFormData struct {
	Base
	ServiceID  string
	Errors     map[string]string
	Protos     []string
	BindScopes []string
	Exposures  []string
	TLSModes   []string
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
}

// newAssetForm builds the asset form context. Parents is the same list the
// table shows, so an asset can be filed under anything already on screen.
func (a *App) newAssetForm(r *http.Request, errs map[string]string, envs []domain.Environment, kinds []store.VocabularyTerm, parents []store.AssetRow) assetFormData {
	teams, roles := a.responsibilityOptions(r)
	return assetFormData{
		Base:         a.base(r, "Assets", "assets"),
		Errors:       orEmpty(errs),
		Environments: envs,
		Kinds:        kinds,
		Lifecycles:   domain.AssetLifecycles,
		Parents:      parents,
		Teams:        teams,
		Roles:        roles,
	}
}

// responsibilityOptions loads the two pickers every entity form carries.
//
// Loaded here rather than threaded through each caller's signature: they are two
// small tables that every form wants and no caller varies. A failure degrades to
// empty pickers and is logged rather than returned, because a form that renders
// without its optional dropdowns is far better than a detail page that 500s over
// a field nobody is required to fill in.
func (a *App) responsibilityOptions(r *http.Request) ([]store.TeamRow, []store.VocabularyTerm) {
	teams, err := a.Store.ListTeams(r.Context(), store.TeamFilter{})
	if err != nil {
		slog.Error("loading teams for a form", "error", err)
		teams = nil
	}
	roles, err := a.Store.ResponsibilityRoles(r.Context())
	if err != nil {
		slog.Error("loading responsibility roles for a form", "error", err)
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
	}
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
		Protos:     domain.L4Protos,
		BindScopes: domain.BindScopes,
		Exposures:  domain.Exposures,
		TLSModes:   domain.TLSModes,
	}
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

func (a *App) newPrefixForm(r *http.Request, errs map[string]string, envs []domain.Environment) prefixFormData {
	return prefixFormData{
		Base:         a.base(r, "Prefixes", "prefixes"),
		Errors:       orEmpty(errs),
		Environments: envs,
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
