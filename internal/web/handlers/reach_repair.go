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
	// The correction form's vocabularies, so the page can offer the same
	// choices the create form does rather than a free-text box that could
	// invent a kind the CHECK constraint refuses.
	Kinds          []string
	Roles          []string
	Availabilities []string
	FailoverModes  []string
	// Edit carries a refused correction of the group itself, so the form
	// reopens with what was typed instead of the stored row -- see
	// renderNetGroupDetail. Nil on an ordinary GET.
	Edit *editState
}

// netGroupView is the group plus what the page says about withdrawing it.
type netGroupView struct {
	ID, Code, Name, Kind, Role, Availability string
	MinHealthy                               string
	FailoverMode                             string
	RowVersion                               int
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
	a.renderNetGroupDetail(w, r, http.StatusOK, r.PathValue("id"), nil)
}

// renderNetGroupDetail draws the page at any status, so a refused correction
// can come back as 422 (or 409, for a stale token) with the group's own form
// reopened on what was typed -- the same shape renderAssetDetail and
// renderPower already have, and for the same reason: a bare flash-and-
// redirect throws the submission away and reports a success-shaped status
// for a refusal.
func (a *App) renderNetGroupDetail(w http.ResponseWriter, r *http.Request, status int,
	id string, edit *editState) {

	ctx := r.Context()
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

	a.Render.Page(w, status, "net_group_detail", netGroupDetailPage{
		Base: a.base(r, "Group "+group.Code, "network"),
		Group: &netGroupView{
			ID: group.ID, Code: group.Code, Name: group.Name,
			Kind: group.Kind, Role: group.Role, Availability: group.Availability,
			MinHealthy:   intOrBlank(group.MinHealthy),
			FailoverMode: derefOr(group.FailoverMode, ""),
			RowVersion:   group.RowVersion,
			Cascades:     len(members) + len(uplinks) + len(attachments),
		},
		Members:        members,
		Uplinks:        uplinks,
		Attachments:    attachments,
		Kinds:          domain.NetGroupKinds,
		Roles:          domain.NetGroupRoles,
		Availabilities: domain.NetGroupAvailabilities,
		FailoverModes:  domain.FailoverModes,
		Edit:           edit,
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

// NetworkGroupUpdate corrects a forwarder group.
//
// availability, min_healthy AND failover_mode ARE WHY THIS EXISTS. HANDOVER
// §3.3 calls them the semantics that make impact analysis mean anything: they
// decide whether losing a member degrades a group or kills it. Declared wrong,
// every verdict computed through that group is wrong -- and the only fix was to
// retire the group, which cascades away every member, uplink, attachment and
// anchor, and then redraw all of them to correct one number.
//
// Copy-then-overwrite, PrefixUpdate's shape: UpdateNetGroup writes every column
// it is given, so building a fresh group from the form would blank whatever the
// form does not carry.
func (a *App) NetworkGroupUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	existing, err := a.Store.GetNetGroup(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	nums := optionalNumbers(r)
	updated := *existing
	updated.Code = formValue(r, "code")
	updated.Name = formValue(r, "name")
	updated.Kind = formValue(r, "kind")
	updated.Role = formValue(r, "role")
	updated.Availability = formValue(r, "availability")
	updated.MinHealthy = nums.opt("min_healthy")
	updated.FailoverMode = optionalString(r, "failover_mode")
	updated.EnvironmentID = optionalString(r, "environment_id")
	updated.RowVersion = submittedVersion(r, updated.RowVersion)

	if msgs := nums.messages(); msgs != nil {
		err = domain.NewValidationFrom(msgs)
	} else {
		err = a.Store.UpdateNetGroup(r.Context(), a.permit(r), &updated)
	}
	if err != nil {
		messages, ok := refusalMessages(err, map[string]string{
			"code": "a forwarder group with that code already exists",
		})
		if !ok {
			a.handleStoreError(w, r, err)
			return
		}
		// 422 (or 409, for a stale token) with the form reopened on what was
		// typed, not a redirect that throws it away -- the house rule
		// (CLAUDE.md), the same shape every other correction on this estate
		// follows.
		a.renderNetGroupDetail(w, r, refusalStatus(err), id,
			rejected(r, id, messages, "code", "name", "kind", "role",
				"availability", "min_healthy", "failover_mode", "environment_id"))
		return
	}
	a.setFlash(r, "success", "Forwarder group "+updated.Code+" updated.")
	render.Redirect(w, r, "/network/groups/"+id)
}

// NetworkAnchorUpdate corrects an anchor.
//
// group_id IS THE FIELD THIS EXISTS FOR. internal/domain calls a misplaced
// anchor "the single highest-leverage wrong row in this model -- one row
// silently changes every external-reachability verdict in the estate", and it
// is exactly the thing somebody gets wrong. The only previous fix was to
// withdraw the anchor and place another, which is fine for the row and useless
// for the audit trail: it records a withdrawal and a fresh declaration where
// what actually happened was a correction.
func (a *App) NetworkAnchorUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	existing, err := a.Store.GetNetAnchor(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	updated := *existing
	updated.Code = formValue(r, "code")
	updated.Name = formValue(r, "name")
	updated.Scope = formValue(r, "scope")
	updated.Plane = formValue(r, "plane")
	updated.EnvironmentID = optionalString(r, "environment_id")
	updated.RowVersion = submittedVersion(r, updated.RowVersion)
	// submittedString's rule, spelled out because group_id is required: an
	// empty picker means "not rendered", never "this anchor points nowhere".
	// An anchor with no group is not a state the domain has.
	if v := formValue(r, "group_id"); v != "" {
		updated.GroupID = v
	}

	if err := a.Store.UpdateNetAnchor(r.Context(), a.permit(r), &updated); err != nil {
		messages, ok := refusalMessages(err, map[string]string{
			"code": "an anchor with that code already exists",
		})
		if !ok {
			a.handleStoreError(w, r, err)
			return
		}
		// 422 (or 409, for a stale token) with the row reopened on what was
		// typed, not a redirect that throws it away -- CLAUDE.md's rule, and
		// buildNetworkListPage already exists to do it: the topology page is
		// assembled from every other panel on it too, so there is no second
		// assembly to keep in step.
		page, err := a.buildNetworkListPage(r, refusalStatus(err), nil, nil, nil, nil, nil,
			rejected(r, id, messages, "code", "name", "scope", "plane", "group_id", "environment_id"))
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		a.Render.Page(w, refusalStatus(err), "network_list", page)
		return
	}
	a.setFlash(r, "success", "Anchor "+updated.Code+" updated.")
	render.Redirect(w, r, "/network")
}

// intOrBlank renders an optional int for a number input: blank, never "0".
// A min_healthy nobody recorded is not a min_healthy of zero, and rendering it
// as one is the "not recorded shown as a number" trap power_rating warns about.
func intOrBlank(n *int) string {
	if n == nil {
		return ""
	}
	return strconv.Itoa(*n)
}

func derefOr(s *string, fallback string) string {
	if s == nil {
		return fallback
	}
	return *s
}
