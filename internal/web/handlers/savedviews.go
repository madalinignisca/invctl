// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web/middleware"
	"github.com/madalinignisca/invctl/internal/web/render"
)

// savedViewKeys is an ALLOWLIST, not "whatever was posted".
//
// A saved view is replayed as a query later, so accepting arbitrary keys
// would let somebody store a parameter a future release starts honouring --
// a stored input that nobody reviewed, replayed under the owner's own
// session. The lists below are the filters assetFilterFrom and
// serviceFilterFrom actually read today; extend this map (never the form
// parsing below it) the day a new filter is added to either list page.
var savedViewKeys = map[string][]string{
	domain.SavedViewAsset:   {"kind", "environment", "lifecycle", "device_type_id", "q", "retired", "tag"},
	domain.SavedViewService: {"environment", "kind", "availability", "project", "q", "tag"},
}

// savedViewParamsFrom collects the filter parameters for one entity into the
// opaque JSON blob a saved view stores.
//
// The entity check happens here, against the allowlist's own keys, rather
// than deferring to domain.NewSavedView -- an unknown entity has no key list
// to read the form against, so there is nothing correct this function could
// return, and returning early keeps the "unknown entity" failure a single
// ValidationError instead of an empty-but-successful params blob that
// NewSavedView would then have to reject for an unrelated reason.
func savedViewParamsFrom(r *http.Request, entity string) (string, error) {
	keys, ok := savedViewKeys[entity]
	if !ok {
		return "", domain.NewValidation("entity", "must be one of: asset, service")
	}
	fields := map[string]string{}
	for _, k := range keys {
		if v := formValue(r, k); v != "" {
			fields[k] = v
		}
	}
	b, err := json.Marshal(fields)
	if err != nil {
		// fields is a map[string]string built by this function; marshaling it
		// cannot fail. Wrapped and surfaced anyway rather than ignored, on the
		// house rule that nothing here swallows an error silently.
		return "", fmt.Errorf("marshaling saved view params: %w", err)
	}
	return string(b), nil
}

// savedViewListPath turns a view's stored params back into the list URL that
// applies them. This is the ONLY place params become a URL again; storing
// parts rather than a string is what lets this change without orphaning
// anybody's views (docs/saved-views-design.md §1).
func savedViewListPath(entity, params string) string {
	base := "/assets"
	if entity == domain.SavedViewService {
		base = "/services"
	}
	var fields map[string]string
	if err := json.Unmarshal([]byte(params), &fields); err != nil {
		return base // a view whose params will not parse still opens the list
	}
	q := url.Values{}
	for _, k := range savedViewKeys[entity] {
		if v, ok := fields[k]; ok && v != "" {
			q.Set(k, v)
		}
	}
	if len(q) == 0 {
		return base
	}
	return base + "?" + q.Encode()
}

// savedViewFailed answers a refused or invalid saved-view write.
//
// domain.ErrForbidden is 403 and NOT 409: a refusal is not a version
// conflict, and an operator who retries a save they will never be allowed to
// make learns nothing. Everything else goes through the package's usual
// validation path -- 422 with the form re-rendered, never 200 with an error
// buried in the body.
func (a *App) savedViewFailed(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, domain.ErrForbidden) {
		http.Error(w, "You are not allowed to do that.", http.StatusForbidden)
		return
	}
	a.handleStoreError(w, r, err)
}

