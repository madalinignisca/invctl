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
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// mustSavedViewService creates a real, live service with a known kind, for
// tests that filter to a kind and want a guaranteed non-empty result --
// TestServiceListHTMXFragmentCarriesTheAppliedFilterIntoTheSaveViewForm needs
// a real save-view form with real filter values to assert on, not the empty
// state. It is unrelated to whether the Views/Columns menus render on an
// EMPTY result: they now do either way (asset_table.html and rows.html hoist
// both above {{if .Assets}}/{{if .Services}}), which
// TestAssetListEmptyResultStillShowsViewsAndColumnsMenus and its service twin
// pin directly, with no fixture asset or service at all.
func mustSavedViewService(t *testing.T, h *harness, code string) string {
	t.Helper()
	svc, err := domain.NewService(store.NewID(), domain.ServiceSpec{
		Code: code, Name: code, Kind: domain.SvcAPI,
		EnvironmentID: h.refs.Environments["prod"], Availability: domain.AvailStandalone, Tier: 2,
	}, h.store.Now())
	if err != nil {
		t.Fatalf("building service %s: %v", code, err)
	}
	if err := h.store.CreateService(context.Background(), domain.AdministratorPermit(domain.SystemActor), svc); err != nil {
		t.Fatalf("creating service %s: %v", code, err)
	}
	return svc.ID
}

// secondClientOn returns a *harness that talks to the SAME running server
// and the SAME database as h, but through a fresh cookie jar -- so a second
// login does not clobber h's session. Same technique
// rbac_boundary_test.go's newBoundaryHarness uses for a differently-shaped
// need; here it only has to reuse an existing server, not build one.
func secondClientOn(t *testing.T, h *harness) *harness {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("building cookie jar: %v", err)
	}
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &harness{t: t, server: h.server, client: client, store: h.store}
}

// TestSavedViewCreateTakesTheOwnerFromTheSessionNotTheForm: a form-supplied
// owner would let anybody create a view in another person's name.
func TestSavedViewCreateTakesTheOwnerFromTheSessionNotTheForm(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	resp := h.post("/views", url.Values{
		"csrf_token": {h.csrfToken("/assets")},
		"entity":     {"asset"},
		"name":       {"Mine"},
		"user_id":    {"somebody-else"}, // THE ATTACK: ignored entirely
		"kind":       {"server"},
	}, false)
	resp.Body.Close()

	if resp.StatusCode >= 400 {
		t.Fatalf("status = %d; the create should succeed and ignore user_id", resp.StatusCode)
	}
	// The view exists and belongs to the signed-in account, not to the
	// id the form named. Assert the actual admin id, not merely that the
	// attacker's value lost -- a positive assertion pins the one right
	// answer instead of ruling out one wrong one among many.
	adminID := h.lookup(`SELECT id FROM app_user WHERE username = 'admin'`)
	owner := h.lookup(`SELECT user_id FROM saved_view WHERE name = ?`, "Mine")
	if owner != adminID {
		t.Fatalf("owner = %q, want the signed-in admin's id %q", owner, adminID)
	}
}

// TestSavedViewCreateStoresOnlyKnownFilterKeys: params are replayed as a
// query later, so an unreviewed key is a stored input with a future.
func TestSavedViewCreateStoresOnlyKnownFilterKeys(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	resp := h.post("/views", url.Values{
		"csrf_token":   {h.csrfToken("/assets")},
		"entity":       {"asset"},
		"name":         {"Filtered"},
		"kind":         {"server"},
		"not_a_filter": {"anything"},
	}, false)
	resp.Body.Close()

	params := h.lookup(`SELECT params FROM saved_view WHERE name = ?`, "Filtered")
	if strings.Contains(params, "not_a_filter") {
		t.Errorf("params kept a key outside the allowlist: %s", params)
	}
	if !strings.Contains(params, "server") {
		t.Errorf("params lost the real filter: %s", params)
	}
}

