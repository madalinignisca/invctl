package handlers

import (
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

type environmentFormData struct {
	Base
	Errors map[string]string
	Form   environmentForm
	Roles  []string
}

type assetFormData struct {
	Base
	Errors       map[string]string
	Environments []domain.Environment
	Kinds        []string
	Lifecycles   []string
	Parents      []store.AssetRow
}

type serviceFormData struct {
	Base
	Errors         map[string]string
	Spec           domain.ServiceSpec
	Environments   []domain.Environment
	Applications   []domain.Application
	Kinds          []string
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
	ClassOptions []string
}

// newAssetForm builds the asset form context. Parents is the same list the
// table shows, so an asset can be filed under anything already on screen.
func (a *App) newAssetForm(r *http.Request, errs map[string]string, envs []domain.Environment, parents []store.AssetRow) assetFormData {
	return assetFormData{
		Base:         a.base(r, "Assets", "assets"),
		Errors:       orEmpty(errs),
		Environments: envs,
		Kinds:        domain.AssetKinds,
		Lifecycles:   domain.AssetLifecycles,
		Parents:      parents,
	}
}

func (a *App) newServiceForm(r *http.Request, errs map[string]string, spec domain.ServiceSpec, envs []domain.Environment, apps []domain.Application) serviceFormData {
	if spec.Tier == 0 {
		spec.Tier = 3
	}
	return serviceFormData{
		Base:           a.base(r, "Services", "services"),
		Errors:         orEmpty(errs),
		Spec:           spec,
		Environments:   envs,
		Applications:   apps,
		Kinds:          domain.ServiceKinds,
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

func (a *App) newDependencyForm(r *http.Request, serviceID string, errs map[string]string, spec domain.DependencySpec, endpoints []store.EndpointRow, routes []store.RouteRow, identities []domain.Identity) dependencyFormData {
	return dependencyFormData{
		Base:         a.base(r, "Services", "services"),
		ServiceID:    serviceID,
		Errors:       orEmpty(errs),
		Spec:         spec,
		AllEndpoints: endpoints,
		AllRoutes:    routes,
		Identities:   identities,
		Natures:      domain.Natures,
		ClassOptions: domain.DataClasses,
	}
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
