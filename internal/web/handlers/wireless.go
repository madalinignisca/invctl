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

// wirelessListPage is WP-F1's list-plus-create page, following vlanListPage's
// shape exactly so the two topology surfaces read the same way.
type wirelessListPage struct {
	Base
	WLANs []store.WirelessLANRow
	// Edit is set only when a correction was refused; see editState. Every
	// method on it is nil-safe, so the template calls through it unguarded.
	Edit     *editState
	FormData wirelessFormData
}

type wirelessFormData struct {
	Base
	Errors     map[string]string
	Securities []store.VocabularyTerm
	VLANs      []store.VLANRow
	Services   []store.ServiceRow
	// Assets is the scope picker: a site, a rack, a cluster -- anything that
	// IS an asset, exactly the trick D6/00031_vlans.sql already made. There is
	// no filter to "site-shaped" kinds; scope_asset_id accepts any asset and
	// narrowing this picker's contents would be a rule this form invents
	// rather than one the schema states.
	Assets []store.AssetRow
}

func (a *App) newWirelessForm(r *http.Request, errs map[string]string, securities []store.VocabularyTerm,
	vlans []store.VLANRow, services []store.ServiceRow, assets []store.AssetRow) wirelessFormData {
	return wirelessFormData{
		Base: a.base(r, "Wireless", "wireless"), Errors: orEmpty(errs),
		Securities: securities, VLANs: vlans, Services: services, Assets: assets,
	}
}

// WirelessList renders every declared SSID.
func (a *App) WirelessList(w http.ResponseWriter, r *http.Request) {
	a.renderWireless(w, r, http.StatusOK, nil, nil)
}

