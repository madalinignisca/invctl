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

// The hardware catalogue: manufacturers and the models they make.
//
// One page rather than two lists. A manufacturer with no models is an entry
// somebody made and abandoned, and a model is meaningless without its maker --
// splitting them across two screens would mean nobody ever sees that pairing.

type cataloguePage struct {
	Base
	Errors        map[string]string
	Manufacturers []store.ManufacturerRow
	DeviceTypes   []store.DeviceTypeRow
	Lifecycles    []string
	// Spec and TypeSpec hold what was typed, so a refused form comes back with
	// it rather than blank.
	Spec     domain.ManufacturerSpec
	TypeSpec domain.DeviceTypeSpec
	// Editing is the id of the row opened for edit, from ?edit=<id>. One row at
	// a time: a table of input boxes is unreadable, and reading is what this
	// table is for.
	Editing string
}

// Catalogue shows the makers, the models and the forms to add either.
func (a *App) Catalogue(w http.ResponseWriter, r *http.Request) {
	a.renderCatalogue(w, r, http.StatusOK, nil, domain.ManufacturerSpec{}, domain.DeviceTypeSpec{})
}

func (a *App) renderCatalogue(w http.ResponseWriter, r *http.Request, status int,
	errs map[string]string, spec domain.ManufacturerSpec, typeSpec domain.DeviceTypeSpec) {

	makers, err := a.Store.ListManufacturers(r.Context(), false)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	types, err := a.Store.ListDeviceTypes(r.Context(), store.DeviceTypeFilter{})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Respond(w, r, status, "catalogue", "catalogue_panel", cataloguePage{
		Base:          a.base(r, "Hardware catalogue", "catalogue"),
		Errors:        orEmpty(errs),
		Manufacturers: makers,
		DeviceTypes:   types,
		Lifecycles:    domain.HardwareLifecycles,
		Spec:          spec,
		TypeSpec:      typeSpec,
		Editing:       r.URL.Query().Get("edit"),
	})
}

