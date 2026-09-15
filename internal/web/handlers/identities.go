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
	"github.com/madalinignisca/invctl/internal/web/render"
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
	// Spec carries what was just typed into the declare form, so a refused
	// create does not make the operator retype every field -- bundleListPage's
	// Values field states the same rule; this is that rule with a typed spec
	// rather than a raw map, because the create form reads scalars this page
	// otherwise has nowhere to keep (rotation_days as *int, not a string).
	Spec domain.IdentitySpec
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
	// Kinds and Teams feed the correction form's two <select>s -- Kinds is
	// static, Teams degrades to nil the way responsibilityOptions already
	// documents (a failed team-list read is not a reason to fail the whole
	// page).
	Kinds []string
	Teams []store.TeamRow
	// RotationInput is what the "Rotated on" box shows: today's date on a
	// plain GET, or exactly what the operator submitted when a rotation is
	// refused. A hardcoded value="{{.Today}}" would silently replace a
	// rejected future date with today's on the way back, which is the
	// opposite of "the typed value survives" -- see
	// TestAFutureRotationIs422WithTheFormReRendered.
	RotationInput string
}

// IdentityList shows every credential reference, live ones by default.
func (a *App) IdentityList(w http.ResponseWriter, r *http.Request) {
	a.renderIdentityList(w, r, http.StatusOK, nil, domain.IdentitySpec{})
}

func (a *App) renderIdentityList(w http.ResponseWriter, r *http.Request, status int,
	errs map[string]string, spec domain.IdentitySpec) {

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
		Spec:       spec,
	})
}

// IdentityDetail is the page somebody opens to see what a service
// authenticates as, whether its rotation policy is being followed, and what
// still names it.
func (a *App) IdentityDetail(w http.ResponseWriter, r *http.Request) {
	a.renderIdentity(w, r, http.StatusOK, nil)
}

func (a *App) renderIdentity(w http.ResponseWriter, r *http.Request, status int, errs map[string]string) {
	a.renderIdentityWith(w, r, status, errs, nil, "")
}