// TestSavedViewCreateWithUnknownEntityIs422NotA200WithAnErrorBuried: the
// package-wide rule (CLAUDE.md) is that a validation failure is a status
// code, never a 200 with the mistake left for the body to explain.
func TestSavedViewCreateWithUnknownEntityIs422NotA200WithAnErrorBuried(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	resp := h.post("/views", url.Values{
		"csrf_token": {h.csrfToken("/assets")},
		"entity":     {"widget"}, // not asset, not service
		"name":       {"Bogus"},
	}, false)
	resp.Body.Close()

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
}

// TestSavedViewCreateAndReopenPreservesEveryTagNotJustTheFirst pins the fix
// for the highest-value defect a whole-branch review found: params used to
// be stored as map[string]string, so a filter posted with more than one
// value for the same key -- "tag" is repeating, per the <select multiple>
// in asset_list.html -- silently kept only the first, and reopening the
// view WIDENED the result to everything carrying that one tag instead of
// everything carrying all of them. A populated, plausible, wrong answer.
//
// Checks the round trip at both ends: the stored params blob must carry
// both tag ids (not just the first), and the "open this view" link the
// Views menu renders must carry both as separate ?tag= query parameters --
// not just that the JSON happens to be right, but that what a click on the
// menu actually requests is right.
func TestSavedViewCreateAndReopenPreservesEveryTagNotJustTheFirst(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	tag1 := mustTagWeb(t, h, "sv-multi-tag-a")
	tag2 := mustTagWeb(t, h, "sv-multi-tag-b")

	resp := h.post("/views", url.Values{
		"csrf_token": {h.csrfToken("/assets")},
		"entity":     {"asset"},
		"name":       {"Two tags"},
		"tag":        {tag1, tag2}, // THE CASE: two values under one key
	}, false)
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		t.Fatalf("creating the two-tag view: status = %d", resp.StatusCode)
	}

	params := h.lookup(`SELECT params FROM saved_view WHERE name = ?`, "Two tags")
	var fields map[string][]string
	if err := json.Unmarshal([]byte(params), &fields); err != nil {
		t.Fatalf("stored params did not parse as a JSON object of arrays: %v (%s)", err, params)
	}
	got := fields["tag"]
	if len(got) != 2 {
		t.Fatalf("stored tag values = %v, want both %s and %s", got, tag1, tag2)
	}
	matched := (got[0] == tag1 && got[1] == tag2) || (got[0] == tag2 && got[1] == tag1)
	if !matched {
		t.Fatalf("stored tag values = %v, want exactly {%s, %s}", got, tag1, tag2)
	}

	// Reopen: the Views menu's link for this view must carry BOTH tags as
	// the query string /assets replays, not just the one that survived a
	// single-value round trip.
	resp = h.get("/assets", false)
	b := body(t, resp)
	if !strings.Contains(b, "tag="+tag1) {
		t.Fatalf("the saved view's reopen link dropped tag %s: %s", tag1, b)
	}
	if !strings.Contains(b, "tag="+tag2) {
		t.Fatalf("the saved view's reopen link dropped tag %s: %s", tag2, b)
	}
}

// mustDeviceTypeWeb creates a live manufacturer + device type through the
// store, bypassing the forms -- same shape as mustAssetWeb, for the tests
// below that need a real device type to filter on and then retire.
func mustDeviceTypeWeb(t *testing.T, h *harness, code, model string) string {
	t.Helper()
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)
	mfr, err := domain.NewManufacturer(store.NewID(), domain.ManufacturerSpec{Code: code, Name: code}, h.store.Now())
	if err != nil {
		t.Fatalf("building manufacturer %s: %v", code, err)
	}
	if err := h.store.CreateManufacturer(ctx, admin, mfr); err != nil {
		t.Fatalf("creating manufacturer %s: %v", code, err)
	}
	dt, err := domain.NewDeviceType(store.NewID(), domain.DeviceTypeSpec{
		ManufacturerID: mfr.ID, Model: model,
	}, h.store.Now())
	if err != nil {
		t.Fatalf("building device type %s: %v", model, err)
	}
	if err := h.store.CreateDeviceType(ctx, admin, dt); err != nil {
		t.Fatalf("creating device type %s: %v", model, err)
	}
	return dt.ID
}

