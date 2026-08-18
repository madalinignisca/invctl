// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"

	"github.com/madalinignisca/invctl/internal/auth"
	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web/render"
)

// assetQueryParams is the full legitimate parameter set for GET
// /api/v1/assets: docs/api-design.md §4.
var assetQueryParams = []string{"after", "limit", "env", "kind", "lifecycle"}

// ListAssets serves GET /api/v1/assets: one page of the assets visible to the
// caller's token, narrowed by the optional env/kind/lifecycle filters.
func (a *API) ListAssets(w http.ResponseWriter, r *http.Request) {
	reader, q, page, err := beginList(r, assetQueryParams...)
	if err != nil {
		writeError(w, err)
		return
	}
	f := store.APIAssetFilter{
		Scope: reader.Environments,
		After: page.After,
		Limit: page.Limit,
	}
	if err := applyAssetFilters(&f, q); err != nil {
		writeError(w, err)
		return
	}
	rows, err := a.Store.APIListAssets(r.Context(), f)
	if err != nil {
		writeError(w, fmt.Errorf("listing assets: %w", err))
		return
	}
	out := make([]Asset, 0, len(rows))
	for _, row := range rows {
		out = append(out, assetDTO(row))
	}
	render.JSON(w, http.StatusOK, Page[Asset]{
		Data: out,
		Next: nextCursor(rows, page.Limit, func(r store.APIAssetRow) string { return r.ID }),
	})
}

// GetAsset serves GET /api/v1/assets/{id}: one asset, or a 404 that is
// byte-identical whether the id names nothing at all or names something
// outside the caller's scope. docs/api-design.md §3: a 403 would be an
// existence oracle over the estate, so the two cases must render the same
// response and differ only in a server-side security event.
func (a *API) GetAsset(w http.ResponseWriter, r *http.Request) {
	reader, _, err := readerAndQuery(r)
	if err != nil {
		writeError(w, err)
		return
	}
	id := r.PathValue("id")
	row, err := a.Store.APIGetAsset(r.Context(), reader.Environments, id)
	if err != nil {
		if errors.Is(err, store.ErrOutOfScope) {
			auth.LogSecurityEvent(r.Context(), slog.LevelWarn, auth.EventReaderScopeDenied,
				"credential", reader.ID, "entity", "asset", "id", id)
		}
		writeError(w, fmt.Errorf("getting asset %s: %w", id, err))
		return
	}
	render.JSON(w, http.StatusOK, assetDTO(*row))
}

// assetDTO is the only place an APIAssetRow becomes a published Asset.
func assetDTO(r store.APIAssetRow) Asset {
	return Asset{
		ID:           r.ID,
		Name:         r.Name,
		Kind:         r.Kind,
		Lifecycle:    r.Lifecycle,
		Environments: r.Environments,
		Site:         r.Site,
		Rack:         r.Rack,
		Addresses:    r.Addresses,
		Services:     r.Services,
	}
}

// applyAssetFilters validates ?env=, ?kind= and ?lifecycle= and, if they
// pass, narrows f to them.
//
// kind and lifecycle are validated against the Go-side enums that back their
// CHECK constraints (domain.AssetKinds, domain.AssetLifecycles): an unknown
// value is ErrBadRequest, because an empty collection there would be
// indistinguishable from a legitimate empty answer, the silent-fallback
// shape docs/api-design.md §6 refuses.
//
// env is validated against the CALLER'S OWN SCOPE (f.Scope), not against the
// estate -- docs/api-design.md §4's "Controller ruling AD". `?env=X` is a 400
// unless X is one of the credential's own environments, whether or not X
// exists at all: validating against the estate would let any token enumerate
// the environment vocabulary by dictionary (a 400 for an unknown code, a 200
// for a real one merely out of scope), and returning 200-with-empty for a
// real but out-of-scope code was itself a §6 violation -- a value the caller
// cannot use is silently replaced by something indistinguishable from a
// legitimate empty answer. Validating against the token's own scope leaks
// nothing new: /api/v1/environments already publishes that scope in full.
// f.Scope is already the reader's scope by the time this runs (ListAssets
// sets it before calling applyAssetFilters), so no additional parameter or
// store access is needed here.
func applyAssetFilters(f *store.APIAssetFilter, q url.Values) error {
	if v := q.Get("kind"); v != "" {
		if !slices.Contains(domain.AssetKinds, v) {
			return badRequest("kind is not a recognised asset kind")
		}
		f.Kind = v
	}
	if v := q.Get("lifecycle"); v != "" {
		if !slices.Contains(domain.AssetLifecycles, v) {
			return badRequest("lifecycle is not a recognised asset lifecycle")
		}
		f.Lifecycle = v
	}
	// THE CANONICAL CODE COMES BACK OUT OF THE SCOPE, never the caller's own
	// spelling. Scope.Allows lower-cases and trims before comparing, so
	// `?env=PROD` is admitted -- but the store compares environment_code as
	// TEXT against the lower-case codes NewEnvironment stores, so assigning
	// the raw value gave a 200 with an empty collection while `?kind=VM`
	// correctly 400s. Same §6 shape, one step later: a value arrives, cannot
	// be used as the caller meant it, and is silently replaced by something
	// indistinguishable from a legitimate empty answer.
	if v := q.Get("env"); v != "" {
		code, ok := f.Scope.Canonical(v)
		if !ok {
			return badRequest("env is not in this credential's scope")
		}
		f.EnvironmentCode = code
	}
	return nil
}
