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
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/auth"
	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

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

// mustWebOwner creates a real, loggable-in project-owner account with no
// project assignments at all. Saved-view authorization is per-row against
// the actor's own id (internal/store/savedviews.go's authorizeSavedViewOwner)
// and never consults a permit's scoped entities, so a second writer for
// these tests needs to be able to reach a write route (CanWrite) and nothing
// more -- unlike mustWebProjectOwner in project_create_test.go, this account
// is deliberately assigned to no project.
func mustWebOwner(t *testing.T, h *harness, username, password string) string {
	t.Helper()
	ctx := context.Background()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hashing password: %v", err)
	}
	u, err := domain.NewAppUser(store.NewID(), username, domain.UserSourceLocal, h.store.Now())
	if err != nil {
		t.Fatalf("building user %s: %v", username, err)
	}
	u.Role = domain.RoleProjectOwner
	u.PasswordHash = &hash
	if err := h.store.CreateUser(ctx, domain.AdministratorPermit(domain.SystemActor), u); err != nil {
		t.Fatalf("creating user %s: %v", username, err)
	}
	return u.ID
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
	// id the form named.
	owner := h.lookup(`SELECT user_id FROM saved_view WHERE name = ?`, "Mine")
	if owner == "somebody-else" {
		t.Fatal("the form's user_id was honoured")
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

// TestSavedViewUpdateRefusesSomebodyElsesView proves the property the whole
// work package exists for: the handler never re-checks ownership itself, so
// this is really a test that internal/store/savedviews.go's
// authorizeSavedViewOwner is actually wired in behind the route, not just
// unit-tested in isolation. A regression here would mean one person can
// rename or repoint another person's saved filters.
func TestSavedViewUpdateRefusesSomebodyElsesView(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	resp := h.post("/views", url.Values{
		"csrf_token": {h.csrfToken("/assets")},
		"entity":     {"asset"},
		"name":       {"Admin's view"},
		"kind":       {"server"},
	}, false)
	resp.Body.Close()
	viewID := h.lookup(`SELECT id FROM saved_view WHERE name = ?`, "Admin's view")

	mustWebOwner(t, h, "po-someone-else", "po-someone-else-password")
	h2 := secondClientOn(t, h)
	h2.login("po-someone-else", "po-someone-else-password")

	resp = h2.post("/views/"+viewID, url.Values{
		"csrf_token":  {h2.csrfToken("/assets")},
		"name":        {"Stolen"},
		"entity":      {"asset"},
		"kind":        {"server"},
		"row_version": {"1"},
	}, false)
	resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
	name := h.lookup(`SELECT name FROM saved_view WHERE id = ?`, viewID)
	if name != "Admin's view" {
		t.Fatalf("the view was renamed by a non-owner: now %q", name)
	}
}

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
}