// renderIdentityWith is renderIdentity's full form.
//
// spec, WHEN NOT NIL, OVERLAYS THE EDITABLE FIELDS ONLY (kind, name, realm,
// secret_ref, rotation_days, team_id) onto the freshly fetched row for
// DISPLAY. Never lifecycle, never last_rotated, never row_version -- those
// come from the store's own read so the version in the re-rendered
// row_version hidden input is the one a retry must actually send, and so
// lifecycle keeps rendering the true stored state rather than the zero value
// domain.Identity.Validate leaves it at when validation fails before
// UpdateIdentity ever pins those two fields from the stored row (see that
// method's own comment).
//
// THIS IS WHAT TestAStaleIdentityCorrectionIs409 IS ABOUT: the loser's typed
// name is the thing they are about to re-apply, and re-showing the WINNER's
// stored name instead turns "reopen the row and re-apply your edit" into
// "retype everything from memory". A plain re-fetch, the shape TeamUpdate and
// BundleUpdate already use, is right for those two forms because every field
// they can lose has a natural fallback -- what is already stored is exactly
// what the operator would retype. identity does not get that fallback for
// free here, so it is done explicitly instead.
//
// rotationInput IS INDEPENDENT OF spec: it is not a stored field at all, it
// is what the NEXT rotation submission will carry. It needs today's date on a
// plain GET and exactly the rejected date when a rotation is refused, and a
// hardcoded value="{{.Today}}" in the template would silently swap a rejected
// future date for today's on the way back.
func (a *App) renderIdentityWith(w http.ResponseWriter, r *http.Request, status int,
	errs map[string]string, spec *domain.IdentitySpec, rotationInput string) {

	id := r.PathValue("id")
	identity, err := a.Store.GetIdentity(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	if spec != nil {
		identity.Kind = spec.Kind
		identity.Name = spec.Name
		identity.Realm = spec.Realm
		identity.SecretRef = spec.SecretRef
		identity.RotationDays = spec.RotationDays
		identity.TeamID = spec.TeamID
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

	rotIn := rotationInput
	if rotIn == "" {
		rotIn = domain.FormatDate(now)
	}

	// Only fetched for an Administrator: a read-only viewer never sees the
	// correction form these feed, and identities.html's IsAdmin gate means
	// the picker never renders for anyone else regardless.
	var teams []store.TeamRow
	if base.IsAdmin {
		teams, _ = a.responsibilityOptions(r)
	}

	a.Render.Respond(w, r, status, "identity_detail", "identity_panel", identityPage{
		Base: base, Errors: orEmpty(errs), Identity: identity,
		SecretRef:     secretRef,
		Rotation:      string(identity.RotationStatus(now)),
		DueOn:         derefOr(identity.RotationDueOn(), ""),
		Usage:         usage,
		Timeline:      timeline,
		Kinds:         domain.IdentityKinds,
		Teams:         teams,
		RotationInput: rotIn,
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

// ---------- the write surface (WP-J8 Task 5) ----------
//
// Four writeAdminOnly routes: declare, correct, withdraw, and record a
// rotation. The last one is the whole reason this work package exists --
// see docs/identity-surface-design.md, "One writer for last_rotated". The
// other three exist because CreateIdentity, UpdateIdentity and RetireIdentity
// (Task 3) had no route reaching them at all until this one.

// identitySpecFromForm reads a credential reference out of a submitted form.
//
// NO last_rotated FIELD, and that is the rule rather than an omission: it
// has exactly one writer and this is not it. See internal/store/identities.go's
// header and internal/store/last_rotated_source_test.go.
//
// rotation_days GOES THROUGH optionalNumbers, NOT a bare optionalInt call --
// optionalInt's own contract is "ok is false only when the field was present
// and is not a number", and swallowing that here would let an unparseable
// count through as nil, which Validate accepts and the store would then
// write as "no rotation policy", silently discarding a typo rather than
// refusing it. numbers.messages() carries the one field this form can get
// wrong this way back to the caller as a 422.
func identitySpecFromForm(r *http.Request) (domain.IdentitySpec, map[string]string) {
	nums := optionalNumbers(r)
	spec := domain.IdentitySpec{
		Kind:         formValue(r, "kind"),
		Name:         formValue(r, "name"),
		Realm:        optionalString(r, "realm"),
		SecretRef:    optionalString(r, "secret_ref"),
		RotationDays: nums.opt("rotation_days"),
		TeamID:       optionalString(r, "team_id"),
	}
	return spec, nums.messages()
}

// identityConflictMessage is what a UNIQUE (realm, name) violation becomes on
// the form -- the same shape BundleCreate and BundleUpdate use for their own
// code collision, so a typo that happens to match an existing live credential
// reads as "fix this field" rather than as the generic 409 handleStoreError
// would otherwise answer with.
func identityConflictMessage() map[string]string {
	return map[string]string{"name": "an active identity with that realm and name already exists"}
}

// IdentityCreate declares a credential reference.
func (a *App) IdentityCreate(w http.ResponseWriter, r *http.Request) {
	spec, numErrs := identitySpecFromForm(r)
	if numErrs != nil {
		a.renderIdentityList(w, r, http.StatusUnprocessableEntity, numErrs, spec)
		return
	}
	i, err := domain.NewIdentity(store.NewID(), spec)
	if err == nil {
		err = a.Store.CreateIdentity(r.Context(), a.permit(r), i)
	}
	if err != nil {
		if errs, ok := validationErrors(err); ok {
			a.renderIdentityList(w, r, refusalStatus(err), errs, spec)
			return
		}
		if isConflict(err) {
			a.renderIdentityList(w, r, http.StatusConflict, identityConflictMessage(), spec)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Identity "+i.Name+" declared.")
	render.Redirect(w, r, "/identities/"+i.ID)
}

// IdentityUpdate corrects a credential reference.
//
// NOT lifecycle and NOT last_rotated -- UpdateIdentity itself pins both from
// the stored row regardless of what this handler sends (see that method's
// own doc comment), so this is the second statement of the same rule at the
// layer where a future edit is most likely to add one by habit.
func (a *App) IdentityUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := a.Store.GetIdentity(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	spec, numErrs := identitySpecFromForm(r)
	if numErrs != nil {
		a.renderIdentityWith(w, r, http.StatusUnprocessableEntity, numErrs, &spec, "")
		return
	}

	updated := existing.Identity
	updated.Kind = spec.Kind
	updated.Name = spec.Name
	updated.Realm = spec.Realm
	updated.SecretRef = spec.SecretRef
	updated.RotationDays = spec.RotationDays
	// submittedString, not the spec: a picker that failed to render must not
	// read as an operator clearing the field -- the same rule assets and
	// services already follow (submittedString's own doc comment).
	updated.TeamID = submittedString(r, "team_id", existing.TeamID)
	updated.RowVersion = submittedVersion(r, updated.RowVersion)

	if err := a.Store.UpdateIdentity(r.Context(), a.permit(r), &updated); err != nil {
		if errs, ok := validationErrors(err); ok {
			a.renderIdentityWith(w, r, refusalStatus(err), errs, &spec, "")
			return
		}
		if isStale(err) {
			a.renderIdentityWith(w, r, http.StatusConflict, staleMessage("name"), &spec, "")
			return
		}
		if isConflict(err) {
			a.renderIdentityWith(w, r, http.StatusConflict, identityConflictMessage(), &spec, "")
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Identity updated.")
	render.Redirect(w, r, "/identities/"+id)
}

// IdentityRetire withdraws a credential reference.
func (a *App) IdentityRetire(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.Store.RetireIdentity(r.Context(), a.permit(r), id); err != nil {
		if isStale(err) {
			// The withdraw form carries the token like every other edit form,
			// so a stale withdrawal is 409 rather than a silent second retire
			// of a row somebody else has already changed.
			a.renderIdentity(w, r, http.StatusConflict, staleMessage("name"))
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Identity withdrawn.")
	// Back to the list rather than the detail page: the credential the
	// operator was looking at is gone from the default view, and leaving
	// them on a page that now says "retired" reads as a failed action.
	render.Redirect(w, r, "/identities")
}

// IdentityRecordRotation stamps the day somebody rotated this credential.
//
// THIS IS THE FEATURE, not a side effect of the correction form above --
// see docs/identity-surface-design.md, "One writer for last_rotated".
// RecordIdentityRotation itself carries both rules this route exists to
// surface as HTTP: a future date and a retired identity are each refused as
// a domain.ValidationError, which validationErrors turns into a 422 with the
// form re-rendered here, never a 200 with the message buried and never a
// flash-and-redirect (TestARefusalIsRenderedNotFlashed AST-scans this
// package for exactly that).
func (a *App) IdentityRecordRotation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	date := formValue(r, "last_rotated")
	err := a.Store.RecordIdentityRotation(r.Context(), a.permit(r), id, date)
	if err != nil {
		if errs, ok := validationErrors(err); ok {
			a.renderIdentityWith(w, r, refusalStatus(err), errs, nil, date)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Rotation recorded.")
	render.Redirect(w, r, "/identities/"+id)
}
