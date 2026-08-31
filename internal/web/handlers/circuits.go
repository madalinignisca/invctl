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

	"github.com/justinas/nosurf"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web/render"
)

// The writes in this file use a.permit(r) -- the caller's OWN permit from
// the request-scoped gate (WP-G1 Task 12) -- and not
// domain.AdministratorPermit(actor(r)).
//
// THEY USED TO, AND THAT WAS A LATENT PRIVILEGE ESCALATION. Task 7 changed
// internal/store/circuits.go's write transactions to take a domain.Permit
// rather than a domain.Actor, so these call sites had to supply one before
// the gate existed; the shim minted an administrator permit and justified it
// with "every route in this file already sits behind RequireWrite, so the
// caller is already an Administrator". That justification expires at Task
// 13: RequireWrite gates on auth.CanWrite, and Task 13 makes CanWrite true
// for a project owner. A shim minting an ADMINISTRATOR permit would then
// have handed a project owner authority over every circuit and provider in
// the estate -- the permit covers everything, so tx.log has nothing left to
// refuse.
//
// The rule this file is an example of: a handler must never mint a permit
// wider than its caller's. "This route is admin-only" is a fact about a
// routing table in another package, and Task 13 changes it with one line.
// Enforced by TestNoHandlerMintsAPermitWiderThanItsCaller.
type circuitListPage struct {
	Base
	Circuits  []store.CircuitRow
	Providers []store.ProviderRow
	Errors    map[string]string
}

// ColumnOptions lists the circuits table's configurable columns, in header
// order. Circuit ID is the identity column and is deliberately absent. The
// providers table below it is out of scope -- see the plan's Task 4.
func (circuitListPage) ColumnOptions() []ColumnOption {
	return []ColumnOption{
		{Key: "provider", Label: "Provider"},
		{Key: "service", Label: "Service"},
		{Key: "commit", Label: "Commit"},
		{Key: "contract_ends", Label: "Contract ends"},
		{Key: "ends_recorded", Label: "Ends recorded"},
	}
}

// CircuitList renders every contracted connection.
func (a *App) CircuitList(w http.ResponseWriter, r *http.Request) {
	a.renderCircuits(w, r, http.StatusOK, nil)
}

