// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"

	"github.com/madalinignisca/invctl/internal/auth"
	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web/middleware"
	"github.com/madalinignisca/invctl/internal/web/render"
)

// assetQueryParams is the full legitimate parameter set for GET
// /api/v1/assets: docs/api-design.md §4.
var assetQueryParams = []string{"after", "limit", "env", "kind", "lifecycle"}

// ListAssets serves GET /api/v1/assets: one page of the assets visible to the
// caller's token, narrowed by the optional env/kind/lifecycle filters.
func (a *API) ListAssets(w http.ResponseWriter, r *http.Request) {
	reader, ok := middleware.ReaderFrom(r.Context())
	if !ok {
		writeError(w, errNoReaderInContext)
		return
	}
	if err := checkKnownParams(r.URL.Query(), assetQueryParams...); err != nil {
		writeError(w, err)
		return
	}
	page, err := ParsePageRequest(r.URL.Query())
	if err != nil {
		writeError(w, err)
		return
	}
	f := store.APIAssetFilter{
		Scope: reader.Environments,
		After: page.After,
		Limit: page.Limit,
	}
	if err := applyAssetFilters(r.Context(), a.Store, &f, r.URL.Query()); err != nil {
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
	reader, ok := middleware.ReaderFrom(r.Context())
	if !ok {
		writeError(w, errNoReaderInContext)
		return
	}
	if err := checkKnownParams(r.URL.Query()); err != nil {
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
		Role:         r.Role,
		Addresses:    r.Addresses,
		Services:     r.Services,
	}
}

// applyAssetFilters validates ?env=, ?kind= and ?lifecycle= against their
// vocabularies and, if they pass, narrows f to them.
//
// An unknown filter VALUE is ErrBadRequest -- an empty collection there would
// be indistinguishable from a legitimate empty answer, the silent-fallback
// shape docs/api-design.md §6 refuses. A KNOWN value that the caller's scope
// simply does not cover is not an error at all: ?env=prod on a {dev} token
// narrows to an empty collection, because the scope predicate runs first and
// a filter can only select a subset of what survived it (store.APIAssetFilter
// doc comment). Filters narrow within scope; they can never widen it.
//
// kind and lifecycle are validated against the Go-side enums that back their
// CHECK constraints (domain.AssetKinds, domain.AssetLifecycles). env has no
// such fixed vocabulary -- environment codes are estate-defined -- so it is
// validated against the environment table itself via GetEnvironmentByCode,
// which is why this function needs a context and the store even though the
// filters it sets belong to the store's own, context-free APIAssetFilter.
func applyAssetFilters(ctx context.Context, s *store.SQLStore, f *store.APIAssetFilter, q url.Values) error {
	if v := q.Get("kind"); v != "" {
		if !slices.Contains(domain.AssetKinds, v) {
			return fmt.Errorf("%w: kind is not a recognised asset kind", ErrBadRequest)
		}
		f.Kind = v
	}
	if v := q.Get("lifecycle"); v != "" {
		if !slices.Contains(domain.AssetLifecycles, v) {
			return fmt.Errorf("%w: lifecycle is not a recognised asset lifecycle", ErrBadRequest)
		}
		f.Lifecycle = v
	}
	if v := q.Get("env"); v != "" {
		if _, err := s.GetEnvironmentByCode(ctx, v); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return fmt.Errorf("%w: env is not a recognised environment code", ErrBadRequest)
			}
			return fmt.Errorf("validating env filter: %w", err)
		}
		f.EnvironmentCode = v
	}
	return nil
}