// TestSavedViewOnARetiredDeviceTypeIsNotStaleAndStillReturnsRows pins the
// coordinator's finding: retiring a device type only ever sets its own
// lifecycle column (RetireDeviceType), it never clears the id an asset
// already carries in asset.device_type_id, and ListAssets' own
// f.DeviceTypeID clause has no lifecycle condition on the device type
// itself -- so a view filtered on a device type that has since been
// retired still returns real rows. Reading that as "no longer exists"
// would be a false alarm on a filter that is still quietly working, the
// exact opposite of what design §6's staleness explanation is for.
//
// Asserts BOTH halves: the Views menu carries no Stale warning for this
// view, and the reopened list genuinely still has the asset in it -- a
// regression that reintroduces IncludeRetired: false would fail the first
// assertion even though the second stayed true, and a regression that
// broke the filter itself (unrelated to staleness) would fail the second
// even though the first stayed true. Neither alone pins the claim.
func TestSavedViewOnARetiredDeviceTypeIsNotStaleAndStillReturnsRows(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	deviceTypeID := mustDeviceTypeWeb(t, h, "sv-retired-dt-mfr", "sv-retired-dt-model")
	a, err := domain.NewAsset(store.NewID(), domain.KindServer, "sv-retired-dt-asset", nil, h.store.Now())
	if err != nil {
		t.Fatalf("building asset: %v", err)
	}
	a.DeviceTypeID = &deviceTypeID
	if err := h.store.CreateAsset(context.Background(), domain.AdministratorPermit(domain.SystemActor), a, nil); err != nil {
		t.Fatalf("creating asset: %v", err)
	}

	resp := h.post("/views", url.Values{
		"csrf_token":     {h.csrfToken("/assets")},
		"entity":         {"asset"},
		"name":           {"Retired device type"},
		"device_type_id": {deviceTypeID},
	}, false)
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		t.Fatalf("creating the device-type view: status = %d", resp.StatusCode)
	}

	if err := h.store.RetireDeviceType(context.Background(), domain.AdministratorPermit(domain.SystemActor), deviceTypeID); err != nil {
		t.Fatalf("retiring the device type: %v", err)
	}

	resp = h.get("/assets", false)
	b := body(t, resp)
	if strings.Contains(b, "no longer exists") {
		t.Errorf("a RETIRED device type is reported as stale, which is a false alarm -- the filter still works: %s", b)
	}

	resp = h.get("/assets?device_type_id="+deviceTypeID, false)
	b = body(t, resp)
	if !strings.Contains(b, "sv-retired-dt-asset") {
		t.Fatalf("filtering by a retired device type's id returned no rows -- the fixture assumption broke: %s", b)
	}
}

// TestSavedViewOnARetiredProjectStaysStaleBecauseRetirementCascades looks
// like TestSavedViewOnARetiredDeviceTypeIsNotStaleAndStillReturnsRows's
// service twin, and an earlier draft of this fix treated it as one --
// applying the SAME "retired is not missing" reasoning to project as to
// device type and tag. That was checked against the actual store behaviour
// and found wrong: RetireProject cascades (releaseLinks) and retires every
// `owns` project_service link the moment a project retires, so
// ListServices' f.ProjectID filter (which only ever matches an `owns`
// link) can never again return a row for that project's id. A retired
// project is a genuine, permanent dead end for this filter -- unlike a
// retired device type, where the reference is left untouched on purpose
// (RetireDeviceType's own doc comment: "ALLOWED WHILE ASSETS STILL POINT AT
// IT"). So staying stale here is the CORRECT behaviour, not the bug fix 4
// found for device_type_id/tag, and this test pins that distinction rather
// than "fixing" it to match -- see savedViewVocabulary.ProjectIDs' own
// comment for the same reasoning next to the field it governs.
func TestSavedViewOnARetiredProjectStaysStaleBecauseRetirementCascades(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)

	proj, err := domain.NewProject(store.NewID(), domain.ProjectSpec{
		Code: "sv-retired-proj", Name: "sv-retired-proj",
	}, h.store.Now())
	if err != nil {
		t.Fatalf("building project: %v", err)
	}
	if err := h.store.CreateProject(ctx, admin, proj); err != nil {
		t.Fatalf("creating project: %v", err)
	}
	svcID := mustSavedViewService(t, h, "sv-retired-proj-svc")
	link, err := domain.NewProjectServiceLink(proj.ID, svcID, domain.ProjectOwns, nil, h.store.Now())
	if err != nil {
		t.Fatalf("building project-service link: %v", err)
	}
	if err := h.store.LinkProjectService(ctx, admin, link); err != nil {
		t.Fatalf("linking service to project: %v", err)
	}

	resp := h.post("/views", url.Values{
		"csrf_token": {h.csrfToken("/services")},
		"entity":     {"service"},
		"name":       {"Retired project"},
		"project":    {proj.ID},
	}, false)
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		t.Fatalf("creating the project view: status = %d", resp.StatusCode)
	}

	if err := h.store.RetireProject(ctx, admin, proj.ID); err != nil {
		t.Fatalf("retiring the project: %v", err)
	}

	// The cascade's own effect, pinned first so a fixture regression here
	// (e.g. LinkProjectService stops actually linking) is not mistaken for
	// the staleness assertion below failing for the right reason.
	resp = h.get("/services?project="+proj.ID, false)
	b := body(t, resp)
	if strings.Contains(b, "sv-retired-proj-svc") {
		t.Fatalf("filtering by a retired project's id still returned its service -- "+
			"RetireProject's cascade assumption broke, so this test's premise no "+
			"longer holds: %s", b)
	}

	resp = h.get("/services", false)
	b = body(t, resp)
	if !strings.Contains(b, "no longer exists") {
		t.Errorf("a retired project's saved view is not reported as stale, "+
			"but its filter genuinely, permanently returns nothing now: %s", b)
	}
}

