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
	// Edit carries a refused correction of a panel, feed or supply row, so
	// that row reopens with what was typed rather than what is stored. Base's
	// EditRow (set from Edit.ID below, the same pattern renderAssetDetail
	// uses) decides WHICH row's editing block renders; Edit itself is what
	// that block reads its field values and errors from.
	Edit *editState
}

type powerReportPage struct {
	Base
	Report *store.PowerReport
}

// Power lists panels and feeds, and the forms to add either.
func (a *App) Power(w http.ResponseWriter, r *http.Request) {
	a.renderPower(w, r, http.StatusOK, nil, domain.PowerPanelSpec{}, domain.PowerFeedSpec{}, nil)
}

func (a *App) renderPower(w http.ResponseWriter, r *http.Request, status int,
	errs map[string]string, panelSpec domain.PowerPanelSpec, feedSpec domain.PowerFeedSpec, edit *editState) {

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
	base := a.base(r, "Power", "power")
	if edit != nil {
		// The refused row opens, whatever the query string said -- the same
		// rule renderAssetDetail and renderEnvironmentsWith follow.
		base.EditRow = edit.ID
	}
	a.Render.Respond(w, r, status, "power", "power_panel_list", powerPage{
		Base:       base,
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
		Edit:       edit,
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
		err = a.Store.CreatePowerPanel(r.Context(), a.permit(r), p)
	}
	if err != nil {
		if errs, ok := validationErrors(err); ok {
			a.renderPower(w, r, http.StatusUnprocessableEntity, errs, spec, domain.PowerFeedSpec{}, nil)
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
		a.renderPower(w, r, http.StatusUnprocessableEntity, notANumber(field), spec, domain.PowerFeedSpec{}, nil)
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
		err = a.Store.CreatePowerSource(r.Context(), a.permit(r), src)
	}
	if err != nil {
		if errs, ok := validationErrors(err); ok {
			a.renderPowerWithSource(w, r, errs, spec, nil)
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
	if err := a.Store.RetirePowerSource(r.Context(), a.permit(r), r.PathValue("id")); err != nil {
		a.setFlash(r, "error", "That supply still feeds panels or other supplies. "+
			"Move them first — otherwise the chain behind them could no longer be traced.")
		render.Redirect(w, r, "/power")
		return
	}
	a.setFlash(r, "success", "Supply withdrawn.")
	render.Redirect(w, r, "/power")
}

func (a *App) renderPowerWithSource(w http.ResponseWriter, r *http.Request,
	errs map[string]string, spec domain.PowerSourceSpec, edit *editState) {

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
	base := a.base(r, "Power", "power")
	if edit != nil {
		base.EditRow = edit.ID
	}
	a.Render.Respond(w, r, http.StatusUnprocessableEntity, "power", "power_panel_list", powerPage{
		Base:       base,
		Errors:     orEmpty(errs),
		Panels:     panels,
		Sources:    sources,
		Kinds:      domain.SourceKinds,
		Feeds:      feeds,
		Sites:      sites,
		Phases:     domain.Phases,
		Lifecycles: domain.PowerLifecycles,
		SourceSpec: spec,
		Edit:       edit,
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
		field := "voltage"
		if vOK {
			field = "amperage"
		}
		a.renderPower(w, r, http.StatusUnprocessableEntity, nil, domain.PowerPanelSpec{}, domain.PowerFeedSpec{},
			rejected(r, id, notANumber(field), "name", "voltage", "amperage", "phase", "source_id"))
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

	if err := a.Store.UpdatePowerPanel(r.Context(), a.permit(r), &updated); err != nil {
		if messages, ok := validationErrors(err); ok {
			// 422 with the row reopened on what was typed, not a redirect that
			// throws it away -- the house rule (CLAUDE.md), and renderPower
			// already exists to do it: a refused save of any row on this page
			// comes back through the same assembly rather than a second one.
			// Errors is nil, not messages: that map feeds the "Add a panel"
			// CREATE form at the top of the page, and this is a correction of
			// an existing row -- Edit below is what the row itself reads.
			a.renderPower(w, r, refusalStatus(err), nil, domain.PowerPanelSpec{}, domain.PowerFeedSpec{},
				rejected(r, id, messages, "name", "voltage", "amperage", "phase", "source_id"))
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/power")
}

// PowerPanelRetire takes a panel out of service.
func (a *App) PowerPanelRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetirePowerPanel(r.Context(), a.permit(r), r.PathValue("id")); err != nil {
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
			domain.PowerPanelSpec{}, spec, nil)
		return
	}

	f, err := domain.NewPowerFeed(store.NewID(), spec, a.Store.Now())
	if err == nil {
		err = a.Store.CreatePowerFeed(r.Context(), a.permit(r), f)
	}
	if err != nil {
		if errs, ok := validationErrors(err); ok {
			a.renderPower(w, r, http.StatusUnprocessableEntity, errs, domain.PowerPanelSpec{}, spec, nil)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/power")
}

// PowerFeedRetire withdraws a circuit.
func (a *App) PowerFeedRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetirePowerFeed(r.Context(), a.permit(r), r.PathValue("id")); err != nil {
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

// powerInputCreateEditID names the sentinel editState.ID an "add an input"
// refusal carries, distinct from an existing power input's own row id (which
// rides the ?power= query parameter, PowerEdit -- see renderAssetDetail) and
// from every other sentinel and row id sharing this page's edit mechanism.
// Never a real row id: those are UUIDv7, this is not.
func powerInputCreateEditID(assetID string) string {
	return "power-input-new:" + assetID
}

// PowerInputCreate plugs an asset into a feed.
func (a *App) PowerInputCreate(w http.ResponseWriter, r *http.Request) {
	assetID := r.PathValue("id")
	draw, numeric := optionalInt(r, "draw_va")
	if !numeric {
		a.renderAssetDetail(w, r, http.StatusUnprocessableEntity, assetID,
			rejected(r, powerInputCreateEditID(assetID), notANumber("draw_va"), "name", "feed_id", "draw_va"))
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
		err = a.Store.CreatePowerInput(r.Context(), a.permit(r), i)
	}
	if err != nil {
		if messages, ok := validationErrors(err); ok {
			// 422 with the create form reopened on what was typed, the house
			// rule (CLAUDE.md): a redirect throws the operator's input away and
			// reports a success-shaped status for a refusal. renderAssetDetail
			// already exists and is already used for every other refusal on
			// this page, so there is no second page assembly to keep in step.
			a.renderAssetDetail(w, r, refusalStatus(err), assetID,
				rejected(r, powerInputCreateEditID(assetID), messages, "name", "feed_id", "draw_va"))
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
	if err := a.Store.RetirePowerInput(r.Context(), a.permit(r), r.PathValue("inputID")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Power input disconnected.")
	render.Redirect(w, r, "/assets/"+assetID)
}

// PowerFeedUpdate corrects a feed's rating.
//
// THE RATING IS THE DENOMINATOR OF EVERY CAPACITY FINDING ON THAT BOARD. A
// mistyped amperage does not read as wrong -- it reads as a feed that is
// comfortably loaded, or as one that is over, and the findings page states it
// with the same confidence either way. Until this route existed the only fix
// was to retire the feed, and the inputs hang off the feed, so correcting one
// digit meant disconnecting every asset on it and reconnecting them by hand.
//
// PanelID IS ABSENT ON PURPOSE, not forgotten: UpdatePowerFeed pins it from
// the stored row (`f.PanelID = before.PanelID`), so a form offering to move a
// feed between boards would be offered-and-silently-ignored. A feed that is on
// the wrong panel is a different act -- withdraw it and declare it where it
// belongs, because its inputs were plugged into the wrong board too.
func (a *App) PowerFeedUpdate(w http.ResponseWriter, r *http.Request) {
	existing, err := a.Store.GetPowerFeed(r.Context(), r.PathValue("id"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	updated := existing.PowerFeed
	volts, vOK := optionalInt(r, "voltage")
	amps, aOK := optionalInt(r, "amperage")
	util, uOK := intValue(r, "max_utilisation", updated.MaxUtilisation)
	if !vOK || !aOK || !uOK {
		field := "voltage"
		switch {
		case vOK && !aOK:
			field = "amperage"
		case vOK && aOK:
			field = "max_utilisation"
		}
		a.renderPower(w, r, http.StatusUnprocessableEntity, nil, domain.PowerPanelSpec{}, domain.PowerFeedSpec{},
			rejected(r, existing.ID, notANumber(field), "name", "voltage", "amperage", "phase", "max_utilisation"))
		return
	}
	updated.Name = formValue(r, "name")
	updated.Voltage, updated.Amperage = volts, amps
	updated.Phase = optional(formValue(r, "phase"))
	updated.MaxUtilisation = util
	updated.Notes = optional(formValue(r, "notes"))
	updated.RowVersion = submittedVersion(r, updated.RowVersion)

	if err := a.Store.UpdatePowerFeed(r.Context(), a.permit(r), &updated); err != nil {
		if messages, ok := refusalMessages(err, map[string]string{
			"name": "that panel already has a feed by that name",
		}); ok {
			// 422 with the row reopened on what was typed -- see PowerPanelUpdate.
			a.renderPower(w, r, refusalStatus(err), nil, domain.PowerPanelSpec{}, domain.PowerFeedSpec{},
				rejected(r, existing.ID, messages, "name", "voltage", "amperage", "phase", "max_utilisation"))
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Feed "+updated.Name+" updated.")
	render.Redirect(w, r, "/power")
}

// PowerSourceUpdate corrects a supply's kind, its catalogue link, or what feeds
// it.
//
// THE PARENT IS THE FIELD THAT MATTERS. It is what decides whether two boards
// are genuinely independent, and a supply entered with no parent -- or with the
// wrong one -- makes an A/B pair look redundant when one UPS carries both. That
// is the single question the power chain exists to answer, and it was the one
// field nobody could fix.
//
// SiteID is pinned by the store the way PowerFeedUpdate's PanelID is, and is
// left out of the form for the same reason. The parent is guarded there too:
// requireNoSupplyCycle refuses a chain that feeds itself, so this handler does
// not have to re-derive it.
func (a *App) PowerSourceUpdate(w http.ResponseWriter, r *http.Request) {
	existing, err := a.Store.GetPowerSource(r.Context(), r.PathValue("id"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	updated := existing.PowerSource
	updated.Name = formValue(r, "name")
	updated.Kind = formValue(r, "kind")
	// submittedString, not optionalString, and for PowerPanelUpdate's reason: a
	// picker that failed to render must not read as an operator detaching this
	// supply from the one that feeds it, which is precisely the state that
	// makes two dependent boards look independent.
	updated.ParentID = submittedString(r, "parent_id", updated.ParentID)
	updated.AssetID = submittedString(r, "asset_id", updated.AssetID)
	updated.Notes = optional(formValue(r, "notes"))
	updated.RowVersion = submittedVersion(r, updated.RowVersion)

	if err := a.Store.UpdatePowerSource(r.Context(), a.permit(r), &updated); err != nil {
		if messages, ok := refusalMessages(err, map[string]string{
			"name": "that site already has a supply by that name",
		}); ok {
			// 422 with the row reopened on what was typed -- see PowerPanelUpdate.
			// renderPower, not renderPowerWithSource: that one exists to carry a
			// refused CREATE form's typed values (SourceSpec), and this is a
			// correction of a row that already exists -- Edit is what it reads.
			a.renderPower(w, r, refusalStatus(err), nil, domain.PowerPanelSpec{}, domain.PowerFeedSpec{},
				rejected(r, existing.ID, messages, "name", "kind", "parent_id"))
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Supply "+updated.Name+" updated.")
	render.Redirect(w, r, "/power")
}

// PowerInputUpdate corrects a declared draw, or moves an asset onto the feed it
// is actually plugged into.
//
// RECORDED AGAINST WP-I2 AND LEFT OPEN: its review noted that "correcting a
// number means Disconnect-and-re-add", which made D7's convergence claim --
// that declared draw improves as operators refine it -- depend on a UI that had
// no way to refine anything. A figure could be entered once and never adjusted.
// The cost report is built on this column, so a mistyped nameplate propagated
// into money.
//
// AssetID is pinned by the store; feed_id is not, and is offered, because
// "this is plugged into the other feed" is a correction rather than a move --
// the asset has not gone anywhere.
func (a *App) PowerInputUpdate(w http.ResponseWriter, r *http.Request) {
	assetID := r.PathValue("id")
	existing, err := a.Store.GetPowerInput(r.Context(), r.PathValue("inputID"))
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	rowID := r.PathValue("inputID")
	draw, numeric := optionalInt(r, "draw_va")
	if !numeric {
		a.renderAssetDetail(w, r, http.StatusUnprocessableEntity, assetID,
			rejected(r, rowID, notANumber("draw_va"), "name", "feed_id", "draw_va"))
		return
	}
	updated := existing.PowerInput
	updated.Name = formValue(r, "name")
	// The same rule as submittedString, spelled out because feed_id is a
	// required column rather than a nullable one: an absent or empty picker
	// means "not rendered", never "detach this asset from its feed". There is
	// no such thing as an input that draws from nothing, and the store would
	// refuse it anyway -- this keeps the refusal from ever being reached by a
	// form that simply failed to draw its options.
	if v := formValue(r, "feed_id"); v != "" {
		updated.FeedID = v
	}
	updated.DrawVA = draw
	updated.Notes = optional(formValue(r, "notes"))
	updated.RowVersion = submittedVersion(r, updated.RowVersion)

	if err := a.Store.UpdatePowerInput(r.Context(), a.permit(r), &updated); err != nil {
		if messages, ok := refusalMessages(err, map[string]string{
			"name": "that asset already has an input by that name",
		}); ok {
			// 422 with the row reopened on what was typed -- the house rule,
			// and the reason for it: a redirect refills the row from storage,
			// so the field the operator just corrected shows the old value
			// back and nothing says whether it saved.
			a.renderAssetDetail(w, r, refusalStatus(err), assetID,
				rejected(r, rowID, messages, "name", "feed_id", "draw_va"))
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Power input updated.")
	render.Redirect(w, r, "/assets/"+assetID)
}