func (a *App) renderCircuits(w http.ResponseWriter, r *http.Request, status int, errs map[string]string) {
	circuits, err := a.Store.ListCircuits(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	providers, err := a.Store.ListProviders(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if render.WantsCSV(r) {
		render.CSV(w, r, store.ExportCircuits(circuits), a.Store.Now())
		return
	}
	a.Render.Page(w, status, "circuit_list", circuitListPage{
		Base:      a.base(r, "Circuits", "circuits"),
		Circuits:  circuits,
		Providers: providers,
		Errors:    orEmpty(errs),
	})
}

// CircuitDetail shows one circuit, both its ends and what it costs.
func (a *App) CircuitDetail(w http.ResponseWriter, r *http.Request) {
	a.renderCircuitDetail(w, r, r.PathValue("id"), http.StatusOK, nil)
}

func (a *App) renderCircuitDetail(w http.ResponseWriter, r *http.Request, id string,
	status int, errs map[string]string) {

	circuit, err := a.Store.GetCircuit(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	terms, err := a.Store.ListCircuitTerminations(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	movement, err := a.Store.PriceMovementForCircuit(r.Context(), id)
	if err != nil {
		slog.Error("resolving price movement", "error", err, "circuit", id)
	}
	costs, err := a.Store.ListCircuitCosts(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	kinds, err := a.Store.CostKinds(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	ports, err := a.Store.ListPortOptions(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	sites, err := a.Store.ListAssets(r.Context(), store.AssetFilter{Kind: domain.KindSite})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	base := a.base(r, circuit.CID, "circuits")
	a.Render.Page(w, status, "circuit_detail", struct {
		Base
		Circuit      *domain.Circuit
		Terminations []store.CircuitTerminationRow
		Costs        []store.CostRow
		Movement     []store.PriceSeries
		CostTotals   domain.CostTotals
		CostKinds    []store.VocabularyTerm
		Periods      []string
		Ports        []store.InterfaceOption
		Sites        []store.AssetRow
		Sides        []string
		Errors       map[string]string
	}{
		Base:         base,
		Circuit:      circuit,
		Terminations: terms,
		Costs:        costs,
		Movement:     movement,
		CostTotals:   store.TotalCosts(costs, domain.FormatDate(a.Store.Now())),
		CostKinds:    kinds,
		Periods:      domain.CostPeriods,
		Ports:        ports,
		Sites:        sites,
		Sides:        domain.CircuitSides,
		Errors:       orEmpty(errs),
	})
}

// CircuitCreate declares a contracted connection.
func (a *App) CircuitCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	circuit, err := domain.NewCircuit(store.NewID(), formValue(r, "cid"), formValue(r, "provider_id"))
	if err == nil {
		circuit.ServiceType = optionalString(r, "service_type")
		circuit.CommitMbps = optionalNumbers(r).opt("commit_mbps")
		circuit.InstallDate = optionalString(r, "install_date")
		circuit.ContractEnd = optionalString(r, "contract_end")
		circuit.Description = optionalString(r, "description")
		err = circuit.Validate()
		if err == nil {
			err = a.Store.CreateCircuit(r.Context(), a.permit(r), circuit)
		}
	}
	if err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			if isConflict(err) {
				messages = map[string]string{"cid": "that provider already has a circuit with that identifier"}
			} else {
				a.handleStoreError(w, r, err)
				return
			}
		}
		a.renderCircuits(w, r, http.StatusUnprocessableEntity, messages)
		return
	}
	a.setFlash(r, "success", "Circuit "+circuit.CID+" recorded.")
	render.Redirect(w, r, "/circuits")
}

// CircuitCreateInProject declares a NEW circuit and links it to the project
// named in the URL, in one transaction (WP-G1 Task 14, docs/rbac-design.md
// §4). See AssetCreateInProject's comment (assets.go) -- the same shape, the
// same reason: the project is a path parameter rather than a form field, so
// the circuit is new by construction and store.NewID() below is the only
// place an id is ever minted.
//
// Unlike CircuitCreate above, this route mints a real, project-owner-aware
// permit (a.permit(r)) rather than a.permit(r): a
// project owner reaching this handler is the whole point, and the permit's
// scope -- not this handler -- is what decides whether the project in the
// URL is theirs.
func (a *App) CircuitCreateInProject(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	projectID := r.PathValue("projectID")
	cid := formValue(r, "cid")
	providerID := formValue(r, "provider_id")

	// store.NewID() is the id, unconditionally. Nothing on this form is ever
	// consulted for one -- see TestNoCreateHandlerReadsAnIdFromTheRequest.
	circuit, err := domain.NewCircuit(store.NewID(), cid, providerID)
	if err == nil {
		circuit.ServiceType = optionalString(r, "service_type")
		circuit.CommitMbps = optionalNumbers(r).opt("commit_mbps")
		circuit.InstallDate = optionalString(r, "install_date")
		circuit.ContractEnd = optionalString(r, "contract_end")
		circuit.Description = optionalString(r, "description")
		err = circuit.Validate()
		if err == nil {
			err = a.Store.CreateCircuitInProject(r.Context(), a.permit(r), projectID, circuit)
		}
	}
	if err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			if isConflict(err) {
				messages = map[string]string{"cid": "that provider already has a circuit with that identifier"}
			} else {
				a.handleStoreError(w, r, err)
				return
			}
		}
		a.renderCircuitCreateInProjectForm(w, r, projectID, http.StatusUnprocessableEntity, messages, cid, providerID)
		return
	}
	a.setFlash(r, "success", "Circuit "+circuit.CID+" recorded and linked to this project.")
	render.Redirect(w, r, "/circuits/"+circuit.ID)
}

// renderCircuitCreateInProjectForm re-renders the create-in-project form
// standalone, per this codebase's rule that a partial must work without its
// parent page having rendered first.
func (a *App) renderCircuitCreateInProjectForm(w http.ResponseWriter, r *http.Request, projectID string,
	status int, errs map[string]string, cid, providerID string) {

	providers, err := a.Store.ListProviders(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Partial(w, status, "project_create_form", projectCreateForm{
		Mode: "circuit", ProjectID: projectID, CSRF: nosurf.Token(r),
		Errors: orEmpty(errs), Providers: providers, CID: cid, ProviderID: providerID,
	})
}

// CircuitRetire ceases a circuit.
func (a *App) CircuitRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetireCircuit(r.Context(), a.permit(r), r.PathValue("id")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Circuit ceased.")
	render.Redirect(w, r, "/circuits")
}

// ProviderCreate declares a carrier.
func (a *App) ProviderCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	p, err := domain.NewProvider(store.NewID(), formValue(r, "name"))
	if err == nil {
		p.AccountRef = optionalString(r, "account_ref")
		p.PortalURL = optionalString(r, "portal_url")
		err = a.Store.CreateProvider(r.Context(), a.permit(r), p)
	}
	if err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			if isConflict(err) {
				messages = map[string]string{"name": "a provider with that name already exists"}
			} else {
				a.handleStoreError(w, r, err)
				return
			}
		}
		a.renderCircuits(w, r, http.StatusUnprocessableEntity, messages)
		return
	}
	a.setFlash(r, "success", "Provider "+p.Name+" recorded.")
	render.Redirect(w, r, "/circuits")
}

// CircuitLand records one end of a circuit.
func (a *App) CircuitLand(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	term, err := domain.NewCircuitTermination(store.NewID(), id, formValue(r, "side"),
		optionalString(r, "asset_id"), optionalString(r, "interface_id"))
	if err == nil {
		err = a.Store.CreateCircuitTermination(r.Context(), a.permit(r), term)
	}
	if err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			if !isConflict(err) {
				a.handleStoreError(w, r, err)
				return
			}
			messages = map[string]string{"side": "that end of this circuit is already recorded"}
		}
		a.renderCircuitDetail(w, r, id, http.StatusUnprocessableEntity, messages)
		return
	}
	a.setFlash(r, "success", "Circuit end recorded.")
	render.Redirect(w, r, "/circuits/"+id)
}

// CircuitLift removes one end.
func (a *App) CircuitLift(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.Store.RetireCircuitTermination(r.Context(), a.permit(r), r.PathValue("termID")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Circuit end removed.")
	render.Redirect(w, r, "/circuits/"+id)
}
