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
	VLANs  []store.VLANRow
	Groups []store.VLANGroupRow
	// Edit is set only when a correction was refused; see editState. The
	// template calls through it unguarded, which is safe because every
	// editState method checks for a nil receiver.
	Edit     *editState
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
	a.renderVLANs(w, r, http.StatusOK, nil, nil)
}

func (a *App) renderVLANs(w http.ResponseWriter, r *http.Request, status int,
	errs map[string]string, edit *editState) {
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
	envs, err := a.Store.ListEnvironments(r.Context(), store.EnvironmentFilter{IncludeRetired: true})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	base := a.base(r, "VLANs", "vlans")
	// A refused correction reopens the row it was refused on, rather than
	// collapsing it and making the operator find it again -- renderPrefixes
	// does the same, and for the same reason.
	if edit != nil {
		base.EditRow = edit.ID
	}
	a.Render.Page(w, status, "vlan_list", vlanListPage{
		Base:     base,
		VLANs:    vlans,
		Groups:   groups,
		Edit:     edit,
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
	base := a.base(r, "VLAN "+vlan.Name, "vlans")
	writable := writableInterfaceOptions(base, options)
	a.Render.Page(w, http.StatusOK, "vlan_detail", struct {
		Base
		VLAN     *domain.VLAN
		Ports    vlanPortRows
		Prefixes []store.PrefixTreeRow
		// Options is FILTERED to ports on assets the caller may write, the
		// same reasoning and the same helper (writableInterfaceOptions,
		// forms.go) newLinkForm's Targets already uses -- fix-b item 3.
		// AddPortToVLAN/SetInterfaceVLANs (vlans.go) is ScopeSubjectDerived
		// through the port's OWNING ASSET (authorizeInterfaceSubject), so a
		// picker offering a port on an asset the caller does not own would
		// be offered-and-refused, the defect Task 3 already closed for the
		// dependency and link pickers.
		Options []store.InterfaceOption
		// OptionsHint explains a picker the filter has thinned, so an
		// operator is never handed a short or blank required <select> with
		// no account of why -- the same defect fix-b item 2 closed for the
		// dependency and link pickers, which this form was missed out of.
		OptionsHint string
		Modes       []string
	}{
		Base:     base,
		VLAN:     vlan,
		Ports:    vlanPortRowsFor(ports, base.CanWriteEntity),
		Prefixes: on,
		Options:  writable,
		OptionsHint: pickerHint(len(writable), len(options),
			"There are no ports in the estate yet.",
			"Every port belongs to an asset you do not own.",
			"Showing %d of %d ports -- the rest are on assets you do not own."),
		Modes: domain.VLANModes,
	})
}

// vlanPortRow decorates one VLAN membership row with CanWrite/ShowActions --
// the same two-field shape depRowData uses (forms.go) and for the identical
// reason (fix-b item 3). Port-to-VLAN membership
// (AddPortToVLAN/RemovePortFromVLAN, vlans.go's SetInterfaceVLANs) is
// ScopeSubjectDerived through the port's OWNING ASSET
// (authorizeInterfaceSubject, internal/store/network.go), so two ports on
// the SAME VLAN can have different owners -- a page-wide .CanWrite over-offers
// Remove on a foreign port's row and, the one time a project owner's own
// port and a foreign one share a table, leaves the header one column short
// of the writable row (the exact defect TestDependencyTableHeaderMatchesItsRows
// pins for dependencies).
type vlanPortRow struct {
	store.VLANPort
	CanWrite    bool
	ShowActions bool
}

type vlanPortRows []vlanPortRow

// AnyWritable reports whether any port in the table has CanWrite -- whether
// the actions column exists at all this render. Mirrors
// depRowList.AnyWritable (forms.go).
func (rows vlanPortRows) AnyWritable() bool {
	for _, r := range rows {
		if r.CanWrite {
			return true
		}
	}
	return false
}

func vlanPortRowsFor(ports []store.VLANPort, covers func(entityType, id string) bool) vlanPortRows {
	out := make(vlanPortRows, len(ports))
	for i, p := range ports {
		out[i] = vlanPortRow{VLANPort: p, CanWrite: covers("asset", p.AssetID)}
	}
	any := out.AnyWritable()
	for i := range out {
		out[i].ShowActions = any
	}
	return out
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
			map[string]string{"vid": "a VLAN needs a tag between 1 and 4094"}, nil)
		return
	}
	vlan, err := domain.NewVLAN(store.NewID(), *vid, formValue(r, "name"),
		optionalString(r, "group_id"))
	if err == nil {
		vlan.Role = optionalString(r, "role")
		vlan.EnvironmentID = optionalString(r, "environment_id")
		vlan.Description = optionalString(r, "description")
		err = a.Store.CreateVLAN(r.Context(), a.permit(r), vlan)
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
		a.renderVLANs(w, r, http.StatusUnprocessableEntity, messages, nil)
		return
	}

	a.setFlash(r, "success", "VLAN "+vlan.Name+" declared.")
	render.Redirect(w, r, "/vlans")
}

