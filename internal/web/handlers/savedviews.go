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

	"github.com/justinas/nosurf"

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
// domain.ErrForbidden (and ErrConflict, ErrNotFound) go through
// handleStoreError unchanged -- that 403/409/404 is already correct and
// nothing here overrides it. domain.ErrInvalid is the one case this project's
// own rule ("Validation errors re-render the form partial with error state
// and return HTTP 422", CLAUDE.md) actually requires more than
// handleStoreError gives: a bare "That request was not valid." text body,
// returned to a plain (non-hx-post) form post, would navigate the whole tab
// away and silently drop the filters the operator had open. See
// SavedViewCreate/SavedViewUpdate for the entity/currentFilters/name each
// passes so the re-render can put the operator back where they were.
func (a *App) savedViewFailed(w http.ResponseWriter, r *http.Request, err error) {
	a.handleStoreError(w, r, err)
}

// savedViewsMenuData is what the "saved_views" partial renders -- the same
// shape the list-page dict calls pass it, plus Name/Error for the one case
// list pages never need: reporting back a rejected "save this view" attempt
// without losing what the operator typed.
type savedViewsMenuData struct {
	Entity         string
	Views          []SavedViewOption
	CurrentFilters []FilterPair
	CSRF           string
	// Name is echoed back into the name field so a rejected submission does
	// not make the operator retype it.
	Name string
	// Error is "" on every ordinary render. Set only when this IS the
	// re-render of a rejected create/update, so the template can show it
	// beside the save form without a second partial to maintain.
	Error string
}

// renderSavedViewsInvalid re-renders the Views menu at 422, with the
// validation message(s) attached and the filters/name that were just
// submitted still in place -- the HTMX+422 convention this project uses
// everywhere else (CLAUDE.md, "HTTP and HTMX conventions"; see
// renderAssetFormError for the identical pattern on the asset form).
//
// entity/currentFilters/name are read from the FAILED request, not looked up
// again, so what comes back is exactly what the operator submitted, with the
// existing saved views listed alongside it unchanged.
func (a *App) renderSavedViewsInvalid(w http.ResponseWriter, r *http.Request, entity string, currentFilters []FilterPair, name string, err error) {
	messages, _ := validationErrors(err)
	msg := ""
	for _, m := range messages {
		if msg != "" {
			msg += "; "
		}
		msg += m
	}
	var views []SavedViewOption
	if user := middleware.UserFrom(r.Context()); user != nil {
		envs, kinds, vocabErr := a.savedViewVocabularyFor(r.Context(), entity)
		if vocabErr != nil {
			a.serverError(w, r, vocabErr)
			return
		}
		views, vocabErr = SavedViewOptionsFor(r.Context(), a.Store, user.ID, entity, envs, kinds)
		if vocabErr != nil {
			a.serverError(w, r, vocabErr)
			return
		}
	}
	a.Render.Partial(w, http.StatusUnprocessableEntity, "saved_views", savedViewsMenuData{
		Entity:         entity,
		Views:          views,
		CurrentFilters: currentFilters,
		CSRF:           nosurf.Token(r),
		Name:           name,
		Error:          msg,
	})
}

