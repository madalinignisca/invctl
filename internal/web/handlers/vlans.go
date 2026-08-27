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

type vlanListPage struct {
	Base
	VLANs    []store.VLANRow
	Groups   []store.VLANGroupRow
	FormData vlanFormData
}

type vlanFormData struct {
	Base
	Errors       map[string]string
	Groups       []store.VLANGroupRow
	Environments []domain.Environment
}

func (a *App) newVLANForm(r *http.Request, errs map[string]string,
	groups []store.VLANGroupRow, envs []domain.Environment) vlanFormData {
	return vlanFormData{
		Base: a.base(r, "VLANs", "vlans"), Errors: orEmpty(errs),
		Groups: groups, Environments: envs,
	}
}

// VLANList renders every broadcast domain, with what is on it.
func (a *App) VLANList(w http.ResponseWriter, r *http.Request) {
	a.renderVLANs(w, r, http.StatusOK, nil)
}

func (a *App) renderVLANs(w http.ResponseWriter, r *http.Request, status int, errs map[string]string) {
	vlans, err := a.Store.ListVLANs(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	groups, err := a.Store.ListVLANGroups(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	envs, err := a.Store.ListEnvironments(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Page(w, status, "vlan_list", vlanListPage{
		Base:     a.base(r, "VLANs", "vlans"),
		VLANs:    vlans,
		Groups:   groups,
		FormData: a.newVLANForm(r, errs, groups, envs),
	})
}

// VLANDetail shows one broadcast domain and everything in it.
//
// THE PORTS ARE THE POINT. A VLAN with prefixes and no ports is a record; the
// port list is what makes it a place things can reach each other, and it is a
// fact no cable trace produces -- two access ports in VLAN 30 are adjacent
// whether or not anybody drew a cable between them.
func (a *App) VLANDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	vlan, err := a.Store.GetVLAN(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	ports, err := a.Store.ListVLANPorts(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	prefixes, err := a.Store.ListPrefixTree(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	var on []store.PrefixTreeRow
	for _, p := range prefixes {
		if p.VLANRefID != nil && *p.VLANRefID == id {
			on = append(on, p)
		}
	}

	options, err := a.Store.ListPortOptions(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Page(w, http.StatusOK, "vlan_detail", struct {
		Base
		VLAN     *domain.VLAN
		Ports    []store.VLANPort
		Prefixes []store.PrefixTreeRow
		Options  []store.InterfaceOption
		Modes    []string
	}{
		Base:     a.base(r, "VLAN "+vlan.Name, "vlans"),
		VLAN:     vlan,
		Ports:    ports,
		Prefixes: on,
		Options:  options,
		Modes:    domain.VLANModes,
	})
}

// VLANCreate declares a broadcast domain.
func (a *App) VLANCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	nums := optionalNumbers(r)
	vid := nums.opt("vid")
	if vid == nil {
		a.renderVLANs(w, r, http.StatusUnprocessableEntity,
			map[string]string{"vid": "a VLAN needs a tag between 1 and 4094"})
		return
	}
	vlan, err := domain.NewVLAN(store.NewID(), *vid, formValue(r, "name"),
		optionalString(r, "group_id"))
	if err == nil {
		vlan.Role = optionalString(r, "role")
		vlan.EnvironmentID = optionalString(r, "environment_id")
		vlan.Description = optionalString(r, "description")
		err = a.Store.CreateVLAN(r.Context(), permit(r), vlan)
	}
	if err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			if isConflict(err) {
				messages = map[string]string{
					"vid": "that VLAN ID is already declared in this group",
				}
			} else {
				a.handleStoreError(w, r, err)
				return
			}
		}
		a.renderVLANs(w, r, http.StatusUnprocessableEntity, messages)
		return
	}

	a.setFlash(r, "success", "VLAN "+vlan.Name+" declared.")
	render.Redirect(w, r, "/vlans")
}

// VLANPortAdd puts a port in this VLAN.
func (a *App) VLANPortAdd(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	vlanID := r.PathValue("id")
	mode := formValue(r, "mode")
	if mode != domain.VLANModeTagged && mode != domain.VLANModeUntagged {
		mode = domain.VLANModeUntagged
	}
	err := a.Store.AddPortToVLAN(r.Context(), permit(r), vlanID, formValue(r, "interface_id"), mode)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Port added to the VLAN.")
	render.Redirect(w, r, "/vlans/"+vlanID)
}

// VLANPortRemove takes a port out of this VLAN.
func (a *App) VLANPortRemove(w http.ResponseWriter, r *http.Request) {
	vlanID := r.PathValue("id")
	err := a.Store.RemovePortFromVLAN(r.Context(), permit(r), vlanID, r.PathValue("ifaceID"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Port removed from the VLAN.")
	render.Redirect(w, r, "/vlans/"+vlanID)
}

// VLANRetire withdraws a broadcast domain, refusing while anything is on it.
func (a *App) VLANRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetireVLAN(r.Context(), permit(r), r.PathValue("id")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "VLAN withdrawn.")
	render.Redirect(w, r, "/vlans")
}
