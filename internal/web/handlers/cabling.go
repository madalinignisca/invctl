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

type tracePage struct {
	Base
	Trace *store.Trace
}

// TracePort follows a cable from one port to wherever it ends.
func (a *App) TracePort(w http.ResponseWriter, r *http.Request) {
	trace, err := a.Store.TracePath(r.Context(), r.PathValue("id"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.Render.Respond(w, r, http.StatusOK, "trace", "trace_path", tracePage{
		Base:  a.base(r, "Trace: "+trace.StartAsset+" "+trace.StartInterface, "assets"),
		Trace: trace,
	})
}

// PassThroughCreate records what a panel does between two of its own ports.
//
// A REFUSAL RE-RENDERS THE FORM AT 422, IT DOES NOT REDIRECT WITH A FLASH.
// Before the position field existed every create was position 1 and a
// collision was rare; now a breakout is recorded one strand at a time and a
// second strand landing on a position already taken is the ORDINARY mistake,
// so it gets the same shape every other create form on this page uses
// (interface_form, link_form): the field is named, the message is one a
// person can act on, and what they already picked survives the round trip
// via the re-fetched Interfaces list -- everything except the values HTML
// does not let a server repopulate without echoing them back explicitly,
// which matches this page's other sub-forms and is not a gap this task opens.
func (a *App) PassThroughCreate(w http.ResponseWriter, r *http.Request) {
	assetID := r.PathValue("id")
	position, numeric := intValue(r, "position", 1)
	var err error
	var p *domain.PassThrough
	if !numeric {
		err = domain.NewValidationFrom(map[string]string{
			"position": "the position has to be a whole number",
		})
	} else {
		p, err = domain.NewPassThrough(store.NewID(), domain.PassThroughSpec{
			FrontInterfaceID: formValue(r, "front_interface_id"),
			RearInterfaceID:  formValue(r, "rear_interface_id"),
			Position:         position,
		}, a.Store.Now())
		if err == nil {
			err = a.Store.CreatePassThrough(r.Context(), a.permit(r), p)
		}
	}
	if err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			a.handleStoreError(w, r, err)
			return
		}
		interfaces, listErr := a.Store.ListInterfaces(r.Context(), assetID)
		if listErr != nil {
			a.serverError(w, r, listErr)
			return
		}
		a.Render.Partial(w, http.StatusUnprocessableEntity, "pass_through_form",
			a.newPassThroughForm(r, assetID, messages, interfaces))
		return
	}
	a.setFlash(r, "success", "Patched through.")
	render.Redirect(w, r, "/assets/"+assetID)
}

// PassThroughRetire unpatches a port.
func (a *App) PassThroughRetire(w http.ResponseWriter, r *http.Request) {
	assetID := r.PathValue("id")
	if err := a.Store.RetirePassThrough(r.Context(), a.permit(r), r.PathValue("patchID")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Unpatched.")
	render.Redirect(w, r, "/assets/"+assetID)
}
