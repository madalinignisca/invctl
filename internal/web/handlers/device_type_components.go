// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web/render"
)

// The catalogue's component template screens (device-type-templates plan,
// Task 7): the ports every instance of a model has, managed inside the same
// row an operator already opens to correct the model's own fields. Tasks 1-6
// built the whole store side -- ListDeviceTypeComponents,
// CreateDeviceTypeComponents, UpdateDeviceTypeComponent and
// RetireDeviceTypeComponent (internal/store/device_type_components.go) --
// and none of it was reachable from a browser until this file.

// componentAddFields are the fields an "add by range" refusal echoes back --
// named once so the three places that build a rejected editState for this
// form cannot drift out of step with each other.
var componentAddFields = []string{"name_spec", "form_factor", "speed_mbps"}

// componentRowFields are the fields a single component's correction echoes
// back.
var componentRowFields = []string{"name", "form_factor", "speed_mbps"}

// componentAddEditID names the sentinel editState.ID an "add by range"
// refusal carries -- powerInputCreateEditID's own pattern (power.go): never
// a real row id, because every component id is a UUIDv7 and this is not.
func componentAddEditID(deviceTypeID string) string {
	return "component-new:" + deviceTypeID
}

// DeviceTypeComponentCreate adds one port, or a whole range of them, to a
// device type's component template in a single batch -- Task 3's
// CreateDeviceTypeComponents, which expands "Ethernet[1-48]" into 48 rows
// inserted in one transaction and audited as one change_log entry. A
// 48-port switch is why domain.ExpandRange exists: the estate this ships
// for has four of one model with fourteen interfaces declared between them
// by hand, one at a time, before this route existed.
func (a *App) DeviceTypeComponentCreate(w http.ResponseWriter, r *http.Request) {
	deviceTypeID := r.PathValue("id")

	speed, numeric := optionalInt(r, "speed_mbps")
	if !numeric {
		a.renderCatalogueComponents(w, r, http.StatusUnprocessableEntity, deviceTypeID, "",
			rejected(r, componentAddEditID(deviceTypeID), notANumber("speed_mbps"), componentAddFields...), nil)
		return
	}

	nameSpec := formValue(r, "name_spec")
	// Checked here, ahead of the store call, because domain.ExpandRange's
	// own error is a plain fmt.Errorf, not a domain.ValidationError --
	// CreateDeviceTypeComponents wraps it with fmt.Errorf("expanding %q: %w",
	// ...) rather than reclassifying it, so validationErrors below would not
	// recognise it and a malformed range would fall through to
	// handleStoreError: a 500, for a typo in a text box. CLAUDE.md's rule is
	// 422 with the box still full, so the same check runs here first and is
	// shaped as a field message against name_spec, where the mistake is.
	expanded, err := domain.ExpandRange(nameSpec)
	if err != nil {
		a.renderCatalogueComponents(w, r, http.StatusUnprocessableEntity, deviceTypeID, "",
			rejected(r, componentAddEditID(deviceTypeID),
				map[string]string{"name_spec": err.Error()}, componentAddFields...), nil)
		return
	}

	spec := store.ComponentSpec{
		// The only kind a device type's template carries -- see
		// domain.ComponentKinds' own comment on why power_input was removed
		// from the kind vocabulary entirely rather than offered here and left
		// uninstantiated.
		Kind:       domain.ComponentKindInterface,
		NameSpec:   nameSpec,
		FormFactor: optionalString(r, "form_factor"),
		SpeedMbps:  speed,
		IsMgmt:     checkbox(r, "is_mgmt"),
	}

	if err := a.Store.CreateDeviceTypeComponents(r.Context(), a.permit(r), deviceTypeID, spec); err != nil {
		if messages, ok := refusalMessages(err, map[string]string{
			"name_spec": "the template already has an active component by one of those names",
		}); ok {
			a.renderCatalogueComponents(w, r, refusalStatus(err), deviceTypeID, "",
				rejected(r, componentAddEditID(deviceTypeID), messages, componentAddFields...), nil)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", fmt.Sprintf("Component template updated: %s.", namePreview(expanded)))
	render.Redirect(w, r, "/catalogue?edit="+deviceTypeID)
}

// namePreview names what a range expanded to, rather than only how many --
// the confirmation an operator gets before 48 rows are created, or the flash
// after. A count alone cannot show a wrong shape: "Ethernet1/[1-48]" against
// an estate whose real ports are named "Ethernet1".."Ethernet48" is a correct
// count (48) and an entirely wrong set of names, and only the names catch it.
// Named a few, not all: a wall of 48 names is as unreadable as a bare count,
// so this shows the first three and the last, which is enough to recognise
// the shape (and catch a typo'd separator) without asking anyone to read a
// list.
func namePreview(names []string) string {
	switch {
	case len(names) == 0:
		return "nothing"
	case len(names) <= 4:
		return strings.Join(names, ", ")
	default:
		return fmt.Sprintf("%s, … %s", strings.Join(names[:3], ", "), names[len(names)-1])
	}
}

// DeviceTypeComponentUpdate corrects one template entry's name, form factor,
// speed or management flag.
//
// device_type_component HAS NO PUBLIC Get METHOD (device_type_components.go
// keeps getDeviceTypeComponent package-private, the way UpdateDeviceTypeComponent
// and RetireDeviceTypeComponent already use it), so the row this handler
// needs is found the same way it is about to render it: read the type's
// active template once and look up the one id both the form and the store
// call need. A component that is not active is not offered an Edit link in
// the first place -- see catalogue.html -- so "not found" here means a
// stale link or a row withdrawn since the page was drawn, either way a 404.
//
// Position is carried over from the stored row and never taken from the
// form -- there is no reordering control on this page (Task 7's brief asks
// for name, form factor, speed and the management flag, not a drag-and-drop
// reorder), and ExpandRange-assigned position is what keeps a later batch of
// interfaces sorting after an earlier one; leaving it off the form is what
// keeps a correction from silently moving a row instead.
func (a *App) DeviceTypeComponentUpdate(w http.ResponseWriter, r *http.Request) {
	deviceTypeID := r.PathValue("id")
	componentID := r.PathValue("componentID")

	components, err := a.Store.ListDeviceTypeComponents(r.Context(), deviceTypeID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	var existing *domain.DeviceTypeComponent
	for i := range components {
		if components[i].ID == componentID {
			existing = &components[i]
			break
		}
	}
	if existing == nil {
		a.notFound(w, r)
		return
	}

	speed, numeric := optionalInt(r, "speed_mbps")
	if !numeric {
		a.renderCatalogueComponents(w, r, http.StatusUnprocessableEntity, deviceTypeID, componentID,
			nil, rejected(r, componentID, notANumber("speed_mbps"), componentRowFields...))
		return
	}

	updated := *existing
	updated.Name = formValue(r, "name")
	updated.FormFactor = optionalString(r, "form_factor")
	updated.SpeedMbps = speed
	updated.IsMgmt = checkbox(r, "is_mgmt")
	updated.RowVersion = submittedVersion(r, updated.RowVersion)

	if err := a.Store.UpdateDeviceTypeComponent(r.Context(), a.permit(r), &updated); err != nil {
		if messages, ok := refusalMessages(err, map[string]string{
			"name": "this model's template already has an active component by that name",
		}); ok {
			// 422 with the row reopened on what was typed -- the house rule,
			// the same shape PowerInputUpdate and InterfaceUpdate already
			// follow.
			a.renderCatalogueComponents(w, r, refusalStatus(err), deviceTypeID, componentID,
				nil, rejected(r, componentID, messages, componentRowFields...))
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Component updated.")
	render.Redirect(w, r, "/catalogue?edit="+deviceTypeID)
}

// DeviceTypeComponentRetire withdraws one template entry.
//
// Nothing already instantiated from this model is touched -- see
// RetireDeviceTypeComponent's own comment: a retired template entry simply
// stops being offered to a NEW asset of this model, and to ApplyTemplate's
// backfill. The confirmation on the button that posts here says so, because
// this changes what every future asset of the model gets, not what is
// racked today.
func (a *App) DeviceTypeComponentRetire(w http.ResponseWriter, r *http.Request) {
	deviceTypeID := r.PathValue("id")
	if err := a.Store.RetireDeviceTypeComponent(r.Context(), a.permit(r), r.PathValue("componentID")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Component withdrawn. Assets already carrying that port keep it.")
	render.Redirect(w, r, "/catalogue?edit="+deviceTypeID)
}
