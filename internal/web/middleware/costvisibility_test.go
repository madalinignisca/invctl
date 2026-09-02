// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/madalinignisca/invctl/internal/auth"
	"github.com/madalinignisca/invctl/internal/domain"
)

// nextCalledRecorder is a tiny http.Handler that records whether it ran, so
// each case below can assert the middleware's actual effect on the request
// -- reached the handler, or was refused before it -- rather than only the
// status code, which a redirect or a validation failure downstream could
// also produce.
func nextCalledRecorder() (http.Handler, *bool) {
	called := false
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	return h, &called
}

// TestRequireCostVisibility covers the four cases WP-1.1 Task 4a's brief
// names by hand: a grant that lets a project owner through, its absence that
// refuses them, an Administrator who is never refused regardless of the
// grant column, and an anonymous request that redirects to sign in rather
// than 403ing (matching RequireWrite's own convention -- see that
// middleware's comment).
//
// Each case is chosen so that RequireCostVisibility is the ONLY thing
// standing between the request and success where it should let the caller
// through, and a case where it is what refuses -- see this task's brief for
// why that pairing matters: a middleware that refuses everyone, or lets
// everyone through, can still pass a suite that only tests one direction.
func TestRequireCostVisibility(t *testing.T) {
	authz := auth.NewAuthorizer(nil, nil)

	administrator := &domain.AppUser{
		ID: "admin-1", Username: "admin", IsActive: true,
		Role: domain.RoleAdministrator, CanSeeCosts: false, // implicit, must not matter
	}
	ownerWithGrant := &domain.AppUser{
		ID: "owner-1", Username: "owner-granted", IsActive: true,
		Role: domain.RoleProjectOwner, CanSeeCosts: true,
	}
	ownerWithoutGrant := &domain.AppUser{
		ID: "owner-2", Username: "owner-ungranted", IsActive: true,
		Role: domain.RoleProjectOwner, CanSeeCosts: false,
	}

	tests := []struct {
		name       string
		user       *domain.AppUser
		wantCalled bool
		wantStatus int
	}{
		{
			name:       "project owner with the grant is let through",
			user:       ownerWithGrant,
			wantCalled: true,
			wantStatus: http.StatusOK,
		},
		{
			name:       "project owner without the grant is refused by this middleware",
			user:       ownerWithoutGrant,
			wantCalled: false,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "administrator is never refused, regardless of the grant column",
			user:       administrator,
			wantCalled: true,
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next, called := nextCalledRecorder()
			handler := RequireCostVisibility(authz)(next)

			req := httptest.NewRequest(http.MethodPost, "/assets/x/costs", nil)
			req = req.WithContext(WithUser(req.Context(), tt.user))
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if *called != tt.wantCalled {
				t.Errorf("next.ServeHTTP called = %v, want %v", *called, tt.wantCalled)
			}
			if rr.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body %q)", rr.Code, tt.wantStatus, rr.Body.String())
			}
		})
	}
}

// TestRequireCostVisibilityRedirectsAnonymousRequests matches RequireWrite's
// own convention (see that middleware's comment): a request with no
// signed-in user at all redirects to sign-in rather than 403ing, because a
// 403 here would tell an anonymous caller a route exists and is cost-gated,
// where a redirect asks for a session first, the same as every other
// authenticated surface.
func TestRequireCostVisibilityRedirectsAnonymousRequests(t *testing.T) {
	authz := auth.NewAuthorizer(nil, nil)
	next, called := nextCalledRecorder()
	handler := RequireCostVisibility(authz)(next)

	req := httptest.NewRequest(http.MethodPost, "/assets/x/costs", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if *called {
		t.Error("next.ServeHTTP was called for an anonymous request")
	}
	if rr.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d (render.Redirect's status for a non-htmx request)", rr.Code, http.StatusSeeOther)
	}
	if rr.Code == http.StatusForbidden {
		t.Error("an anonymous request received RequireCostVisibility's own 403 rather than a redirect to sign in")
	}
}
