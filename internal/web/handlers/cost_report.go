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
func (a *App) CostReport(w http.ResponseWriter, r *http.Request) {
	report, err := a.Store.EstateCosts(r.Context(), a.Store.Now())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Respond(w, r, http.StatusOK, "cost_report", "cost_report", costReportPage{
		Base:   a.base(r, "What it costs", "cost-report"),
		Report: report,
	})
}

type costReportPage struct {
	Base
	Report *store.EstateCostReport
}
