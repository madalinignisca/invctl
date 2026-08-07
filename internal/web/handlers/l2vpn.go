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

type l2vpnListPage struct {
	Base
	Overlays []store.L2VPNRow
	Kinds    []string
	Errors   map[string]string
}

// L2VPNList renders every overlay.
func (a *App) L2VPNList(w http.ResponseWriter, r *http.Request) {
	a.renderL2VPNs(w, r, http.StatusOK, nil)
}

func (a *App) renderL2VPNs(w http.ResponseWriter, r *http.Request, status int, errs map[string]string) {
	overlays, err := a.Store.ListL2VPNs(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Page(w, status, "l2vpn_list", l2vpnListPage{
		Base:     a.base(r, "Overlays", "l2vpn"),
		Overlays: overlays,
		Kinds:    domain.L2VPNKinds,
		Errors:   orEmpty(errs),
	})
}

// L2VPNDetail shows one overlay and what terminates into it.
func (a *App) L2VPNDetail(w http.ResponseWriter, r *http.Request) {
	a.renderL2VPNDetail(w, r, r.PathValue("id"), http.StatusOK, nil)
}

// renderL2VPNDetail draws the page, optionally with a refused attachment shown
// against the form.
//
// A REFUSAL RE-RENDERS AT 422 rather than redirecting with a flash. The rule is
// the project's ("validation failure returns 422 with the form re-rendered"),
// and the reason is that a redirect throws away what the operator chose -- they
// come back to an empty form and a sentence, and have to reconstruct their own
// input to act on it.
func (a *App) renderL2VPNDetail(w http.ResponseWriter, r *http.Request, id string,
	status int, errs map[string]string) {

	vpn, err := a.Store.GetL2VPN(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	terms, err := a.Store.ListL2VPNTerminations(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	vlans, err := a.Store.ListVLANs(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	ports, err := a.Store.ListPortOptions(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Page(w, status, "l2vpn_detail", struct {
		Base
		VPN          *domain.L2VPN
		Terminations []store.L2VPNTerminationRow
		VLANs        []store.VLANRow
		Ports        []store.InterfaceOption
		Reach        domain.L2VPNReach
		Note         string
		Errors       map[string]string
	}{
		Base:         a.base(r, vpn.Name, "l2vpn"),
		VPN:          vpn,
		Terminations: terms,
		VLANs:        vlans,
		Ports:        ports,
		Reach:        domain.Reach(len(terms)),
		Note:         domain.ReachDescription(domain.Reach(len(terms))),
		Errors:       orEmpty(errs),
	})
}

// L2VPNCreate declares an overlay.
func (a *App) L2VPNCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	vpn, err := domain.NewL2VPN(store.NewID(), formValue(r, "name"), formValue(r, "kind"))
	if err == nil {
		if n := optionalNumbers(r).opt("identifier"); n != nil {
			id := int64(*n)
			vpn.Identifier = &id
		}
		vpn.Description = optionalString(r, "description")
		err = vpn.Validate()
		if err == nil {
			err = a.Store.CreateL2VPN(r.Context(), actor(r), vpn)
		}
	}
	if err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			if isConflict(err) {
				messages = map[string]string{"name": "an overlay with that name already exists"}
			} else {
				a.handleStoreError(w, r, err)
				return
			}
		}
		a.renderL2VPNs(w, r, http.StatusUnprocessableEntity, messages)
		return
	}
	a.setFlash(r, "success", "Overlay "+vpn.Name+" declared.")
	render.Redirect(w, r, "/overlays")
}

// L2VPNAttach terminates a VLAN or a port into the overlay.
func (a *App) L2VPNAttach(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	// The form offers one control per end and the operator fills exactly one;
	// the constructor is what refuses both or neither, with a sentence.
	term, err := domain.NewL2VPNTermination(store.NewID(), id,
		optionalString(r, "vlan_id"), optionalString(r, "interface_id"))
	if err == nil {
		err = a.Store.CreateL2VPNTermination(r.Context(), actor(r), term)
	}
	if err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			if !isConflict(err) {
				a.handleStoreError(w, r, err)
				return
			}
			messages = map[string]string{
				"vlan_id": "that end already terminates into this overlay",
			}
		}
		a.renderL2VPNDetail(w, r, id, http.StatusUnprocessableEntity, messages)
		return
	}
	a.setFlash(r, "success", "Attached to the overlay.")
	render.Redirect(w, r, "/overlays/"+id)
}

// L2VPNDetach removes one attachment.
func (a *App) L2VPNDetach(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.Store.RetireL2VPNTermination(r.Context(), actor(r), r.PathValue("termID")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Detached from the overlay.")
	render.Redirect(w, r, "/overlays/"+id)
}

// L2VPNRetire withdraws an overlay.
func (a *App) L2VPNRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetireL2VPN(r.Context(), actor(r), r.PathValue("id")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Overlay withdrawn.")
	render.Redirect(w, r, "/overlays")
}
