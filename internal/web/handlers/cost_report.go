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
	if base.CanSeeCosts {
		var err error
		report, err = a.Store.EstateCosts(r.Context(), a.Store.Now())
		if err != nil {
			a.serverError(w, r, err)
			return
		}
	}
	a.Render.Respond(w, r, http.StatusOK, "cost_report", "cost_report", costReportPage{
		Base:   base,
		Report: report,
	})
}

type costReportPage struct {
	Base
	Report *store.EstateCostReport
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
