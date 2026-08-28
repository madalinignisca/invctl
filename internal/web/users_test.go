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
	"net/url"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// User administration (WP-G1 Task 5): the roster and its six routes, through
// the real router -- the same reasoning every other *_test.go in this package
// gives for driving HTTP rather than calling a handler function directly.
// TestAnObserverCannotReachAnyUserAdministrationRoute in particular exists
// because a handler test that injects router params by hand never asks
// whether the route is actually reachable.

func strPtr(s string) *string { return &s }

// mustWebUserWithRole creates an account carrying a given role directly
// through the store, so a test can set up "the sole real Administrator" or "a
// project owner" without depending on the very routes under test to get
// there. Mirrors internal/store's mustUserWithRole for the same reason.
func mustWebUserWithRole(t *testing.T, h *harness, username, role string) *domain.AppUser {
	t.Helper()
	ctx := context.Background()
	u, err := domain.NewAppUser(store.NewID(), username, domain.UserSourceLocal, h.store.Now())
	if err != nil {
		t.Fatalf("building user %s: %v", username, err)
	}
	u.Role = role
	if err := h.store.CreateUser(ctx, domain.AdministratorPermit(domain.SystemActor), u); err != nil {
		t.Fatalf("creating user %s: %v", username, err)
	}
	return u
}

// TestAnObserverCannotReachAnyUserAdministrationRoute: built from the route
// patterns themselves rather than six copies of the same assertion, so adding
// a seventh route to routes.go without adding it here is the only way to miss
// coverage, and that omission is visible in the diff.
func TestAnObserverCannotReachAnyUserAdministrationRoute(t *testing.T) {
	h := newHarness(t)
	subject := mustWebUserWithRole(t, h, "route-subject", domain.RoleObserver)

	h.login("viewer", "viewer-password")
	// A page the viewer CAN read, purely to harvest a valid CSRF token --
	// without one every POST below would fail at the CSRF layer (400) and
	// prove nothing about RequireWrite, which is what this test is for.
	token := h.csrfToken("/")

	cases := []struct {
		method, path string
	}{
		{http.MethodGet, "/users"},
		{http.MethodPost, "/users"},
		{http.MethodPost, "/users/" + subject.ID + "/role"},
		{http.MethodPost, "/users/" + subject.ID + "/costs"},
		{http.MethodPost, "/users/" + subject.ID + "/active"},
		{http.MethodPost, "/users/" + subject.ID + "/scrub"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			var resp *http.Response
			if tc.method == http.MethodGet {
				resp = h.get(tc.path, false)
			} else {
				resp = h.post(tc.path, url.Values{"csrf_token": {token}}, false)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("status = %d, want 403", resp.StatusCode)
			}
		})
	}
}

