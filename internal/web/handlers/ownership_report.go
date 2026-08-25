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
// OwnershipReport ITSELF IS STILL READ-ONLY, like every other report in this
// package -- no CSRF, no RequireAdmin, nothing this handler does can change
// the estate. The fix it now offers inline (WP-G7 piece 3, design §6) lives
// in ownership_assign.go's two admin-only routes, wired separately in
// routes.go under the write bucket -- exactly the split piece 2's retirement
// flow already uses between TeamRetireConfirm (read) and
// TeamReassignAndRetire (write).
//
// NOT COST-GATED, unlike CostReport and SupplierReport. Who owns what is not
// money, and gating it behind CanSeeCosts would hide a finding from exactly
// the reader most likely to be first on the scene of it.

// ownershipEntityOrder is the WP-G7 ownership surface (design §3), in the
// order the Unowned section groups them -- matching the sort unownedRows
// already applies (entity_type, then name), so the bulk-assignment groups
// appear in the same order the flat list used to.
var ownershipEntityOrder = []string{"asset", "service", "project", "identity", "custom_field"}

// ownershipEntityLabels names each group for its heading and its confirm
// message ("assign 12 ASSETS to Network Ops").
var ownershipEntityLabels = map[string]string{
	"asset": "assets", "service": "services", "project": "projects",
	"identity": "identities", "custom_field": "custom fields",
}

// ownershipEntityGroup is one entity type's slice of the Unowned finding,
// with its own checkbox list and its own team target -- the interaction
// model design §6 asks for: "these twelve unowned ASSETS go to Network Ops",
// never a single picker spanning every type on the page.
type ownershipEntityGroup struct {
	EntityType  string
	EntityLabel string
	Rows        []store.OwnershipRow
}

// unownedGroups partitions the Unowned finding by entity type -- the read
// model piece 1 already computed, just regrouped for the bulk-assignment
// form rather than a second query.
func unownedGroups(rows []store.OwnershipRow) []ownershipEntityGroup {
	byType := make(map[string][]store.OwnershipRow, len(ownershipEntityOrder))
	for _, r := range rows {
		byType[r.EntityType] = append(byType[r.EntityType], r)
	}
	var out []ownershipEntityGroup
	for _, et := range ownershipEntityOrder {
		if rs, ok := byType[et]; ok {
			out = append(out, ownershipEntityGroup{EntityType: et, EntityLabel: ownershipEntityLabels[et], Rows: rs})
		}
	}
	return out
}

// OwnershipReport shows what has no owner, or an owner who cannot act, and
// (WP-G7 piece 3) offers a bulk fix for the Unowned finding, grouped by
// entity type.
func (a *App) OwnershipReport(w http.ResponseWriter, r *http.Request) {
	report, err := a.Store.OwnershipFindings(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	// Only fetched when there is something to assign: an estate with no
	// unowned entities needs no team picker, matching TeamRetireConfirm's own
	// rule for a zero-count team (design §5).
	var targets []store.TeamRow
	if len(report.Unowned) > 0 {
		targets, err = a.Store.TeamOptions(r.Context())
		if err != nil {
			a.serverError(w, r, err)
			return
		}
	}

	a.Render.Respond(w, r, http.StatusOK, "ownership_report", "ownership_report",
		ownershipReportPage{
			Base:          a.base(r, "Ownership", "ownership-report"),
			Report:        report,
			UnownedGroups: unownedGroups(report.Unowned),
			Targets:       targets,
		})
}

type ownershipReportPage struct {
	Base
	Report        *store.OwnershipReport
	UnownedGroups []ownershipEntityGroup
	Targets       []store.TeamRow
}
