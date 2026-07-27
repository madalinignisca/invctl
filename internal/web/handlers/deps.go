package handlers

import (
	"net/http"

	"github.com/gabriel/invctl/internal/domain"
	"github.com/gabriel/invctl/internal/store"
	"github.com/gabriel/invctl/internal/web/render"
)

// DependencyCreate declares an edge from a consumer service to a provider
// endpoint or route.
func (a *App) DependencyCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	consumerID := r.PathValue("id")

	spec := domain.DependencySpec{
		ConsumerServiceID:  consumerID,
		ProviderEndpointID: optionalString(r, "provider_endpoint_id"),
		ProviderRouteID:    optionalString(r, "provider_route_id"),
		Nature:             formValue(r, "nature"),
		ToleranceSeconds:   optionalInt(r, "tolerance_seconds"),
		FailureMode:        formValue(r, "failure_mode"),
		IdentityID:         optionalString(r, "identity_id"),
		AuthMethod:         optionalString(r, "auth_method"),
		FirewallRuleRef:    optionalString(r, "firewall_rule_ref"),
	}

	dep, err := domain.NewDependency(store.NewID(), spec, a.Store.Now())
	if err == nil {
		err = a.Store.CreateDependency(r.Context(), actor(r), dep, r.Form["data_class"])
	}
	if err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			a.handleStoreError(w, r, err)
			return
		}
		a.renderDependencyForm(w, r, consumerID, messages, spec)
		return
	}

	a.setFlash(r, "success", "Dependency recorded.")
	render.Redirect(w, r, "/services/"+consumerID)
}

// DependencyRetire withdraws an edge without losing the record that it existed.
func (a *App) DependencyRetire(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dep, err := a.Store.GetDependency(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	if err := a.Store.RetireDependency(r.Context(), actor(r), id); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Dependency retired.")
	render.Redirect(w, r, "/services/"+dep.ConsumerServiceID)
}

// DependencyVerify records that a human confirmed the edge is still real.
//
// This is what separates "someone wrote this down two years ago" from
// "this is current", and it is the only defence against a dependency graph
// quietly rotting.
func (a *App) DependencyVerify(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.Store.VerifyDependency(r.Context(), actor(r), id); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	dep, err := a.Store.GetDependency(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	classes, err := a.Store.DataClassesFor(r.Context(), []string{id})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	// Swap just the row that changed, and confirm the action out of band --
	// the row itself has nowhere to show a message.
	b := a.base(r, "", "")
	a.Render.PartialWithOOB(w, http.StatusOK, "dependency_row", depRowData{
		Dep:         dep,
		CanWrite:    b.CanWrite,
		CSRF:        b.CSRF,
		Direction:   directionOf(r),
		DataClasses: classes[id],
	}, oobFlash("success", "Marked "+dep.ConsumerCode+" → "+dep.ProviderCode+" as verified."))
}

// directionOf lets the row re-render in the panel it came from. The client
// sends it because only the client knows which of the two panels the button
// was in.
func directionOf(r *http.Request) string {
	if r.URL.Query().Get("direction") == "downstream" {
		return "downstream"
	}
	return "upstream"
}

func (a *App) renderDependencyForm(w http.ResponseWriter, r *http.Request, consumerID string, messages map[string]string, spec domain.DependencySpec) {
	endpoints, err := a.Store.ListAllEndpoints(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	routes, err := a.Store.ListAllRoutes(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	identities, err := a.Store.ListIdentities(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Partial(w, http.StatusUnprocessableEntity, "dependency_form",
		a.newDependencyForm(r, consumerID, messages, spec, endpoints, routes, identities))
}
