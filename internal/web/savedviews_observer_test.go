// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
)

// WP-G4b Wave B: the security-sensitive half of moving the saved-view
// routes off `write` (Administrator or project owner only) onto `self`
// (merely signed in, ownership enforced downstream in the store -- see
// routes.go's own comment on the self registrar). This file is the
// Observer-specific coverage that motivated the move: an Observer reads
// everything and writes nothing to the ESTATE (docs/ROLES.md), but a saved
// view's subject is the person, not the estate
// (docs/saved-views-design.md §2-3), so an Observer must be able to manage
// their OWN views while staying refused everywhere else.
//
// "viewer" / "viewer-password" is the pre-seeded Observer fixture account
// every other *_test.go in this package uses for the same reason
// (TestReadOnlyUserCannotWrite, TestCSRFIsEnforcedOnNetworkRoutes, and
// others) -- reusing it here rather than inventing a second mechanism.

// TestAnObserverCanSaveReopenAndRetireTheirOwnSavedView is the positive
// case this whole wave exists for: before it, `write` refused "viewer" at
// middleware.RequireWrite before SavedViewCreate's handler ever ran, so the
// Views menu's Save button was a 403 trap for every Observer account. Save
// it back with the two routes on `write` and this test goes RED at the
// create step -- see this test's own mutation note below.
func TestAnObserverCanSaveReopenAndRetireTheirOwnSavedView(t *testing.T) {
	h := newHarness(t)
	h.login("viewer", "viewer-password")

	// Save.
	resp := h.post("/views", url.Values{
		"csrf_token": {h.csrfToken("/assets")},
		"entity":     {"asset"},
		"name":       {"Viewer-own-view"},
		"kind":       {"server"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		t.Fatalf("an Observer saving their own view: status = %d, want a redirect. "+
			"If this is 403, the two saved-view routes are back on write() instead of "+
			"self() -- see routes.go's saved-views comment.", resp.StatusCode)
	}

	viewID := h.lookup(`SELECT id FROM saved_view WHERE name = ?`, "Viewer-own-view")
	if viewID == "" {
		t.Fatal("no saved_view row was created for the Observer's own view")
	}
	owner := h.lookup(`SELECT user_id FROM saved_view WHERE id = ?`, viewID)
	viewerID := h.lookup(`SELECT id FROM app_user WHERE username = 'viewer'`)
	if owner != viewerID {
		t.Fatalf("owner = %q, want the signed-in viewer's id %q", owner, viewerID)
	}

	// Reopen: the Views menu on the list page the view belongs to must
	// offer it back, the same as it does for any other role.
	resp = h.get("/assets", false)
	b := body(t, resp)
	resp.Body.Close()
	if !strings.Contains(b, "Viewer-own-view") {
		t.Fatalf("the Observer's own saved view does not appear in the Views menu: %s", b)
	}

	// Retire.
	resp = h.post("/views/"+viewID+"/retire", url.Values{
		"csrf_token": {h.csrfToken("/assets")},
	}, false)
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		t.Fatalf("an Observer retiring their own view: status = %d, want a redirect", resp.StatusCode)
	}
	lifecycle := h.lookup(`SELECT lifecycle FROM saved_view WHERE id = ?`, viewID)
	if lifecycle != domain.LifecycleRetired {
		t.Fatalf("lifecycle = %q, want %q", lifecycle, domain.LifecycleRetired)
	}
}

// TestAnObserverStillCannotWriteEstateState is the regression guard the
// brief asks for: `self` is authentication only, not "any signed-in user
// may write" -- see routes.go's own warning on the registrar. Moving the
// two saved-view routes off write() must not have widened anything an
// Observer could not already reach; /environments (still behind write())
// is the same probe TestReadOnlyUserCannotWrite already uses for the
// general case, driven again here so this file's own claim about the scope
// of the change is self-contained rather than resting on a different file
// continuing to pass.
func TestAnObserverStillCannotWriteEstateState(t *testing.T) {
	h := newHarness(t)
	h.login("viewer", "viewer-password")

	resp := h.post("/environments", url.Values{
		"csrf_token": {h.csrfToken("/environments")},
		"code":       {"sv-wave-b-sneaky"},
		"name":       {"Sneaky"},
		"role":       {domain.EnvRoleDev},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("an Observer posting to an estate-write route: status = %d, want 403", resp.StatusCode)
	}
	if count := h.count(`SELECT COUNT(*) FROM environment WHERE code = ?`, "sv-wave-b-sneaky"); count != 0 {
		t.Fatalf("an Observer created an environment despite the 403: %d rows", count)
	}
}

// TestAnObserverCannotRetireSomebodyElsesSavedView is the negative case
// authorizeSavedViewOwner exists for, driven through the real router rather
// than asserted at the store level alone -- the same reasoning
// TestSavedViewUpdateRefusesSomebodyElsesView used to give for the (now
// removed) rename route. Ownership, not role, is what refuses this: an
// Observer is not special-cased anywhere in authorizeSavedViewOwner, it is
// simply not the actor on the row.
//
// There is no GET route for a single saved view to also assert "cannot
// read" against -- ListSavedViews (the only read path) is scoped to
// `WHERE user_id = ?`, the caller's own id, by construction, so another
// person's view is never offered to read in the first place. That is
// covered structurally by ListSavedViews' own query, not by a route this
// suite can drive.
func TestAnObserverCannotRetireSomebodyElsesSavedView(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	resp := h.post("/views", url.Values{
		"csrf_token": {h.csrfToken("/assets")},
		"entity":     {"asset"},
		"name":       {"Admin's view (wave B)"},
		"kind":       {"server"},
	}, false)
	resp.Body.Close()
	viewID := h.lookup(`SELECT id FROM saved_view WHERE name = ?`, "Admin's view (wave B)")

	h2 := secondClientOn(t, h)
	h2.login("viewer", "viewer-password")

	resp = h2.post("/views/"+viewID+"/retire", url.Values{
		"csrf_token": {h2.csrfToken("/assets")},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("an Observer retiring another person's view: status = %d, want 403", resp.StatusCode)
	}
	lifecycle := h.lookup(`SELECT lifecycle FROM saved_view WHERE id = ?`, viewID)
	if lifecycle != domain.LifecycleActive {
		t.Fatalf("lifecycle = %q; another person's view was retired by an Observer", lifecycle)
	}
}

// TestCSRFStillRejectsAnUntokenedPostToViewsFromAnObserver pins the fact
// verified during this wave rather than assumed: moving POST /views onto
// the new `self` registrar keeps it behind CSRF automatically, because CSRF
// wraps the ENTIRE mux (routes.go's middleware.Chain, below the comment at
// what is now the self registrar's call site) and self's two paths were not
// added to csrfExempt. Without that verification, a new registrar would be
// exactly the kind of change that could silently carry an exemption if
// someone had wired it differently.
func TestCSRFStillRejectsAnUntokenedPostToViewsFromAnObserver(t *testing.T) {
	h := newHarness(t)
	h.login("viewer", "viewer-password")

	resp := h.post("/views", url.Values{
		"csrf_token": {""}, // THE CASE: no token at all
		"entity":     {"asset"},
		"name":       {"Should never exist"},
		"kind":       {"server"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /views with no CSRF token: status = %d, want 400", resp.StatusCode)
	}
	if count := h.count(`SELECT COUNT(*) FROM saved_view WHERE name = ?`, "Should never exist"); count != 0 {
		t.Fatalf("a saved view was created despite the missing CSRF token: %d rows", count)
	}
}

// TestAnObserverSavingAViewLogsNoError is WP-G4b Wave C's regression guard
// for the auth review's first hardening item: handlers.App.permit used to
// log at slog.Error whenever a.Authz.Permit refused the caller, with no
// regard for WHY -- and since Wave B put SavedViewCreate/SavedViewRetire on
// the `self` registrar (RequireAuth alone, no RequireWrite), an Observer's
// resolvePermit call is refused on every single save or retire, because
// they are not an Administrator or a project owner. authorizeSavedViewOwner
// (internal/store/savedviews.go) still authorizes the write correctly off
// the fallback permit's Actor(), so the request succeeds -- but the old,
// unconditional log line fired anyway, on request after request that did
// nothing wrong. That is the exact case the fix narrows: see app.go's
// permit() comment for the CanWrite-gated condition this test is pinning.
//
// This drives the real router with a real Observer rather than asserting
// against app.permit directly, because the point being proved is what a
// signed-in Observer's ordinary traffic writes to the process log, not what
// one function returns in isolation -- same reasoning as
// TestAccessLogRecordsTheUser, whose syncBuffer/slog.SetDefault harness this
// test reuses rather than inventing a second logging fixture.
func TestAnObserverSavingAViewLogsNoError(t *testing.T) {
	buf := &syncBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	h := newHarness(t)
	h.login("viewer", "viewer-password")

	buf.Reset()
	resp := h.post("/views", url.Values{
		"csrf_token": {h.csrfToken("/assets")},
		"entity":     {"asset"},
		"name":       {"Viewer-view-no-error-log"},
		"kind":       {"server"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		t.Fatalf("an Observer saving their own view: status = %d, want a redirect", resp.StatusCode)
	}

	viewID := h.lookup(`SELECT id FROM saved_view WHERE name = ?`, "Viewer-view-no-error-log")
	if viewID == "" {
		t.Fatal("no saved_view row was created for the Observer's own view")
	}

	// Retire too -- the second self-route handler that calls a.permit(r).
	// Deliberately NOT buf.Reset() here: the assertion below covers both
	// requests, so the create's own access line (and any error alongside
	// it) stays in scope.
	resp = h.post("/views/"+viewID+"/retire", url.Values{
		"csrf_token": {h.csrfToken("/assets")},
	}, false)
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		t.Fatalf("an Observer retiring their own view: status = %d, want a redirect", resp.StatusCode)
	}

	// WAITED FOR, NOT READ IMMEDIATELY -- same reasoning as
	// TestAccessLogRecordsTheUser: the access line (which this handler set
	// always logs, successful or not) is written from the server's own
	// goroutine after the response is flushed, so its presence is what
	// proves the buffer has caught up before asserting an ERROR line is
	// absent from it.
	logged := ""
	for i := 0; i < 100; i++ {
		logged = buf.String()
		if strings.Contains(logged, "path=/views/"+viewID+"/retire") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(logged, "path=/views/"+viewID+"/retire") {
		t.Fatalf("the retire request was not logged after waiting: %s", logged)
	}
	if strings.Contains(logged, "level=ERROR") {
		t.Errorf("an Observer's own successful view retirement logged an error: %s", logged)
	}
}