// TestSavedViewUpdateRefusesSomebodyElsesView and
// TestSavedViewUpdateIgnoresASubmittedEntity, which used to live here and
// drove POST /views/{id}, were removed alongside that route (WP-G4b Wave B:
// nothing posts to it -- see SavedViewUpdate's removed-handler comment in
// internal/web/handlers/savedviews.go). The ownership and
// entity-not-editable properties they pinned are still covered, at the
// store level where UpdateSavedView itself lives:
// internal/store/savedviews_test.go's TestAPersonCannotUpdateSomebodyElses-
// SavedView and TestUpdateSavedViewReadsTheOwnerFromTheStoredRow.

// TestSavedViewRetireIsIdempotentAndSoftDelete: like every entity in this
// product, retiring a saved view sets lifecycle rather than removing the
// row -- and retiring twice must not become an error, since a double click
// or a retried request is not a second decision.
func TestSavedViewRetireIsIdempotentAndSoftDelete(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	resp := h.post("/views", url.Values{
		"csrf_token": {h.csrfToken("/assets")},
		"entity":     {"service"},
		"name":       {"To retire"},
		"kind":       {"http"},
	}, false)
	resp.Body.Close()
	viewID := h.lookup(`SELECT id FROM saved_view WHERE name = ?`, "To retire")

	for i := 0; i < 2; i++ {
		resp = h.post("/views/"+viewID+"/retire", url.Values{
			"csrf_token": {h.csrfToken("/services")},
		}, false)
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			t.Fatalf("retire attempt %d: status = %d", i+1, resp.StatusCode)
		}
	}

	lifecycle := h.lookup(`SELECT lifecycle FROM saved_view WHERE id = ?`, viewID)
	if lifecycle != domain.LifecycleRetired {
		t.Fatalf("lifecycle = %q, want %q", lifecycle, domain.LifecycleRetired)
	}
	// A retired view drops out of the picker: ListSavedViews filters on
	// lifecycle = active, so nothing here should look it up as if it were
	// still current.
	count := h.lookup(`SELECT COUNT(*) FROM saved_view WHERE id = ? AND lifecycle = ?`,
		viewID, domain.LifecycleActive)
	if count != "0" {
		t.Fatalf("retired view still counts as active: %s", count)
	}
	// change_log stays at one 'update' row across both retires. Filtered to
	// action = 'update' since the create above already wrote its own
	// action = 'create' row for the same entity_id.
	//
	// NOTE this alone does not prove RetireSavedView's dedup branch does
	// anything: diffJSON already collapses the second retire to a no-op on
	// its own (lifecycle is unchanged the second time around, and
	// updated_at/row_version are excluded from what it diffs -- see
	// diff.go's auditFields), so logUpdate writes nothing on the second call
	// whether or not the dedup branch exists. Confirmed by mutation: with
	// the dedup branch deleted, this assertion still reads 1 and the test
	// still goes green. It is kept anyway because "exactly one update row"
	// is a real invariant worth pinning, just not the one the branch
	// provides.
	logRows := h.count(`SELECT COUNT(*) FROM change_log WHERE entity_type = ? AND entity_id = ? AND action = ?`,
		"saved_view", viewID, "update")
	if logRows != 1 {
		t.Fatalf("change_log 'update' rows for the retired view = %d, want 1", logRows)
	}
	// THIS is the assertion that actually exercises the dedup branch. With
	// it, the second retire short-circuits before the UPDATE statement runs
	// at all, so row_version stops moving after the first retire (1 -> 2,
	// then held). The UPDATE carries no row_version guard, so without the
	// branch the second call runs it again regardless (-> 3) even though no
	// other column changes. Verified by mutation: deleting the dedup branch
	// turns this red while every check above it (including the change_log
	// count) stays green.
	rowVersion := h.lookup(`SELECT row_version FROM saved_view WHERE id = ?`, viewID)
	if rowVersion != "2" {
		t.Fatalf("row_version = %s, want 2 (a second retire must not re-run the UPDATE)", rowVersion)
	}
}

