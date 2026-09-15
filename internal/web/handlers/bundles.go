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

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web/render"
)

// The bundle UI, Task 4 of docs/cable-bundles-design.md. Everything Tasks 1-3
// built -- the entity, the store CRUD, BundleCutEffect and its own /bundles/{id}/impact
// page -- was unreachable from a browser except that one cut view. This file
// is what makes a bundle something an operator can actually declare, correct,
// populate and withdraw.

// bundleListPage is every live bundle, plus the declare form.
type bundleListPage struct {
	Base
	Bundles []store.BundleRow
	Errors  map[string]string
	// Values carries what was just typed, so a refused declare does not make
	// the operator retype every field -- CLAUDE.md's default, and this form
	// had nowhere else to keep it once the create route stopped redirecting
	// on refusal.
	Values map[string]string
}

// BundleList renders every live bundle.
func (a *App) BundleList(w http.ResponseWriter, r *http.Request) {
	a.renderBundleList(w, r, http.StatusOK, nil)
}

func (a *App) renderBundleList(w http.ResponseWriter, r *http.Request, status int, errs map[string]string) {
	bundles, err := a.Store.ListBundles(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Page(w, status, "bundle_list", bundleListPage{
		Base:    a.base(r, "Bundles", "bundles"),
		Bundles: bundles,
		Errors:  orEmpty(errs),
		Values: map[string]string{
			"code":        formValue(r, "code"),
			"name":        formValue(r, "name"),
			"description": formValue(r, "description"),
		},
	})
}

// BundleCreate declares a bundle: a duct, a tray, a trunk. Membership is set
// separately (BundleSetMembers) -- a bundle can exist with no cables in it
// yet.
func (a *App) BundleCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	b, err := domain.NewCableBundle(store.NewID(), domain.CableBundleSpec{
		Code:        formValue(r, "code"),
		Name:        formValue(r, "name"),
		Description: optionalString(r, "description"),
	}, a.Store.Now())
	if err == nil {
		err = a.Store.CreateBundle(r.Context(), a.permit(r), b)
	}
	if err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			if isConflict(err) {
				messages = map[string]string{"code": "a bundle with that code already exists"}
			} else {
				a.handleStoreError(w, r, err)
				return
			}
		}
		a.renderBundleList(w, r, http.StatusUnprocessableEntity, messages)
		return
	}
	a.setFlash(r, "success", "Bundle "+b.Name+" declared.")
	render.Redirect(w, r, "/bundles")
}

// bundleDetailPage is one bundle: its own fields, every cable pulled through
// it (live and retired -- membership is history), and the correction and
// membership forms an Administrator gets on this same page.
type bundleDetailPage struct {
	Base
	Bundle *domain.CableBundle
	// Members is EVERY cable in this bundle, retired ones included: a
	// retired link stays in its bundle (design doc, "Rules"), and this page
	// is where that history is shown, marked, rather than dropped.
	Members []store.BundleMemberRow
	// Candidates is what the membership editor may offer -- live cables
	// this bundle could add, per CandidateLinksForBundle. Left nil for a
	// retired bundle: SetBundleMembers refuses to edit one, so there is
	// nothing here to offer.
	Candidates []store.BundleMemberRow
	// LiveMemberIDs is which of Candidates are currently ticked in the
	// membership editor -- every LIVE member, by id. A retired member is
	// never in here: it is not among Candidates either (see the type's own
	// doc comment), so there is nothing to tick.
	LiveMemberIDs []string
	// Edit is the refused correction or membership save on its way back to
	// this page, or nil.
	Edit *editState
}

// BundleDetail shows one bundle.
func (a *App) BundleDetail(w http.ResponseWriter, r *http.Request) {
	a.renderBundleDetail(w, r, http.StatusOK, r.PathValue("id"), nil)
}

