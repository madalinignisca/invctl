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

// correctCostLine is correctHV01CostLine generalised to any cost surface: it
// finds the first (only, per fixture) cost row on the panel at
// urlPrefix+ownerID and reprices it to 9000.00 (900000 minor), the same way
// TestCorrectingACostLine and correctHV01CostLine do for an asset.
func correctCostLine(t *testing.T, h *harness, urlPrefix, ownerID string) (costID string) {
	t.Helper()
	return correctCostLineByID(t, h, urlPrefix, ownerID,
		firstCostFormID(t, body(t, h.get(urlPrefix+ownerID, false))))
}

// correctCostLineByID is correctCostLine with the cost row's id supplied
// rather than scraped off the owner's detail page.
// TestAnUngrantedViewerCannotReadAnAmountFromTheChangeLogList's circuit_cost
// case needs this: circuit_detail.html has an unrelated, pre-existing
// rendering defect (references ".Providers", a field CircuitDetail's page
// struct does not carry -- present on main before this task and out of its
// scope; see the report) that 500s on GET, independent of any authorization
// concern. Resolving the id another way avoids that GET entirely; the POST
// this function makes still exercises the real edit route and redirect.
func correctCostLineByID(t *testing.T, h *harness, urlPrefix, ownerID, costID string) string {
	t.Helper()
	// The CSRF token is session-wide, not page-specific, so it can come from
	// any page -- deliberately "/" rather than urlPrefix+ownerID, since the
	// circuit case must not GET the broken circuit_detail page either.
	resp := h.post(urlPrefix+ownerID+"/costs/"+costID, url.Values{
		"csrf_token": {h.csrfToken("/")},
		"kind":       {"operating"}, "period": {"monthly"},
		"amount": {"9000"}, "valid_from": {"2024-01-01"},
		"note": {"corrected against the invoice"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != 303 {
		t.Fatalf("correcting the cost line at %s%s returned %d, want a redirect",
			urlPrefix, ownerID, resp.StatusCode)
	}
	return costID
}

// TestAnUngrantedViewerCannotReadAnAmountFromTheChangeLogList is the defect
// this task exists to close: a change-log diff for asset_cost rendered
// amount_minor in plain sight to an Observer with no can_see_costs grant.
//
// FOUR SURFACES, NOT ONE. Deleting "service_cost", "project_cost" or
// "circuit_cost" from store.costEntityTypes left both suites green before
// this test existed -- only asset_cost was ever proven redacted, and the seed
// fixture already carries a priced line on all four (seed_costs.go's asset,
// service and project lines; seed_drlink.go's single circuit). This loop is
// what would have caught that: each case reprices the fixture's one line on
// that surface and proves the correction is redacted the same way hv-01's is.
func TestAnUngrantedViewerCannotReadAnAmountFromTheChangeLogList(t *testing.T) {
	for _, fx := range []struct {
		entityType string
		urlPrefix  string
		owner      func(*harness) string
		// costID resolves the row to correct. Every surface but circuit
		// scrapes it off the owner's detail page (correctCostLine); circuit
		// resolves it from the store instead, because circuit_detail.html
		// currently 500s on GET for an unrelated, pre-existing reason (see
		// correctCostLineByID's comment) and this test has no business
		// depending on that page rendering.
		costID func(*testing.T, *harness, string) string
	}{
		{"asset_cost", "/assets/", func(h *harness) string { return h.refs.Assets["hv-01"] }, nil},
		{"service_cost", "/services/", func(h *harness) string { return h.refs.Services["vault"] }, nil},
		{"project_cost", "/projects/", func(h *harness) string { return h.refs.Projects["platform"] }, nil},
		{"circuit_cost", "/circuits/", func(h *harness) string {
			return h.lookup(`SELECT id FROM circuit LIMIT 1`)
		}, func(t *testing.T, h *harness, circuitID string) string {
			// The base fixture's circuit ("TN-DEMO-1", seed_engine.go) carries
			// no cost line. The one that does (seed_drlink.go's DR link) only
			// exists when seed.CompanyEstate is on, which web_test's harness
			// never sets -- that flag is for internal/seed's own tests. Add a
			// line through the real handler rather than depending on a seed
			// fixture this harness does not build.
			resp := h.post("/circuits/"+circuitID+"/costs", url.Values{
				"csrf_token": {h.csrfToken("/")},
				"kind":       {"operating"}, "period": {"monthly"},
				"amount": {"1450"}, "note": {"fixture line for the redaction test"},
			}, false)
			resp.Body.Close()
			if resp.StatusCode != 303 {
				t.Fatalf("adding the fixture circuit cost line returned %d, want a redirect",
					resp.StatusCode)
			}
			ctx := context.Background()
			rows, err := h.store.ListCircuitCosts(ctx, circuitID)
			if err != nil {
				t.Fatalf("listing circuit costs: %v", err)
			}
			if len(rows) == 0 {
				t.Fatal("adding a circuit cost line did not produce one")
			}
			return rows[0].ID
		}},
	} {
		t.Run(fx.entityType, func(t *testing.T) {
			h := newHarness(t)
			h.login("admin", "admin-password")
			ownerID := fx.owner(h)

			var costID string
			if fx.costID != nil {
				costID = correctCostLineByID(t, h, fx.urlPrefix, ownerID, fx.costID(t, h, ownerID))
			} else {
				costID = correctCostLine(t, h, fx.urlPrefix, ownerID)
			}

			// Resolve the correction's own change_log id, so the "still
			// listed" check below can assert the row that entry actually
			// rendered -- not just that the entity type's name appears
			// somewhere on the page. store.ChangeEntityTypes() derives the
			// filter <select> from the classification census, so it lists
			// every cost entity type on EVERY render regardless of what the
			// viewer can see: strings.Contains(page, fx.entityType) would
			// pass even if the row itself never rendered.
			ctx := context.Background()
			changes, err := h.store.ListChangesForEntity(ctx, fx.entityType, costID, 5)
			if err != nil {
				t.Fatalf("listing changes for the %s correction: %v", fx.entityType, err)
			}
			if len(changes) == 0 {
				t.Fatalf("the %s correction wrote no change_log entry", fx.entityType)
			}
			entryHref := "href=\"/changes/" + changes[0].ID + "\""
			h.logout()

			h.login("viewer", "viewer-password")
			page := body(t, h.get("/changes?entity_type="+fx.entityType, false))

			// The entry must still be LISTED and countable -- redact, do not hide.
			if !strings.Contains(page, entryHref) {
				t.Fatalf("the %s correction's own row (%s) is not on the page for the ungranted "+
					"viewer -- an audit trail that hides rows by viewer is worse than one that "+
					"withholds a figure", fx.entityType, entryHref)
			}
			if strings.Contains(page, "900000") {
				t.Errorf("an ungranted viewer could read the raw corrected amount 900000 "+
					"in the %s change log list", fx.entityType)
			}
			if !strings.Contains(page, "you do not have the cost-visibility grant") {
				t.Errorf("the withheld %s amount carries no explanation -- an unexplained blank "+
					"reads as corruption", fx.entityType)
			}
		})
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
//
// BOTH SURFACES, LIST AND ENTRY. WP-1.1's review found the entry page
// (misc.go's ChangeEntry) had no positive-path test at all: deleting its
// "!base.CanSeeCosts &&" guard -- so the entry page redacts unconditionally,
// for everyone -- left the whole suite green, because nothing fetched
// /changes/{id} as an Administrator or a granted viewer and checked the
// figure was there. handleEntry below closes that; ChangeEntry's own doc
// comment calls this page "evidence" someone might cite in an incident
// write-up, and evidence that silently shows nobody the number it names is
// worse than evidence that never existed.
func TestAGrantedViewerAndAnAdministratorStillReadTheAmount(t *testing.T) {
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

	// The Administrator's own view of the log they just wrote -- list and
	// single-entry page both.
	adminPage := body(t, h.get("/changes?entity_type=asset_cost", false))
	if !strings.Contains(adminPage, "900000") {
		t.Error("an Administrator could not read the amount they just corrected, in their own audit trail")
	}
	adminEntry := body(t, h.get("/changes/"+entryID, false))
	if !strings.Contains(adminEntry, "900000") {
		t.Error("an Administrator citing the entry page directly could not read the amount they just corrected")
	}

	// A non-admin holder of the grant, granted the same way
	// TestAProjectOwnersCostWriteTurnsOnTheGrantAlone grants it.
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
	entryPage := body(t, h.get("/changes/"+entryID, false))
	if !strings.Contains(entryPage, "900000") {
		t.Error("a viewer WITH the can_see_costs grant could not read the amount on the entry page")
	}
	if strings.Contains(entryPage, "you do not have the cost-visibility grant") {
		t.Error("a granted viewer was shown the entry page's redaction note anyway")
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
