// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"errors"
	"net/http"

	"github.com/madalinignisca/invctl/internal/auth"
	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web/render"
)

// User administration (WP-G1 Task 5): the roster, the six routes that mutate
// it, and nothing else. This is the ONLY screen that grants a role, grants
// cost visibility, activates or deactivates an account, or scrubs one.
//
// EVERY ROUTE HERE SITS BEHIND THE SAME write(...) CLOSURE AS EVERYTHING ELSE
// IN routes.go -- RequireAuth, RequireAdmin, and the mux-wide CSRF wrapper.
// There is no second admission check in this file, on purpose: a route added
// here without going through write(...) would be a silent hole in exactly the
// screen that grants privilege.
//
// minPasswordLength is a POC-level floor, not a policy engine. A local
// account is one of two authentication paths (the other is LDAP, which never
// touches a password at all), so this is the only place a weak local password
// could be chosen.
const minPasswordLength = 12

// userListPage is the roster.
type userListPage struct {
	Base
	Rows []userRowView
	// Roles is the fixed vocabulary the role picker offers, from the domain
	// constant set -- never a caller-supplied list, so a rendered option can
	// never name a role the CHECK constraint would refuse.
	Roles []string
	// ActiveAdminCount lets the roster warn before the operator even submits:
	// "this is the last one" is more useful read on the page than discovered
	// only after a refused POST.
	ActiveAdminCount int
	Errors           map[string]string
	FormUsername     string
}

// userRowView decorates one account for the row partial. A real type rather
// than a bare *domain.AppUser because the row also needs the CSRF token and
// the role vocabulary, and those are not properties of the account.
type userRowView struct {
	// A pointer, deliberately: domain.AppUser.Display() has a pointer
	// receiver, and this struct is handed to html/template by value (never
	// addressable once it has been through an `any`), so a value field here
	// makes every method call on it fail at render time with "can't evaluate
	// field Display in type domain.AppUser" instead of at compile time.
	User  *domain.AppUser
	CSRF  string
	Roles []string
	// EffectiveAdmin and OverrideNote answer "who can write here" from
	// a.Authz, NEVER from User.Role read directly -- see userRow's own doc
	// comment for why that distinction is the whole point of this screen.
	EffectiveAdmin bool
	OverrideNote   string
}

// userRow builds one row's view model, computing effective access from
// a.Authz rather than reading User.Role straight off the database.
//
// EVERY EXISTING ESTATE UPGRADING INTO ROLE-BASED ACCESS HAS EVERY ROW AT
// role='observer' -- migration 00058's default -- while INV_ADMIN_USERS keeps
// naming whoever can actually write, break-glass override (Task 4). A roster
// that rendered User.Role unmodified would show the one person who can
// change anything on this estate as a read-only Observer, on the one screen
// whose entire job is answering who can write here. Computed here, in the
// handler's view model, for the same reason secret_ref redaction lives here
// and not in the template: a template-side branch on the role string is one
// {{end}} away from silently reintroducing exactly this.
//
// Mutation: render User.Role alone again (drop this function's callers) --
// TestTheRosterShowsEffectiveAdminAccessGrantedByEnvOverride must go red.
func (a *App) userRow(u *domain.AppUser, csrf string) userRowView {
	v := userRowView{User: u, CSRF: csrf, Roles: domain.Roles}
	v.EffectiveAdmin = a.Authz.IsAdministrator(u)
	// The note is only useful when it explains a DIVERGENCE: an active
	// administrator named in INV_ADMIN_USERS whose role column also says
	// administrator needs no explanation, and showing one anyway is the
	// "otherwise the marker is decoration" case this file's own tests guard.
	if v.EffectiveAdmin && u.Role != domain.RoleAdministrator && a.Authz.EnvOverride(u) {
		v.OverrideNote = "Administrator (from INV_ADMIN_USERS) — overrides the role picker below. See docs/RECOVERY.md."
	}
	return v
}

// UserList renders the roster.
func (a *App) UserList(w http.ResponseWriter, r *http.Request) {
	a.renderUserList(w, r, http.StatusOK, nil, "")
}

