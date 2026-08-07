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

// Reservations: declaring that a span of addresses is spoken for.

type rangeFormData struct {
	Base
	Errors map[string]string
}

func (a *App) newRangeForm(r *http.Request, errs map[string]string) rangeFormData {
	return rangeFormData{Base: a.base(r, "Prefixes", "prefixes"), Errors: orEmpty(errs)}
}

// IPRangeCreate declares a reservation.
func (a *App) IPRangeCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	rng, err := domain.NewIPRange(store.NewID(),
		formValue(r, "start_text"), formValue(r, "end_text"))
	if err == nil {
		rng.Role = optionalString(r, "role")
		rng.Description = optionalString(r, "description")
		err = a.Store.CreateIPRange(r.Context(), actor(r), rng)
	}
	if err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			if isConflict(err) {
				messages = map[string]string{
					"start_text": "a reservation with exactly those bounds already exists",
				}
			} else {
				a.handleStoreError(w, r, err)
				return
			}
		}
		a.Render.Partial(w, http.StatusUnprocessableEntity, "range_form",
			a.newRangeForm(r, messages))
		return
	}

	a.setFlash(r, "success", "Reserved "+rng.StartText+" – "+rng.EndText+".")
	render.Redirect(w, r, "/prefixes")
}

// IPRangeRetire withdraws a reservation, returning its space to the allocator.
func (a *App) IPRangeRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetireIPRange(r.Context(), actor(r), r.PathValue("id")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Reservation withdrawn; the addresses are allocatable again.")
	render.Redirect(w, r, "/prefixes")
}
