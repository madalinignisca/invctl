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
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// WP-G4a piece 3: filtering the asset/service lists by tag, and bulk
// application from a filtered list, through the real router
// (docs/tags-design.md §4a, §5).

// mustTagWeb creates a tag through the real registry form -- tag.created_by
// carries a REFERENCES app_user(id) (migration 00056), so this has to be a
// real, currently logged-in user's id rather than an actor built by hand
// (domain.SystemActor.ID, "system", names no app_user row at all). The
// caller must already be logged in as an admin.
func mustTagWeb(t *testing.T, h *harness, code string) string {
	t.Helper()
	form := url.Values{
		"csrf_token":  {h.csrfToken("/tags")},
		"code":        {code},
		"label":       {code},
		"description": {"a fixture tag for the http-level tag suite"},
	}
	resp := h.post("/tags", form, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("creating tag %s: got %d, want 303", code, resp.StatusCode)
	}
	rows, err := h.store.ListTags(context.Background(), false)
	if err != nil {
		t.Fatalf("reading back tag %s: %v", code, err)
	}
	for _, row := range rows {
		if row.Code == code {
			return row.ID
		}
	}
	t.Fatalf("tag %s was not found in the registry after creating it", code)
	return ""
}

// adminActorWeb resolves the seeded "admin" user's real id, for a store call
// that has to carry an actor an app_user foreign key (entity_tag.created_by,
// migration 00057) will actually accept -- domain.SystemActor's id ("system")
// names no such row.
func adminActorWeb(t *testing.T, h *harness) domain.Actor {
	t.Helper()
	user, err := h.store.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatalf("looking up the admin user: %v", err)
	}
	return domain.UserActor(user)
}

// mustAssetWeb creates a live asset through the store, bypassing the form.
func mustAssetWeb(t *testing.T, h *harness, kind, name string) string {
	t.Helper()
	a, err := domain.NewAsset(store.NewID(), kind, name, nil, h.store.Now())
	if err != nil {
		t.Fatalf("building asset %s: %v", name, err)
	}
	if err := h.store.CreateAsset(context.Background(), domain.SystemActor, a, nil); err != nil {
		t.Fatalf("creating asset %s: %v", name, err)
	}
	return a.ID
}

// assetRowVersion reads back an asset's current row_version, for building
// the "id:version" selection value a bulk-tag-apply checkbox posts.
func assetRowVersionWeb(t *testing.T, h *harness, id string) int {
	t.Helper()
	a, err := h.store.GetAsset(context.Background(), id)
	if err != nil {
		t.Fatalf("GetAsset(%s): %v", id, err)
	}
	return a.RowVersion
}

func selectionValue(id string, version int) string {
	return fmt.Sprintf("%s:%d", id, version)
}

// TestAssetListFiltersByTag proves the query string reaches ListAssets: an
// asset carrying the tag is listed, one that does not is not.
func TestAssetListFiltersByTag(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	dr := mustTagWeb(t, h, "http-dr")
	tagged := mustAssetWeb(t, h, domain.KindVM, "http-filter-tagged")
	mustAssetWeb(t, h, domain.KindVM, "http-filter-untagged")
	if err := h.store.SetEntityTags(context.Background(), adminActorWeb(t, h), domain.TagEntityAsset,
		tagged, assetRowVersionWeb(t, h, tagged), []string{dr}); err != nil {
		t.Fatalf("tagging: %v", err)
	}

	page := body(t, h.get("/assets?tag="+dr, false))
	if !strings.Contains(page, "http-filter-tagged") {
		t.Error("the tagged asset does not appear when filtering by its tag")
	}
	if strings.Contains(page, "http-filter-untagged") {
		t.Error("an untagged asset appears when filtering by a tag it does not carry")
	}
}

// TestAssetsBulkTagApplyTagsExactlyTheSelectedRows drives the mutation
// through HTTP: only the checked rows are tagged.
func TestAssetsBulkTagApplyTagsExactlyTheSelectedRows(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	dr := mustTagWeb(t, h, "http-bulk-dr")
	selected := mustAssetWeb(t, h, domain.KindVM, "http-bulk-selected")
	leftOut := mustAssetWeb(t, h, domain.KindVM, "http-bulk-left-out")

	token := h.csrfToken("/assets")
	resp := h.post("/assets/tags/apply", url.Values{
		"csrf_token": {token},
		"tag_id":     {dr},
		"entity":     {selectionValue(selected, assetRowVersionWeb(t, h, selected))},
	}, true)
	page := body(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", resp.StatusCode, page)
	}
	if !strings.Contains(page, "http-bulk-selected") {
		t.Error("the result page does not name the tagged asset")
	}
	if !strings.Contains(page, "tagged") {
		t.Error("the result page does not report the tag application")
	}

	tags, err := h.store.EntityTagsFor(context.Background(), domain.TagEntityAsset, selected)
	if err != nil {
		t.Fatalf("EntityTagsFor(selected): %v", err)
	}
	if len(tags) != 1 || tags[0].ID != dr {
		t.Fatalf("selected asset tags = %+v, want exactly [%s]", tags, dr)
	}

	untouched, err := h.store.EntityTagsFor(context.Background(), domain.TagEntityAsset, leftOut)
	if err != nil {
		t.Fatalf("EntityTagsFor(leftOut): %v", err)
	}
	if len(untouched) != 0 {
		t.Fatalf("an asset OUTSIDE the selection was tagged: %+v", untouched)
	}
}

// TestAssetsBulkTagApplyRequiresAdmin: a read-only user cannot reach the
// mutation, even with a valid CSRF token.
func TestAssetsBulkTagApplyRequiresAdmin(t *testing.T) {
	h := newHarness(t)
	// The tag has to already exist, and creating one is itself write() --
	// set up as admin, then switch to the read-only user under test.
	h.login("admin", "admin-password")
	dr := mustTagWeb(t, h, "http-viewer-dr")
	assetID := mustAssetWeb(t, h, domain.KindVM, "http-viewer-cannot-tag")

	logoutToken := h.csrfToken("/")
	h.post("/logout", url.Values{"csrf_token": {logoutToken}}, false).Body.Close()
	h.login("viewer", "viewer-password")
	token := h.csrfToken("/assets")
	resp := h.post("/assets/tags/apply", url.Values{
		"csrf_token": {token},
		"tag_id":     {dr},
		"entity":     {selectionValue(assetID, assetRowVersionWeb(t, h, assetID))},
	}, true)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}

	tags, err := h.store.EntityTagsFor(context.Background(), domain.TagEntityAsset, assetID)
	if err != nil {
		t.Fatalf("EntityTagsFor: %v", err)
	}
	if len(tags) != 0 {
		t.Error("a read-only user's request tagged the asset")
	}
}
