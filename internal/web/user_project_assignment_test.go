// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"net/http"
	"net/url"
	"testing"
)

// TestAProjectOwnerCannotAssignThemselvesAProject is WP-G1 Task 19's own
// escalation test, named in the task brief. user_project is the table that
// decides a project owner's OWN scope (docs/rbac-design.md §11,
// internal/domain/role.go's comment on domain.ScopeEstateConfig): if a
// project owner could write it, they could assign themselves every project
// in the estate and become an Administrator in every way that matters.
//
// It drives the REAL route -- not a unit call into store.AssignProject --
// so what is asserted is what an attacker actually reaches: the router,
// CSRF, the session, RequireAuth/RequireAdministrator, and only then the
// handler and the permit layer. A project owner is signed in and asked to
// assign themselves fx.projectOther, a project they are NOT already
// assigned to (setupBoundary's own fixture comment), which is exactly the
// row that would grant them scope over it.
func TestAProjectOwnerCannotAssignThemselvesAProject(t *testing.T) {
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)
			h.login(boundaryOwnerUser, boundaryOwnerPassword)

			ownerID := h.lookup(`SELECT id FROM app_user WHERE username = ?`, boundaryOwnerUser)

			before := h.count(`SELECT COUNT(*) FROM user_project WHERE user_id = ? AND project_id = ?`,
				ownerID, fx.projectOther)
			if before != 0 {
				t.Fatalf("fixture already assigns the owner to fx.projectOther -- the test's own "+
					"premise (a project they do NOT hold) does not hold, got %d rows", before)
			}

			token := boundaryCSRFToken(t, h)
			resp := h.post("/users/"+ownerID+"/projects", url.Values{
				"csrf_token": {token},
				"project_id": {fx.projectOther},
			}, false)
			defer resp.Body.Close()
			gotBody := drainedBody(t, resp)

			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("assigning fx.projectOther to themselves as a project owner returned %d (body %q), want 403",
					resp.StatusCode, gotBody)
			}
			// THE LAYER THAT REFUSED. This route is registered writeAdminOnly
			// (routes.go), so a project owner is refused at
			// middleware.RequireAdministrator, before the handler -- and
			// therefore the permit layer -- is ever reached. See this
			// test's own mutation below, which moves the route to `write`
			// and checks the permit layer still catches it independently.
			const wantBody = "This requires an Administrator.\n"
			if gotBody != wantBody {
				t.Errorf("refusal body = %q, want %q -- this pins WHICH layer refused the request",
					gotBody, wantBody)
			}

			after := h.count(`SELECT COUNT(*) FROM user_project WHERE user_id = ? AND project_id = ?`,
				ownerID, fx.projectOther)
			if after != 0 {
				t.Fatalf("a project owner's self-assignment attempt created %d user_project row(s) -- "+
					"this is the exact escalation this test exists to catch, whatever status "+
					"the response carried", after)
			}
			afterLog := h.count(`SELECT COUNT(*) FROM change_log WHERE entity_type = 'user_project' `+
				`AND entity_id = ?`, ownerID+"/"+fx.projectOther)
			if afterLog != 0 {
				t.Errorf("change_log recorded %d row(s) for the refused self-assignment -- a refused "+
					"write must not leave an audit trail implying it happened", afterLog)
			}
		})
	}
}

// TestAnAdministratorCanAssignAndReleaseProjects covers the ordinary path
// AssignProject/ReleaseProject were built for and had no route driving until
// this task: an Administrator grants and revokes project scope, releasing an
// assignment that is not active is a no-op rather than an error, and
// assigning a pair that already holds an active assignment does not create a
// second row -- the partial unique index on (user_id, project_id) WHERE
// lifecycle = 'active' (migration 00059) exists to make that true at the
// schema level, and AssignProject's own SELECT-before-INSERT is the
// application-level half.
func TestAnAdministratorCanAssignAndReleaseProjects(t *testing.T) {
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)
			h.login(boundaryAdminUser, boundaryAdminPassword)

			targetID := fx.userOtherID
			token := boundaryCSRFToken(t, h)

			assign := func() *http.Response {
				return h.post("/users/"+targetID+"/projects", url.Values{
					"csrf_token": {token},
					"project_id": {fx.projectOther},
				}, false)
			}

			resp := assign()
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusOK {
				t.Fatalf("administrator assigning a project returned %d (body %q)",
					resp.StatusCode, drainedBody(t, resp))
			}
			if got := h.count(`SELECT COUNT(*) FROM user_project WHERE user_id = ? AND project_id = ? `+
				`AND lifecycle = 'active'`, targetID, fx.projectOther); got != 1 {
				t.Fatalf("active user_project rows after assign = %d, want 1", got)
			}

			// Assigning the same pair again must not create a duplicate row.
			resp2 := assign()
			defer resp2.Body.Close()
			if resp2.StatusCode != http.StatusSeeOther && resp2.StatusCode != http.StatusOK {
				t.Fatalf("re-assigning the same pair returned %d (body %q)",
					resp2.StatusCode, drainedBody(t, resp2))
			}
			if got := h.count(`SELECT COUNT(*) FROM user_project WHERE user_id = ? AND project_id = ?`,
				targetID, fx.projectOther); got != 1 {
				t.Fatalf("total user_project rows after re-assigning the same pair = %d, want 1 "+
					"(the partial unique index on active rows exists to guarantee this)", got)
			}

			// Release, then release again -- idempotent, no error either time.
			release := func() *http.Response {
				return h.post("/users/"+targetID+"/projects/"+fx.projectOther+"/release",
					url.Values{"csrf_token": {token}}, false)
			}
			resp3 := release()
			defer resp3.Body.Close()
			if resp3.StatusCode != http.StatusSeeOther && resp3.StatusCode != http.StatusOK {
				t.Fatalf("releasing an assignment returned %d (body %q)",
					resp3.StatusCode, drainedBody(t, resp3))
			}
			if got := h.count(`SELECT COUNT(*) FROM user_project WHERE user_id = ? AND project_id = ? `+
				`AND lifecycle = 'active'`, targetID, fx.projectOther); got != 0 {
				t.Fatalf("active user_project rows after release = %d, want 0", got)
			}

			resp4 := release()
			defer resp4.Body.Close()
			if resp4.StatusCode != http.StatusSeeOther && resp4.StatusCode != http.StatusOK {
				t.Fatalf("releasing an already-released assignment returned %d (body %q), want a no-op success",
					resp4.StatusCode, drainedBody(t, resp4))
			}
		})
	}
}
