// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import "github.com/madalinignisca/invctl/internal/domain"

// Permit is the one gate that mints a real domain.Permit from a signed-in
// user (WP-G1 Task 7). Every other minter in this codebase --
// domain.AdministratorPermit, domain.SystemPermit, domain.ScopedPermit -- is
// a low-level constructor a caller supplies its own decision to; this is the
// place that actually LOOKS at a user's role and decides what they get.
//
// TODO(WP-G1 Task 12): this is called from nowhere yet. The request-scoped
// gate that mints one Permit per request and threads it down through a
// handler into the store is Task 12's job, downstream of Task 10's 148-site
// store conversion. Until then, handlers reaching the six circuits.go write
// transactions mint domain.AdministratorPermit(actor(r)) directly at the call
// site -- see internal/web/handlers/circuits.go's package comment -- because
// every one of those routes already sits behind RequireAdmin and nothing
// changes about who can reach them by adding this method now. Adding this
// method here, ahead of its first caller, is deliberate: Task 9's tests
// (docs/AUDIT.md rule on machine credentials) need SystemPermit to exist and
// Task 12 needs THIS to exist, and both need internal/domain.Permit's shape
// to already be settled -- see the WP-G1 plan's "build order is not ship
// order".
//
// Project owners are NOT wired to a ScopedPermit that actually covers
// anything yet. CanWrite(ProjectOwner) already fails closed today (see that
// method's own doc comment, WP-G1 Task 4) because the object-level gate does
// not exist until Task 13 -- so returning a ScopedPermit with an empty scope
// here, rather than an error or a nil, keeps that same fail-closed answer
// available to a caller that asks this method before Task 13 lands, without
// this method having to know that Task 13 has not landed.
func (a *Authorizer) Permit(user *domain.AppUser) domain.Permit {
	actor := domain.Actor{Kind: domain.ActorKindUser}
	if user != nil {
		actor = domain.UserActor(user)
	}
	if user != nil && user.IsActive && a.isAdministrator(user) {
		return domain.AdministratorPermit(actor)
	}
	// nil projects, nil entities: a ScopedPermit that authorizes nothing.
	// Task 13/14 is what populates these from the user's actual project
	// assignments and the join tables that back them.
	return domain.ScopedPermit(actor, nil, nil)
}