// savedViewVocabularyFor loads the same environment/kind lists the asset and
// service list pages already carry, so a validation-failure re-render can
// compute Stale (via SavedViewOptionsFor) the same way an ordinary page load
// does -- see savedViewStaleness's doc comment for why those two lists.
func (a *App) savedViewVocabularyFor(ctx context.Context, entity string) ([]domain.Environment, []store.VocabularyTerm, error) {
	envs, err := a.Store.ListEnvironments(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("listing environments for the views menu: %w", err)
	}
	var kinds []store.VocabularyTerm
	switch entity {
	case domain.SavedViewAsset:
		kinds, err = a.Store.AssetKinds(ctx)
	case domain.SavedViewService:
		kinds, err = a.Store.ServiceKinds(ctx)
	default:
		// An unknown entity has no kind vocabulary to check against; the
		// caller's own validation already refuses this before staleness
		// would matter.
		return envs, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("listing kinds for the views menu: %w", err)
	}
	return envs, kinds, nil
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
	entity := formValue(r, "entity")
	name := formValue(r, "name")
	// Reconstructed from the SAME posted form the create attempt itself is
	// read from (r.PostForm, populated by ParseForm above), through the same
	// allowlist as CurrentFiltersFor -- so a rejected submission's re-render
	// carries the filters the operator was actually looking at, not the ones
	// on an unrelated request.
	currentFilters := CurrentFiltersFor(r.PostForm, entity)
	params, err := savedViewParamsFrom(r, entity)
	if err != nil {
		if errors.Is(err, domain.ErrInvalid) {
			a.renderSavedViewsInvalid(w, r, entity, currentFilters, name, err)
			return
		}
		a.savedViewFailed(w, r, err)
		return
	}
	v, err := domain.NewSavedView(store.NewID(), user.ID, entity, name, params, a.Store.Now())
	if err != nil {
		if errors.Is(err, domain.ErrInvalid) {
			a.renderSavedViewsInvalid(w, r, entity, currentFilters, name, err)
			return
		}
		a.savedViewFailed(w, r, err)
		return
	}
	if err := a.Store.CreateSavedView(r.Context(), a.permit(r), v); err != nil {
		if errors.Is(err, domain.ErrInvalid) {
			a.renderSavedViewsInvalid(w, r, entity, currentFilters, name, err)
			return
		}
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
	// entity is not editable (UpdateSavedView never writes it), so the
	// allowlist that decides which keys are read is always the row's own,
	// never a submitted one. Reading a form-supplied entity here would let a
	// caller pick which allowlist gates the write -- e.g. post entity=service
	// while editing an asset view to smuggle a service-only key into that
	// view's stored params. Those keys are inert today only because
	// savedViewListPath replays on the STORED entity; savedViewKeys' own doc
	// comment exists precisely to stop an unreviewed key from sitting there
	// waiting for a future release to start honouring it. The stored value is
	// authoritative, full stop -- no fallback needed either, since it can
	// never be empty for an existing row.
	name := formValue(r, "name")
	currentFilters := CurrentFiltersFor(r.PostForm, existing.Entity)
	params, err := savedViewParamsFrom(r, existing.Entity)
	if err != nil {
		if errors.Is(err, domain.ErrInvalid) {
			a.renderSavedViewsInvalid(w, r, existing.Entity, currentFilters, name, err)
			return
		}
		a.savedViewFailed(w, r, err)
		return
	}
	updated := *existing
	updated.Name = name
	updated.Params = params
	updated.RowVersion = submittedVersion(r, updated.RowVersion)

	if err := a.Store.UpdateSavedView(r.Context(), a.permit(r), &updated); err != nil {
		if errors.Is(err, domain.ErrInvalid) {
			a.renderSavedViewsInvalid(w, r, existing.Entity, currentFilters, name, err)
			return
		}
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

// SavedViewOption is the view model Task 5's list-page templates render:
// everything the "Views" menu needs, already resolved so the template never
// has to know the storage shape or decide anything (docs/saved-views-design.md
// treats the template as presentation only).
type SavedViewOption struct {
	ID   string
	Name string
	// Path is the list URL with this view's filters applied.
	Path string
	// Stale is "" when every stored parameter still names something that
	// exists. Otherwise it is a short explanation of what is missing, e.g.
	// `environment "prod" no longer exists`. The view still opens and still
	// runs either way -- Stale only carries the explanation so nobody looking
	// at a suspiciously empty result concludes the estate itself is empty
	// (docs/saved-views-design.md §6). Never computed in the template:
	// business logic does not go there.
	Stale string
}

// SavedViewOptionsFor lists one person's saved views for a list page, ready
// to render. It exists here rather than being inlined into AssetList /
// ServiceList so Task 5 has a single call to make regardless of which list
// page it is wiring, and so the URL-building rule above (savedViewListPath is
// the only place params become a URL) has exactly one caller.
//
// environments and kinds are the SAME slices the filter form on that page
// already renders (assetListPage.Environments/.Kinds or their service
// equivalents) -- passed in rather than re-queried, so a view's staleness is
// judged against the exact vocabulary the page is showing the operator right
// now, not a second read that could race it.
func SavedViewOptionsFor(ctx context.Context, s *store.SQLStore, userID, entity string, environments []domain.Environment, kinds []store.VocabularyTerm) ([]SavedViewOption, error) {
	views, err := s.ListSavedViews(ctx, userID, entity)
	if err != nil {
		return nil, fmt.Errorf("listing saved views for the picker: %w", err)
	}
	options := make([]SavedViewOption, 0, len(views))
	for _, v := range views {
		options = append(options, SavedViewOption{
			ID:    v.ID,
			Name:  v.Name,
			Path:  savedViewListPath(v.Entity, v.Params),
			Stale: savedViewStaleness(v.Params, environments, kinds),
		})
	}
	return options, nil
}

// savedViewStaleness checks a stored view's params against the live
// vocabulary and returns a human explanation of the first mismatch found, or
// "" if none.
//
// Limited to "environment" and "kind" ON PURPOSE: those are the two lists
// every list page already carries in identical shape (.Environments,
// .Kinds), so this can run for both the asset and service pickers with no
// per-entity branching. Other allowlisted keys (project, availability,
// lifecycle, tag, q, retired, device_type_id) do not have a uniformly
// available "live list" on both page structs today -- extending this is a
// follow-up, not a silent gap, since a stale reference to one of those still
// simply narrows the result to nothing, which is confusing but not wrong.
func savedViewStaleness(params string, environments []domain.Environment, kinds []store.VocabularyTerm) string {
	var fields map[string]string
	if err := json.Unmarshal([]byte(params), &fields); err != nil {
		// Malformed params already degrade gracefully in savedViewListPath
		// (the view opens the unfiltered list); nothing more specific to say
		// here.
		return ""
	}
	if envID, ok := fields["environment"]; ok && envID != "" {
		found := false
		for _, e := range environments {
			if e.ID == envID {
				found = true
				break
			}
		}
		if !found {
			return fmt.Sprintf("environment %q no longer exists", envID)
		}
	}
	if kind, ok := fields["kind"]; ok && kind != "" {
		found := false
		for _, k := range kinds {
			if k.Code == kind {
				found = true
				break
			}
		}
		if !found {
			return fmt.Sprintf("kind %q no longer exists", kind)
		}
	}
	return ""
}

// FilterPair is one currently-applied filter, rendered by the "saved_views"
// partial as a hidden input on its "save this view" form.
type FilterPair struct {
	Key   string
	Value string
}

// CurrentFiltersFor reads the filters actually applied to a list request,
// through the SAME allowlist (savedViewKeys) that SavedViewCreate reads the
// posted form through.
//
// Deliberately built from r.URL.Query() and this allowlist, NOT from the
// page's typed Filter struct (store.AssetFilter / store.ServiceFilter).
// Reading the typed struct back into key/value pairs would need a second
// struct-field-to-form-key mapping alongside savedViewKeys, and two mappings
// that must independently agree are two mappings that will eventually
// disagree silently -- one adds a filter field and forgets the other. This
// way there is exactly one allowlist, and the round trip is provably exact:
// the hidden inputs this produces are precisely the keys SavedViewCreate
// re-reads.
//
// One value per key, via q.Get, to match formValue's single-value read in
// savedViewParamsFrom -- the value that actually gets saved on submit is a
// single value per key today, so offering more here would just mislead about
// what "save this view" will do with it.
func CurrentFiltersFor(q url.Values, entity string) []FilterPair {
	keys, ok := savedViewKeys[entity]
	if !ok {
		return nil
	}
	pairs := make([]FilterPair, 0, len(keys))
	for _, k := range keys {
		if v := q.Get(k); v != "" {
			pairs = append(pairs, FilterPair{Key: k, Value: v})
		}
	}
	return pairs
}
