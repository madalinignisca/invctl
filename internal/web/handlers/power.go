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

// The power chain's screens. See internal/store/power_findings.go for what they
// are ultimately in aid of.

type powerPage struct {
	Base
	Errors     map[string]string
	Sources    []store.PowerSourceRow
	Kinds      []string
	Panels     []store.PowerPanelRow
	Feeds      []store.PowerFeedRow
	Sites      []store.AssetRow
	Phases     []string
	Lifecycles []string
	PanelSpec  domain.PowerPanelSpec
	FeedSpec   domain.PowerFeedSpec
	SourceSpec domain.PowerSourceSpec
	Editing    string
}

type powerReportPage struct {
	Base
	Report *store.PowerReport
}

// Power lists panels and feeds, and the forms to add either.
func (a *App) Power(w http.ResponseWriter, r *http.Request) {
	a.renderPower(w, r, http.StatusOK, nil, domain.PowerPanelSpec{}, domain.PowerFeedSpec{})
}

func (a *App) renderPower(w http.ResponseWriter, r *http.Request, status int,
	errs map[string]string, panelSpec domain.PowerPanelSpec, feedSpec domain.PowerFeedSpec) {

	panels, err := a.Store.ListPowerPanels(r.Context(), false)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	sources, err := a.Store.ListPowerSources(r.Context(), false)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	feeds, err := a.Store.ListPowerFeeds(r.Context(), store.PowerFeedFilter{})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	// Panels hang off a location, and the containment tree already knows where
	// things are. Offering every asset would be offering a VM as a place to put
	// a distribution board.
	sites, err := a.Store.ListAssets(r.Context(), store.AssetFilter{Kind: domain.KindSite})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Respond(w, r, status, "power", "power_panel_list", powerPage{
		Base:       a.base(r, "Power", "power"),
		Errors:     orEmpty(errs),
		Panels:     panels,
		Sources:    sources,
		Kinds:      domain.SourceKinds,
		Feeds:      feeds,
		Sites:      sites,
		Phases:     domain.Phases,
		Lifecycles: domain.PowerLifecycles,
		PanelSpec:  panelSpec,
		FeedSpec:   feedSpec,
		Editing:    r.URL.Query().Get("edit"),
	})
}