// VLANUpdate corrects a broadcast domain.
//
// UpdateVLAN HAS BEEN IN THE STORE THE WHOLE TIME, complete and unreachable:
// it validates, guards on row_version, writes its change_log entry and
// reindexes for search, and nothing anywhere called it. So a VLAN could be
// declared, have ports added and removed, and be withdrawn -- and never
// renamed. Getting the name wrong meant withdrawing it and declaring another,
// losing its port membership and its history to fix a typo.
//
// Found on the live demo, whose VLANs are named "VLAN 10" and "VLAN 30" by a
// seed that predates the current naming, so the wireless top-up could not
// resolve `production-workloads` and left the guest SSID with no VLAN. The
// data was correctable; the product had no way to correct it.
//
// The copy-then-overwrite shape is PrefixUpdate's, and it matters: UpdateVLAN
// writes every column, so a handler that built a fresh VLAN from the form
// would blank whatever the form does not carry. Starting from the stored row
// means an omitted field keeps its value rather than silently becoming empty.
func (a *App) VLANUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	existing, err := a.Store.GetVLAN(r.Context(), r.PathValue("id"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	nums := optionalNumbers(r)
	updated := *existing
	updated.Name = formValue(r, "name")
	updated.GroupID = optionalString(r, "group_id")
	updated.Role = optionalString(r, "role")
	updated.EnvironmentID = optionalString(r, "environment_id")
	updated.Description = optionalString(r, "description")
	updated.RowVersion = submittedVersion(r, updated.RowVersion)
	// The VID is editable because a mistyped tag is exactly the kind of
	// correction this handler exists for, and it is the one field the store
	// refuses to duplicate within a group -- so a wrong one is caught rather
	// than accepted.
	if vid := nums.opt("vid"); vid != nil {
		updated.VID = *vid
	}

	if msgs := nums.messages(); msgs != nil {
		err = domain.NewValidationFrom(msgs)
	} else {
		err = a.Store.UpdateVLAN(r.Context(), a.permit(r), &updated)
	}
	if err != nil {
		messages, ok := refusalMessages(err, map[string]string{
			"vid": "that VLAN ID is already declared in this group",
		})
		if !ok {
			a.handleStoreError(w, r, err)
			return
		}
		a.renderVLANs(w, r, refusalStatus(err),
			nil, rejected(r, existing.ID, messages,
				"vid", "name", "group_id", "role", "environment_id", "description"))
		return
	}
	a.setFlash(r, "success", "VLAN "+updated.Name+" updated.")
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
	err := a.Store.AddPortToVLAN(r.Context(), a.permit(r), vlanID, formValue(r, "interface_id"), mode)
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
	err := a.Store.RemovePortFromVLAN(r.Context(), a.permit(r), vlanID, r.PathValue("ifaceID"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Port removed from the VLAN.")
	render.Redirect(w, r, "/vlans/"+vlanID)
}

// VLANRetire withdraws a broadcast domain, refusing while anything is on it.
func (a *App) VLANRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetireVLAN(r.Context(), a.permit(r), r.PathValue("id")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "VLAN withdrawn.")
	render.Redirect(w, r, "/vlans")
}
