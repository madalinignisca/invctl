// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Task 4a (9da0919) gated every cost WRITE behind CanSeeCosts. Nothing gated
// the READ: GET /changes and GET /changes/{id} are read(...) routes, and
// neither the handler nor the templates consulted CanSeeCosts, so an amount
// that could no longer be written could still be read straight out of the
// audit trail. These tests prove the read side is now closed, without
// destroying the audit trail's usefulness for the people who ARE allowed to
// see money -- which a write-time redaction (the domain.RedactedFields
// pattern used for password_hash) would have done permanently, for everyone.

// correctHV01CostLine edits hv-01's acquisition line from 8400.00 to 9000.00,
// producing a field-level change_log diff with an amount_minor movement
// (840000 -> 900000) to redact. Mirrors TestCorrectingACostLine's fixture use.
func correctHV01CostLine(t *testing.T, h *harness) (costID string) {
	t.Helper()
	id := h.refs.Assets["hv-01"]
	costID = firstCostFormID(t, body(t, h.get("/assets/"+id, false)))
	resp := h.post("/assets/"+id+"/costs/"+costID, url.Values{
		"csrf_token": {h.csrfToken("/assets/" + id)},
		"kind":       {"acquisition"}, "period": {"once"},
		"amount": {"9000"}, "valid_from": {"2024-01-01"},
		"note": {"corrected against the invoice"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != 303 {
		t.Fatalf("correcting the cost line returned %d, want a redirect", resp.StatusCode)
	}
	return costID
}

// TestAnUngrantedViewerCannotReadAnAmountFromTheChangeLogList is the defect
// this task exists to close: a change-log diff for asset_cost rendered
// amount_minor in plain sight to an Observer with no can_see_costs grant.
func TestAnUngrantedViewerCannotReadAnAmountFromTheChangeLogList(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	correctHV01CostLine(t, h)
	h.logout()

	h.login("viewer", "viewer-password")
	page := body(t, h.get("/changes?entity_type=asset_cost", false))

	// The entry must still be LISTED and countable -- redact, do not hide.
	if !strings.Contains(page, "asset_cost") {
		t.Fatal("the cost correction is not listed at all for the ungranted viewer -- " +
			"an audit trail that hides rows by viewer is worse than one that withholds a figure")
	}
	for _, figure := range []string{"840000", "900000"} {
		if strings.Contains(page, figure) {
			t.Errorf("an ungranted viewer could read the raw amount %q in the change log list", figure)
		}
	}
	if !strings.Contains(page, "you do not have the cost-visibility grant") {
		t.Error("the withheld amount carries no explanation -- an unexplained blank reads as corruption")
	}
}

// TestAnUngrantedViewerCannotReadAnAmountFromTheChangeEntryPage is the same
// defect on the single-entry surface. The brief calls this out by name: "do
// not fix the list and leave the detail page open" -- the RetireEndpoint/
// RetireLink sibling-oversight shape this branch has already hit twice.
func TestAnUngrantedViewerCannotReadAnAmountFromTheChangeEntryPage(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	costID := correctHV01CostLine(t, h)

	ctx := context.Background()
	changes, err := h.store.ListChangesForEntity(ctx, "asset_cost", costID, 5)
	if err != nil {
		t.Fatalf("listing changes for the cost line: %v", err)
	}
	if len(changes) == 0 {
		t.Fatal("the correction wrote no change_log entry")
	}
	entryID := changes[0].ID
	h.logout()

	h.login("viewer", "viewer-password")
	page := body(t, h.get("/changes/"+entryID, false))
	if !strings.Contains(page, "amount_minor") {
		t.Fatal("the field row itself is gone -- redaction must blank the value, not remove the row")
	}
	for _, figure := range []string{"840000", "900000"} {
		if strings.Contains(page, figure) {
			t.Errorf("an ungranted viewer could read the raw amount %q on the entry page", figure)
		}
	}
	if !strings.Contains(page, "you do not have the cost-visibility grant") {
		t.Error("the entry page's withheld amount carries no explanation")
	}
}

// TestAGrantedViewerAndAnAdministratorStillReadTheAmount is the case that
// goes missing if redaction is made unconditional: without this, a change
// that redacts for EVERYONE ships green, because nothing above proves the
// figure is ever actually shown to anybody.
func TestAGrantedViewerAndAnAdministratorStillReadTheAmount(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	correctHV01CostLine(t, h)

	// The Administrator's own view of the log they just wrote.
	adminPage := body(t, h.get("/changes?entity_type=asset_cost", false))
	if !strings.Contains(adminPage, "900000") {
		t.Error("an Administrator could not read the amount they just corrected, in their own audit trail")
	}

	// A non-admin holder of the grant, granted the same way
	// TestAProjectOwnersCostWriteTurnsOnTheGrantAlone grants it.
	ctx := context.Background()
	viewerRow, err := h.store.GetUserByUsername(ctx, "viewer")
	if err != nil {
		t.Fatalf("looking up viewer: %v", err)
	}
	if err := h.store.SetUserCostVisibility(ctx,
		domain.AdministratorPermit(domain.SystemActor), viewerRow.ID, true); err != nil {
		t.Fatalf("granting can_see_costs: %v", err)
	}
	h.logout()
	h.login("viewer", "viewer-password")

	page := body(t, h.get("/changes?entity_type=asset_cost", false))
	if !strings.Contains(page, "900000") {
		t.Error("a viewer WITH the can_see_costs grant could not read the amount")
	}
	if strings.Contains(page, "you do not have the cost-visibility grant") {
		t.Error("a granted viewer was shown the redaction note anyway")
	}
}

// TestANonCostEntitysDiffIsUnaffectedByCostRedaction guards against the
// redaction pass reaching past cost entities -- e.g. matching on the field
// name "amount_minor" globally, or on entity_type substring rather than exact
// match, rather than on the four cost entity types precisely.
func TestANonCostEntitysDiffIsUnaffectedByCostRedaction(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	id := h.refs.Assets["vm-app-1"]
	page := body(t, h.get("/assets/"+id+"?edit="+id, false))
	version := versionInForm(t, page)
	resp := h.post("/assets/"+id, url.Values{
		"csrf_token": {h.csrfToken("/assets/" + id)}, "row_version": {version},
		"name": {"vm-app-1"}, "kind": {"vm"}, "lifecycle": {"active"},
		"asset_tag": {"redaction-boundary-serial"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != 303 {
		t.Fatalf("editing the asset returned %d, want a redirect", resp.StatusCode)
	}

	h.logout()
	h.login("viewer", "viewer-password")
	changeLogPage := body(t, h.get("/changes?entity_type=asset", false))
	if !strings.Contains(changeLogPage, "redaction-boundary-serial") {
		t.Error("an ordinary (non-cost) field was redacted, or the entry vanished, " +
			"for a viewer with no cost grant -- redaction must be scoped to cost entity types")
	}
}