// TestARoleChangeFromAnHTMXRequestReturnsTheRowPartialAndNotAWholePage.
func TestARoleChangeFromAnHTMXRequestReturnsTheRowPartialAndNotAWholePage(t *testing.T) {
	h := newHarness(t)
	subject := mustWebUserWithRole(t, h, "role-htmx-subject", domain.RoleObserver)
	h.login("admin", "admin-password")
	token := h.csrfToken("/users")

	resp := h.post("/users/"+subject.ID+"/role",
		url.Values{"csrf_token": {token}, "role": {domain.RoleProjectOwner}}, true)
	text := body(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if strings.Contains(text, "<!doctype html>") {
		t.Error("an HTMX role change received a whole page")
	}
	if !strings.Contains(text, "user-row-"+subject.ID) {
		t.Error("the response is not the row partial for the subject")
	}
}

// TestARoleChangeWithoutACSRFTokenIsRefused: these routes are inside the
// mux-wide CSRF wrapper and are not in the exemption list.
func TestARoleChangeWithoutACSRFTokenIsRefused(t *testing.T) {
	h := newHarness(t)
	subject := mustWebUserWithRole(t, h, "csrf-subject", domain.RoleObserver)
	h.login("admin", "admin-password")

	resp := h.post("/users/"+subject.ID+"/role",
		url.Values{"csrf_token": {""}, "role": {domain.RoleProjectOwner}}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}

	after, err := h.store.GetUser(context.Background(), subject.ID)
	if err != nil {
		t.Fatalf("loading subject: %v", err)
	}
	if after.Role != domain.RoleObserver {
		t.Error("the role change took effect despite the missing CSRF token")
	}
}

// TestRefusingTheLastAdministratorRendersTheReasonAndNotAGenericError.
//
// The seeded "admin" account carries write access through INV_ADMIN_USERS,
// not through app_user.role -- its own row is role=observer, by design (see
// Authorizer.isAdministrator). CountActiveAdministrators counts the ROLE
// column, so this test builds a second, genuine Administrator: the one whose
// demotion the guard exists to refuse.
func TestRefusingTheLastAdministratorRendersTheReasonAndNotAGenericError(t *testing.T) {
	h := newHarness(t)
	sole := mustWebUserWithRole(t, h, "sole-admin", domain.RoleAdministrator)
	h.login("admin", "admin-password")
	token := h.csrfToken("/users")

	resp := h.post("/users/"+sole.ID+"/role",
		url.Values{"csrf_token": {token}, "role": {domain.RoleObserver}}, false)
	text := body(t, resp)

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if !strings.Contains(text, "administ") {
		t.Errorf("the refusal does not explain why: %s", text)
	}
	if strings.Contains(text, "You have read-only access") || strings.Contains(text, "You are not allowed") {
		t.Errorf("the refusal fell back to the generic message: %s", text)
	}

	after, err := h.store.GetUser(context.Background(), sole.ID)
	if err != nil {
		t.Fatalf("loading subject: %v", err)
	}
	if after.Role != domain.RoleAdministrator {
		t.Error("the refused demotion still took effect")
	}
}

// TestCreatingALocalUserStoresAnArgon2idHashAndNeverThePlaintext.
func TestCreatingALocalUserStoresAnArgon2idHashAndNeverThePlaintext(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	token := h.csrfToken("/users")

	const plaintext = "correct-horse-battery-staple"
	resp := h.post("/users", url.Values{
		"csrf_token": {token},
		"username":   {"new-local-user"},
		"password":   {plaintext},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}

	ctx := context.Background()
	u, err := h.store.GetUserByUsername(ctx, "new-local-user")
	if err != nil {
		t.Fatalf("loading the new user: %v", err)
	}
	if u.PasswordHash == nil || !strings.HasPrefix(*u.PasswordHash, "$argon2id$") {
		t.Errorf("password_hash = %v, want an argon2id hash", u.PasswordHash)
	}
	if u.Role != domain.RoleObserver {
		t.Errorf("role = %s, want the safe default observer", u.Role)
	}

	changes, err := h.store.ListChangesForEntity(ctx, "app_user", u.ID, 10)
	if err != nil {
		t.Fatalf("listing changes: %v", err)
	}
	if len(changes) == 0 {
		t.Fatal("the account creation was not audited at all")
	}
	for _, c := range changes {
		if strings.Contains(c.Diff, plaintext) {
			t.Fatalf("the plaintext password reached the audit trail: %s", c.Diff)
		}
	}
}

// TestTheCostGrantIsOneControlForObserversAndProjectOwnersAlike.
//
// One column, one screen, one audited change: POST /users/{id}/costs is the
// SAME route for an Observer and a project owner, and net/http.ServeMux
// itself would panic at startup if routes.go tried to register a second
// handler under that pattern -- so "there is exactly one route" is enforced
// by the router, not merely asserted here. What this test proves is the
// other half: the one route actually works, identically, for both roles.
func TestTheCostGrantIsOneControlForObserversAndProjectOwnersAlike(t *testing.T) {
	h := newHarness(t)
	observer := mustWebUserWithRole(t, h, "cost-observer", domain.RoleObserver)
	owner := mustWebUserWithRole(t, h, "cost-owner", domain.RoleProjectOwner)
	h.login("admin", "admin-password")
	token := h.csrfToken("/users")

	for _, subject := range []*domain.AppUser{observer, owner} {
		t.Run(subject.Role, func(t *testing.T) {
			resp := h.post("/users/"+subject.ID+"/costs",
				url.Values{"csrf_token": {token}, "can_see_costs": {"true"}}, false)
			resp.Body.Close()
			if resp.StatusCode != http.StatusSeeOther {
				t.Fatalf("status = %d, want 303", resp.StatusCode)
			}
			after, err := h.store.GetUser(context.Background(), subject.ID)
			if err != nil {
				t.Fatalf("loading subject: %v", err)
			}
			if !after.CanSeeCosts {
				t.Errorf("can_see_costs was not granted to a %s through /users/{id}/costs", subject.Role)
			}
		})
	}
}

// TestTheRosterShowsEffectiveAdminAccessGrantedByEnvOverride.
//
// The seeded "admin" account is exactly the case that motivates this: its
// app_user.role column is "observer" (NewAppUser's default, migration
// 00058), and it writes everything only because the harness's INV_ADMIN_USERS
// names it (webTemplate / newHarnessSecure's cfg.AdminUsers). Every existing
// estate upgrading into role-based access looks like this on day one. A
// roster that rendered the stored column alone would show the only account
// that can change anything here as read-only.
//
// Mutation: make a.userRow return userRowView{User: u, ...} without computing
// EffectiveAdmin/OverrideNote (i.e. render .User.Role alone again) -- this
// must go red.
func TestTheRosterShowsEffectiveAdminAccessGrantedByEnvOverride(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	admin, err := h.store.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatalf("loading the seeded admin: %v", err)
	}
	if admin.Role != domain.RoleObserver {
		t.Fatalf("setup: the seeded admin's role column = %q, want %q -- "+
			"the whole point of this test depends on the column disagreeing "+
			"with effective access", admin.Role, domain.RoleObserver)
	}

	page := body(t, h.get("/users", false))
	adminRow := rowByID(t, page, "user-row-"+admin.ID)
	if !strings.Contains(adminRow, "INV_ADMIN_USERS") {
		t.Errorf("the seeded admin's row does not say its access comes from INV_ADMIN_USERS: %s", adminRow)
	}
	if !strings.Contains(strings.ToLower(adminRow), "administrator") {
		t.Errorf("the seeded admin's row does not say Administrator anywhere despite writing everything: %s", adminRow)
	}
}

// TestTheRosterShowsNoOverrideMarkerForARealAdministrator: the marker must be
// absent when it would explain nothing, or it is decoration rather than
// information -- an administrator by role, not named in INV_ADMIN_USERS at
// all, needs no note about where their access comes from.
func TestTheRosterShowsNoOverrideMarkerForARealAdministrator(t *testing.T) {
	h := newHarness(t)
	real := mustWebUserWithRole(t, h, "role-based-admin", domain.RoleAdministrator)
	h.login("admin", "admin-password")

	page := body(t, h.get("/users", false))
	row := rowByID(t, page, "user-row-"+real.ID)
	if strings.Contains(row, "INV_ADMIN_USERS") {
		t.Errorf("a role-based administrator's row carries an override note it does not need: %s", row)
	}
}

// rowByID extracts the <tr id="{marker}">...</tr> fragment so an assertion
// about one row cannot accidentally match another row's markup elsewhere on
// the same page. Distinct from costs_test.go's rowContaining, which matches
// on a form attribute rather than the row's own id.
func rowByID(t *testing.T, page, marker string) string {
	t.Helper()
	start := strings.Index(page, `id="`+marker+`"`)
	if start == -1 {
		t.Fatalf("no row found with id %q", marker)
	}
	end := strings.Index(page[start:], "</tr>")
	if end == -1 {
		t.Fatalf("row %q was never closed", marker)
	}
	return page[start : start+end]
}

// TestScrubbingTheLastActiveAdministratorIsRefused: spec §8's guard applies to
// this verb too, reached through its own HTTP route rather than only at the
// store layer -- store.TestTheLastAdministratorGuardCoversAllThreeVerbs
// covers the store call directly; this proves the route wired to it is the
// same call.
func TestScrubbingTheLastActiveAdministratorIsRefused(t *testing.T) {
	h := newHarness(t)
	sole := mustWebUserWithRole(t, h, "sole-admin-scrub", domain.RoleAdministrator)
	h.login("admin", "admin-password")
	token := h.csrfToken("/users")

	resp := h.post("/users/"+sole.ID+"/scrub", url.Values{"csrf_token": {token}}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}

	after, err := h.store.GetUser(context.Background(), sole.ID)
	if err != nil {
		t.Fatalf("loading subject: %v", err)
	}
	if after.Username != "sole-admin-scrub" {
		t.Error("the refused scrub still took effect")
	}
}

// TestAScrubbedUserResolvesToNoPersonalDataAnywhereItIsRendered: the journal,
// the change log, and the user roster itself.
func TestAScrubbedUserResolvesToNoPersonalDataAnywhereItIsRendered(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	subject, err := domain.NewAppUser(store.NewID(), "scrub-render-subject", domain.UserSourceLocal, h.store.Now())
	if err != nil {
		t.Fatalf("building user: %v", err)
	}
	subject.DisplayName = strPtr("Priya Patel")
	subject.Email = strPtr("priya@example.com")
	if err := h.store.CreateUser(ctx, domain.AdministratorPermit(domain.SystemActor), subject); err != nil {
		t.Fatalf("creating user: %v", err)
	}

	assetID := h.refs.Assets["hv-01"]
	entry, err := domain.NewJournalEntry(store.NewID(), "asset", assetID, domain.JournalNote,
		"left a note", subject.ID, h.store.Now())
	if err != nil {
		t.Fatalf("building journal entry: %v", err)
	}
	if err := h.store.CreateJournalEntry(ctx, domain.AdministratorPermit(domain.UserActor(subject)), entry); err != nil {
		t.Fatalf("creating journal entry: %v", err)
	}

	h.login("admin", "admin-password")
	before := body(t, h.get("/assets/"+assetID, false))
	if !strings.Contains(before, "Priya Patel") {
		t.Fatal("setup: the journal does not show the author's display name before scrubbing")
	}

	token := h.csrfToken("/users")
	resp := h.post("/users/"+subject.ID+"/scrub", url.Values{"csrf_token": {token}}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("scrub status = %d, want 303", resp.StatusCode)
	}

	for _, path := range []string{"/assets/" + assetID, "/changes", "/users"} {
		page := body(t, h.get(path, false))
		if strings.Contains(page, "Priya Patel") {
			t.Errorf("GET %s still renders the scrubbed user's display name", path)
		}
		if strings.Contains(page, "priya@example.com") {
			t.Errorf("GET %s still renders the scrubbed user's email", path)
		}
	}
}

// TestScrubbingIsItselfAudited: one change_log row, and the diff carries no
// scrubbed value -- an erasure that records what it erased has erased
// nothing. Covers domain.RedactedFieldsByEntity["AppUser"] for username,
// display_name and email through the HTTP route rather than only at the
// store layer.
func TestScrubbingIsItselfAudited(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	subject, err := domain.NewAppUser(store.NewID(), "audited-scrub-subject", domain.UserSourceLocal, h.store.Now())
	if err != nil {
		t.Fatalf("building user: %v", err)
	}
	subject.DisplayName = strPtr("Jamie Rivera")
	subject.Email = strPtr("jamie@example.com")
	if err := h.store.CreateUser(ctx, domain.AdministratorPermit(domain.SystemActor), subject); err != nil {
		t.Fatalf("creating user: %v", err)
	}

	h.login("admin", "admin-password")
	token := h.csrfToken("/users")
	resp := h.post("/users/"+subject.ID+"/scrub", url.Values{"csrf_token": {token}}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}

	changes, err := h.store.ListChangesForEntity(ctx, "app_user", subject.ID, 10)
	if err != nil {
		t.Fatalf("listing changes: %v", err)
	}
	if len(changes) == 0 {
		t.Fatal("scrubbing wrote no change_log row at all")
	}
	latest := changes[0]
	if latest.EntityType != "app_user" {
		t.Errorf("entity_type = %q, want app_user", latest.EntityType)
	}
	if strings.Contains(latest.Diff, "Jamie Rivera") {
		t.Errorf("the scrub diff carries the old display name: %s", latest.Diff)
	}
	if strings.Contains(latest.Diff, "jamie@example.com") {
		t.Errorf("the scrub diff carries the old email: %s", latest.Diff)
	}
	if strings.Contains(latest.Diff, "audited-scrub-subject") {
		t.Errorf("the scrub diff carries the old username: %s", latest.Diff)
	}
	if !strings.Contains(latest.Diff, domain.Redacted) {
		t.Errorf("the scrub diff has no redaction marker at all: %s", latest.Diff)
	}
}

// ---------------------------------------------------------------------------
// secret_ref redaction (spec §10): the read path, not the audit trail --
// internal/store already redacts identity.secret_ref in snapshotJSON/
// diffJSON (see internal/store/boundary_test.go's TestSnapshotRedactsSecretRef
// and store_test.go's TestSecretRefNeverReachesTheAuditTrail).

// dependencyWithSecretRef wires up one dependency whose identity carries a
// secret_ref, on an existing seeded consumer/provider pair, and returns the
// consumer service id and the raw secret value.
func dependencyWithSecretRef(t *testing.T, h *harness, ref string) (serviceID, secret string) {
	t.Helper()
	ctx := context.Background()

	identity, err := domain.NewIdentity(store.NewID(), domain.IdentityServiceAccount, "svc-redaction-test")
	if err != nil {
		t.Fatalf("building identity: %v", err)
	}
	identity.SecretRef = strPtr(ref)
	if err := h.store.CreateIdentity(ctx, domain.AdministratorPermit(domain.SystemActor), identity); err != nil {
		t.Fatalf("creating identity: %v", err)
	}

	consumerID := h.refs.Services["orders-web"]
	spec := domain.DependencySpec{
		ConsumerServiceID:  consumerID,
		ProviderEndpointID: strPtr(h.refs.Endpoints["rabbitmq/amqp"]),
		Nature:             domain.NatureHard,
		FailureMode:        "redaction test",
		IdentityID:         strPtr(identity.ID),
	}
	dep, err := domain.NewDependency(store.NewID(), spec, h.store.Now())
	if err != nil {
		t.Fatalf("building dependency: %v", err)
	}
	if err := h.store.CreateDependency(ctx, domain.AdministratorPermit(domain.SystemActor), dep, nil); err != nil {
		t.Fatalf("creating dependency: %v", err)
	}
	return consumerID, ref
}

// TestANonAdministratorNeverSeesASecretReferenceOnAServicePage.
//
// Mutation: put the raw SecretRef back on the view model for every role (i.e.
// pass isAdmin unconditionally as true, or read .Dep.IdentitySecretRef
// directly from the template) -- this test must go red.
func TestANonAdministratorNeverSeesASecretReferenceOnAServicePage(t *testing.T) {
	h := newHarness(t)
	serviceID, secret := dependencyWithSecretRef(t, h, "kv/prod/redaction-test/db")

	h.login("viewer", "viewer-password")
	page := body(t, h.get("/services/"+serviceID, false))

	if strings.Contains(page, secret) {
		t.Errorf("a non-administrator's page contains the raw secret reference %q", secret)
	}
	if !strings.Contains(page, domain.Redacted) {
		t.Error("a non-administrator's page does not show the redaction marker at all")
	}
}

// TestAnAdministratorSeesTheSecretReference: redaction that also hides the
// value from the one person who legitimately needs it is indistinguishable
// from the field being broken.
func TestAnAdministratorSeesTheSecretReference(t *testing.T) {
	h := newHarness(t)
	serviceID, secret := dependencyWithSecretRef(t, h, "kv/prod/redaction-test/admin-view")

	h.login("admin", "admin-password")
	page := body(t, h.get("/services/"+serviceID, false))

	if !strings.Contains(page, secret) {
		t.Error("an administrator's page does not show the secret reference")
	}
}

// TestASecretReferenceIsAbsentFromEveryCSVExportForANonAdministrator: the
// export path is the one that gets forgotten, because it never passes
// through a template at all.
func TestASecretReferenceIsAbsentFromEveryCSVExportForANonAdministrator(t *testing.T) {
	h := newHarness(t)
	_, secret := dependencyWithSecretRef(t, h, "kv/prod/redaction-test/csv")

	h.login("viewer", "viewer-password")
	for _, path := range []string{"/services?format=csv", "/assets?format=csv", "/circuits?format=csv"} {
		text := body(t, h.get(path, false))
		if strings.Contains(text, secret) {
			t.Errorf("GET %s leaks the secret reference into a CSV export", path)
		}
	}
}