// TestAssetListHTMXFragmentCarriesTheAppliedFilterIntoTheSaveViewForm pins
// the fix for a defect a whole-branch review caught before it shipped: the
// filter toolbar's hx-get swaps ONLY #asset-table (hx-target="#asset-table"
// hx-swap="outerHTML" in asset_list.html), so if the Views menu lived
// outside that element -- as it did in an earlier draft of this task, on the
// page template rather than inside asset_table.html -- filtering never
// touched it. Its "Save this view" form kept whatever CurrentFilters were on
// the page at the LAST FULL LOAD, so filtering to kind=firewall and clicking
// "Save this view" saved a view with no filters at all: the feature's most
// common action, silently wrong.
//
// This requests exactly what the real toolbar requests -- GET with
// HX-Request true, the fragment HTMX itself would receive -- and asserts the
// hidden input the save form would submit already carries kind=firewall.
// Move the menu back onto the page template and this goes red.
func TestAssetListHTMXFragmentCarriesTheAppliedFilterIntoTheSaveViewForm(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	mustAssetWeb(t, h, domain.KindFirewall, "sv-fixture-firewall")

	resp := h.get("/assets?kind=firewall", true)
	b := body(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(b, `id="asset-table"`) {
		t.Fatalf("the HTMX response is not the #asset-table fragment: %s", b)
	}
	if !strings.Contains(b, `id="saved-views-asset"`) {
		t.Fatalf("the Views menu did not come back inside the swapped fragment: %s", b)
	}
	if !strings.Contains(b, `name="kind" value="firewall"`) {
		t.Fatalf("the save-view form did not carry the applied kind=firewall filter: %s", b)
	}
}

// TestServiceListHTMXFragmentCarriesTheAppliedFilterIntoTheSaveViewForm is
// TestAssetListHTMXFragmentCarriesTheAppliedFilterIntoTheSaveViewForm's
// service-list twin: #service-table is rows.html's swap target for the
// service filter toolbar, and the same defect (menu outside the swap
// target) would have frozen this one's filters too.
func TestServiceListHTMXFragmentCarriesTheAppliedFilterIntoTheSaveViewForm(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	mustSavedViewService(t, h, "sv-fixture-api")

	resp := h.get("/services?kind="+domain.SvcAPI, true)
	b := body(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(b, `id="service-table"`) {
		t.Fatalf("the HTMX response is not the #service-table fragment: %s", b)
	}
	if !strings.Contains(b, `id="saved-views-service"`) {
		t.Fatalf("the Views menu did not come back inside the swapped fragment: %s", b)
	}
	if !strings.Contains(b, `name="kind" value="`+domain.SvcAPI+`"`) {
		t.Fatalf("the save-view form did not carry the applied kind filter: %s", b)
	}
}

// TestSavedViewCreateValidationFailureRerendersTheMenuAt422 pins the second
// review fix: SavedViewCreate used to fall through to handleStoreError's
// plain-text 422 on a validation failure (domain.NewSavedView rejects an
// empty name), which this project's own rule refuses -- "Validation errors
// re-render the form partial with error state and return HTTP 422"
// (CLAUDE.md). Posted with HX-Request true, the way the menu's own hx-post
// form actually submits it (see saved_views.html), so this exercises the
// real swap path, not a hypothetical one.
func TestSavedViewCreateValidationFailureRerendersTheMenuAt422(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	resp := h.post("/views", url.Values{
		"csrf_token": {h.csrfToken("/assets")},
		"entity":     {"asset"},
		"name":       {""}, // THE FAILURE: NewSavedView rejects an empty name
		"kind":       {"server"},
	}, true)
	b := body(t, resp)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
	if strings.Contains(b, "That request was not valid.") {
		t.Fatalf("body is handleStoreError's bare text, not the re-rendered menu: %s", b)
	}
	if !strings.Contains(b, `id="saved-views-asset"`) {
		t.Fatalf("body is not the re-rendered saved_views partial: %s", b)
	}
	// The filter being saved must survive the round trip too -- otherwise
	// fixing the name and resubmitting silently saves a view with no
	// filters, the same failure mode fix 1 above pins from a different angle.
	if !strings.Contains(b, `name="kind" value="server"`) {
		t.Fatalf("re-rendered menu lost the filter that was being saved: %s", b)
	}
}

// TestAssetListEmptyResultStillShowsViewsAndColumnsMenus pins the fix for a
// FAIL the code review caught: both menus lived inside {{if .Assets}} in
// asset_table.html, so a filter matching nothing hid the Views button, the
// saved-view list, and every Stale explanation along with the table -- in
// exactly the case design §6 exists for. A stale view (naming an
// environment or kind that no longer exists) is the LIKELIEST way to reach
// zero rows, so the explanation that stops an operator concluding the
// estate is empty was hidden precisely when it was needed, replaced by a
// bare "Nothing matches those filters." The Columns picker shared the same
// wrapper and vanished the same way -- a pre-existing WP-G4c defect, fixed
// here on the same edit rather than left half done.
//
// kind=router matches no seeded asset (the fixture asset kinds this
// package's other tests create are firewall/server/vm/switch/storage), so
// this exercises the true empty-result path, not a coincidentally-populated
// one.
func TestAssetListEmptyResultStillShowsViewsAndColumnsMenus(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	resp := h.get("/assets?kind=router", true)
	b := body(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(b, "Nothing matches those filters.") {
		t.Fatalf("kind=router should match nothing -- test fixture assumption broke: %s", b)
	}
	if !strings.Contains(b, `id="saved-views-asset"`) {
		t.Fatalf("Views menu is missing from an empty-result fragment: %s", b)
	}
	if !strings.Contains(b, "Save this view") {
		t.Fatalf("the save-view form is missing from an empty-result fragment: %s", b)
	}
	if !strings.Contains(b, `data-table-key="asset"`) {
		t.Fatalf("Columns picker is missing from an empty-result fragment: %s", b)
	}
}

// TestServiceListEmptyResultStillShowsViewsAndColumnsMenus is
// TestAssetListEmptyResultStillShowsViewsAndColumnsMenus's service twin.
func TestServiceListEmptyResultStillShowsViewsAndColumnsMenus(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	resp := h.get("/services?kind=nonexistent-service-kind", true)
	b := body(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(b, "No services match those filters.") {
		t.Fatalf("kind=nonexistent-service-kind should match nothing -- test fixture assumption broke: %s", b)
	}
	if !strings.Contains(b, `id="saved-views-service"`) {
		t.Fatalf("Views menu is missing from an empty-result fragment: %s", b)
	}
	if !strings.Contains(b, "Save this view") {
		t.Fatalf("the save-view form is missing from an empty-result fragment: %s", b)
	}
	if !strings.Contains(b, `data-table-key="service"`) {
		t.Fatalf("Columns picker is missing from an empty-result fragment: %s", b)
	}
}
