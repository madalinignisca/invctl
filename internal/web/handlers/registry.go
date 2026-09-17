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
	// Edit is set only when a correction was refused; see editState. The
	// template calls through it unguarded -- every editState method is
	// nil-safe -- the same shape renderVLANs and renderCircuits use.
	Edit *editState
}

// RegistryList renders the layer above prefixes: what was delegated, by whom.
func (a *App) RegistryList(w http.ResponseWriter, r *http.Request) {
	a.renderRegistry(w, r, http.StatusOK, nil, nil)
}

func (a *App) renderRegistry(w http.ResponseWriter, r *http.Request, status int,
	errs map[string]string, edit *editState) {
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
	base := a.base(r, "Allocations", "registry")
	// A refused correction reopens the row it was refused on, whatever the
	// query string said -- renderVLANs and renderCircuits do the same.
	if edit != nil {
		base.EditRow = edit.ID
	}
	a.Render.Page(w, status, "registry_list", registryPage{
		Base:       base,
		Aggregates: aggs,
		ASNs:       asns,
		RIRs:       rirs,
		Errors:     orEmpty(errs),
		Edit:       edit,
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
		a.renderRegistry(w, r, http.StatusUnprocessableEntity, messages, nil)
		return
	}
	a.setFlash(r, "success", "Allocation "+agg.CIDRText+" recorded.")
	render.Redirect(w, r, "/allocations")
}

// AggregateUpdate corrects a delegation's CIDR, registry link, allocation date
// or description (write-surface-gaps Task 5).
//
// Copy-then-overwrite, ProviderUpdate's shape: UpdateAggregate writes every
// column, so building a fresh Aggregate from the form would blank whatever the
// form does not carry.
func (a *App) AggregateUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	existing, err := a.Store.GetAggregate(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	updated := *existing
	if err := updated.SetCIDR(formValue(r, "cidr_text")); err != nil {
		messages, _ := validationErrors(err)
		a.renderRegistry(w, r, http.StatusUnprocessableEntity, messages,
			rejected(r, id, messages, "cidr_text", "rir_id", "allocated_on", "description"))
		return
	}
	updated.RIRID = optionalString(r, "rir_id")
	updated.AllocatedOn = optionalString(r, "allocated_on")
	updated.Description = optionalString(r, "description")
	updated.RowVersion = submittedVersion(r, updated.RowVersion)

	if err := a.Store.UpdateAggregate(r.Context(), a.permit(r), &updated); err != nil {
		messages, ok := refusalMessages(err, map[string]string{
			"cidr_text": "that allocation is already recorded",
		})
		if !ok {
			a.handleStoreError(w, r, err)
			return
		}
		a.renderRegistry(w, r, refusalStatus(err), messages,
			rejected(r, id, messages, "cidr_text", "rir_id", "allocated_on", "description"))
		return
	}
	a.setFlash(r, "success", "Allocation "+updated.CIDRText+" updated.")
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

// RIRUpdate corrects a registry's name, privacy flag or description
// (write-surface-gaps Task 5). No create route exists for RIR -- see
// routes.go's own comment -- so this and RIRRetire are the whole surface.
func (a *App) RIRUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	existing, err := a.Store.GetRIR(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	updated := *existing
	updated.Name = formValue(r, "name")
	updated.IsPrivate = formValue(r, "is_private") != ""
	updated.Description = optionalString(r, "description")
	updated.RowVersion = submittedVersion(r, updated.RowVersion)

	if err := a.Store.UpdateRIR(r.Context(), a.permit(r), &updated); err != nil {
		messages, ok := refusalMessages(err, map[string]string{
			"name": "a registry with that name already exists",
		})
		if !ok {
			a.handleStoreError(w, r, err)
			return
		}
		a.renderRegistry(w, r, refusalStatus(err), messages,
			rejected(r, id, messages, "name", "is_private", "description"))
		return
	}
	a.setFlash(r, "success", "Registry "+updated.Name+" updated.")
	render.Redirect(w, r, "/allocations")
}

// RIRRetire withdraws a registry, refusing while a live aggregate still names
// it.
func (a *App) RIRRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetireRIR(r.Context(), a.permit(r), r.PathValue("id")); err != nil {
		if isConflict(err) {
			a.setFlash(r, "error", "That registry still has live allocations recorded "+
				"against it. Withdraw them, or move them to another registry, first.")
			render.Redirect(w, r, "/allocations")
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Registry withdrawn.")
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
			map[string]string{"number": "an AS number is a whole number between 1 and 4294967294"}, nil)
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
		a.renderRegistry(w, r, http.StatusUnprocessableEntity, messages, nil)
		return
	}
	a.setFlash(r, "success", "AS number recorded.")
	render.Redirect(w, r, "/allocations")
}

// ASNUpdate corrects an AS number's declared attributes, including the number
// itself (write-surface-gaps Task 5) -- see UpdateASN's own comment for why
// the number is correctable, unlike a route's frontend or a pool's service.
func (a *App) ASNUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	existing, err := a.Store.GetASN(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	raw := formValue(r, "number")
	n, convErr := strconv.ParseInt(raw, 10, 64)
	if convErr != nil {
		messages := map[string]string{"number": "an AS number is a whole number between 1 and 4294967294"}
		a.renderRegistry(w, r, http.StatusUnprocessableEntity, messages,
			rejected(r, id, messages, "number", "name", "rir_id"))
		return
	}

	updated := *existing
	updated.Number = n
	updated.Name = optionalString(r, "name")
	updated.RIRID = optionalString(r, "rir_id")
	updated.RowVersion = submittedVersion(r, updated.RowVersion)

	if err := a.Store.UpdateASN(r.Context(), a.permit(r), &updated); err != nil {
		messages, ok := refusalMessages(err, map[string]string{
			"number": "that AS number is already recorded",
		})
		if !ok {
			a.handleStoreError(w, r, err)
			return
		}
		a.renderRegistry(w, r, refusalStatus(err), messages,
			rejected(r, id, messages, "number", "name", "rir_id"))
		return
	}
	a.setFlash(r, "success", "AS number updated.")
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
