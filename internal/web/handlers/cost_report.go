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
)

// The estate-wide cost report.
//
// ITS OWN FILE BECAUSE THE FIRST HOME WAS WRONG. It was written into power.go
// for no better reason than that the power report lived there, and the manual's
// staleness checker caught it immediately: the `estate` fragment watches
// power.go, so an unrelated cost page marked the hardware documentation stale.
// A misfiled handler is usually harmless; here something measures it.

// CostReport totals what the estate costs, and what it could not price.
//
// GATED BEHIND CanSeeCosts, like every other money page (see SupplierReport
// below and the cost-write routes in routes.go). An Observer or project owner
// without app_user.can_see_costs gets the "Not for you" panel
// cost_report.html renders instead -- the same panel and the same wording
// supplier_report.html already used, kept identical on purpose rather than
// reworded per page. The estate-wide query is skipped entirely when the
// viewer cannot see its result: computing a cost report nobody granted may
// look at is wasted work, and not fetching money you may not show is the
// safer default.
func (a *App) CostReport(w http.ResponseWriter, r *http.Request) {
	base := a.base(r, "What it costs", "cost-report")

	var report *store.EstateCostReport
	var power domain.PowerEstimate
	if base.CanSeeCosts {
		var err error
		report, err = a.Store.EstateCosts(r.Context(), a.Store.Now())
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		// THE DRAW IS ONLY QUERIED WHEN THERE IS A RATE TO APPLY. With no
		// tariff the section renders one sentence saying so (D5) and no
		// figures at all, so scanning the estate for a draw nobody can price
		// is the same waste as computing EstateCosts for a viewer who may not
		// see it -- which is why that fetch is already conditional above.
		if tariff := a.powerTariff(); tariff > 0 {
			draw, err := a.Store.DeclaredPowerDraw(r.Context())
			if err != nil {
				a.serverError(w, r, err)
				return
			}
			power = domain.PowerEstimate{Draw: draw, TariffHundredthsMinorPerKWh: tariff, PUEHundredths: a.powerPUE()}
		}
	}
	a.Render.Respond(w, r, http.StatusOK, "cost_report", "cost_report", costReportPage{
		Base:   base,
		Report: report,
		Power:  power,
	})
}

type costReportPage struct {
	Base
	Report *store.EstateCostReport
	// Power is the ESTIMATED electricity figure, and it is a separate field
	// rather than a surface inside Report on purpose: EstateCostReport totals
	// what somebody PRICED, and a derived figure inside it would make that
	// total part-declared and part-derived with no way to tell which
	// (docs/power-cost-design.md §2.4). The zero value is the unconfigured
	// state and renders D5's explanation, never nothing.
	Power domain.PowerEstimate
}

// powerTariff is the configured electricity rate, or zero.
//
// Guarded against a nil Config because the failure mode matters: App.Config is
// set by every real construction and by both test harnesses, so nil is a
// programming error -- but the page it would take down is otherwise fine, and
// "no tariff configured" is a truthful answer to give while somebody fixes the
// wiring. A panic here would lose the estate totals as well.
func (a *App) powerTariff() int64 {
	if a.Config == nil {
		return 0
	}
	return a.Config.PowerTariffHundredthsMinorPerKWh
}

// powerPUE is the operator-declared facility PUE (D6), or zero when nothing
// was declared. Same nil-Config guard as powerTariff, and the same reasoning:
// App.Config is set by every real construction, nil is a programming error,
// and "no PUE declared" is a truthful thing to say while somebody fixes it.
func (a *App) powerPUE() int64 {
	if a.Config == nil {
		return 0
	}
	return a.Config.PowerPUEHundredths
}

// SupplierReport answers the third of the CEO's questions: which suppliers raise
// prices beyond inflation (WP-J6).
//
// Behind the cost permission like every other money page. A reader who cannot
// see a rack's price cannot see it ranked by supplier either. The movement
// query itself is skipped for a viewer without the grant, for the same reason
// CostReport now skips EstateCosts: the template was already refusing to
// render it, so running it first was pure waste.
func (a *App) SupplierReport(w http.ResponseWriter, r *http.Request) {
	base := a.base(r, "Suppliers", "supplier-report")

	var report *store.SupplierReport
	if base.CanSeeCosts {
		var err error
		report, err = a.Store.SupplierMovements(r.Context(), "")
		if err != nil {
			a.serverError(w, r, err)
			return
		}
	}
	a.Render.Respond(w, r, http.StatusOK, "supplier_report", "supplier_report",
		supplierReportPage{
			Base:   base,
			Report: report,
		})
}

type supplierReportPage struct {
	Base
	Report *store.SupplierReport
}