// PowerReport is the findings page: what the chain says is wrong.
func (a *App) PowerReport(w http.ResponseWriter, r *http.Request) {
	report, err := a.Store.PowerFindings(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Respond(w, r, http.StatusOK, "power_report", "power_findings", powerReportPage{
		Base:   a.base(r, "Power findings", "power-report"),
		Report: report,
	})
}

// PowerPanelCreate adds a distribution board.
func (a *App) PowerPanelCreate(w http.ResponseWriter, r *http.Request) {
	spec, ok := a.panelSpecFrom(w, r)
	if !ok {
		return
	}
	p, err := domain.NewPowerPanel(store.NewID(), spec, a.Store.Now())
	if err == nil {
		err = a.Store.CreatePowerPanel(r.Context(), permit(r), p)
	}
	if err != nil {
		if errs, ok := validationErrors(err); ok {
			a.renderPower(w, r, http.StatusUnprocessableEntity, errs, spec, domain.PowerFeedSpec{})
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/power")
}

// panelSpecFrom reads the form, refusing a rating that is not a number rather
// than storing it as nothing.
func (a *App) panelSpecFrom(w http.ResponseWriter, r *http.Request) (domain.PowerPanelSpec, bool) {
	volts, vOK := optionalInt(r, "voltage")
	amps, aOK := optionalInt(r, "amperage")
	spec := domain.PowerPanelSpec{
		SiteID:   formValue(r, "site_id"),
		SourceID: optional(formValue(r, "source_id")),
		Name:     formValue(r, "name"),
		Rating:   domain.Rating{Voltage: volts, Amperage: amps, Phase: optional(formValue(r, "phase"))},
		Notes:    optional(formValue(r, "notes")),
	}
	if !vOK || !aOK {
		field := "voltage"
		if vOK {
			field = "amperage"
		}
		a.renderPower(w, r, http.StatusUnprocessableEntity, notANumber(field), spec, domain.PowerFeedSpec{})
		return spec, false
	}
	return spec, true
}

// PowerSourceCreate adds a supply: a UPS, a generator, a transfer switch.
func (a *App) PowerSourceCreate(w http.ResponseWriter, r *http.Request) {
	spec := domain.PowerSourceSpec{
		SiteID:   formValue(r, "site_id"),
		ParentID: optional(formValue(r, "parent_id")),
		AssetID:  optional(formValue(r, "asset_id")),
		Name:     formValue(r, "name"),
		Kind:     formValue(r, "kind"),
		Notes:    optional(formValue(r, "notes")),
	}
	src, err := domain.NewPowerSource(store.NewID(), spec, a.Store.Now())
	if err == nil {
		err = a.Store.CreatePowerSource(r.Context(), permit(r), src)
	}
	if err != nil {
		if errs, ok := validationErrors(err); ok {
			a.renderPowerWithSource(w, r, errs, spec)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/power")
}

// PowerSourceImpact simulates losing a supply: a UPS group, a generator.
//
// Same shape as losing a feed, and deliberately so: it resolves and redirects
// into the ordinary impact page rather than rendering a second one, and a supply
// that takes nothing down says so instead of showing an empty result.
func (a *App) PowerSourceImpact(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	src, err := a.Store.GetPowerSource(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	down, err := a.Store.AssetsLosingSupply(r.Context(), []string{id})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if len(down) == 0 {
		a.setFlash(r, "success", "Nothing loses power if "+src.Name+
			" fails: every asset below it has another live input elsewhere.")
		render.Redirect(w, r, "/power")
		return
	}
	render.Redirect(w, r, impactURL(down, 180))
}

// PowerSourceRetire withdraws a supply.
func (a *App) PowerSourceRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetirePowerSource(r.Context(), permit(r), r.PathValue("id")); err != nil {
		a.setFlash(r, "error", "That supply still feeds panels or other supplies. "+
			"Move them first — otherwise the chain behind them could no longer be traced.")
		render.Redirect(w, r, "/power")
		return
	}
	a.setFlash(r, "success", "Supply withdrawn.")
	render.Redirect(w, r, "/power")
}

func (a *App) renderPowerWithSource(w http.ResponseWriter, r *http.Request,
	errs map[string]string, spec domain.PowerSourceSpec) {

	// Rendered through the same assembly as everything else on this page, so a
	// refused supply form comes back with the panels and feeds still on screen.
	sources, err := a.Store.ListPowerSources(r.Context(), false)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	panels, err := a.Store.ListPowerPanels(r.Context(), false)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	feeds, err := a.Store.ListPowerFeeds(r.Context(), store.PowerFeedFilter{})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	sites, err := a.Store.ListAssets(r.Context(), store.AssetFilter{Kind: domain.KindSite})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Respond(w, r, http.StatusUnprocessableEntity, "power", "power_panel_list", powerPage{
		Base:       a.base(r, "Power", "power"),
		Errors:     orEmpty(errs),
		Panels:     panels,
		Sources:    sources,
		Kinds:      domain.SourceKinds,
		Feeds:      feeds,
		Sites:      sites,
		Phases:     domain.Phases,
		Lifecycles: domain.PowerLifecycles,
		SourceSpec: spec,
	})
}

// PowerPanelUpdate corrects a board, including what feeds it.
//
// Written after the supply layer landed and it became obvious that create and
// retire were not enough: every panel recorded before 00024 had no supply and
// no way to gain one short of retiring and re-entering it, which would have
// thrown away its audit history to fix a field.
func (a *App) PowerPanelUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := a.Store.GetPowerPanel(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	volts, vOK := optionalInt(r, "voltage")
	amps, aOK := optionalInt(r, "amperage")
	if !vOK || !aOK {
		a.setFlash(r, "error", "The rating has to be whole numbers, or left empty.")
		render.Redirect(w, r, "/power")
		return
	}

	updated := existing.PowerPanel
	updated.Name = formValue(r, "name")
	// submittedString, not optionalString: a picker that failed to render must
	// not read as an operator clearing the field -- and clearing this one would
	// silently detach a board from its UPS, which is exactly the state the
	// findings cannot see through.
	updated.SourceID = submittedString(r, "source_id", updated.SourceID)
	updated.Voltage, updated.Amperage = volts, amps
	updated.Phase = optional(formValue(r, "phase"))
	updated.Notes = optional(formValue(r, "notes"))
	updated.RowVersion = submittedVersion(r, updated.RowVersion)

	if err := a.Store.UpdatePowerPanel(r.Context(), permit(r), &updated); err != nil {
		if messages, ok := validationErrors(err); ok {
			a.setFlash(r, "error", "That panel was not accepted: "+joinMessages(messages))
			render.Redirect(w, r, "/power")
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/power")
}

// PowerPanelRetire takes a panel out of service.
func (a *App) PowerPanelRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetirePowerPanel(r.Context(), permit(r), r.PathValue("id")); err != nil {
		// A panel with live feeds is refused, and the reason is worth a sentence
		// rather than a bare 409: it names what is in the way.
		a.setFlash(r, "error", "That panel still carries feeds. Retire them first.")
		render.Redirect(w, r, "/power")
		return
	}
	a.setFlash(r, "success", "Panel retired.")
	render.Redirect(w, r, "/power")
}

// PowerFeedCreate adds a circuit off a panel.
func (a *App) PowerFeedCreate(w http.ResponseWriter, r *http.Request) {
	volts, vOK := optionalInt(r, "voltage")
	amps, aOK := optionalInt(r, "amperage")
	util, uOK := intValue(r, "max_utilisation", domain.DefaultMaxUtilisation)
	spec := domain.PowerFeedSpec{
		PanelID:        formValue(r, "panel_id"),
		Name:           formValue(r, "name"),
		Rating:         domain.Rating{Voltage: volts, Amperage: amps, Phase: optional(formValue(r, "phase"))},
		MaxUtilisation: util,
		Notes:          optional(formValue(r, "notes")),
	}
	if !vOK || !aOK || !uOK {
		field := "voltage"
		switch {
		case vOK && !aOK:
			field = "amperage"
		case vOK && aOK:
			field = "max_utilisation"
		}
		a.renderPower(w, r, http.StatusUnprocessableEntity, notANumber(field),
			domain.PowerPanelSpec{}, spec)
		return
	}

	f, err := domain.NewPowerFeed(store.NewID(), spec, a.Store.Now())
	if err == nil {
		err = a.Store.CreatePowerFeed(r.Context(), permit(r), f)
	}
	if err != nil {
		if errs, ok := validationErrors(err); ok {
			a.renderPower(w, r, http.StatusUnprocessableEntity, errs, domain.PowerPanelSpec{}, spec)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/power")
}

// PowerFeedRetire withdraws a circuit.
func (a *App) PowerFeedRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetirePowerFeed(r.Context(), permit(r), r.PathValue("id")); err != nil {
		a.setFlash(r, "error", "That feed still carries inputs. Disconnect them first — "+
			"otherwise the assets on it would claim power from a circuit this model says is gone.")
		render.Redirect(w, r, "/power")
		return
	}
	a.setFlash(r, "success", "Feed withdrawn.")
	render.Redirect(w, r, "/power")
}

// PowerFeedImpact simulates losing a feed.
//
// It resolves and REDIRECTS into the ordinary impact page rather than rendering
// a second one. The engine has always taken a set of down assets, so "this feed
// fails" is a question about which assets that is -- and the answer belongs on
// the screen that already explains outages, with the same window control and the
// same wording. A second impact view would be a second place for the two to
// disagree.
func (a *App) PowerFeedImpact(w http.ResponseWriter, r *http.Request) {
	feedID := r.PathValue("id")
	feed, err := a.Store.GetPowerFeed(r.Context(), feedID)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	down, err := a.Store.AssetsLosingPower(r.Context(), []string{feedID})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if len(down) == 0 {
		// NOT an empty impact page. "Nothing breaks" and "nothing loses power in
		// the first place" are different answers, and rendering the first for the
		// second is the most dangerous thing this tool can say. A feed with
		// inputs that are all on redundant assets genuinely takes nothing down,
		// and saying so is the point.
		a.setFlash(r, "success", "Nothing loses power if "+feed.PanelName+" / "+feed.Name+
			" fails: every asset on it has another live input.")
		render.Redirect(w, r, "/power")
		return
	}
	render.Redirect(w, r, impactURL(down, 180))
}

// PowerInputCreate plugs an asset into a feed.
func (a *App) PowerInputCreate(w http.ResponseWriter, r *http.Request) {
	assetID := r.PathValue("id")
	draw, numeric := optionalInt(r, "draw_va")
	if !numeric {
		a.setFlash(r, "error", "The draw has to be a whole number of volt-amps, or left empty.")
		render.Redirect(w, r, "/assets/"+assetID)
		return
	}
	i, err := domain.NewPowerInput(store.NewID(), domain.PowerInputSpec{
		AssetID: assetID,
		FeedID:  formValue(r, "feed_id"),
		Name:    formValue(r, "name"),
		DrawVA:  draw,
		Notes:   optional(formValue(r, "notes")),
	}, a.Store.Now())
	if err == nil {
		err = a.Store.CreatePowerInput(r.Context(), permit(r), i)
	}
	if err != nil {
		if messages, ok := validationErrors(err); ok {
			// Flashed rather than re-rendered into the form partial, the same
			// trade the cost rows make: the asset page is assembled from a dozen
			// queries and the input row is three short fields. The message names
			// the field either way.
			a.setFlash(r, "error", "That power input was not accepted: "+joinMessages(messages))
			render.Redirect(w, r, "/assets/"+assetID)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/assets/"+assetID)
}

// PowerInputRetire unplugs one.
func (a *App) PowerInputRetire(w http.ResponseWriter, r *http.Request) {
	assetID := r.PathValue("id")
	if err := a.Store.RetirePowerInput(r.Context(), permit(r), r.PathValue("inputID")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Power input disconnected.")
	render.Redirect(w, r, "/assets/"+assetID)
}
