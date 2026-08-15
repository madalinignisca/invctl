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

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web/render"
)

type circuitListPage struct {
	Base
	Circuits  []store.CircuitRow
	Providers []store.ProviderRow
	Errors    map[string]string
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
			err = a.Store.CreateCircuit(r.Context(), actor(r), circuit)
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

// CircuitRetire ceases a circuit.
func (a *App) CircuitRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetireCircuit(r.Context(), actor(r), r.PathValue("id")); err != nil {
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
		err = a.Store.CreateProvider(r.Context(), actor(r), p)
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
		err = a.Store.CreateCircuitTermination(r.Context(), actor(r), term)
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
	if err := a.Store.RetireCircuitTermination(r.Context(), actor(r), r.PathValue("termID")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Circuit end removed.")
	render.Redirect(w, r, "/circuits/"+id)
}
