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

// fhrpDetailPage is what /redundancy/{id} renders: the group, its members,
// and its virtual addresses, with room for a refused correction of any of
// the three to come back on the same page instead of a redirect that throws
// the submission away.
type fhrpDetailPage struct {
	Base
	Group      *domain.FHRPGroup
	Members    []store.FHRPMemberRow
	Options    []store.InterfaceOption
	Redundancy domain.FHRPRedundancy
	Note       string
	Label      string
	Protocols  []string
	VIPs       []domain.IPAddress
	// VIPCandidates is every live address AssignVIP could point this group's
	// VIP at: not retired, and not already answering for some group -- see
	// ListVIPCandidates.
	VIPCandidates []store.VIPCandidate
	// GroupEdit carries a refused correction of the group's OWN fields back
	// onto this page -- nil on an ordinary GET, the shape
	// renderNetGroupDetail already uses for NetworkGroupUpdate.
	GroupEdit *editState
	// MemberEdit carries a refused priority correction, keyed by the
	// INTERFACE id rather than the group id -- editFor's own rule (a page
	// carries one refusal per editor, matched by id) is what keeps this form
	// and GroupEdit from bleeding into each other's error state.
	MemberEdit *editState
	// MemberEditRow is MemberEdit.ID, lifted onto the page as a plain string
	// so the template can compare it with `eq` -- the same reason
	// vlan_list.html compares .ID against $.EditRow rather than probing
	// $.Edit.ID directly: a nil *editState has no field to read in a
	// template, only nil-safe METHODS, and every row in the range needs to
	// ask "is this refusal mine?" before it may call one.
	MemberEditRow string
	// VIPEdit carries a refused VIP move, keyed by the group id -- there is
	// only one VIP form on this page, so no separate id space is needed.
	VIPEdit *editState
}

// FHRPDetail shows one group, the routers in it, and its virtual address.
func (a *App) FHRPDetail(w http.ResponseWriter, r *http.Request) {
	a.renderFHRPDetail(w, r, http.StatusOK, r.PathValue("id"), nil, nil, nil)
}

