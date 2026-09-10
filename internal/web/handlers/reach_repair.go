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

	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web/render"
)

// THE REACHABILITY LAYER WAS CREATE-ONLY IN BOTH DIRECTIONS.
//
// Five create routes, no retire routes, and five complete Retire* methods in
// internal/store/reach.go that nothing called: RetireNetGroup,
// RetireNetGroupMember, RetireNetUplink, RetireNetAttachment, RetireNetAnchor.
// An estate could declare a forwarder group, put assets in it, draw uplinks and
// place anchors, and never take any of it back.
//
// The store half was already written and argued. This is the wiring, which is
// the same shape WP-1.2 took for the six correction paths: the methods were
// never wrong, they were unreachable, and the reachability guard
// (unreachableRepairPaths) is what refused to let that stay quiet.
//
// WHY A GROUP DETAIL PAGE. /network renders groups and anchors as tables with
// COUNTS -- "4 members, 2 uplinks" -- so three of the five retires had nowhere
// to live: you cannot remove one member from a number. The page below lists
// what a group actually holds, which is also the view somebody wants when
// asking why an impact verdict came out the way it did.

type netGroupDetailPage struct {
	Base
	Group       *netGroupView
	Members     []store.NetGroupMemberRow
	Uplinks     []store.NetUplinkRow
	Attachments []store.NetAttachmentRow
}

// netGroupView is the group plus what the page says about withdrawing it.
type netGroupView struct {
	ID, Code, Name, Kind, Role, Availability string
	// Cascades is what RetireNetGroup will take with it, counted so the
	// confirmation can say so rather than leaving an operator to find out.
	//
	// THE CASCADE IS DELIBERATE AND STRUCTURAL, not a convenience: the store's
	// own comment records that retiring the vertex without cascading "would
	// strand its members forever (the partial unique index gives a
	// retired-group membership no route back to any live group), leave active
	// anchors pointing at a group the M3 loader will exclude, and leave uplinks
	// and attachments dangling". That is why this differs from
	// RetireInterface, which refuses instead: a cable on a port is a fact about
	// two ports and survives on its own, while a group membership has no
	// meaning without its group and cannot be recovered.
	Cascades int
}

// NetworkGroupDetail lists what one forwarder group holds.
func (a *App) NetworkGroupDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	group, err := a.Store.GetNetGroup(ctx, id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	members, err := a.Store.ListNetGroupMembers(ctx, id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	uplinks, err := a.Store.ListNetUplinks(ctx, id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	attachments, err := a.Store.ListNetAttachments(ctx, id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	a.Render.Page(w, http.StatusOK, "net_group_detail", netGroupDetailPage{
		Base: a.base(r, "Group "+group.Code, "network"),
		Group: &netGroupView{
			ID: group.ID, Code: group.Code, Name: group.Name,
			Kind: group.Kind, Role: group.Role, Availability: group.Availability,
			Cascades: len(members) + len(uplinks) + len(attachments),
		},
		Members:     members,
		Uplinks:     uplinks,
		Attachments: attachments,
	})
}

// NetworkGroupRetire withdraws a forwarder group, and everything hanging off it.
func (a *App) NetworkGroupRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetireNetGroup(r.Context(), a.permit(r), r.PathValue("id")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Forwarder group withdrawn, with its members, "+
		"uplinks, attachments and anchors — none of them means anything without it.")
	render.Redirect(w, r, "/network")
}

// NetworkGroupMemberRetire takes one asset out of a group.
func (a *App) NetworkGroupMemberRetire(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	err := a.Store.RetireNetGroupMember(r.Context(), a.permit(r), groupID, r.PathValue("assetID"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Asset removed from the group.")
	render.Redirect(w, r, "/network/groups/"+groupID)
}

// NetworkUplinkRetire withdraws one group-to-group edge.
//
// A DECLARED uplink only. An edge DERIVED from a circuit or a cable has no row
// here -- graph.go builds it on the fly from the terminations -- so it is
// withdrawn by cutting the circuit or unpatching the cable, not from this page.
func (a *App) NetworkUplinkRetire(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	if err := a.Store.RetireNetUplink(r.Context(), a.permit(r), r.PathValue("uplinkID")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Uplink withdrawn.")
	render.Redirect(w, r, "/network/groups/"+groupID)
}

// NetworkAttachmentRetire detaches a host from a group.
func (a *App) NetworkAttachmentRetire(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	if err := a.Store.RetireNetAttachment(r.Context(), a.permit(r), r.PathValue("attachmentID")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Attachment withdrawn.")
	render.Redirect(w, r, "/network/groups/"+groupID)
}

// NetworkAnchorRetire withdraws an anchor.
//
// THE HIGHEST-LEVERAGE ROW IN THIS MODEL, by internal/domain's own account: an
// anchor decides external reachability for everything behind it, so one placed
// on the wrong group silently changes every verdict in the estate. Until this
// route existed it could be placed and never removed.
func (a *App) NetworkAnchorRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetireNetAnchor(r.Context(), a.permit(r), r.PathValue("id")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Anchor withdrawn.")
	render.Redirect(w, r, "/network")
}
