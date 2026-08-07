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

type fhrpListPage struct {
	Base
	Groups    []store.FHRPGroupRow
	Protocols []string
	Errors    map[string]string
}

// FHRPList renders every first-hop redundancy group.
func (a *App) FHRPList(w http.ResponseWriter, r *http.Request) {
	a.renderFHRP(w, r, http.StatusOK, nil)
}

func (a *App) renderFHRP(w http.ResponseWriter, r *http.Request, status int, errs map[string]string) {
	groups, err := a.Store.ListFHRPGroups(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Page(w, status, "fhrp_list", fhrpListPage{
		Base:      a.base(r, "Redundancy groups", "fhrp"),
		Groups:    groups,
		Protocols: domain.FHRPProtocols,
		Errors:    orEmpty(errs),
	})
}

// FHRPDetail shows one group and the routers in it.
func (a *App) FHRPDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	group, err := a.Store.GetFHRPGroup(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	members, err := a.Store.ListFHRPMembers(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	options, err := a.Store.ListPortOptions(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Page(w, http.StatusOK, "fhrp_detail", struct {
		Base
		Group      *domain.FHRPGroup
		Members    []store.FHRPMemberRow
		Options    []store.InterfaceOption
		Redundancy domain.FHRPRedundancy
		Note       string
		Label      string
	}{
		Base:       a.base(r, group.Name, "fhrp"),
		Group:      group,
		Members:    members,
		Options:    options,
		Redundancy: domain.Redundancy(len(members)),
		Note:       domain.RedundancyDescription(domain.Redundancy(len(members))),
		Label:      domain.FHRPProtocolLabel(group.Protocol),
	})
}

// FHRPCreate declares a group.
func (a *App) FHRPCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	nums := optionalNumbers(r)
	num := nums.opt("group_number")
	if num == nil {
		a.renderFHRP(w, r, http.StatusUnprocessableEntity,
			map[string]string{"group_number": "a group number is between 0 and 255"})
		return
	}
	group, err := domain.NewFHRPGroup(store.NewID(), formValue(r, "protocol"), *num, formValue(r, "name"))
	if err == nil {
		group.Description = optionalString(r, "description")
		err = a.Store.CreateFHRPGroup(r.Context(), actor(r), group)
	}
	if err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			if isConflict(err) {
				messages = map[string]string{"name": "a group with that name already exists"}
			} else {
				a.handleStoreError(w, r, err)
				return
			}
		}
		a.renderFHRP(w, r, http.StatusUnprocessableEntity, messages)
		return
	}
	a.setFlash(r, "success", "Redundancy group "+group.Name+" declared.")
	render.Redirect(w, r, "/redundancy")
}

// FHRPMemberAdd puts a router in the group.
func (a *App) FHRPMemberAdd(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	current, err := a.Store.ListFHRPMembers(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	members := make([]domain.FHRPMember, 0, len(current)+1)
	for _, m := range current {
		members = append(members, domain.FHRPMember{
			GroupID: id, InterfaceID: m.InterfaceID, Priority: m.Priority,
		})
	}
	nums := optionalNumbers(r)
	members = append(members, domain.FHRPMember{
		GroupID: id, InterfaceID: formValue(r, "interface_id"), Priority: nums.opt("priority"),
	})
	if err := a.Store.SetFHRPMembers(r.Context(), actor(r), id, members); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Router added to the group.")
	render.Redirect(w, r, "/redundancy/"+id)
}

// FHRPMemberRemove takes a router out of the group.
func (a *App) FHRPMemberRemove(w http.ResponseWriter, r *http.Request) {
	id, ifaceID := r.PathValue("id"), r.PathValue("ifaceID")
	current, err := a.Store.ListFHRPMembers(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	members := make([]domain.FHRPMember, 0, len(current))
	for _, m := range current {
		if m.InterfaceID == ifaceID {
			continue
		}
		members = append(members, domain.FHRPMember{
			GroupID: id, InterfaceID: m.InterfaceID, Priority: m.Priority,
		})
	}
	if err := a.Store.SetFHRPMembers(r.Context(), actor(r), id, members); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Router removed from the group.")
	render.Redirect(w, r, "/redundancy/"+id)
}

// FHRPRetire withdraws a group.
func (a *App) FHRPRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetireFHRPGroup(r.Context(), actor(r), r.PathValue("id")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Redundancy group withdrawn.")
	render.Redirect(w, r, "/redundancy")
}