func (a *App) renderUserList(w http.ResponseWriter, r *http.Request, status int,
	errs map[string]string, formUsername string) {

	users, err := a.Store.ListUsers(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	activeAdmins, err := a.Store.CountActiveAdministrators(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	b := a.base(r, "Users", "users")
	rows := make([]userRowView, len(users))
	for i := range users {
		rows[i] = a.userRow(&users[i], b.CSRF)
	}

	a.Render.Respond(w, r, status, "user_list", "user_list_panel", userListPage{
		Base:             b,
		Rows:             rows,
		Roles:            domain.Roles,
		ActiveAdminCount: activeAdmins,
		Errors:           orEmpty(errs),
		FormUsername:     formUsername,
	})
}

// UserCreate adds a local account. Always an Observer to start -- NewAppUser's
// safe default, matching the same reasoning ScrubUser's doc comment gives for
// why a re-created LDAP account never inherits a former one's role: nobody
// should gain a privileged role except through the role route, deliberately,
// by an administrator who can see what they are granting.
func (a *App) UserCreate(w http.ResponseWriter, r *http.Request) {
	username := formValue(r, "username")
	password := formValue(r, "password")

	if password == "" {
		a.renderUserList(w, r, http.StatusUnprocessableEntity,
			map[string]string{"password": "is required"}, username)
		return
	}
	if len(password) < minPasswordLength {
		a.renderUserList(w, r, http.StatusUnprocessableEntity,
			map[string]string{"password": "must be at least 12 characters"}, username)
		return
	}

	u, err := domain.NewAppUser(store.NewID(), username, domain.UserSourceLocal, a.Store.Now())
	if err != nil {
		if errs, ok := validationErrors(err); ok {
			a.renderUserList(w, r, http.StatusUnprocessableEntity, errs, username)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	u.PasswordHash = &hash

	if err := a.Store.CreateUser(r.Context(), a.permit(r), u); err != nil {
		if isConflict(err) {
			a.renderUserList(w, r, http.StatusUnprocessableEntity,
				map[string]string{"username": "an account with that username already exists"}, username)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}

	a.setFlash(r, "success", "User "+u.Username+" created.")
	render.Redirect(w, r, "/users")
}

// UserSetRole grants or revokes a role. POST /users/{id}/role.
func (a *App) UserSetRole(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	role := formValue(r, "role")
	// a.permit(r), never domain.AdministratorPermit: this previously minted
	// an administrator permit on the reasoning that the route is
	// RequireAdmin-only. Of every place that shim appeared, this was the
	// worst -- SetUserRole is the route that GRANTS roles, and since Task 13
	// made auth.CanWrite true for a project owner, RequireAdmin now admits
	// one here. With the caller's own permit, app_user is estate
	// configuration and tx.log refuses it, so a project owner reaching this
	// handler still cannot make themselves an Administrator. See
	// circuits.go's comment.
	err := a.Store.SetUserRole(r.Context(), a.permit(r), id, role)
	a.respondUserMutation(w, r, id, err, "Role updated.")
}

// UserSetCosts is the single cost-grant control, for an Observer and a
// project owner alike -- see this file's package comment and
// docs/rbac-design.md §3. There is no second route anywhere in this codebase
// that flips can_see_costs; TestTheCostGrantIsOneControlForObserversAndProject
// OwnersAlike exists to keep it that way. POST /users/{id}/costs.
func (a *App) UserSetCosts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := a.Store.SetUserCostVisibility(r.Context(), a.permit(r), id, checkbox(r, "can_see_costs"))
	a.respondUserMutation(w, r, id, err, "Cost visibility updated.")
}

// UserSetActive activates or deactivates an account. POST /users/{id}/active.
func (a *App) UserSetActive(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := a.Store.SetUserActive(r.Context(), a.permit(r), id, checkbox(r, "active"))
	a.respondUserMutation(w, r, id, err, "Account state updated.")
}

// UserScrub is the GDPR erasure route (spec §10a) -- see store.ScrubUser's own
// doc comment for what it does and why id is kept. POST /users/{id}/scrub.
func (a *App) UserScrub(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := a.Store.ScrubUser(r.Context(), a.permit(r), id)
	a.respondUserMutation(w, r, id, err, "Account scrubbed. Its history stays; it no longer names anyone.")
}

// respondUserMutation is the one place all four mutating user routes decide
// how to answer, so the shape is enforced once rather than four times.
//
// A refusal because of the last-Administrator guard is domain.ErrForbidden
// and is answered with THE STORE'S OWN MESSAGE, not the generic "You have
// read-only access." handleStoreError gives every other ErrForbidden in this
// codebase -- that generic text is correct for "you may not do this at all"
// and actively misleading for "this specific write is refused because it
// would leave nobody able to administer invctl", which is a fact about the
// estate the operator needs to see to understand what to do next
// (TestRefusingTheLastAdministratorRendersTheReasonAndNotAGenericError).
func (a *App) respondUserMutation(w http.ResponseWriter, r *http.Request, id string, err error, successText string) {
	if err != nil {
		if errors.Is(err, domain.ErrForbidden) {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		if _, ok := validationErrors(err); ok {
			a.renderUserRowError(w, r, id, err)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}

	u, err := a.Store.GetUser(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.respondUserRow(w, r, u, "success", successText)
}

// renderUserRowError re-opens the row a refused edit belongs to, in error
// state -- 422, with the row re-rendered rather than a generic failure page,
// the same rule every other inline editor in this codebase follows.
func (a *App) renderUserRowError(w http.ResponseWriter, r *http.Request, id string, err error) {
	u, gerr := a.Store.GetUser(r.Context(), id)
	if gerr != nil {
		a.handleStoreError(w, r, gerr)
		return
	}
	if render.IsHTMX(r) {
		b := a.base(r, "", "")
		a.Render.PartialWithOOB(w, http.StatusUnprocessableEntity, "user_row",
			a.userRow(u, b.CSRF),
			oobFlash("error", err.Error()))
		return
	}
	a.setFlash(r, "error", err.Error())
	a.renderUserList(w, r, http.StatusUnprocessableEntity, nil, "")
}

// respondUserRow answers a successful mutation: the one row that changed,
// swapped in place for an HTMX request (the same shape DependencyVerify's row
// update uses), or a real redirect back to the roster for a plain form post --
// which is what keeps this page working with JavaScript off, per CLAUDE.md's
// HTMX conventions.
func (a *App) respondUserRow(w http.ResponseWriter, r *http.Request, u *domain.AppUser, flashKind, flashText string) {
	if render.IsHTMX(r) {
		b := a.base(r, "", "")
		a.Render.PartialWithOOB(w, http.StatusOK, "user_row",
			a.userRow(u, b.CSRF),
			oobFlash(flashKind, flashText))
		return
	}
	a.setFlash(r, flashKind, flashText)
	render.Redirect(w, r, "/users")
}
