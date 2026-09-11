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

// ---------- interfaces ----------

// InterfaceCreate adds a port to an asset.
func (a *App) InterfaceCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	assetID := r.PathValue("id")

	nums := optionalNumbers(r)
	iface, err := domain.NewInterface(store.NewID(), assetID, formValue(r, "name"), formValue(r, "form_factor"))
	if err == nil {
		if macErr := iface.SetMAC(formValue(r, "mac")); macErr != nil {
			err = macErr
		}
	}
	if err == nil {
		iface.SpeedMbps = nums.opt("speed_mbps")
		iface.MTU = nums.opt("mtu")
		iface.IsMgmt = checkbox(r, "is_mgmt")
		if msgs := nums.messages(); msgs != nil {
			err = domain.NewValidationFrom(msgs)
		} else {
			err = a.Store.CreateInterface(r.Context(), a.permit(r), iface)
		}
	}
	if err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			if isConflict(err) {
				messages = map[string]string{"name": "this asset already has a port with that name"}
			} else {
				a.handleStoreError(w, r, err)
				return
			}
		}
		formFactors, listErr := a.Store.InterfaceFormFactors(r.Context())
		if listErr != nil {
			a.serverError(w, r, listErr)
			return
		}
		a.Render.Partial(w, http.StatusUnprocessableEntity, "interface_form",
			a.newInterfaceForm(r, assetID, messages, formFactors))
		return
	}

	a.setFlash(r, "success", "Interface "+iface.Name+" added.")
	render.Redirect(w, r, "/assets/"+assetID)
}

// ---------- ip addresses ----------

// IPAddressCreate assigns an address to an interface. The interface is a form
// field rather than a path segment, because the form lives on the asset page
// and offers a choice of the asset's own interfaces -- there is no single
// interface id to hang the route off.
func (a *App) IPAddressCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	assetID := formValue(r, "asset_id")
	interfaceID := formValue(r, "interface_id")

	role := formValue(r, "role")
	if role == "" {
		role = domain.IPRolePrimary
	}
	addr, err := domain.NewIPAddress(store.NewID(), formValue(r, "addr_text"), &interfaceID, role)
	if err == nil {
		err = a.Store.CreateIPAddress(r.Context(), a.permit(r), addr)
	}
	if err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			if isConflict(err) {
				messages = map[string]string{"addr_text": "this interface already has that address"}
			} else {
				a.handleStoreError(w, r, err)
				return
			}
		}
		interfaces, listErr := a.Store.ListInterfaces(r.Context(), assetID)
		if listErr != nil {
			a.serverError(w, r, listErr)
			return
		}
		roles, listErr := a.Store.IPAddressRoles(r.Context())
		if listErr != nil {
			a.serverError(w, r, listErr)
			return
		}
		a.Render.Partial(w, http.StatusUnprocessableEntity, "ip_address_form",
			a.newIPAddressForm(r, assetID, messages, interfaces, roles))
		return
	}

	a.setFlash(r, "success", "Address "+addr.AddrText+" assigned.")
	render.Redirect(w, r, "/assets/"+assetID)
}

// ---------- links (cables) ----------

// LinkCreate patches one interface to another. Both sides are form fields for
// the same reason as IPAddressCreate: the form offers a choice, so there is
// no single interface id to hang the route off.
func (a *App) LinkCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	assetID := formValue(r, "asset_id")

	nums := optionalNumbers(r)
	link, err := domain.NewLink(store.NewID(), formValue(r, "a_interface_id"), formValue(r, "target_interface_id"))
	if err == nil {
		link.Medium = optionalString(r, "medium")
		link.LengthM = nums.opt("length_m")
		if msgs := nums.messages(); msgs != nil {
			err = domain.NewValidationFrom(msgs)
		} else {
			err = a.Store.CreateLink(r.Context(), a.permit(r), link)
		}
	}
	if err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			if isConflict(err) {
				messages = map[string]string{"target_interface_id": "one of those ports is already patched"}
			} else {
				a.handleStoreError(w, r, err)
				return
			}
		}
		a.renderLinkFormError(w, r, assetID, messages)
		return
	}

	a.setFlash(r, "success", "Cable patched.")
	render.Redirect(w, r, "/assets/"+assetID)
}

// LinkRetire unpatches a cable. The row and its audit history are kept; the
// far end simply stops showing (docs/DECISIONS.md, 2026-07-28 decisions).
func (a *App) LinkRetire(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// The redirect target is wherever the operator was looking, not
	// necessarily the "a" side of the cable -- the form on the asset page
	// carries it explicitly.
	redirectAssetID := formValue(r, "asset_id")
	if redirectAssetID == "" {
		link, err := a.Store.GetLink(r.Context(), id)
		if err != nil {
			a.handleStoreError(w, r, err)
			return
		}
		iface, err := a.Store.GetInterface(r.Context(), link.AInterfaceID)
		if err != nil {
			a.handleStoreError(w, r, err)
			return
		}
		redirectAssetID = iface.AssetID
	}

	if err := a.Store.RetireLink(r.Context(), a.permit(r), id); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Cable unpatched. The record is kept.")
	render.Redirect(w, r, "/assets/"+redirectAssetID)
}