// renderFHRPDetail draws the page at any status, so a refused correction of
// the group's own fields, a member's priority, or the virtual address can
// come back as 422 (or 409, for a stale token) with the right form reopened
// on what was typed -- the same shape renderNetGroupDetail and
// renderAssetDetail already have, and for the same reason: a bare
// flash-and-redirect throws the submission away and reports a success-shaped
// status for a refusal.
func (a *App) renderFHRPDetail(w http.ResponseWriter, r *http.Request, status int, id string,
	groupEdit, memberEdit, vipEdit *editState) {

	ctx := r.Context()
	group, err := a.Store.GetFHRPGroup(ctx, id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	members, err := a.Store.ListFHRPMembers(ctx, id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	options, err := a.Store.ListPortOptions(ctx)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	vips, err := a.Store.ListFHRPVIPs(ctx, id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	candidates, err := a.Store.ListVIPCandidates(ctx)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	var memberEditRow string
	if memberEdit != nil {
		memberEditRow = memberEdit.ID
	}

	a.Render.Page(w, status, "fhrp_detail", fhrpDetailPage{
		Base:          a.base(r, group.Name, "fhrp"),
		Group:         group,
		Members:       members,
		Options:       options,
		Redundancy:    domain.Redundancy(len(members)),
		Note:          domain.RedundancyDescription(domain.Redundancy(len(members))),
		Label:         domain.FHRPProtocolLabel(group.Protocol),
		Protocols:     domain.FHRPProtocols,
		VIPs:          vips,
		VIPCandidates: candidates,
		GroupEdit:     groupEdit,
		MemberEdit:    memberEdit,
		MemberEditRow: memberEditRow,
		VIPEdit:       vipEdit,
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
		err = a.Store.CreateFHRPGroup(r.Context(), a.permit(r), group)
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
	if err := a.Store.SetFHRPMembers(r.Context(), a.permit(r), id, members); err != nil {
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
	if err := a.Store.SetFHRPMembers(r.Context(), a.permit(r), id, members); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Router removed from the group.")
	render.Redirect(w, r, "/redundancy/"+id)
}

// FHRPUpdate corrects a group's own fields: protocol, group number, name and
// description.
//
// UpdateFHRPGroup HAS BEEN IN THE STORE COMPLETE AND UNREACHABLE -- the same
// gap TestEveryUpdateMethodIsReachable exists to catch, and the same shape
// VLANUpdate and NetworkGroupUpdate already closed for their own entities.
// Before this route, a mistyped VRID or a typo'd name meant withdrawing the
// group -- and RetireFHRPGroup refuses outright while a VIP still names it,
// so fixing one digit meant first somehow detaching the virtual address and
// then redrawing the group and its whole membership from nothing.
//
// Copy-then-overwrite, the same shape NetworkGroupUpdate and VLANUpdate use:
// UpdateFHRPGroup writes every column it is given, so building a fresh group
// from the form would blank whatever the form does not carry.
func (a *App) FHRPUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	existing, err := a.Store.GetFHRPGroup(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	nums := optionalNumbers(r)
	updated := *existing
	updated.Protocol = formValue(r, "protocol")
	updated.Name = formValue(r, "name")
	updated.Description = optionalString(r, "description")
	updated.RowVersion = submittedVersion(r, updated.RowVersion)
	// The group number is editable because a mistyped VRID is exactly the
	// correction this handler exists for -- optional here only in the
	// nums.opt sense (blank means "not submitted", not "clear it"), the same
	// rule VLANUpdate's vid field follows.
	if num := nums.opt("group_number"); num != nil {
		updated.GroupNumber = *num
	}

	if msgs := nums.messages(); msgs != nil {
		err = domain.NewValidationFrom(msgs)
	} else {
		err = a.Store.UpdateFHRPGroup(r.Context(), a.permit(r), &updated)
	}
	if err != nil {
		messages, ok := refusalMessages(err, map[string]string{
			"name": "a group with that name already exists",
		})
		if !ok {
			a.handleStoreError(w, r, err)
			return
		}
		// 422 (or 409, for a stale token) with the form reopened on what was
		// typed, not a redirect that throws it away -- CLAUDE.md's rule.
		a.renderFHRPDetail(w, r, refusalStatus(err), id,
			rejected(r, id, messages, "protocol", "group_number", "name", "description"),
			nil, nil)
		return
	}
	a.setFlash(r, "success", "Redundancy group "+updated.Name+" updated.")
	render.Redirect(w, r, "/redundancy/"+id)
}

// FHRPMemberUpdate corrects a member's priority in place.
//
// SetFHRPMembers replaces the group's whole membership, which is also
// exactly the operation a priority correction needs: rebuild the same list
// with the named router's priority changed and everyone else carried
// untouched, rather than removing the router and adding it back. The
// latter is what an operator had to do before this route existed, and
// SetFHRPMembers folds a membership change into the group's audited value --
// so a remove-then-add would have written a departure and an arrival for a
// router that never left. See TestFHRPMemberPriorityIsCorrectedNotChurned.
func (a *App) FHRPMemberUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	id, ifaceID := r.PathValue("id"), r.PathValue("ifaceID")
	current, err := a.Store.ListFHRPMembers(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	nums := optionalNumbers(r)
	priority := nums.opt("priority")

	found := false
	members := make([]domain.FHRPMember, 0, len(current))
	for _, m := range current {
		p := m.Priority
		if m.InterfaceID == ifaceID {
			p = priority
			found = true
		}
		members = append(members, domain.FHRPMember{GroupID: id, InterfaceID: m.InterfaceID, Priority: p})
	}
	if !found {
		// The interface named in the URL is not a member of this group any
		// more -- somebody else removed it between the page rendering and
		// this submit. There is no row left to correct.
		a.notFound(w, r)
		return
	}

	if msgs := nums.messages(); msgs != nil {
		err = domain.NewValidationFrom(msgs)
	} else {
		err = a.Store.SetFHRPMembers(r.Context(), a.permit(r), id, members)
	}
	if err != nil {
		messages, ok := refusalMessages(err, map[string]string{})
		if !ok {
			a.handleStoreError(w, r, err)
			return
		}
		a.renderFHRPDetail(w, r, refusalStatus(err), id,
			nil, rejected(r, ifaceID, messages, "priority"), nil)
		return
	}
	a.setFlash(r, "success", "Priority updated.")
	render.Redirect(w, r, "/redundancy/"+id)
}

// FHRPVIPAssign points the group's virtual address at a different address,
// or -- for a group with none yet -- declares its first.
//
// AssignVIP was complete IN THE STORE AND UNREACHABLE: nothing anywhere
// called it, so a VIP could never be declared or moved through the product
// at all. It now also releases whichever address the group previously
// answered through, in the same transaction as the new binding -- closing
// the stuck-row bug the old AssignVIP left behind: an address wrongly
// declared as a VIP could never be withdrawn, because RetireIPAddress
// refuses while fhrp_group_id is set and nothing anywhere ever cleared it.
func (a *App) FHRPVIPAssign(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	addressID := formValue(r, "address_id")
	if addressID == "" {
		a.renderFHRPDetail(w, r, http.StatusUnprocessableEntity, id,
			nil, nil, rejected(r, id, map[string]string{"address_id": "choose an address"}, "address_id"))
		return
	}
	if err := a.Store.AssignVIP(r.Context(), a.permit(r), addressID, id); err != nil {
		messages, ok := refusalMessages(err, map[string]string{})
		if !ok {
			a.handleStoreError(w, r, err)
			return
		}
		a.renderFHRPDetail(w, r, refusalStatus(err), id,
			nil, nil, rejected(r, id, messages, "address_id"))
		return
	}
	a.setFlash(r, "success", "Virtual address updated.")
	render.Redirect(w, r, "/redundancy/"+id)
}

// FHRPRetire withdraws a group.
func (a *App) FHRPRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetireFHRPGroup(r.Context(), a.permit(r), r.PathValue("id")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Redundancy group withdrawn.")
	render.Redirect(w, r, "/redundancy")
}
