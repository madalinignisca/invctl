package handlers

import (
	"net/http"

	"github.com/gabriel/invctl/internal/domain"
	"github.com/gabriel/invctl/internal/store"
	"github.com/gabriel/invctl/internal/web/render"
)

// ---------- interfaces ----------

// InterfaceCreate adds a port to an asset.
func (a *App) InterfaceCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	assetID := r.PathValue("id")

	iface, err := domain.NewInterface(store.NewID(), assetID, formValue(r, "name"), formValue(r, "form_factor"))
	if err == nil {
		if macErr := iface.SetMAC(formValue(r, "mac")); macErr != nil {
			err = macErr
		}
	}
	if err == nil {
		iface.SpeedMbps = optionalInt(r, "speed_mbps")
		iface.MTU = optionalInt(r, "mtu")
		iface.IsMgmt = checkbox(r, "is_mgmt")
		err = a.Store.CreateInterface(r.Context(), actor(r), iface)
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
		a.Render.Partial(w, http.StatusUnprocessableEntity, "interface_form",
			a.newInterfaceForm(r, assetID, messages))
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
		err = a.Store.CreateIPAddress(r.Context(), actor(r), addr)
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
		a.Render.Partial(w, http.StatusUnprocessableEntity, "ip_address_form",
			a.newIPAddressForm(r, assetID, messages, interfaces))
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

	link, err := domain.NewLink(store.NewID(), formValue(r, "a_interface_id"), formValue(r, "target_interface_id"))
	if err == nil {
		link.Medium = optionalString(r, "medium")
		link.LengthM = optionalInt(r, "length_m")
		err = a.Store.CreateLink(r.Context(), actor(r), link)
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

	if err := a.Store.RetireLink(r.Context(), actor(r), id); err != nil {
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
	Prefixes     []store.PrefixRow
	Environments []domain.Environment
	FormData     prefixFormData
}

// PrefixList renders every network, with its environment, VLAN and how many
// addresses in it are actually assigned.
func (a *App) PrefixList(w http.ResponseWriter, r *http.Request) {
	prefixes, err := a.Store.ListPrefixes(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	envs, err := a.Store.ListEnvironments(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Page(w, http.StatusOK, "prefix_list", prefixListPage{
		Base:         a.base(r, "Prefixes", "prefixes"),
		Prefixes:     prefixes,
		Environments: envs,
		FormData:     a.newPrefixForm(r, nil, envs),
	})
}

// PrefixCreate declares a network.
func (a *App) PrefixCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	prefix, err := domain.NewPrefix(store.NewID(), formValue(r, "cidr_text"))
	if err == nil {
		prefix.VLANID = optionalInt(r, "vlan_id")
		prefix.EnvironmentID = optionalString(r, "environment_id")
		prefix.Role = optionalString(r, "role")
		err = a.Store.CreatePrefix(r.Context(), actor(r), prefix)
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
		a.Render.Partial(w, http.StatusUnprocessableEntity, "prefix_form",
			a.newPrefixForm(r, messages, envs))
		return
	}

	a.setFlash(r, "success", "Prefix "+prefix.CIDRText+" declared.")
	render.Redirect(w, r, "/prefixes")
}
