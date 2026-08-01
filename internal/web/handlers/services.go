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
	Projects       []store.ProjectRow
	Kinds          []store.VocabularyTerm
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
		ProjectID:     q.Get("project"),
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
	projects, err := a.Store.ListProjects(r.Context(), store.ProjectFilter{})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	kinds, err := a.Store.ServiceKinds(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	data := serviceListPage{
		Base:         a.base(r, "Services", "services"),
		Services:     services,
		Environments: envs,
		Projects:     projects,
		Kinds:        kinds,
		// Availability stays a domain slice: the impact engine switches on it.
		Availabilities: domain.Availabilities,
		Filter:         filter,
		FormData:       a.newServiceForm(r, nil, domain.ServiceSpec{}, envs, kinds),
	}
	a.Render.Respond(w, r, http.StatusOK, "service_list", "service_table", data)
}

type serviceDetailPage struct {
	Base
	Service      *store.ServiceRow
	Certificates []store.DeployedCertificate
	Costs        []store.CostRow
	CostTotals   domain.CostTotals
	CostKinds    []store.VocabularyTerm
	CostPeriods  []string
	Instances    []store.InstanceRow
	Endpoints    []store.EndpointRow
	Routes       []store.RouteRow
	Upstream     []depRowData
	Downstream   []depRowData
	// InstanceHealth is what the estate reports about each placement, keyed by
	// instance id, with staleness applied and any override alongside rather
	// than merged in. A service has no health of its own -- only the places it
	// actually runs can be up or down.
	InstanceHealth map[string]store.EntityHealth
	// Timeline folds this service's declared history with its one-hop declared
	// neighbours' -- its placements, the hosts under them, its endpoints, its
	// dependencies -- and with the observed transitions for the same rows.
	Timeline     []store.TimelineEntry
	InstanceForm instanceFormData
	EndpointForm endpointFormData
	// EndpointEdit is set when ?edit names one of THIS service's endpoints. The
	// ownership check matters: without it the id in the query string chooses
	// which row the page renders a form for, and any endpoint in the estate
	// could be opened -- and saved -- from any service's page.
	EndpointEdit *endpointFormData
	// RuntimeTypes and DesiredStates feed the inline placement editor. From the
	// domain rather than a lookup table because both are CHECK-constrained
	// enums with a Go constant set beside them.
	RuntimeTypes  []string
	DesiredStates []string
	// Edit is set only when a placement correction was refused; see editState.
	Edit           *editState
	DependencyForm dependencyFormData
	OverrideForm   overrideFormData
}

// ServiceDetail is the page the whole tool builds towards: header, placement,
// endpoints, and the two dependency panels.
func (a *App) ServiceDetail(w http.ResponseWriter, r *http.Request) {
	a.renderServiceDetail(w, r, http.StatusOK, r.PathValue("id"), endpointFormState{}, nil)
}

// endpointFormState carries a rejected endpoint back to the page it came from.
// failed nil means the errors belong to the ADD form; otherwise they belong to
// the correction form for that endpoint.
type endpointFormState struct {
	errs   map[string]string
	failed *domain.Endpoint
}

