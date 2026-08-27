// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"fmt"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Permit is the one gate that mints a real domain.Permit from a signed-in
// user (WP-G1 Task 7/Task 12). Every other minter in this codebase --
// domain.AdministratorPermit, domain.SystemPermit, domain.ScopedPermit -- is
// a low-level constructor a caller supplies its own decision to; this is the
// place that actually LOOKS at a user's role and project assignments and
// decides what they get. internal/web/handlers/app.go's permit(r) is the
// only caller (WP-G1 Task 12, step 3) -- see that function's own comment.
//
// A PERMIT COVERING NOTHING AND NO PERMIT AT ALL ARE DIFFERENT. An Observer,
// or anyone who is not signed in, active, an Administrator or a project
// owner, gets (nil, domain.ErrForbidden) here -- never a
// domain.ScopedPermit with an empty scope. The two look interchangeable to a
// caller that only asks Covers() and gets false either way, but they fail at
// different points: an error here refuses the write before a transaction
// opens, while an empty-scope permit would only be refused inside one, at
// tx.log, after every other statement in that transaction has already run
// and has to be rolled back. See TestAnObserverGetsNoPermitAtAll.
//
// NO CACHE, ON PURPOSE. This method is called once per request (Task 12) and
// asks a.projects fresh every time -- it does not remember the answer across
// requests, and nothing here should be "optimised" to. The estate this is
// sized for (docs/rbac-design.md §1) is 50-100 people and a project's linked
// entities number in the hundreds, not millions, so the query cost of asking
// every time is not the problem a cache would be solving. What a cache WOULD
// buy is staleness: a project owner removed from a project mid-session would
// keep writing to it for as long as the cache lived, which is precisely the
// class of bug WP-G1 exists to close. A permit holding project IDS and
// asking the store per Covers() call, inside the write transaction, is also
// correct and slightly fresher than resolving everything up front here --
// but internal/store/store.go's tx.log runs on SQLite's single-connection
// writer pool, so a lookup made from inside an open write transaction
// serialises behind the very lock that write already holds. Resolving the
// whole scope once, before the transaction opens, is what avoids that.
func (a *Authorizer) Permit(ctx context.Context, user *domain.AppUser) (domain.Permit, error) {
	if user == nil || !user.IsActive {
		return nil, domain.ErrForbidden
	}
	actor := domain.UserActor(user)
	if a.isAdministrator(user) {
		return domain.AdministratorPermit(actor), nil
	}
	if user.Role != domain.RoleProjectOwner {
		// Observer, or any role this package does not otherwise recognise:
		// no permit at all. See the doc comment above for why this is not
		// domain.ScopedPermit(actor, nil, nil).
		return nil, domain.ErrForbidden
	}

	// A project owner assigned to nothing is the honest edge
	// docs/rbac-design.md §4 calls out: "entities in no project are
	// Administrator territory" cuts both ways, so this still returns a real
	// permit -- one whose scope happens to be empty -- rather than an error.
	// Unlike an Observer, a project owner IS meant to reach a write handler
	// in general; it is the OBJECT that may turn out to be out of scope, not
	// the role itself, and that distinction is exactly why this branch does
	// not also return domain.ErrForbidden.
	projectIDs, err := a.projects.ProjectsForUser(ctx, user.ID)
	if err != nil {
		return nil, fmt.Errorf("loading project assignments for %s: %w", user.Username, err)
	}

	assetIDs, err := a.projects.AssetIDsForProjects(ctx, projectIDs)
	if err != nil {
		return nil, fmt.Errorf("resolving asset scope for %s: %w", user.Username, err)
	}
	serviceIDs, err := a.projects.ServiceIDsForProjects(ctx, projectIDs)
	if err != nil {
		return nil, fmt.Errorf("resolving service scope for %s: %w", user.Username, err)
	}
	circuitIDs, err := a.projects.CircuitIDsForProjects(ctx, projectIDs)
	if err != nil {
		return nil, fmt.Errorf("resolving circuit scope for %s: %w", user.Username, err)
	}

	entities := domain.ScopedEntities{
		"asset":   idSet(assetIDs),
		"service": idSet(serviceIDs),
		"circuit": idSet(circuitIDs),
	}
	return domain.ScopedPermit(actor, projectIDs, entities), nil
}

// idSet turns a flat id list into the map shape domain.ScopedEntities wants
// for one entity type. A nil or empty ids is a nil map, which
// domain.ScopedPermit copies into an empty (not nil) entry -- either way
// Covers answers false for it, which is the correct "linked to nothing"
// state for a project owner with no assignments.
func idSet(ids []string) map[string]bool {
	if len(ids) == 0 {
		return nil
	}
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}
