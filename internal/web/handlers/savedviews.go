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
	"strings"

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
	domain.SavedViewService: {"environment", "kind", "availability", "project", "q", "tag", "tier"},
}

// formValues reads every value posted under key, trimmed and with blanks
// dropped -- the multi-value twin of formValue. A filter like "tag" is a
// repeating form field (see the <select multiple> in asset_list.html), and
// r.PostFormValue/formValue only ever return the FIRST one. Using that
// single-value read here is exactly the defect this function exists to fix:
// see savedViewParamsFrom's own comment for the consequence.
func formValues(r *http.Request, key string) []string {
	raw := r.PostForm[key]
	vals := make([]string, 0, len(raw))
	for _, v := range raw {
		v = strings.TrimSpace(v)
		if v != "" {
			vals = append(vals, v)
		}
	}
	return vals
}

// savedViewParamsFrom collects the filter parameters for one entity into the
// opaque JSON blob a saved view stores.
//
// The stored shape is map[string][]string -- url.Values, not
// map[string]string -- because a filter key can carry more than one value:
// "tag" is a repeating field (docs/tags-design.md §5's AND-combined tag
// picker), and assetFilterFrom/serviceFilterFrom already read every posted
// value via q["tag"]. Collapsing to the first value here, as an earlier
// version of this function did, silently WIDENS a saved view's result set --
// a view saved from "tag=production AND tag=pci-scope" would reopen as just
// "tag=production", matching every production asset including the ones out
// of PCI scope. A populated, plausible, wrong answer is worse than an empty
// one, because nobody double-checks it.
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
	fields := map[string][]string{}
	for _, k := range keys {
		if vals := formValues(r, k); len(vals) > 0 {
			fields[k] = vals
		}
	}
	b, err := json.Marshal(fields)
	if err != nil {
		// fields is a map[string][]string built by this function; marshaling
		// it cannot fail. Wrapped and surfaced anyway rather than ignored, on
		// the house rule that nothing here swallows an error silently.
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
	var fields map[string][]string
	if err := json.Unmarshal([]byte(params), &fields); err != nil {
		return base // a view whose params will not parse still opens the list
	}
	q := url.Values{}
	for _, k := range savedViewKeys[entity] {
		if vals, ok := fields[k]; ok && len(vals) > 0 {
			// One ?k=v pair per stored value, not just the first -- see
			// savedViewParamsFrom's comment for what collapsing to one value
			// does to a multi-tag view.
			q[k] = append([]string(nil), vals...)
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
		vocab, vocabErr := a.savedViewVocabularyFor(r.Context(), entity)
		if vocabErr != nil {
			a.serverError(w, r, vocabErr)
			return
		}
		views, vocabErr = SavedViewOptionsFor(r.Context(), a.Store, user.ID, entity, vocab)
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

// savedViewVocabulary is the live vocabulary savedViewStaleness checks a
// stored view's params against -- one struct rather than five parameters
// threaded through SavedViewOptionsFor and savedViewStaleness, since every
// caller either has all of it already loaded (the list pages) or needs to
// load all of it (savedViewVocabularyFor, below).
//
// DeviceTypeIDs/ProjectIDs/TagIDs carry only IDs, not the full rows: staying
// with IDs keeps this package's dependency on store row shapes to the one
// place (savedViewVocabularyFor and the two list handlers) that already
// reads them.
type savedViewVocabulary struct {
	Environments []domain.Environment
	Kinds        []store.VocabularyTerm
	// DeviceTypeIDs is asset-only (device_type_id is not a service filter);
	// nil on the service path, where it is never consulted since
	// savedViewKeys[SavedViewService] has no device_type_id entry.
	DeviceTypeIDs []string
	// ProjectIDs is service-only, the mirror image of DeviceTypeIDs.
	ProjectIDs []string
	// TagIDs applies to both entities. Deliberately the SAME list
	// loadTagListOptions' filterTags return already carries -- which
	// includes retired tags. A retired tag is still a real one that assets
	// still carry (see asset_list.html's "Tags (all of)" comment); only a
	// deleted tag id should read as stale.
	TagIDs []string
}

// savedViewVocabularyFor loads the live vocabulary for one entity, so a
// validation-failure re-render (renderSavedViewsInvalid) can compute Stale
// the same way an ordinary page load does. The asset and service list
// handlers build the same shape from lists they already have loaded for
// their own filter forms, rather than calling this -- see AssetList and
// ServiceList.
func (a *App) savedViewVocabularyFor(ctx context.Context, entity string) (savedViewVocabulary, error) {
	envs, err := a.Store.ListEnvironments(ctx)
	if err != nil {
		return savedViewVocabulary{}, fmt.Errorf("listing environments for the views menu: %w", err)
	}
	filterTagRows, _, err := a.loadTagListOptions(ctx)
	if err != nil {
		return savedViewVocabulary{}, fmt.Errorf("listing tags for the views menu: %w", err)
	}
	filterTags := make([]string, 0, len(filterTagRows))
	for _, t := range filterTagRows {
		filterTags = append(filterTags, t.ID)
	}
	switch entity {
	case domain.SavedViewAsset:
		kinds, err := a.Store.AssetKinds(ctx)
		if err != nil {
			return savedViewVocabulary{}, fmt.Errorf("listing kinds for the views menu: %w", err)
		}
		deviceTypes, err := a.Store.ListDeviceTypes(ctx, store.DeviceTypeFilter{})
		if err != nil {
			return savedViewVocabulary{}, fmt.Errorf("listing device types for the views menu: %w", err)
		}
		ids := make([]string, 0, len(deviceTypes))
		for _, d := range deviceTypes {
			ids = append(ids, d.ID)
		}
		return savedViewVocabulary{Environments: envs, Kinds: kinds, DeviceTypeIDs: ids, TagIDs: filterTags}, nil
	case domain.SavedViewService:
		kinds, err := a.Store.ServiceKinds(ctx)
		if err != nil {
			return savedViewVocabulary{}, fmt.Errorf("listing kinds for the views menu: %w", err)
		}
		projects, err := a.Store.ListProjects(ctx, store.ProjectFilter{})
		if err != nil {
			return savedViewVocabulary{}, fmt.Errorf("listing projects for the views menu: %w", err)
		}
		ids := make([]string, 0, len(projects))
		for _, p := range projects {
			ids = append(ids, p.ID)
		}
		return savedViewVocabulary{Environments: envs, Kinds: kinds, ProjectIDs: ids, TagIDs: filterTags}, nil
	default:
		// An unknown entity has no kind vocabulary to check against; the
		// caller's own validation already refuses this before staleness
		// would matter.
		return savedViewVocabulary{Environments: envs, TagIDs: filterTags}, nil
	}
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
// vocab is the SAME live lists the filter form on that page already renders
// (assetListPage.Environments/.Kinds or their service equivalents, plus the
// id lists AssetList/ServiceList build for staleness -- see
// savedViewVocabulary) -- passed in rather than re-queried, so a view's
// staleness is judged against exactly what the page is showing the operator
// right now, not a second read that could race it.
func SavedViewOptionsFor(ctx context.Context, s *store.SQLStore, userID, entity string, vocab savedViewVocabulary) ([]SavedViewOption, error) {
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
			Stale: savedViewStaleness(v.Entity, v.Params, vocab),
		})
	}
	return options, nil
}

// idKnown reports whether id appears in a live id list -- the shared check
// behind every one of savedViewStaleness's per-key lookups below.
func idKnown(ids []string, id string) bool {
	for _, known := range ids {
		if known == id {
			return true
		}
	}
	return false
}

// firstStoredValue returns the first stored value for key, or "" if the key
// is absent or empty. environment and kind are rendered as single-select
// inputs, so they only ever carry one value in practice; checking just the
// first is not a narrowing of a multi-select the way it would be for tag.
func firstStoredValue(fields map[string][]string, key string) string {
	vals := fields[key]
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

// savedViewStaleness checks a stored view's params against the live
// vocabulary and returns a human explanation of the first mismatch found, or
// "" if none.
//
// entity gates two things: which keys are even allowed to be stored (an
// unrecognised key is itself stale -- see below) and which of the two
// id-valued checks (device_type_id, project) applies, since they belong to
// different entities and vocab only carries the one the caller's list page
// actually has.
//
// environment, kind, device_type_id, project and tag are all checked --
// every allowlisted key that names something that can be renamed or
// deleted. lifecycle, availability and retired are NOT checked: they are
// fixed enum sets (domain constants plus a DB CHECK), so a stored value
// there can never stop existing. q is free text with nothing to look up.
// That is the whole of what is deliberately left out, not an oversight.
func savedViewStaleness(entity, params string, vocab savedViewVocabulary) string {
	var fields map[string][]string
	if err := json.Unmarshal([]byte(params), &fields); err != nil {
		// Malformed params already degrade gracefully in savedViewListPath
		// (the view opens the unfiltered list); nothing more specific to say
		// here.
		return ""
	}
	// A stored key outside the current allowlist -- e.g. a filter that was
	// renamed or removed since the view was saved -- would otherwise be
	// silently DROPPED by savedViewListPath (it only ever reads keys in
	// savedViewKeys[entity]), which widens the view's result rather than
	// explaining it. Surfacing it here turns that into the visible warning
	// design §6 promises, the same way the tier gap (fix 2) would have been
	// caught immediately had this check already existed.
	allowed := savedViewKeys[entity]
	for k := range fields {
		if !idKnown(allowed, k) {
			return fmt.Sprintf("filter %q is no longer recognised", k)
		}
	}
	if envID := firstStoredValue(fields, "environment"); envID != "" {
		found := false
		for _, e := range vocab.Environments {
			if e.ID == envID {
				found = true
				break
			}
		}
		if !found {
			return fmt.Sprintf("environment %q no longer exists", envID)
		}
	}
	if kind := firstStoredValue(fields, "kind"); kind != "" {
		found := false
		for _, k := range vocab.Kinds {
			if k.Code == kind {
				found = true
				break
			}
		}
		if !found {
			return fmt.Sprintf("kind %q no longer exists", kind)
		}
	}
	if dtID := firstStoredValue(fields, "device_type_id"); dtID != "" && !idKnown(vocab.DeviceTypeIDs, dtID) {
		return fmt.Sprintf("device type %q no longer exists", dtID)
	}
	if projectID := firstStoredValue(fields, "project"); projectID != "" && !idKnown(vocab.ProjectIDs, projectID) {
		return fmt.Sprintf("project %q no longer exists", projectID)
	}
	for _, tagID := range fields["tag"] {
		if tagID != "" && !idKnown(vocab.TagIDs, tagID) {
			return fmt.Sprintf("tag %q no longer exists", tagID)
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
// One FilterPair per stored value, not per key, via q[k] rather than q.Get
// -- to mirror formValues' multi-value read in savedViewParamsFrom. A
// key like "tag" can carry more than one value (the multi-select in
// asset_list.html), and what "Save this view" is about to submit is
// EXACTLY these hidden inputs (saved_views.html ranges over CurrentFilters
// to build them) -- so offering only the first value here, while
// savedViewParamsFrom saves all of them, would make the menu's own preview
// lie about what pressing the button actually captures.
func CurrentFiltersFor(q url.Values, entity string) []FilterPair {
	keys, ok := savedViewKeys[entity]
	if !ok {
		return nil
	}
	pairs := make([]FilterPair, 0, len(keys))
	for _, k := range keys {
		for _, v := range q[k] {
			if v != "" {
				pairs = append(pairs, FilterPair{Key: k, Value: v})
			}
		}
	}
	return pairs
}