// SavedViewCreate saves the filters currently applied, under a name.
//
// The owner is middleware.UserFrom(r.Context()), never a form field.
// Attribution is server-derived from the credential everywhere in this
// codebase (docs/AUDIT.md), and a form-supplied owner would let anybody
// create a view in somebody else's name.
func (a *App) SavedViewCreate(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFrom(r.Context())
	if user == nil {
		// This route sits behind RequireWrite, so RequireAuth has already run
		// -- reaching here with no user means the middleware chain broke, not
		// that a visitor forgot to sign in. That is a bug worth a 500 and a
		// log line, not a quiet 401 a caller could mistake for ordinary.
		a.serverError(w, r, errors.New("saved view create reached without a user"))
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	params, err := savedViewParamsFrom(r, formValue(r, "entity"))
	if err != nil {
		a.savedViewFailed(w, r, err)
		return
	}
	v, err := domain.NewSavedView(store.NewID(), user.ID, formValue(r, "entity"),
		formValue(r, "name"), params, a.Store.Now())
	if err != nil {
		a.savedViewFailed(w, r, err)
		return
	}
	if err := a.Store.CreateSavedView(r.Context(), a.permit(r), v); err != nil {
		a.savedViewFailed(w, r, err)
		return
	}
	render.Redirect(w, r, savedViewListPath(v.Entity, v.Params))
}

// SavedViewUpdate renames a view or changes what filters it captures.
//
// The row is loaded first so UpdateSavedView can authorize against the
// STORED owner, never a submitted one -- see that function's own comment in
// internal/store/savedviews.go. This handler does not repeat the ownership
// check: the store is where that decision lives, and a second copy here
// would only give the two a chance to disagree.
func (a *App) SavedViewUpdate(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFrom(r.Context())
	if user == nil {
		a.serverError(w, r, errors.New("saved view update reached without a user"))
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	existing, err := a.Store.GetSavedView(r.Context(), id)
	if err != nil {
		a.savedViewFailed(w, r, err)
		return
	}
	entity := formValue(r, "entity")
	if entity == "" {
		// entity is not editable (UpdateSavedView never writes it), but the
		// allowlist that decides which keys are read still needs one to
		// consult -- fall back to the row's own, so a form that only posts
		// name/filters/row_version still resolves the same list it was
		// rendered from.
		entity = existing.Entity
	}
	params, err := savedViewParamsFrom(r, entity)
	if err != nil {
		a.savedViewFailed(w, r, err)
		return
	}
	updated := *existing
	updated.Name = formValue(r, "name")
	updated.Params = params
	updated.RowVersion = submittedVersion(r, updated.RowVersion)

	if err := a.Store.UpdateSavedView(r.Context(), a.permit(r), &updated); err != nil {
		a.savedViewFailed(w, r, err)
		return
	}
	render.Redirect(w, r, savedViewListPath(updated.Entity, updated.Params))
}

// SavedViewRetire soft-deletes a view. Like every entity here, a saved view
// is never hard-deleted -- see RetireSavedView.
func (a *App) SavedViewRetire(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Read first only so the redirect can send the operator back to the list
	// they were looking at; RetireSavedView re-derives ownership from the
	// stored row itself and does not trust this copy.
	existing, err := a.Store.GetSavedView(r.Context(), id)
	if err != nil {
		a.savedViewFailed(w, r, err)
		return
	}
	if err := a.Store.RetireSavedView(r.Context(), a.permit(r), id); err != nil {
		a.savedViewFailed(w, r, err)
		return
	}
	base := "/assets"
	if existing.Entity == domain.SavedViewService {
		base = "/services"
	}
	render.Redirect(w, r, base)
}

// SavedViewOption is the view model Task 5's list-page templates render: a
// name to show in a picker and the URL that applies it, with the params blob
// already turned back into a query string so the template never has to know
// the storage shape.
type SavedViewOption struct {
	ID   string
	Name string
	URL  string
}

// SavedViewOptionsFor lists one person's saved views for a list page, ready
// to render. It exists here rather than being inlined into AssetList /
// ServiceList so Task 5 has a single call to make regardless of which list
// page it is wiring, and so the URL-building rule above (savedViewListPath is
// the only place params become a URL) has exactly one caller.
func SavedViewOptionsFor(ctx context.Context, s *store.SQLStore, userID, entity string) ([]SavedViewOption, error) {
	views, err := s.ListSavedViews(ctx, userID, entity)
	if err != nil {
		return nil, fmt.Errorf("listing saved views for the picker: %w", err)
	}
	options := make([]SavedViewOption, 0, len(views))
	for _, v := range views {
		options = append(options, SavedViewOption{
			ID:   v.ID,
			Name: v.Name,
			URL:  savedViewListPath(v.Entity, v.Params),
		})
	}
	return options, nil
}