func (a *App) renderWireless(w http.ResponseWriter, r *http.Request, status int,
	errs map[string]string, edit *editState) {
	wlans, err := a.Store.ListWirelessLANs(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	securities, err := a.Store.WirelessSecurities(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	vlans, err := a.Store.ListVLANs(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	services, err := a.Store.ListServices(r.Context(), store.ServiceFilter{})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	assets, err := a.Store.ListAssets(r.Context(), store.AssetFilter{})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	base := a.base(r, "Wireless", "wireless")
	// A refused correction reopens the row it was refused on, rather than
	// collapsing it and making the operator find it again.
	if edit != nil {
		base.EditRow = edit.ID
	}
	a.Render.Page(w, status, "wireless_list", wirelessListPage{
		Base:     base,
		WLANs:    wlans,
		Edit:     edit,
		FormData: a.newWirelessForm(r, errs, securities, vlans, services, assets),
	})
}

// WirelessDetail shows one SSID and the radios broadcasting it.
//
// THE RADIOS ARE THE POINT. A wireless LAN with a VLAN and no radios is a
// record; the radio list is what makes it a broadcast domain, and it is a
// fact no cable trace can produce -- two laptops on `corp` can reach each
// other and no cable joins them (docs/wireless-design.md §2.1).
func (a *App) WirelessDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	wlan, err := a.Store.GetWirelessLAN(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	radios, err := a.Store.ListWLANRadios(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	options, err := a.Store.ListRadioOptions(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	// The security mode's LABEL, not its code. GetWirelessLAN returns the
	// domain row, which carries `wpa2_personal`; the list page joins
	// wireless_security for the readable form and this page showed the code
	// beside it. One vocabulary read rather than widening GetWirelessLAN's
	// return, since every other caller of it wants the domain value.
	securities, err := a.Store.WirelessSecurities(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	securityLabel := wlan.Security
	for _, term := range securities {
		if term.Code == wlan.Security {
			securityLabel = term.Label
			break
		}
	}
	base := a.base(r, "Wireless — "+wlan.SSID, "wireless")
	// Options is FILTERED to radios on assets the caller may write, the same
	// reasoning and the same helper vlan_detail.html's port picker uses
	// (writableInterfaceOptions, forms.go) -- SetInterfaceWLANs
	// (AddRadioToWLAN/RemoveRadioFromWLAN, internal/store/wireless.go) is
	// ScopeSubjectDerived through the radio's OWNING ASSET
	// (authorizeInterfaceSubject), so a picker offering a radio on an asset
	// the caller does not own would be offered-and-refused -- the defect
	// fix-b item 3 closed for the dependency, link and VLAN pickers.
	writable := writableInterfaceOptions(base, options)
	a.Render.Page(w, http.StatusOK, "wireless_detail", struct {
		Base
		WLAN          *domain.WirelessLAN
		SecurityLabel string
		Radios        wlanRadioRows
		// Options is the picker's contents; OptionsHint explains why it is
		// shorter than the estate's full radio count, the same fix-b item 2
		// pattern every other filtered picker on this codebase carries.
		Options     []store.InterfaceOption
		OptionsHint string
	}{
		Base:          base,
		WLAN:          wlan,
		SecurityLabel: securityLabel,
		Radios:        wlanRadioRowsFor(radios, base.CanWriteEntity),
		Options:       writable,
		OptionsHint: pickerHint(len(writable), len(options),
			"There are no radios in the estate yet.",
			"Every radio belongs to an asset you do not own.",
			"Showing %d of %d radios -- the rest are on assets you do not own."),
	})
}

// wlanRadioRow decorates one radio-on-an-SSID row with CanWrite/ShowActions,
// the vlanPortRow pattern (vlans.go) verbatim and for the identical reason:
// radio membership (AddRadioToWLAN/RemoveRadioFromWLAN, wireless.go's
// SetInterfaceWLANs) is ScopeSubjectDerived through the radio's OWNING
// ASSET, so two radios on the SAME SSID can have different owners -- a
// page-wide .CanWrite over-offers Remove on a foreign radio's row and leaves
// the header one column short of the writable row.
type wlanRadioRow struct {
	store.WLANRadio
	CanWrite    bool
	ShowActions bool
}

type wlanRadioRows []wlanRadioRow

// AnyWritable mirrors vlanPortRows.AnyWritable: whether the actions column
// exists at all this render.
func (rows wlanRadioRows) AnyWritable() bool {
	for _, r := range rows {
		if r.CanWrite {
			return true
		}
	}
	return false
}

func wlanRadioRowsFor(radios []store.WLANRadio, covers func(entityType, id string) bool) wlanRadioRows {
	out := make(wlanRadioRows, len(radios))
	for i, r := range radios {
		out[i] = wlanRadioRow{WLANRadio: r, CanWrite: covers("asset", r.AssetID)}
	}
	any := out.AnyWritable()
	for i := range out {
		out[i].ShowActions = any
	}
	return out
}

// WirelessCreate declares an SSID.
func (a *App) WirelessCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	wlan, err := domain.NewWirelessLAN(store.NewID(), formValue(r, "name"), formValue(r, "ssid"),
		formValue(r, "security"), optionalString(r, "scope_asset_id"))
	if err == nil {
		wlan.VLANID = optionalString(r, "vlan_id")
		wlan.AuthServiceID = optionalString(r, "auth_service_id")
		// psk_ref is a PATH, never a passphrase -- see the field's hint in
		// wireless_list.html. Nothing here validates its shape beyond what
		// NewWirelessLAN already does (none): a reference format is the
		// operator's secret store's business, not this form's.
		wlan.PSKRef = optionalString(r, "psk_ref")
		wlan.Notes = optionalString(r, "notes")
		err = a.Store.CreateWirelessLAN(r.Context(), a.permit(r), wlan)
	}
	if err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			if isConflict(err) {
				messages = map[string]string{
					"ssid": "that SSID is already declared in this scope",
				}
			} else {
				a.handleStoreError(w, r, err)
				return
			}
		}
		a.renderWireless(w, r, http.StatusUnprocessableEntity, messages, nil)
		return
	}

	a.setFlash(r, "success", "Wireless LAN "+wlan.SSID+" declared.")
	render.Redirect(w, r, "/wireless")
}

// WirelessUpdate corrects an SSID's declared configuration.
//
// UpdateWirelessLAN HAS BEEN IN THE STORE SINCE WP-F1 SHIPPED THIS MORNING,
// complete and unreachable -- validating, checking the security vocabulary,
// guarding on row_version, writing its change_log entry and reindexing for
// search -- with no route, no handler and no form. So an SSID could be
// declared, gain and lose radios, and be withdrawn, and never corrected.
//
// THIS IS THE SAME GAP WP-F1 FOUND IN VLANs AND FIXED THREE HOURS EARLIER,
// reproduced in the feature that found it. Worth stating plainly rather than
// quietly closing: the create/retire pair is what gets built, and the
// correction path is what gets forgotten, and knowing that is the only reason
// anybody checks.
//
// It surfaced on the demo. A top-up creates an SSID once and then skips it --
// deliberately, so it never clobbers what somebody set -- which means an SSID
// created before its VLAN existed can never gain one from a later top-up. The
// data was correctable and there was no way to correct it, exactly as with the
// VLANs whose names it needed.
//
// Copy-then-overwrite, like VLANUpdate: UpdateWirelessLAN writes every column,
// so building a fresh row from the form would blank whatever the form omits.
func (a *App) WirelessUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	existing, err := a.Store.GetWirelessLAN(r.Context(), r.PathValue("id"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	updated := *existing
	updated.Name = formValue(r, "name")
	updated.SSID = formValue(r, "ssid")
	updated.Security = formValue(r, "security")
	updated.ScopeAssetID = optionalString(r, "scope_asset_id")
	updated.VLANID = optionalString(r, "vlan_id")
	updated.AuthServiceID = optionalString(r, "auth_service_id")
	updated.PSKRef = optionalString(r, "psk_ref")
	updated.Notes = optionalString(r, "notes")
	updated.RowVersion = submittedVersion(r, updated.RowVersion)

	if err := a.Store.UpdateWirelessLAN(r.Context(), a.permit(r), &updated); err != nil {
		messages, ok := refusalMessages(err, map[string]string{
			"ssid": "that SSID is already declared in this scope",
		})
		if !ok {
			a.handleStoreError(w, r, err)
			return
		}
		a.renderWireless(w, r, refusalStatus(err),
			nil, rejected(r, existing.ID, messages,
				"name", "ssid", "security", "scope_asset_id", "vlan_id",
				"auth_service_id", "psk_ref", "notes"))
		return
	}
	a.setFlash(r, "success", "SSID "+updated.SSID+" updated.")
	render.Redirect(w, r, "/wireless")
}

// WirelessRetire withdraws an SSID, refusing while any radio still broadcasts
// it (RetireWirelessLAN, internal/store/wireless.go).
func (a *App) WirelessRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetireWirelessLAN(r.Context(), a.permit(r), r.PathValue("id")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Wireless LAN withdrawn.")
	render.Redirect(w, r, "/wireless")
}

// WirelessRadioAdd puts one radio on this SSID, keeping its other
// memberships (AddRadioToWLAN, which delegates to SetInterfaceWLANs and
// folds the change into the radio's own change_log entry -- D7).
func (a *App) WirelessRadioAdd(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	wlanID := r.PathValue("id")
	err := a.Store.AddRadioToWLAN(r.Context(), a.permit(r), wlanID, formValue(r, "interface_id"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Radio added to the SSID.")
	render.Redirect(w, r, "/wireless/"+wlanID)
}

// WirelessRadioRemove takes one radio off this SSID, keeping the rest.
func (a *App) WirelessRadioRemove(w http.ResponseWriter, r *http.Request) {
	wlanID := r.PathValue("id")
	err := a.Store.RemoveRadioFromWLAN(r.Context(), a.permit(r), wlanID, r.PathValue("ifaceID"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Radio removed from the SSID.")
	render.Redirect(w, r, "/wireless/"+wlanID)
}
