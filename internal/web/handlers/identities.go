// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"net/http"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// Credential references: what a service authenticates as, and whether anybody
// has recorded rotating it.
//
// TWO DIFFERENT ANSWERS TO secret_ref ON TWO PAGES, and the difference is not a
// stylistic one.
//
// The LIST carries no path at all, for anybody, which is why identityListRow
// exists as a PROJECTION rather than the page simply holding []store.IdentityRow
// and the template declining to print one field. A row type with no SecretRef
// field cannot leak one through a template edit, a CSV export, or a debug dump.
// One screen listing every credential path in the estate is a reconnaissance
// gift in its most convenient possible form (domain.RedactedFields' own comment,
// and docs/AUDIT.md rule 12), and it is one export away from leaving the
// building.
//
// The DETAIL page renders the path, to an Administrator, one credential at a
// time -- computed HERE, in the handler, exactly where depRowData.SecretRef is
// computed (internal/web/handlers/forms.go:846-854) and for the reason that
// field's comment gives: "a template-side {{if .IsAdmin}} around
// .Dep.IdentitySecretRef is one {{end}} away from leaking it, and it does
// nothing at all for a CSV export, which never passes through a template."

// identityListRow is what the list page renders. NOT store.IdentityRow: that
// type carries SecretRef (embedded via domain.Identity), and this page must
// never be able to print it, not even by a future template edit.
type identityListRow struct {
	ID        string
	Name      string
	Realm     string
	Kind      string
	TeamID    string
	TeamCode  string
	Lifecycle string
	// Rotation is the derived state, and DueOn is non-empty only for the two
	// states where a due date is meaningful.
	Rotation string
	DueOn    string
	// DaysUntilDue is signed: positive is "due in n", negative is "overdue by
	// n". Computed in Go because a template must not do arithmetic on dates.
	DaysUntilDue int
	// HasSecretRef says WHETHER a path is recorded. Never the path.
	HasSecretRef bool
	Uses         int
}

type identityListPage struct {
	Base
	Errors     map[string]string
	Identities []identityListRow
	Teams      []store.TeamRow
	Kinds      []string
	States     []string
	Filter     store.IdentityFilter
}

type identityPage struct {
	Base
	Errors   map[string]string
	Identity *store.IdentityRow
	// SecretRef is the stored path, ALREADY GATED: empty for anyone who is not
	// a full Administrator, and empty when the credential carries none. See
	// this file's header for why the gate is here and not in the template.
	SecretRef string
	Rotation  string
	DueOn     string
	Usage     *store.IdentityUsageRows
	Timeline  []store.TimelineEntry
}

// IdentityList shows every credential reference, live ones by default.
func (a *App) IdentityList(w http.ResponseWriter, r *http.Request) {
	a.renderIdentityList(w, r, http.StatusOK, nil)
}

func (a *App) renderIdentityList(w http.ResponseWriter, r *http.Request, status int,
	errs map[string]string) {

	q := r.URL.Query()
	filter := store.IdentityFilter{
		Query:          q.Get("q"),
		Kind:           q.Get("kind"),
		TeamID:         q.Get("team"),
		Rotation:       domain.RotationState(q.Get("rotation")),
		IncludeRetired: q.Get("lifecycle") == domain.LifecycleRetired || q.Get("lifecycle") == "any",
	}
	rows, err := a.Store.ListIdentities(r.Context(), filter)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if q.Get("lifecycle") == domain.LifecycleRetired {
		// IncludeRetired above widens the query to BOTH lifecycles, because
		// that is what "find a retired credential" needs from the store --
		// the alternative, a store-side lifecycle=retired filter, would still
		// need this same narrowing done somewhere. Narrowed here, once, so the
		// default list (IncludeRetired: false) never runs this branch at all.
		kept := rows[:0]
		for _, row := range rows {
			if row.Lifecycle == domain.LifecycleRetired {
				kept = append(kept, row)
			}
		}
		rows = kept
	}
	teams, _ := a.responsibilityOptions(r)
	now := a.Store.Now()

	out := make([]identityListRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, identityListRow{
			ID: row.ID, Name: row.Name, Realm: derefOr(row.Realm, ""),
			Kind: row.Kind, TeamID: derefOr(row.TeamID, ""), TeamCode: row.TeamCode,
			Lifecycle:    row.Lifecycle,
			Rotation:     string(row.RotationStatus(now)),
			DueOn:        derefOr(row.RotationDueOn(), ""),
			DaysUntilDue: daysUntilDue(now, row.RotationDueOn()),
			HasSecretRef: row.SecretRef != nil,
			Uses:         row.DependencyCount + row.WindowsCount,
		})
	}

	a.Render.Respond(w, r, status, "identity_list", "identity_list_panel", identityListPage{
		Base:       a.base(r, "Identities", "identities"),
		Errors:     orEmpty(errs),
		Identities: out,
		Teams:      teams,
		Kinds:      domain.IdentityKinds,
		States:     domain.RotationStates,
		Filter:     filter,
	})
}

