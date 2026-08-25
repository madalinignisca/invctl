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

// The ownership report (WP-G7, piece 1): what has no owner, or an owner who
// cannot act. docs/ownership-report-design.md.
//
// READ-ONLY, like every other report in this package -- no CSRF, no
// RequireAdmin, nothing on this page can change the estate. Bulk assignment
// and the retirement-flow offer are pieces 2 and 3 of the same work package
// and are not in this file.
//
// NOT COST-GATED, unlike CostReport and SupplierReport. Who owns what is not
// money, and gating it behind CanSeeCosts would hide a finding from exactly
// the reader most likely to be first on the scene of it.

// OwnershipReport shows what has no owner, or an owner who cannot act.
func (a *App) OwnershipReport(w http.ResponseWriter, r *http.Request) {
	report, err := a.Store.OwnershipFindings(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Respond(w, r, http.StatusOK, "ownership_report", "ownership_report",
		ownershipReportPage{
			Base:   a.base(r, "Ownership", "ownership-report"),
			Report: report,
		})
}

type ownershipReportPage struct {
	Base
	Report *store.OwnershipReport
}