// ManufacturerCreate adds a maker.
func (a *App) ManufacturerCreate(w http.ResponseWriter, r *http.Request) {
	spec := domain.ManufacturerSpec{
		Code:       formValue(r, "code"),
		Name:       formValue(r, "name"),
		SupportRef: optional(formValue(r, "support_ref")),
		Lifecycle:  formValue(r, "lifecycle"),
	}
	m, err := domain.NewManufacturer(store.NewID(), spec, a.Store.Now())
	if err == nil {
		err = a.Store.CreateManufacturer(r.Context(), permit(r), m)
	}
	if err != nil {
		if errs, ok := validationErrors(err); ok {
			a.renderCatalogue(w, r, http.StatusUnprocessableEntity, errs, spec, domain.DeviceTypeSpec{})
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/catalogue")
}

// ManufacturerUpdate edits a maker.
func (a *App) ManufacturerUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := a.Store.GetManufacturer(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	updated := existing.Manufacturer
	updated.Code = formValue(r, "code")
	updated.Name = formValue(r, "name")
	updated.SupportRef = optional(formValue(r, "support_ref"))
	updated.Lifecycle = formValue(r, "lifecycle")
	updated.RowVersion = submittedVersion(r, updated.RowVersion)

	if err := a.Store.UpdateManufacturer(r.Context(), permit(r), &updated); err != nil {
		if errs, ok := validationErrors(err); ok {
			// Back to the row it refused, so a typo is one keystroke to fix
			// rather than a hunt through the table.
			a.renderCatalogueEditing(w, r, errs, id)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/catalogue")
}

// ManufacturerRetire takes a maker off the list.
func (a *App) ManufacturerRetire(w http.ResponseWriter, r *http.Request) {
	err := a.Store.RetireManufacturer(r.Context(), permit(r), r.PathValue("id"))
	if err != nil {
		// A maker with live models is refused, and the reason is worth saying
		// out loud rather than as a bare 409: it names what is in the way.
		a.setFlash(r, "error", "That manufacturer still has models catalogued. "+
			"Retire them first, or leave it as it is.")
		render.Redirect(w, r, "/catalogue")
		return
	}
	a.setFlash(r, "success", "Manufacturer retired.")
	render.Redirect(w, r, "/catalogue")
}

// DeviceTypeCreate catalogues a model.
func (a *App) DeviceTypeCreate(w http.ResponseWriter, r *http.Request) {
	nums := optionalNumbers(r)
	height := nums.opt("u_height")
	spec := domain.DeviceTypeSpec{
		ManufacturerID: formValue(r, "manufacturer_id"),
		Model:          formValue(r, "model"),
		PartNumber:     optional(formValue(r, "part_number")),
		UHeight:        height,
		FullDepth:      formValue(r, "full_depth") != "",
		DepthMM:        nums.opt("depth_mm"),
		WeightGrams:    nums.kilos("weight_kg"),
		Airflow:        optional(formValue(r, "airflow")),
		PortFace:       optional(formValue(r, "port_face")),
		EOLDate:        optional(formValue(r, "eol_date")),
		Notes:          optional(formValue(r, "notes")),
		Lifecycle:      formValue(r, "lifecycle"),
	}
	// Refused, not silently zeroed. A rack height that quietly became 0 would
	// put the model in every elevation calculation as occupying nothing, and a
	// depth that became 0 would report as fitting any cabinet.
	if msgs := nums.messages(); msgs != nil {
		a.renderCatalogue(w, r, http.StatusUnprocessableEntity,
			msgs, domain.ManufacturerSpec{}, spec)
		return
	}

	d, err := domain.NewDeviceType(store.NewID(), spec, a.Store.Now())
	if err == nil {
		err = a.Store.CreateDeviceType(r.Context(), permit(r), d)
	}
	if err != nil {
		if errs, ok := validationErrors(err); ok {
			a.renderCatalogue(w, r, http.StatusUnprocessableEntity, errs, domain.ManufacturerSpec{}, spec)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/catalogue")
}

// DeviceTypeUpdate edits a model.
//
// The manufacturer is not among the fields, and the store enforces that too: it
// decides what the model IS, and moving one between makers silently re-labels
// every asset of it.
func (a *App) DeviceTypeUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := a.Store.GetDeviceType(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	nums := optionalNumbers(r)
	height := nums.opt("u_height")
	depth := nums.opt("depth_mm")
	weight := nums.kilos("weight_kg")
	if msgs := nums.messages(); msgs != nil {
		a.renderCatalogueEditing(w, r, msgs, id)
		return
	}

	updated := existing.DeviceType
	updated.Model = formValue(r, "model")
	updated.PartNumber = optional(formValue(r, "part_number"))
	updated.UHeight = height
	updated.FullDepth = formValue(r, "full_depth") != ""
	updated.DepthMM = depth
	updated.WeightGrams = weight
	updated.Airflow = optional(formValue(r, "airflow"))
	updated.PortFace = optional(formValue(r, "port_face"))
	updated.EOLDate = optional(formValue(r, "eol_date"))
	updated.Notes = optional(formValue(r, "notes"))
	updated.Lifecycle = formValue(r, "lifecycle")
	updated.RowVersion = submittedVersion(r, updated.RowVersion)

	if err := a.Store.UpdateDeviceType(r.Context(), permit(r), &updated); err != nil {
		if errs, ok := validationErrors(err); ok {
			a.renderCatalogueEditing(w, r, errs, id)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/catalogue")
}

// DeviceTypeRetire takes a model off the catalogue.
//
// Allowed while assets still point at it. Retiring a model is how an estate
// records "we no longer buy these", and the boxes already racked are the reason
// somebody wanted to say so -- their inherited end-of-support date keeps
// resolving, which is the point.
func (a *App) DeviceTypeRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetireDeviceType(r.Context(), permit(r), r.PathValue("id")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Model retired. Assets of it keep their inherited support date.")
	render.Redirect(w, r, "/catalogue")
}

// renderCatalogueEditing re-renders with one row opened, carrying its errors.
func (a *App) renderCatalogueEditing(w http.ResponseWriter, r *http.Request,
	errs map[string]string, editing string) {

	makers, err := a.Store.ListManufacturers(r.Context(), false)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	types, err := a.Store.ListDeviceTypes(r.Context(), store.DeviceTypeFilter{})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Respond(w, r, http.StatusUnprocessableEntity, "catalogue", "catalogue_panel", cataloguePage{
		Base:          a.base(r, "Hardware catalogue", "catalogue"),
		Errors:        orEmpty(errs),
		Manufacturers: makers,
		DeviceTypes:   types,
		Lifecycles:    domain.HardwareLifecycles,
		Editing:       editing,
	})
}
