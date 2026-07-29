package handlers

import (
	"net/http"

	"github.com/gabriel/invctl/internal/domain"
	"github.com/gabriel/invctl/internal/store"
	"github.com/gabriel/invctl/internal/web/render"
)

type serviceListPage struct {
	Base
	Services       []store.ServiceRow
	Environments   []domain.Environment
	Applications   []domain.Application
	Kinds          []string
	Availabilities []string
	Filter         store.ServiceFilter
	FormData       serviceFormData
}

// ServiceList renders the service catalogue.
func (a *App) ServiceList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := store.ServiceFilter{
		EnvironmentID: q.Get("environment"),
		Kind:          q.Get("kind"),
		Availability:  q.Get("availability"),
		Tier:          queryInt(r, "tier", 0),
		Query:         q.Get("q"),
	}

	services, err := a.Store.ListServices(r.Context(), filter)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	envs, err := a.Store.ListEnvironments(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	apps, err := a.Store.ListApplications(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	data := serviceListPage{
		Base:           a.base(r, "Services", "services"),
		Services:       services,
		Environments:   envs,
		Applications:   apps,
		Kinds:          domain.ServiceKinds,
		Availabilities: domain.Availabilities,
		Filter:         filter,
		FormData:       a.newServiceForm(r, nil, domain.ServiceSpec{}, envs, apps),
	}
	a.Render.Respond(w, r, http.StatusOK, "service_list", "service_table", data)
}

type serviceDetailPage struct {
	Base
	Service    *store.ServiceRow
	Instances  []store.InstanceRow
	Endpoints  []store.EndpointRow
	Routes     []store.RouteRow
	Upstream   []depRowData
	Downstream []depRowData
	// InstanceHealth is what the estate reports about each placement, keyed by
	// instance id, with staleness applied and any override alongside rather
	// than merged in. A service has no health of its own -- only the places it
	// actually runs can be up or down.
	InstanceHealth map[string]store.EntityHealth
	// Timeline folds this service's declared history with its one-hop declared
	// neighbours' -- its placements, the hosts under them, its endpoints, its
	// dependencies -- and with the observed transitions for the same rows.
	Timeline       []store.TimelineEntry
	InstanceForm   instanceFormData
	EndpointForm   endpointFormData
	DependencyForm dependencyFormData
	OverrideForm   overrideFormData
}

// ServiceDetail is the page the whole tool builds towards: header, placement,
// endpoints, and the two dependency panels.
func (a *App) ServiceDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	service, err := a.Store.GetService(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	instances, err := a.Store.ListInstancesByService(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	endpoints, err := a.Store.ListEndpointsByService(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	routes, err := a.Store.ListRoutesByService(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	upstream, err := a.Store.ListUpstream(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	downstream, err := a.Store.ListDownstream(r.Context(), id)
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
	timeline, _, err := a.Store.TimelineForEntityAndNeighbours(r.Context(), "service", id, timelineLimit)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	overrideTargets, err := a.instanceOverrideTargets(r.Context(), instances)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	depIDs := make([]string, 0, len(upstream)+len(downstream))
	for _, d := range upstream {
		depIDs = append(depIDs, d.ID)
	}
	for _, d := range downstream {
		depIDs = append(depIDs, d.ID)
	}
	classes, err := a.Store.DataClassesFor(r.Context(), depIDs)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	// Pickers for the inline forms.
	hosts, err := a.Store.ListAssets(r.Context(), store.AssetFilter{})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	hostable := hostableAssets(hosts)
	allEndpoints, err := a.Store.ListAllEndpoints(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	allRoutes, err := a.Store.ListAllRoutes(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	identities, err := a.Store.ListIdentities(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	b := a.base(r, service.Name, "services")
	a.Render.Page(w, http.StatusOK, "service_detail", serviceDetailPage{
		Base:           b,
		Service:        service,
		Instances:      instances,
		Endpoints:      endpoints,
		Routes:         routes,
		Upstream:       depRows(upstream, classes, "upstream", b.CSRF, b.CanWrite),
		Downstream:     depRows(downstream, classes, "downstream", b.CSRF, b.CanWrite),
		InstanceHealth: instanceHealth,
		Timeline:       timeline,
		InstanceForm:   a.newInstanceForm(r, id, nil, hostable),
		EndpointForm:   a.newEndpointForm(r, id, nil),
		DependencyForm: a.newDependencyForm(r, id, nil, domain.DependencySpec{}, allEndpoints, allRoutes, identities),
		OverrideForm:   a.newOverrideForm(r, overrideTargets, nil, overrideForm{}),
	})
}

// ServiceCreate adds a service.
func (a *App) ServiceCreate(w http.ResponseWriter, r *http.Request) {
	spec := domain.ServiceSpec{
		Code:          formValue(r, "code"),
		Name:          formValue(r, "name"),
		Kind:          formValue(r, "kind"),
		EnvironmentID: formValue(r, "environment_id"),
		Availability:  formValue(r, "availability"),
		Tier:          intValue(r, "tier", 3),
		MinHealthy:    optionalInt(r, "min_healthy"),
		FailoverMode:  optionalString(r, "failover_mode"),
		RTOMinutes:    optionalInt(r, "rto_minutes"),
		RPOMinutes:    optionalInt(r, "rpo_minutes"),
		OwnerTeam:     optionalString(r, "owner_team"),
		ApplicationID: optionalString(r, "application_id"),
	}

	svc, err := domain.NewService(store.NewID(), spec, a.Store.Now())
	if err == nil {
		err = a.Store.CreateService(r.Context(), actor(r), svc)
	}
	if err != nil {
		a.respondServiceFormError(w, r, err, spec)
		return
	}

	a.setFlash(r, "success", "Service "+svc.Code+" created.")
	render.Redirect(w, r, "/services/"+svc.ID)
}

// ServiceUpdate saves field changes.
func (a *App) ServiceUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := a.Store.GetService(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	updated := existing.Service
	updated.Code = formValue(r, "code")
	updated.Name = formValue(r, "name")
	updated.Kind = formValue(r, "kind")
	updated.EnvironmentID = formValue(r, "environment_id")
	updated.Availability = formValue(r, "availability")
	updated.Tier = intValue(r, "tier", existing.Tier)
	updated.MinHealthy = optionalInt(r, "min_healthy")
	updated.FailoverMode = optionalString(r, "failover_mode")
	updated.RTOMinutes = optionalInt(r, "rto_minutes")
	updated.RPOMinutes = optionalInt(r, "rpo_minutes")
	updated.OwnerTeam = optionalString(r, "owner_team")
	updated.Lifecycle = formValue(r, "lifecycle")

	if err := a.Store.UpdateService(r.Context(), actor(r), &updated); err != nil {
		a.respondServiceFormError(w, r, err, domain.ServiceSpec{
			Code: updated.Code, Name: updated.Name, Kind: updated.Kind,
			EnvironmentID: updated.EnvironmentID, Availability: updated.Availability,
			Tier: updated.Tier, MinHealthy: updated.MinHealthy, FailoverMode: updated.FailoverMode,
		})
		return
	}

	a.setFlash(r, "success", "Service updated.")
	render.Redirect(w, r, "/services/"+id)
}

// ServiceRetire soft-deletes a service.
func (a *App) ServiceRetire(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.Store.RetireService(r.Context(), actor(r), id); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Service retired. Its history is kept.")
	render.Redirect(w, r, "/services/"+id)
}

func (a *App) respondServiceFormError(w http.ResponseWriter, r *http.Request, err error, spec domain.ServiceSpec) {
	messages, ok := validationErrors(err)
	if !ok {
		if isConflict(err) {
			messages = map[string]string{"code": "a service with that code already exists"}
		} else {
			a.handleStoreError(w, r, err)
			return
		}
	}
	envs, listErr := a.Store.ListEnvironments(r.Context())
	if listErr != nil {
		a.serverError(w, r, listErr)
		return
	}
	apps, listErr := a.Store.ListApplications(r.Context())
	if listErr != nil {
		a.serverError(w, r, listErr)
		return
	}
	a.Render.Partial(w, http.StatusUnprocessableEntity, "service_form",
		a.newServiceForm(r, messages, spec, envs, apps))
}

// ---------- instances ----------

// InstanceCreate places a service on a host.
func (a *App) InstanceCreate(w http.ResponseWriter, r *http.Request) {
	serviceID := r.PathValue("id")

	si, err := domain.NewServiceInstance(store.NewID(), serviceID,
		formValue(r, "host_asset_id"), formValue(r, "runtime_type"),
		intValue(r, "ordinal", 0), a.Store.Now())
	if err == nil {
		si.Role = optionalString(r, "role")
		si.Shard = optionalString(r, "shard")
		err = a.Store.CreateInstance(r.Context(), actor(r), si)
	}
	if err != nil {
		a.respondInstanceError(w, r, serviceID, err)
		return
	}

	a.setFlash(r, "success", "Instance placed.")
	render.Redirect(w, r, "/services/"+serviceID)
}

// InstanceRetire withdraws a placement from the estate.
//
// The soft delete for a placement, and distinct from stopping one: withdrawing
// says the service is no longer deployed here, while desired_state says what it
// should be doing while it is. Those were one column until migration 00002, and
// the row plus its whole audit history stay either way.
func (a *App) InstanceRetire(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	instance, err := a.Store.GetInstance(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	if err := a.Store.RetireInstance(r.Context(), actor(r), id); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success",
		"Placement withdrawn. It no longer counts as capacity, and the service can be placed on that host again.")
	render.Redirect(w, r, "/services/"+instance.ServiceID)
}

func (a *App) respondInstanceError(w http.ResponseWriter, r *http.Request, serviceID string, err error) {
	messages, ok := validationErrors(err)
	if !ok {
		if isConflict(err) {
			messages = map[string]string{
				"ordinal": "this service already has an instance with that ordinal on that host",
			}
		} else {
			a.handleStoreError(w, r, err)
			return
		}
	}
	hosts, listErr := a.Store.ListAssets(r.Context(), store.AssetFilter{})
	if listErr != nil {
		a.serverError(w, r, listErr)
		return
	}
	a.Render.Partial(w, http.StatusUnprocessableEntity, "instance_form",
		a.newInstanceForm(r, serviceID, messages, hostableAssets(hosts)))
}

// ---------- endpoints ----------

// EndpointCreate adds a listening socket to a service.
func (a *App) EndpointCreate(w http.ResponseWriter, r *http.Request) {
	serviceID := r.PathValue("id")

	e := &domain.Endpoint{
		ID:        store.NewID(),
		ServiceID: serviceID,
		Name:      formValue(r, "name"),
		L4Proto:   formValue(r, "l4_proto"),
		Port:      optionalInt(r, "port"),
		UnixPath:  optionalString(r, "unix_path"),
		BindScope: formValue(r, "bind_scope"),
		L7Proto:   optionalString(r, "l7_proto"),
		TLSMode:   formValue(r, "tls_mode"),
		Exposure:  formValue(r, "exposure"),
	}

	if err := a.Store.CreateEndpoint(r.Context(), actor(r), e); err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			if isConflict(err) {
				messages = map[string]string{"name": "this service already has an endpoint with that name"}
			} else {
				a.handleStoreError(w, r, err)
				return
			}
		}
		a.Render.Partial(w, http.StatusUnprocessableEntity, "endpoint_form",
			a.newEndpointForm(r, serviceID, messages))
		return
	}

	a.setFlash(r, "success", "Endpoint "+e.Name+" added.")
	render.Redirect(w, r, "/services/"+serviceID)
}