func (a *App) renderLinkFormError(w http.ResponseWriter, r *http.Request, assetID string, messages map[string]string) {
	interfaces, err := a.Store.ListInterfaces(r.Context(), assetID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	targets, err := a.Store.ListAvailableInterfaces(r.Context(), "")
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Partial(w, http.StatusUnprocessableEntity, "link_form",
		a.newLinkForm(r, assetID, messages, interfaces, targets))
}

// ---------- prefixes ----------

type prefixListPage struct {
	Base
	Prefixes     []store.PrefixTreeRow
	Environments []domain.Environment
	Ranges       []store.IPRangeRow
	VLANs        []store.VLANRow
	// Edit is set only when a correction was refused; see editState.
	Edit      *editState
	FormData  prefixFormData
	RangeForm rangeFormData
}

// ColumnOptions lists the prefixes table's configurable columns, in header
// order. CIDR is the identity column and is deliberately absent. The ranges
// table below it is out of scope -- see the plan's Task 4.
func (prefixListPage) ColumnOptions() []ColumnOption {
	return []ColumnOption{
		{Key: "vlan", Label: "VLAN"},
		{Key: "environment", Label: "Environment"},
		{Key: "role", Label: "Role"},
		{Key: "addresses", Label: "Addresses"},
		{Key: "allocated", Label: "Allocated"},
		{Key: "next_free", Label: "Next free"},
	}
}

// PrefixList renders every network, with its environment, VLAN and how many
// addresses in it are actually assigned.
func (a *App) PrefixList(w http.ResponseWriter, r *http.Request) {
	a.renderPrefixes(w, r, http.StatusOK, nil)
}

func (a *App) renderPrefixes(w http.ResponseWriter, r *http.Request, status int, edit *editState) {
	prefixes, err := a.Store.ListPrefixTree(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	envs, err := a.Store.ListEnvironments(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	ranges, err := a.Store.ListIPRanges(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	// The VLAN picker. A prefix names a VLAN by reference now, so the form
	// needs the list -- and an estate with no VLANs declared gets an empty
	// select rather than a free-text box that could invent one.
	vlans, err := a.Store.ListVLANs(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	base := a.base(r, "Prefixes", "prefixes")
	if edit != nil {
		base.EditRow = edit.ID
	}
	if render.WantsCSV(r) {
		render.CSV(w, r, store.ExportPrefixes(prefixes), a.Store.Now())
		return
	}
	a.Render.Page(w, status, "prefix_list", prefixListPage{
		Base:         base,
		Prefixes:     prefixes,
		Environments: envs,
		Ranges:       ranges,
		VLANs:        vlans,
		Edit:         edit,
		FormData:     a.newPrefixForm(r, nil, envs, vlans),
		RangeForm:    a.newRangeForm(r, nil),
	})
}

// PrefixCreate declares a network.
func (a *App) PrefixCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	nums := optionalNumbers(r)
	prefix, err := domain.NewPrefix(store.NewID(), formValue(r, "cidr_text"))
	if err == nil {
		prefix.VLANRefID = optionalString(r, "vlan_ref_id")
		prefix.EnvironmentID = optionalString(r, "environment_id")
		prefix.Role = optionalString(r, "role")
		if msgs := nums.messages(); msgs != nil {
			err = domain.NewValidationFrom(msgs)
		} else {
			err = a.Store.CreatePrefix(r.Context(), a.permit(r), prefix)
		}
	}
	if err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			if isConflict(err) {
				messages = map[string]string{"cidr_text": "that network is already declared"}
			} else {
				a.handleStoreError(w, r, err)
				return
			}
		}
		envs, listErr := a.Store.ListEnvironments(r.Context())
		if listErr != nil {
			a.serverError(w, r, listErr)
			return
		}
		vlans, listErr := a.Store.ListVLANs(r.Context())
		if listErr != nil {
			a.serverError(w, r, listErr)
			return
		}
		a.Render.Partial(w, http.StatusUnprocessableEntity, "prefix_form",
			a.newPrefixForm(r, messages, envs, vlans))
		return
	}

	a.setFlash(r, "success", "Prefix "+prefix.CIDRText+" declared.")
	render.Redirect(w, r, "/prefixes")
}

// ---------- corrections ----------
//
// Editing what a port, an address or a network IS. None of these move a thing:
// a port stays in its chassis and out of or in its bond, an address stays on
// its port. Those decide what the topology and the reachability walk see, and
// the store carries them over from the stored row rather than trusting the
// form — the UI not offering a field is not a control.

// InterfaceRetire withdraws a port that is no longer in the chassis.
//
// A PORT COULD ONLY EVER BE ADDED before migration 00062. `interface` was the
// most-referenced table in the schema with no lifecycle column at all, so a NIC
// pulled out of a machine stayed on the asset for ever -- renameable, never
// removable. The write-surface census found it; the reachability guard is what
// insists this handler exists rather than leaving RetireInterface unreachable
// the way six other repair methods once were.
//
// THE REFUSAL IS THE USEFUL PART and it belongs to the store: a port carrying a
// cable, an address, a VLAN membership or a circuit end cannot be withdrawn
// until those are removed. That keeps "a retired port is bare" true, which is
// what lets fifty-eight queries go on reading this table unchanged.
func (a *App) InterfaceRetire(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	iface, err := a.Store.GetInterface(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	// Read before the write, so the redirect lands on the asset even when the
	// retire is refused -- the operator needs to be looking at the port list to
	// act on the reason.
	assetID := iface.AssetID

	if err := a.Store.RetireInterface(r.Context(), a.permit(r), id); err != nil {
		if isConflict(err) {
			a.setFlash(r, "error", "That port still has something attached — a cable, "+
				"an address, a VLAN or a circuit end. Remove those first; withdrawing "+
				"it would leave them pointing at a port that is no longer there.")
			render.Redirect(w, r, "/assets/"+assetID)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Port "+iface.Name+" withdrawn. Re-adding a port of the "+
		"same name brings this one back, with its history.")
	render.Redirect(w, r, "/assets/"+assetID)
}

// InterfaceUpdate corrects a port.
func (a *App) InterfaceUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	existing, err := a.Store.GetInterface(r.Context(), r.PathValue("id"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	nums := optionalNumbers(r)
	updated := *existing
	updated.Name = formValue(r, "name")
	updated.FormFactor = formValue(r, "form_factor")
	updated.SpeedMbps = nums.opt("speed_mbps")
	updated.MTU = nums.opt("mtu")
	updated.IsMgmt = checkbox(r, "is_mgmt")
	updated.Enabled = checkbox(r, "enabled")
	updated.RowVersion = submittedVersion(r, updated.RowVersion)
	err = updated.SetMAC(formValue(r, "mac"))
	if err == nil {
		if msgs := nums.messages(); msgs != nil {
			err = domain.NewValidationFrom(msgs)
		} else {
			err = a.Store.UpdateInterface(r.Context(), a.permit(r), &updated)
		}
	}
	if err != nil {
		a.refuseAssetEdit(w, r, err, existing.AssetID, existing.ID,
			map[string]string{"name": "this asset already has a port with that name"},
			"name", "form_factor", "speed_mbps", "mac", "mtu", "is_mgmt", "enabled")
		return
	}
	a.setFlash(r, "success", "Port updated.")
	render.Redirect(w, r, "/assets/"+existing.AssetID)
}

// IPAddressUpdate corrects an address.
func (a *App) IPAddressUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	existing, err := a.Store.GetIPAddress(r.Context(), r.PathValue("id"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	// The asset to return to comes from the address's own port, not from the
	// form: a posted asset id would decide which page an operator lands on
	// after editing somebody else's record, which is a small lie with a long
	// tail. Nothing here needs the caller to tell us where this address lives.
	assetID := ""
	if existing.InterfaceID != nil {
		if iface, ifErr := a.Store.GetInterface(r.Context(), *existing.InterfaceID); ifErr == nil {
			assetID = iface.AssetID
		}
	}
	back := "/assets"
	if assetID != "" {
		back = "/assets/" + assetID
	}

	updated := *existing
	updated.Role = formValue(r, "role")
	updated.RowVersion = submittedVersion(r, updated.RowVersion)
	err = updated.SetAddress(formValue(r, "addr_text"))
	if err == nil {
		err = a.Store.UpdateIPAddress(r.Context(), a.permit(r), &updated)
	}
	if err != nil {
		a.refuseAssetEdit(w, r, err, assetID, existing.ID,
			map[string]string{"addr_text": "this interface already has that address"},
			"addr_text", "role")
		return
	}
	a.setFlash(r, "success", "Address updated.")
	render.Redirect(w, r, back)
}

// IPAddressRetire frees an address back to the allocator.
//
// THE REFUSAL IS THE USEFUL PART, and it belongs to the store: an address
// still bound to an endpoint or answering as an FHRP group's virtual address
// cannot be withdrawn until that binding is cleared. There is no form here to
// re-render on refusal -- a retire button carries nothing the operator typed
// -- so this flashes and redirects, the same shape as InterfaceRetire and
// refuseAssetEdit's own documented exception.
func (a *App) IPAddressRetire(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := a.Store.GetIPAddress(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	// Read before the write, so the redirect lands on the right asset page
	// even when the retire is refused -- the operator needs to be looking at
	// the address to act on the reason.
	assetID := ""
	if existing.InterfaceID != nil {
		if iface, ifErr := a.Store.GetInterface(r.Context(), *existing.InterfaceID); ifErr == nil {
			assetID = iface.AssetID
		}
	}
	back := "/assets"
	if assetID != "" {
		back = "/assets/" + assetID
	}

	if err := a.Store.RetireIPAddress(r.Context(), a.permit(r), id); err != nil {
		if isConflict(err) {
			a.setFlash(r, "error", "That address is still in use — an endpoint binds to "+
				"it, or it answers as a redundancy group's virtual address. Clear that "+
				"first; withdrawing it would leave the binding pointing at an address "+
				"nothing holds any more.")
			render.Redirect(w, r, back)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Address "+existing.AddrText+" withdrawn. The record is kept.")
	render.Redirect(w, r, back)
}

// PrefixUpdate corrects a network.
func (a *App) PrefixUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	existing, err := a.Store.GetPrefix(r.Context(), r.PathValue("id"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	nums := optionalNumbers(r)
	updated := *existing
	updated.VLANRefID = optionalString(r, "vlan_ref_id")
	updated.EnvironmentID = optionalString(r, "environment_id")
	updated.Role = optionalString(r, "role")
	updated.RowVersion = submittedVersion(r, updated.RowVersion)
	err = updated.SetCIDR(formValue(r, "cidr_text"))
	if err == nil {
		if msgs := nums.messages(); msgs != nil {
			err = domain.NewValidationFrom(msgs)
		} else {
			err = a.Store.UpdatePrefix(r.Context(), a.permit(r), &updated)
		}
	}
	if err != nil {
		messages, ok := refusalMessages(err, map[string]string{"cidr_text": "that network is already declared"})
		if !ok {
			a.handleStoreError(w, r, err)
			return
		}
		a.renderPrefixes(w, r, refusalStatus(err),
			rejected(r, existing.ID, messages, "cidr_text", "vlan_id", "environment_id", "role"))
		return
	}
	a.setFlash(r, "success", "Network updated.")
	render.Redirect(w, r, "/prefixes")
}

// PrefixRetire withdraws a network, refusing while a child prefix, an address
// or a reservation still lives inside it.
//
// THE REFUSAL IS THE USEFUL PART, and it belongs to the store (RetirePrefix,
// network.go): it is the one that walks the same tree the prefixes page
// renders, so a network offered for withdrawal here is one the store will
// actually accept. There is no form to lose on refusal -- a retire button
// carries nothing the operator typed -- so this flashes and redirects, the
// same shape as InterfaceRetire.
func (a *App) PrefixRetire(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := a.Store.GetPrefix(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	if err := a.Store.RetirePrefix(r.Context(), a.permit(r), id); err != nil {
		if isConflict(err) {
			a.setFlash(r, "error", "That network still has something inside it — a "+
				"child prefix, an assigned address or a reservation. Withdraw those "+
				"first; retiring this network would leave them delegated from a "+
				"network that no longer exists.")
			render.Redirect(w, r, "/prefixes")
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Network "+existing.CIDRText+" withdrawn. The record is kept.")
	render.Redirect(w, r, "/prefixes")
}

// refusalMessages turns a store error into field messages, or reports that it
// is not a refusal at all and belongs to the error handler.
func refusalMessages(err error, conflict map[string]string) (map[string]string, bool) {
	if messages, ok := validationErrors(err); ok {
		return messages, true
	}
	// Stale first: it is a conflict too, so checking the general case first
	// would answer "somebody else got here" with "that name is taken".
	if isStale(err) {
		for field := range conflict {
			return staleMessage(field), true
		}
		return staleMessage("cidr_text"), true
	}
	if isConflict(err) {
		return conflict, true
	}
	return nil, false
}

// refuseAssetEdit redraws the asset page at 422 with the refused row reopened
// on what the operator typed. Ports and addresses both live on that page, so
// they share it.
func (a *App) refuseAssetEdit(w http.ResponseWriter, r *http.Request, err error,
	assetID, rowID string, conflict map[string]string, fields ...string) {

	messages, ok := refusalMessages(err, conflict)
	if !ok {
		a.handleStoreError(w, r, err)
		return
	}
	if assetID == "" {
		// An address with no port has no asset page to go back to. Rare, and
		// better than rendering a page for an asset we cannot name.
		a.setFlash(r, "error", "Not accepted: "+joinMessages(messages))
		render.Redirect(w, r, "/assets")
		return
	}
	a.renderAssetDetail(w, r, refusalStatus(err), assetID,
		rejected(r, rowID, messages, fields...))
}
