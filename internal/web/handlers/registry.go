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
	"strconv"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web/render"
)

type registryPage struct {
	Base
	Aggregates []store.AggregateRow
	ASNs       []store.ASNRow
	RIRs       []domain.RIR
	Errors     map[string]string
}

// RegistryList renders the layer above prefixes: what was delegated, by whom.
func (a *App) RegistryList(w http.ResponseWriter, r *http.Request) {
	a.renderRegistry(w, r, http.StatusOK, nil)
}

func (a *App) renderRegistry(w http.ResponseWriter, r *http.Request, status int, errs map[string]string) {
	aggs, err := a.Store.ListAggregates(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	asns, err := a.Store.ListASNs(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	rirs, err := a.Store.ListRIRs(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Page(w, status, "registry_list", registryPage{
		Base:       a.base(r, "Allocations", "registry"),
		Aggregates: aggs,
		ASNs:       asns,
		RIRs:       rirs,
		Errors:     orEmpty(errs),
	})
}

// AggregateCreate declares a delegation.
func (a *App) AggregateCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	agg, err := domain.NewAggregate(store.NewID(), formValue(r, "cidr_text"))
	if err == nil {
		agg.RIRID = optionalString(r, "rir_id")
		agg.AllocatedOn = optionalString(r, "allocated_on")
		agg.Description = optionalString(r, "description")
		err = a.Store.CreateAggregate(r.Context(), a.permit(r), agg)
	}
	if err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			if isConflict(err) {
				messages = map[string]string{"cidr_text": "that allocation is already recorded"}
			} else {
				a.handleStoreError(w, r, err)
				return
			}
		}
		a.renderRegistry(w, r, http.StatusUnprocessableEntity, messages)
		return
	}
	a.setFlash(r, "success", "Allocation "+agg.CIDRText+" recorded.")
	render.Redirect(w, r, "/allocations")
}

// AggregateRetire withdraws a delegation.
func (a *App) AggregateRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetireAggregate(r.Context(), a.permit(r), r.PathValue("id")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Allocation withdrawn.")
	render.Redirect(w, r, "/allocations")
}

// ASNCreate declares an autonomous system number.
func (a *App) ASNCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	raw := formValue(r, "number")
	n, convErr := strconv.ParseInt(raw, 10, 64)
	if convErr != nil {
		a.renderRegistry(w, r, http.StatusUnprocessableEntity,
			map[string]string{"number": "an AS number is a whole number between 1 and 4294967294"})
		return
	}
	asn, err := domain.NewASN(store.NewID(), n)
	if err == nil {
		asn.Name = optionalString(r, "name")
		asn.RIRID = optionalString(r, "rir_id")
		err = a.Store.CreateASN(r.Context(), a.permit(r), asn)
	}
	if err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			if isConflict(err) {
				messages = map[string]string{"number": "that AS number is already recorded"}
			} else {
				a.handleStoreError(w, r, err)
				return
			}
		}
		a.renderRegistry(w, r, http.StatusUnprocessableEntity, messages)
		return
	}
	a.setFlash(r, "success", "AS number recorded.")
	render.Redirect(w, r, "/allocations")
}

// ASNRetire withdraws an autonomous system number.
func (a *App) ASNRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetireASN(r.Context(), a.permit(r), r.PathValue("id")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "AS number withdrawn.")
	render.Redirect(w, r, "/allocations")
}
