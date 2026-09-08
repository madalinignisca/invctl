// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"net/http"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web/render"
)

// DependencyCreate declares an edge from a consumer service to a provider
// endpoint or route.
func (a *App) DependencyCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	consumerID := r.PathValue("id")

	nums := optionalNumbers(r)
	spec := domain.DependencySpec{
		ConsumerServiceID:  consumerID,
		ProviderEndpointID: optionalString(r, "provider_endpoint_id"),
		ProviderRouteID:    optionalString(r, "provider_route_id"),
		Nature:             formValue(r, "nature"),
		ToleranceSeconds:   nums.opt("tolerance_seconds"),
		FailureMode:        formValue(r, "failure_mode"),
		IdentityID:         optionalString(r, "identity_id"),
		AuthMethod:         optionalString(r, "auth_method"),
		FirewallRuleRef:    optionalString(r, "firewall_rule_ref"),
	}

	dep, err := domain.NewDependency(store.NewID(), spec, a.Store.Now())
	if err == nil {
		err = a.Store.CreateDependency(r.Context(), a.permit(r), dep, r.Form["data_class"])
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

// DependencyUpdate corrects an edge's nature, tolerance, failure mode or the
// data crossing it.
//
// NATURE IS A WRONG ANSWER AT THREE IN THE MORNING, not a wrong label. It is
// what the impact engine reasons over: an edge marked `hard` takes its consumer
// down with the provider, `soft` degrades it, `optional` does neither. An edge
// entered as hard when it is optional makes a routine restart read as an
// outage; entered as optional when it is hard, the engine says a service will
// survive something that will kill it. The only fix before this route was to
// retire the edge and redraw it -- which loses the verification somebody
// attested to and the history of what the edge used to say.
//
// WHAT THIS DOES NOT LET ANYBODY DO, and why each is deliberate:
//
//   - RE-POINT THE EDGE. The consumer and both provider columns are carried
//     from the stored row. Moving an edge is declaring a different one, and
//     the store guards it with TWO subject authorizations -- the stored
//     subjects prove the caller may touch this row, the submitted ones prove
//     they may touch what it is being moved to -- because re-pointing is a
//     seizure risk rather than a typo. A correction form is the wrong place
//     for it.
//   - CHANGE `source`. Flipping discovered to declared is an operator act with
//     its own audit rule (docs/AUDIT.md rule 7), and laundering an existing
//     discovered edge is the cheaper of the two attacks because the edge
//     already looks established. VerifyDependency is the route for a human
//     putting their name to an edge, and it derives verified_by from the
//     actor rather than accepting it.
//   - TOUCH lifecycle, verified_by or verified_at. The store carries all three
//     from the stored row whatever this handler sends, so they are simply
//     absent here rather than sent and ignored.
//
// The data classes are a set table, replaced wholesale and folded into the
// dependency's own audited value -- so the form must submit the complete set
// every time. An unticked box submits nothing, which is why editState.Multi
// exists and why the template reads through it rather than falling back to
// what is stored: a correction that unticked every class would otherwise
// silently restore them all.
func (a *App) DependencyUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	existing, err := a.Store.GetDependency(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	nums := optionalNumbers(r)
	updated := existing.Dependency
	updated.Nature = formValue(r, "nature")
	updated.ToleranceSeconds = nums.opt("tolerance_seconds")
	updated.FailureMode = formValue(r, "failure_mode")
	updated.IdentityID = optionalString(r, "identity_id")
	updated.AuthMethod = optionalString(r, "auth_method")
	updated.FirewallRuleRef = optionalString(r, "firewall_rule_ref")
	updated.RowVersion = submittedVersion(r, updated.RowVersion)

	// nil AND EMPTY MEAN DIFFERENT THINGS TO THE STORE, and r.Form hands back
	// the wrong one of the two. setDataClasses treats nil as "not managing
	// classes here, leave them alone" and a non-nil slice as a wholesale
	// replacement -- but a checkbox group with every box unticked submits no
	// key at all, so r.Form["data_class"] is nil for the one case that must
	// clear the set. Without this line, unticking the last class silently
	// restored it, and the operator saw the class they had just removed still
	// on the row. Found by a test that ticked one and then unticked it, which
	// is not a case anybody writes by accident.
	classes := r.Form["data_class"]
	if classes == nil {
		classes = []string{}
	}
	if msgs := nums.messages(); msgs != nil {
		err = domain.NewValidationFrom(msgs)
	} else {
		err = a.Store.UpdateDependency(r.Context(), a.permit(r), &updated, classes)
	}
	if err != nil {
		messages, ok := refusalMessages(err, nil)
		if !ok {
			a.handleStoreError(w, r, err)
			return
		}
		edit := rejected(r, id, messages, "nature", "tolerance_seconds",
			"failure_mode", "identity_id", "auth_method", "firewall_rule_ref")
		// The ticked classes travel separately: a set that came back empty
		// means "the operator unticked them all", not "the form did not carry
		// them", and Value's fall-back-to-stored rule would get that backwards.
		edit.Multi = map[string][]string{"data_class": classes}
		a.renderServiceDetail(w, r, refusalStatus(err), existing.ConsumerServiceID,
			endpointFormState{}, edit)
		return
	}
	a.setFlash(r, "success", "Dependency updated.")
	render.Redirect(w, r, "/services/"+existing.ConsumerServiceID)
}

// DependencyRetire withdraws an edge without losing the record that it existed.
func (a *App) DependencyRetire(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dep, err := a.Store.GetDependency(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	if err := a.Store.RetireDependency(r.Context(), a.permit(r), id); err != nil {
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
	if err := a.Store.VerifyDependency(r.Context(), a.permit(r), id); err != nil {
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
	// CanWrite uses the same canWriteDependency (forms.go) depRows calls, so
	// this standalone re-render and the table it swaps into can never
	// disagree on the two-ended rule (fix-b item 5). ShowActions is
	// unconditionally true, not the table's AnyWritable: this handler only
	// reaches here after a.Store.VerifyDependency has already accepted the
	// write, which itself requires the two-ended authorizeDependencySubjects
	// check to pass -- so CanWrite is always true on this path, and the row
	// being swapped in must carry its own actions cell regardless of what any
	// OTHER row in the (untouched) table looks like.
	a.Render.PartialWithOOB(w, http.StatusOK, "dependency_row", depRowData{
		Dep:         dep,
		CanWrite:    canWriteDependency(b.CanWriteEntity, dep.ConsumerServiceID, dep.ProviderSvc),
		ShowActions: true,
		CSRF:        b.CSRF,
		Direction:   directionOf(r),
		DataClasses: classes[id],
		SecretRef:   secretRefDisplay(dep.IdentitySecretRef, b.IsAdmin),
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
	classOptions, err := a.Store.DataClassVocabulary(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Partial(w, http.StatusUnprocessableEntity, "dependency_form",
		a.newDependencyForm(r, consumerID, messages, spec, endpoints, routes, identities, classOptions))
}