// IdentityDetail is the page somebody opens to see what a service
// authenticates as, whether its rotation policy is being followed, and what
// still names it.
func (a *App) IdentityDetail(w http.ResponseWriter, r *http.Request) {
	a.renderIdentity(w, r, http.StatusOK, nil)
}

func (a *App) renderIdentity(w http.ResponseWriter, r *http.Request, status int, errs map[string]string) {
	id := r.PathValue("id")
	identity, err := a.Store.GetIdentity(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	usage, err := a.Store.IdentityUsage(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	// NeighbourRefs has no `identity` case, so this degrades to the subject's
	// own history (internal/store/audit.go) -- which is exactly what the spec
	// asks for: "the entity's change_log, where a rotation reads as a
	// last_rotated change with its actor and actor_kind". The used-by panel
	// answers the neighbourhood question from the other direction, and widening
	// NeighbourRefs is deliberately not part of this work.
	timeline, _, err := a.Store.TimelineForEntityAndNeighbours(r.Context(), "identity", id, timelineLimit)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	now := a.Store.Now()
	base := a.base(r, "Identity: "+identity.Name, "identities")

	// SECRET_REF IS GATED HERE, in the view model, never in the template --
	// see this file's header. Empty for anyone who is not a full
	// Administrator, and empty when the credential carries no path at all.
	secretRef := ""
	if base.IsAdmin && identity.SecretRef != nil {
		secretRef = *identity.SecretRef
	}

	a.Render.Respond(w, r, status, "identity_detail", "identity_panel", identityPage{
		Base: base, Errors: orEmpty(errs), Identity: identity,
		SecretRef: secretRef,
		Rotation:  string(identity.RotationStatus(now)),
		DueOn:     derefOr(identity.RotationDueOn(), ""),
		Usage:     usage, Timeline: timeline,
	})
}

// daysUntilDue is the signed day count between now and a due date: positive
// when the due date is still ahead ("due in n"), negative once it has passed
// ("overdue by n" is the absolute value of this, computed by the template with
// the existing `sub` func rather than a new one -- CLAUDE.md's "no business
// logic in templates" still leaves room for `sub 0 x`, which is arithmetic a
// template func already does, not a decision).
//
// due is nil for three of the five rotation states (unmanaged, never_recorded,
// unreadable), all of which render with no due date at all, so this returns 0
// for a nil input rather than a value nothing displays.
func daysUntilDue(now time.Time, due *string) int {
	if due == nil {
		return 0
	}
	d, err := domain.ParseDate(*due)
	if err != nil {
		// RotationDueOn already refuses to return a date it cannot parse
		// itself back, so this branch is unreachable in practice. Kept as a
		// safe default rather than a panic: a rendering bug must never be
		// worse than a wrong number on a page that is otherwise fine.
		return 0
	}
	today := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	return int(d.Sub(today).Hours() / 24)
}
