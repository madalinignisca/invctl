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
		err = a.Store.CreateIPRange(r.Context(), a.permit(r), rng)
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

// IPRangeUpdate corrects a reservation's bounds or its purpose.
//
// A RESERVATION IS A CLAIM ON SPACE THE ALLOCATOR WILL NEVER OFFER, so a
// mistyped bound is not a cosmetic error: too wide and addresses nobody holds
// are withheld for ever, too narrow and the allocator hands out an address a
// DHCP pool or a load balancer is already using. Until this route existed the
// only fix was to withdraw the reservation and declare another -- which
// returns the whole span to the allocator in the gap between the two, exactly
// where a fresh allocation can land in the middle of the range being repaired.
//
// SetBounds rather than a field assignment, for the reason PrefixUpdate uses
// SetCIDR: the text is the label and the bytes are what every containment
// question is answered from, so all four columns are rewritten together or the
// reservation covers a span nobody can see.
//
// Built from the stored row so that vrf_id survives -- the form does not carry
// it, and UpdateIPRange writes every column.
func (a *App) IPRangeUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	existing, err := a.Store.GetIPRange(r.Context(), r.PathValue("id"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	updated := *existing
	updated.Role = optionalString(r, "role")
	updated.Description = optionalString(r, "description")
	updated.RowVersion = submittedVersion(r, updated.RowVersion)
	err = updated.SetBounds(formValue(r, "start_text"), formValue(r, "end_text"))
	if err == nil {
		err = a.Store.UpdateIPRange(r.Context(), a.permit(r), &updated)
	}
	if err != nil {
		messages, ok := refusalMessages(err, map[string]string{
			"start_text": "a reservation with exactly those bounds already exists",
		})
		if !ok {
			a.handleStoreError(w, r, err)
			return
		}
		a.renderPrefixes(w, r, refusalStatus(err),
			rejected(r, existing.ID, messages, "start_text", "end_text", "role", "description"))
		return
	}
	a.setFlash(r, "success", "Reservation updated.")
	render.Redirect(w, r, "/prefixes")
}

// IPRangeRetire withdraws a reservation, returning its space to the allocator.
func (a *App) IPRangeRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetireIPRange(r.Context(), a.permit(r), r.PathValue("id")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Reservation withdrawn; the addresses are allocatable again.")
	render.Redirect(w, r, "/prefixes")
}
