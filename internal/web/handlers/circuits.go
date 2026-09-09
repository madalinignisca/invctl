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
	// Edit is set only when a correction was refused; every editState method
	// is nil-safe, so the template calls through it unguarded.
	Edit   *editState
	Errors map[string]string
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
	a.renderCircuits(w, r, http.StatusOK, nil, nil)
}

func (a *App) renderCircuits(w http.ResponseWriter, r *http.Request, status int,
	errs map[string]string, edit *editState) {
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
	base := a.base(r, "Circuits", "circuits")
	// A refused correction reopens the row it was refused on, whatever the
	// query string said -- renderPrefixes and renderVLANs do the same.
	if edit != nil {
		base.EditRow = edit.ID
	}
	a.Render.Page(w, status, "circuit_list", circuitListPage{
		Base:      base,
		Circuits:  circuits,
		Providers: providers,
		Edit:      edit,
		Errors:    orEmpty(errs),
	})
}

// CircuitDetail shows one circuit, both its ends and what it costs.
func (a *App) CircuitDetail(w http.ResponseWriter, r *http.Request) {
	a.renderCircuitDetail(w, r, r.PathValue("id"), http.StatusOK, nil, nil)
}

func (a *App) renderCircuitDetail(w http.ResponseWriter, r *http.Request, id string,
	status int, errs map[string]string, edit *editState) {

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
	// GATED BEHIND CanSeeCosts, like the same query on the asset page (see
	// assets.go): price_movement_panel is entirely money, so a viewer without
	// the grant gets neither the query nor the render.
	base := a.base(r, circuit.CID, "circuits")
	// A refused correction reopens the form it was refused on, whatever the
	// query string said -- renderPrefixes and the asset page do the same.
	if edit != nil {
		base.EditRow = edit.ID
	}
	var movement []store.PriceSeries
	if base.CanSeeCosts {
		movement, err = a.Store.PriceMovementForCircuit(r.Context(), id)
		if err != nil {
			slog.Error("resolving price movement", "error", err, "circuit", id)
		}
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
	// The cost panel offers a supplier picker, so this page owes it the
	// provider list. Missing since J6 (732c6b0) added "Providers" .Providers
	// to circuit_detail.html without adding the field here: html/template
	// fails execution on an absent struct field, so every GET /circuits/{id}
	// returned 500. Nothing caught it because no test rendered this page --
	// handler tests call handlers directly and never execute the template
	// against the real page struct.
	providers, err := a.Store.ListProviders(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
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
		Providers    []store.ProviderRow
		Errors       map[string]string
		// Editing is true when the correction form is open. Gated on
		// CanWriteEntity for THIS circuit rather than on .CanWrite: a circuit
		// is project-linked, so its owner may correct it while .CanWrite alone
		// is still false for every project owner -- the same reasoning as the
		// asset page's canWriteAsset (assets.go).
		Editing bool
		Edit    *editState
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
		Providers:    providers,
		Errors:       orEmpty(errs),
		Editing:      base.CanWriteEntity("circuit", circuit.ID) && base.EditRow == circuit.ID,
		Edit:         edit,
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
		a.renderCircuits(w, r, http.StatusUnprocessableEntity, messages, nil)
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

// CircuitUpdate corrects a circuit's identifier, supplier or commitment.
//
// A CIRCUIT IS A FAILURE TARGET, not a record. Its terminations are what make
// "simulate cutting this" answer anything, and its impact history is what a
// person reads during the incident. Retire-and-redeclare -- the only fix before
// this route -- throws both away to correct a typed digit: the new circuit has
// no ends until somebody lands them again, and the change log for the old one
// stops at a cessation that never happened.
//
// THE CID IS THE FIELD WITH TEETH. It is the string somebody reads down the
// phone to the supplier at three in the morning, and it is what the search
// index titles this circuit by -- so a wrong one is not merely untidy, it is
// unfindable. The store refuses a duplicate within a provider, so a correction
// onto an existing identifier is caught rather than accepted.
//
// contract_end is offered because it drives the expiry report: a circuit
// auto-renewing at a rate nobody checked is the failure that report exists to
// prevent, and a date entered wrong silences it.
func (a *App) CircuitUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	existing, err := a.Store.GetCircuit(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	nums := optionalNumbers(r)
	updated := *existing
	updated.CID = formValue(r, "cid")
	// submittedString's rule, spelled out because provider_id is a required
	// column: an empty picker means "not rendered", never "this circuit has no
	// supplier". A circuit with no provider is not a state the domain has.
	if v := formValue(r, "provider_id"); v != "" {
		updated.ProviderID = v
	}
	updated.ServiceType = optionalString(r, "service_type")
	updated.CommitMbps = nums.opt("commit_mbps")
	updated.InstallDate = optionalString(r, "install_date")
	updated.ContractEnd = optionalString(r, "contract_end")
	updated.Description = optionalString(r, "description")
	updated.RowVersion = submittedVersion(r, updated.RowVersion)

	if msgs := nums.messages(); msgs != nil {
		err = domain.NewValidationFrom(msgs)
	} else {
		err = a.Store.UpdateCircuit(r.Context(), a.permit(r), &updated)
	}
	if err != nil {
		messages, ok := refusalMessages(err, map[string]string{
			"cid": "that provider already has a circuit with that identifier",
		})
		if !ok {
			a.handleStoreError(w, r, err)
			return
		}
		a.renderCircuitDetail(w, r, id, refusalStatus(err), messages,
			rejected(r, id, messages, "cid", "provider_id", "service_type",
				"commit_mbps", "install_date", "contract_end", "description"))
		return
	}
	a.setFlash(r, "success", "Circuit "+updated.CID+" updated.")
	render.Redirect(w, r, "/circuits/"+id)
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
		a.renderCircuits(w, r, http.StatusUnprocessableEntity, messages, nil)
		return
	}
	a.setFlash(r, "success", "Provider "+p.Name+" recorded.")
	render.Redirect(w, r, "/circuits")
}

// ProviderUpdate corrects a carrier.
//
// THE ONLY ENTITY WITH A LIVE CREATE ROUTE AND NO REPAIR AT ALL, until now.
// `name` is what somebody reads down the phone during an outage, `account_ref`
// is what they quote when they get through, and `portal_url` is where they go
// first -- the three-in-the-morning fields, none of which could be fixed. Found
// by the write-surface census, which keys on creation exactly because the
// reachability guard cannot see an entity that has no Update method to be
// unreachable.
//
// Copy-then-overwrite, PrefixUpdate's shape: UpdateProvider writes every
// column, so building a fresh Provider from the form would blank whatever the
// form does not carry.
func (a *App) ProviderUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	existing, err := a.Store.GetProvider(r.Context(), r.PathValue("id"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	updated := existing.Provider
	updated.Name = formValue(r, "name")
	updated.AccountRef = optionalString(r, "account_ref")
	updated.PortalURL = optionalString(r, "portal_url")
	updated.Description = optionalString(r, "description")
	updated.RowVersion = submittedVersion(r, updated.RowVersion)

	if err := a.Store.UpdateProvider(r.Context(), a.permit(r), &updated); err != nil {
		messages, ok := refusalMessages(err, map[string]string{
			"name": "a provider with that name already exists",
		})
		if !ok {
			a.handleStoreError(w, r, err)
			return
		}
		a.renderCircuits(w, r, refusalStatus(err), messages,
			rejected(r, existing.ID, messages, "name", "account_ref", "portal_url", "description"))
		return
	}
	a.setFlash(r, "success", "Provider "+updated.Name+" updated.")
	render.Redirect(w, r, "/circuits")
}

// ProviderRetire withdraws a carrier, refusing while circuits still hang off it.
//
// The refusal is the useful part, and it is the store's: a withdrawn supplier
// under a live circuit is not a tidy-up, because circuit.provider_id is NOT
// NULL and every circuit page would go on rendering a carrier nobody deals with
// any more. Move the circuits first -- the same order the real-world act
// happens in.
func (a *App) ProviderRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetireProvider(r.Context(), a.permit(r), r.PathValue("id")); err != nil {
		if isConflict(err) {
			a.setFlash(r, "error", "That provider still carries live circuits. "+
				"Move them to another supplier first — withdrawing it would leave "+
				"them pointing at a carrier the estate says is gone.")
			render.Redirect(w, r, "/circuits")
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Provider withdrawn.")
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
		a.renderCircuitDetail(w, r, id, http.StatusUnprocessableEntity, messages, nil)
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
