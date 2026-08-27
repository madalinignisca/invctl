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
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// WP-G4a piece 3: bulk application of a tag from a filtered asset or service
// list (docs/tags-design.md §4a, §5). Two admin-only, CSRF-protected routes
// per entity type, the same shape WP-G7 piece 3 uses for bulk ownership
// assignment (ownership_assign.go) -- select-all applies to the CURRENT
// FILTERED VIEW, the confirmation names the count and the tag before the
// request leaves the browser (app.js's bulkTagApply, the tag twin of
// bulkAssign), and the result reports one outcome per entity, never a bare
// count.
//
// UNLIKE THE OWNERSHIP SCREEN, THERE IS NO SEPARATE "candidates" GET ROUTE.
// The rows an operator can select ARE the asset or service list's own
// filtered table (asset_table.html / rows.html's service_table) -- adding a
// checkbox column and a footer form to a list that already exists, rather
// than a second list this feature would have to keep in sync with the first.
// A tag-filtered view is therefore also the easiest way to find candidates
// for a tag NOT yet on them: filter by the OTHER tags first, then apply.

// tagSelectionField is the repeating form field a checked row posts:
// "<entity id>:<row version>", encoding the row_version the operator was
// shown alongside the id it belongs to in one value, so the two cannot drift
// apart the way two same-named parallel arrays could if the browser (or a
// hand-built request) ever reordered one relative to the other.
const tagSelectionField = "entity"

// parseTagSelections reads tagSelectionField's repeating values into
// store.TagSelection. A value with no ":" or a non-numeric version is
// dropped rather than refusing the whole submission -- a corrupted single
// value here can only ever cost that one row a stale-looking refusal at
// worst (ApplyTagToSelection's own row_version guard), never widen what gets
// written.
func parseTagSelections(r *http.Request) []store.TagSelection {
	raw := r.PostForm[tagSelectionField]
	out := make([]store.TagSelection, 0, len(raw))
	for _, v := range raw {
		id, verStr, ok := strings.Cut(v, ":")
		if !ok || id == "" {
			continue
		}
		ver, err := strconv.Atoi(verStr)
		if err != nil {
			continue
		}
		out = append(out, store.TagSelection{ID: id, RowVersion: ver})
	}
	return out
}

// bulkTagResultPage is what "bulk_tag_result" renders: either the per-item
// outcome (design.md §4a: never a bare count) or a refusal, at whatever
// status the caller set (200 for a completed batch, 422 for a submission
// mistake -- nothing selected, no tag chosen, or a retired/unknown tag).
type bulkTagResultPage struct {
	Base
	EntityLabel string
	ListPath    string
	Outcomes    []store.TagApplyOutcome
	// Error is set only on a refusal.
	Error string
}

func (a *App) renderBulkTagResult(w http.ResponseWriter, r *http.Request, status int, entityLabel, listPath string, outcomes []store.TagApplyOutcome, refusal string) {
	a.Render.Partial(w, status, "bulk_tag_result", bulkTagResultPage{
		Base:        a.base(r, "Tags", ""),
		EntityLabel: entityLabel,
		ListPath:    listPath,
		Outcomes:    outcomes,
		Error:       refusal,
	})
}

// bulkApplyTag is the mutation shared by AssetsBulkTagApply and
// ServicesBulkTagApply: apply tag_id to exactly the rows the form named,
// trusting nothing but that list -- the same submission-contract rule
// OwnershipAssign is held to.
func (a *App) bulkApplyTag(w http.ResponseWriter, r *http.Request, entityType, entityLabel, listPath string) {
	if err := r.ParseForm(); err != nil {
		a.serverError(w, r, fmt.Errorf("parsing bulk tag-apply form: %w", err))
		return
	}
	tagID := formValue(r, "tag_id")
	selections := parseTagSelections(r)

	outcomes, err := a.Store.ApplyTagToSelection(r.Context(), permit(r), entityType, tagID, selections)
	if err != nil {
		if errors.Is(err, domain.ErrInvalid) {
			// err.Error() is operator-safe here: every domain.ErrInvalid
			// ApplyTagToSelection returns is a message it composed itself
			// ("no entities were selected", "the tag is retired ..."), never
			// a raw driver error -- the same reasoning postEntityTags already
			// relies on for its own _form message.
			a.renderBulkTagResult(w, r, http.StatusUnprocessableEntity, entityLabel, listPath, nil, err.Error())
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	a.renderBulkTagResult(w, r, http.StatusOK, entityLabel, listPath, outcomes, "")
}

// AssetsBulkTagApply applies a tag to the selected rows of a filtered asset list.
func (a *App) AssetsBulkTagApply(w http.ResponseWriter, r *http.Request) {
	a.bulkApplyTag(w, r, domain.TagEntityAsset, "assets", "/assets")
}

// ServicesBulkTagApply is the service half of the same thing.
func (a *App) ServicesBulkTagApply(w http.ResponseWriter, r *http.Request) {
	a.bulkApplyTag(w, r, domain.TagEntityService, "services", "/services")
}