func (a *App) renderBundleDetail(w http.ResponseWriter, r *http.Request, status int, id string, edit *editState) {
	bundle, err := a.Store.GetBundle(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	members, err := a.Store.ListBundleMembers(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	// A retired bundle offers nothing to add to it -- SetBundleMembers
	// refuses the edit outright (Task 2b), so a candidate list here would be
	// a control with nothing behind it.
	var candidates []store.BundleMemberRow
	if !bundle.IsRetired() {
		candidates, err = a.Store.CandidateLinksForBundle(r.Context(), id)
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		// A refused membership save must redraw with what was picked still
		// visible and selected -- the house rule CLAUDE.md states and
		// refusal_status_test.go enforces elsewhere in this package. The
		// picked cable is very often exactly the one CandidateLinksForBundle
		// just excluded (it was refused for being claimed elsewhere), so
		// without this it would silently vanish from the option list instead
		// of staying there, refused and explained. LinksByID resolves
		// whatever the operator actually posted, whatever its current claim.
		if edit != nil {
			missing := missingLinkIDs(candidates, edit.Multi["link_id"])
			if len(missing) > 0 {
				extra, err := a.Store.LinksByID(r.Context(), missing)
				if err != nil {
					a.serverError(w, r, err)
					return
				}
				candidates = append(candidates, extra...)
			}
		}
	}
	var liveIDs []string
	for _, m := range members {
		if m.Lifecycle == domain.LifecycleActive {
			liveIDs = append(liveIDs, m.LinkID)
		}
	}
	a.Render.Page(w, status, "bundle_detail", bundleDetailPage{
		Base:          a.base(r, bundle.Name, "bundles"),
		Bundle:        bundle,
		Members:       members,
		Candidates:    candidates,
		LiveMemberIDs: liveIDs,
		Edit:          editFor(edit, id),
	})
}

// BundleUpdate corrects a bundle's code, name or description. NOT lifecycle
// -- UpdateBundle itself carries that over from the stored row (see its own
// doc comment), so this handler cannot revive or withdraw a bundle by any
// value it sends.
func (a *App) BundleUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	existing, err := a.Store.GetBundle(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	updated := *existing
	updated.Code = formValue(r, "code")
	updated.Name = formValue(r, "name")
	updated.Description = optionalString(r, "description")
	updated.RowVersion = submittedVersion(r, updated.RowVersion)

	if err := a.Store.UpdateBundle(r.Context(), a.permit(r), &updated); err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			switch {
			case isStale(err):
				messages = staleMessage("name")
			case isConflict(err):
				messages = map[string]string{"code": "a bundle with that code already exists"}
			default:
				a.handleStoreError(w, r, err)
				return
			}
		}
		edit := rejected(r, id, messages, "code", "name", "description")
		a.renderBundleDetail(w, r, refusalStatus(err), id, edit)
		return
	}
	a.setFlash(r, "success", "Bundle updated.")
	render.Redirect(w, r, "/bundles/"+id)
}

// BundleSetMembers replaces which cables are pulled through this bundle.
//
// A RETIRED MEMBER IS NEVER DROPPED BY THIS HANDLER, EVEN THOUGH THE FORM
// NEVER OFFERS ONE TO KEEP TICKED. CandidateLinksForBundle only lists live
// links, so a retired cable's checkbox does not exist on the rendered page --
// there is nothing there for the operator to leave ticked. Without the lines
// below, an unrelated save (adding one live cable to the bundle) would still
// submit only what the picker offered and SetBundleMembers's wholesale
// replace would silently drop every retired member: the exact
// set-replacement trap CLAUDE.md names (setDataClasses, three times over).
// So this handler reads the CURRENT retired members itself and folds them
// back into what gets saved, regardless of what the form carried.
func (a *App) BundleSetMembers(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	existing, err := a.Store.ListBundleMembers(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	chosen := r.Form["link_id"]
	linkIDs := append([]string(nil), chosen...)
	for _, m := range existing {
		if m.Lifecycle != domain.LifecycleActive {
			linkIDs = append(linkIDs, m.LinkID)
		}
	}

	if err := a.Store.SetBundleMembers(r.Context(), a.permit(r), id, linkIDs); err != nil {
		var messages map[string]string
		switch {
		case isStale(err):
			// SetBundleMembers takes no row_version today (see its own doc
			// comment -- it is not versioned the way a correction is), so
			// this branch is unreached in practice. Kept for the same
			// reason refusalStatus itself keeps the distinction: a future
			// change that adds a version check must not silently start
			// answering a stale save with the wrong status.
			messages = staleMessage("link_id")
		default:
			if msgs, ok := validationErrors(err); ok {
				messages = msgs
			} else if isConflict(err) {
				// Named the cable and the bundle holding it already --
				// SetBundleMembers's own error text does that (and is
				// tested to), so this is not re-derived here.
				messages = map[string]string{"link_id": err.Error()}
			} else {
				a.handleStoreError(w, r, err)
				return
			}
		}
		edit := rejected(r, id, messages)
		edit.Multi = map[string][]string{"link_id": chosen}
		a.renderBundleDetail(w, r, refusalStatus(err), id, edit)
		return
	}
	a.setFlash(r, "success", "Bundle membership updated.")
	render.Redirect(w, r, "/bundles/"+id)
}

// BundleRetire withdraws a bundle. No cascade: retiring a bundle retires
// nothing else, and every cable pulled through it stays exactly as it was
// (design doc, "Rules").
func (a *App) BundleRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetireBundle(r.Context(), a.permit(r), r.PathValue("id")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Bundle withdrawn.")
	render.Redirect(w, r, "/bundles")
}

// missingLinkIDs is which of a refused submission's picks are not already
// among the candidates about to be rendered -- exactly the ones that would
// otherwise vanish from the picker instead of staying visible and refused.
func missingLinkIDs(candidates []store.BundleMemberRow, picked []string) []string {
	present := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		present[c.LinkID] = true
	}
	var missing []string
	for _, id := range picked {
		if id != "" && !present[id] {
			missing = append(missing, id)
		}
	}
	return missing
}
