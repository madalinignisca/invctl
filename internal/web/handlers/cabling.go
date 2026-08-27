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
func (a *App) PassThroughCreate(w http.ResponseWriter, r *http.Request) {
	assetID := r.PathValue("id")
	position, numeric := intValue(r, "position", 1)
	if !numeric {
		a.setFlash(r, "error", "The position has to be a whole number.")
		render.Redirect(w, r, "/assets/"+assetID)
		return
	}
	p, err := domain.NewPassThrough(store.NewID(), domain.PassThroughSpec{
		FrontInterfaceID: formValue(r, "front_interface_id"),
		RearInterfaceID:  formValue(r, "rear_interface_id"),
		Position:         position,
	}, a.Store.Now())
	if err == nil {
		err = a.Store.CreatePassThrough(r.Context(), permit(r), p)
	}
	if err != nil {
		if messages, ok := validationErrors(err); ok {
			a.setFlash(r, "error", "That patch was not accepted: "+joinMessages(messages))
			render.Redirect(w, r, "/assets/"+assetID)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/assets/"+assetID)
}

// PassThroughRetire unpatches a port.
func (a *App) PassThroughRetire(w http.ResponseWriter, r *http.Request) {
	assetID := r.PathValue("id")
	if err := a.Store.RetirePassThrough(r.Context(), permit(r), r.PathValue("patchID")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Unpatched.")
	render.Redirect(w, r, "/assets/"+assetID)
}
