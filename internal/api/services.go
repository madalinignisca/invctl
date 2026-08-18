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

	"github.com/madalinignisca/invctl/internal/auth"
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web/render"
)

// serviceQueryParams is the full legitimate parameter set for GET
// /api/v1/services: docs/api-design.md §4.
var serviceQueryParams = []string{"after", "limit"}

// ListServices serves GET /api/v1/services: one page of the services visible
// to the caller's token.
func (a *API) ListServices(w http.ResponseWriter, r *http.Request) {
	reader, _, page, err := beginList(r, serviceQueryParams...)
	if err != nil {
		writeError(w, err)
		return
	}
	rows, err := a.Store.APIListServices(r.Context(), reader.Environments, page.After, page.Limit)
	if err != nil {
		writeError(w, fmt.Errorf("listing services: %w", err))
		return
	}
	out := make([]Service, 0, len(rows))
	for _, row := range rows {
		out = append(out, serviceDTO(row))
	}
	render.JSON(w, http.StatusOK, Page[Service]{
		Data: out,
		Next: nextCursor(rows, page.Limit, func(r store.APIServiceRow) string { return r.ID }),
	})
}

// GetService serves GET /api/v1/services/{id}, with the same byte-identical
// 404 behaviour as GetAsset for an out-of-scope id.
func (a *API) GetService(w http.ResponseWriter, r *http.Request) {
	reader, _, err := readerAndQuery(r)
	if err != nil {
		writeError(w, err)
		return
	}
	id := r.PathValue("id")
	row, err := a.Store.APIGetService(r.Context(), reader.Environments, id)
	if err != nil {
		if errors.Is(err, store.ErrOutOfScope) {
			auth.LogSecurityEvent(r.Context(), slog.LevelWarn, auth.EventReaderScopeDenied,
				"credential", reader.ID, "entity", "service", "id", id)
		}
		writeError(w, fmt.Errorf("getting service %s: %w", id, err))
		return
	}
	render.JSON(w, http.StatusOK, serviceDTO(*row))
}

// serviceDTO is the only place an APIServiceRow becomes a published Service.
//
// Environments is a ONE-element slice built from the row's single
// EnvironmentCode, so the contract has one shape for "what is this in" across
// every entity even though the schema differs between assets (a set) and
// services (a single column). Criticality is the schema's tier, renamed at
// the contract boundary because "tier" is an internal word.
func serviceDTO(r store.APIServiceRow) Service {
	return Service{
		ID:           r.ID,
		Code:         r.Code,
		Name:         r.Name,
		Kind:         r.Kind,
		Lifecycle:    r.Lifecycle,
		Environments: []string{r.EnvironmentCode},
		Criticality:  r.Criticality,
		Assets:       r.Assets,
	}
}