// renderServiceDetail draws the page, at any status.
//
// SEPARATE FROM ServiceDetail because a validation failure has to redraw the
// whole page when the request did not come from HTMX, and could not: the 422
// path handed the "service_detail" template an endpointFormData, which carries
// none of the fields a service page reads. Submitting an endpoint form with
// JavaScript off returned 500 -- for corrections, and for additions since the
// form was written. A form that only works with JavaScript is not what the rest
// of this codebase does; the row editors are plain forms precisely so they work
// without it, and this was the one path that did not.
func (a *App) renderServiceDetail(w http.ResponseWriter, r *http.Request, status int,
	id string, epState endpointFormState, edit *editState) {

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
	classOptions, err := a.Store.DataClassVocabulary(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	b := a.base(r, service.Name, "services")
	if edit != nil {
		// The refused row opens, whatever the query string said.
		b.EditRow = edit.ID
	}
	certificates, err := a.Store.CertificatesOnService(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	costs, err := a.Store.ListServiceCosts(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	costKinds, err := a.Store.CostKinds(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	a.Render.Page(w, status, "service_detail", serviceDetailPage{
		Base:           b,
		Service:        service,
		Certificates:   certificates,
		Costs:          costs,
		CostTotals:     store.TotalCosts(costs, domain.FormatDate(a.Store.Now())),
		CostKinds:      costKinds,
		CostPeriods:    domain.CostPeriods,
		Instances:      instances,
		Endpoints:      endpoints,
		Routes:         routes,
		Upstream:       depRows(upstream, classes, "upstream", b.CSRF, b.CanWrite),
		Downstream:     depRows(downstream, classes, "downstream", b.CSRF, b.CanWrite),
		InstanceHealth: instanceHealth,
		Timeline:       timeline,
		InstanceForm:   a.newInstanceForm(r, id, nil, hostable),
		EndpointForm:   a.newEndpointForm(r, id, addEndpointErrs(epState)),
		EndpointEdit:   a.endpointEditForm(r, b, endpoints, epState),
		RuntimeTypes:   domain.RuntimeTypes,
		DesiredStates:  domain.DesiredStates,
		Edit:           edit,
		DependencyForm: a.newDependencyForm(r, id, nil, domain.DependencySpec{}, allEndpoints, allRoutes, identities, classOptions),
		OverrideForm:   a.newOverrideForm(r, overrideTargets, nil, overrideForm{}),
	})
}

// endpointEditForm builds the correction form for the one endpoint the operator
// opened, or nil. The endpoint must be one this page already lists -- an id
// naming somebody else's socket is simply not found, not a form.
// addEndpointErrs returns the messages that belong to the add form, which is
// only when nothing was being corrected.
func addEndpointErrs(s endpointFormState) map[string]string {
	if s.failed != nil {
		return nil
	}
	return s.errs
}

func (a *App) endpointEditForm(r *http.Request, b Base, endpoints []store.EndpointRow,
	state endpointFormState) *endpointFormData {

	// A rejected correction wins over the query string: the operator is looking
	// at the values they just typed, not at what is stored.
	if state.failed != nil {
		f := a.newEndpointEditForm(r, state.failed, state.errs)
		return &f
	}
	if b.EditRow == "" || !b.CanWrite {
		return nil
	}
	for i := range endpoints {
		if endpoints[i].ID == b.EditRow {
			f := a.newEndpointEditForm(r, &endpoints[i].Endpoint, nil)
			return &f
		}
	}
	return nil
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
		TeamID:        optionalString(r, "team_id"),
		ManagerRole:   optionalString(r, "manager_role"),
		EOLDate:       optionalString(r, "eol_date"),
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
	// submittedString, not optionalString: a picker that failed to render must
	// not read as an operator clearing the field. See its doc comment.
	updated.TeamID = submittedString(r, "team_id", updated.TeamID)
	updated.ManagerRole = submittedString(r, "manager_role", updated.ManagerRole)
	updated.EOLDate = optionalString(r, "eol_date")
	updated.Lifecycle = formValue(r, "lifecycle")

	if err := a.Store.UpdateService(r.Context(), actor(r), &updated); err != nil {
		a.respondServiceFormError(w, r, err, domain.ServiceSpec{
			Code: updated.Code, Name: updated.Name, Kind: updated.Kind,
			EnvironmentID: updated.EnvironmentID, Availability: updated.Availability,
			Tier: updated.Tier, MinHealthy: updated.MinHealthy, FailoverMode: updated.FailoverMode,
			EOLDate: updated.EOLDate,
			// Carried back, or the re-rendered form cannot show what was picked
			// -- and the rule most likely to have caused the 422 is the one
			// about these two fields.
			TeamID: updated.TeamID, ManagerRole: updated.ManagerRole,
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
	kinds, listErr := a.Store.ServiceKinds(r.Context())
	if listErr != nil {
		a.serverError(w, r, listErr)
		return
	}
	a.Render.Partial(w, http.StatusUnprocessableEntity, "service_form",
		a.newServiceForm(r, messages, spec, envs, kinds))
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

// InstanceUpdate corrects a placement.
//
// The HOST is not a field. Moving a service to another box is a migration: it
// changes what an outage of either machine reaches, and the placement's history
// on the old host is part of why somebody would look. Withdraw and place again,
// which leaves both facts.
//
// SOURCE is not a field either, for a harder reason. It is provenance -- a
// claim about where a fact came from -- and CheckProvenanceWrite exists to stop
// a caller asserting one. Reading it from a form would let a request choose its
// own authority, so it is carried over from the stored row and never parsed.
func (a *App) InstanceUpdate(w http.ResponseWriter, r *http.Request) {
	existing, err := a.Store.GetInstance(r.Context(), r.PathValue("id"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	updated := existing.ServiceInstance
	updated.RuntimeType = formValue(r, "runtime_type")
	updated.Role = optionalString(r, "role")
	updated.Shard = optionalString(r, "shard")
	updated.Ordinal = intValue(r, "ordinal", existing.Ordinal)
	updated.DesiredState = formValue(r, "desired_state")

	if err := a.Store.UpdateInstance(r.Context(), actor(r), &updated); err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			if !isConflict(err) {
				a.handleStoreError(w, r, err)
				return
			}
			messages = map[string]string{
				"ordinal": "another placement already has that ordinal on that host, or this one has been withdrawn",
			}
		}
		// 422 with the row reopened on what was typed. Same rule, same reason
		// as everywhere else: a redirect refills from storage and the operator
		// cannot tell their edit was refused rather than saved.
		a.renderServiceDetail(w, r, http.StatusUnprocessableEntity, existing.ServiceID,
			endpointFormState{}, rejected(r, existing.ID, messages,
				"runtime_type", "role", "shard", "ordinal", "desired_state"))
		return
	}

	a.setFlash(r, "success", "Placement corrected.")
	render.Redirect(w, r, "/services/"+existing.ServiceID)
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

// EndpointUpdate corrects a listening socket.
//
// WHAT IS NOT HERE IS THE POINT. service_id, ip_address_id and certificate_id
// are carried over untouched from the stored row: each of them decides what the
// endpoint is ATTACHED to, and moving an endpoint takes every dependency that
// resolves through it along with it. Correcting a port is a correction;
// re-pointing a socket is a different act and needs its own flow with its own
// warning. l7_proto is absent for a duller reason -- the add form does not
// offer it either, and a field on one and not the other is how the two drift.
func (a *App) EndpointUpdate(w http.ResponseWriter, r *http.Request) {
	existing, err := a.Store.GetEndpoint(r.Context(), r.PathValue("id"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	updated := existing.Endpoint
	updated.Name = formValue(r, "name")
	updated.L4Proto = formValue(r, "l4_proto")
	updated.Port = optionalInt(r, "port")
	updated.UnixPath = optionalString(r, "unix_path")
	updated.BindScope = formValue(r, "bind_scope")
	updated.TLSMode = formValue(r, "tls_mode")
	updated.Exposure = formValue(r, "exposure")

	if err := a.Store.UpdateEndpoint(r.Context(), actor(r), &updated); err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			if isConflict(err) {
				messages = map[string]string{
					"port": "this service already has an endpoint on that port and protocol",
				}
			} else {
				a.handleStoreError(w, r, err)
				return
			}
		}
		if render.IsHTMX(r) {
			a.Render.Partial(w, http.StatusUnprocessableEntity, "endpoint_form",
				a.newEndpointEditForm(r, &updated, messages))
			return
		}
		a.renderServiceDetail(w, r, http.StatusUnprocessableEntity, existing.ServiceID,
			endpointFormState{errs: messages, failed: &updated}, nil)
		return
	}

	a.setFlash(r, "success", "Endpoint corrected.")
	render.Redirect(w, r, "/services/"+existing.ServiceID)
}

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
		// Without HTMX this used to answer a form post with a bare form
		// fragment -- no layout, no navigation, no way back. The whole page,
		// with the errors on the form, is what a browser submitting a form
		// expects.
		if render.IsHTMX(r) {
			a.Render.Partial(w, http.StatusUnprocessableEntity, "endpoint_form",
				a.newEndpointForm(r, serviceID, messages))
			return
		}
		a.renderServiceDetail(w, r, http.StatusUnprocessableEntity, serviceID,
			endpointFormState{errs: messages}, nil)
		return
	}

	a.setFlash(r, "success", "Endpoint "+e.Name+" added.")
	render.Redirect(w, r, "/services/"+serviceID)
}
