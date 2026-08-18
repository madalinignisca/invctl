// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"fmt"
	"net/http"

	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web/middleware"
	"github.com/madalinignisca/invctl/internal/web/render"
)

// addressQueryParams is the full legitimate parameter set for GET
// /api/v1/addresses: docs/api-design.md §4. There is no single-resource
// address route -- an address is reached through the asset it belongs to.
var addressQueryParams = []string{"after", "limit"}

// ListAddresses serves GET /api/v1/addresses: one page of the addresses
// visible to the caller's token, scoped by the environments of the asset each
// one belongs to.
func (a *API) ListAddresses(w http.ResponseWriter, r *http.Request) {
	reader, ok := middleware.ReaderFrom(r.Context())
	if !ok {
		writeError(w, errNoReaderInContext)
		return
	}
	if err := checkKnownParams(r.URL.Query(), addressQueryParams...); err != nil {
		writeError(w, err)
		return
	}
	page, err := ParsePageRequest(r.URL.Query())
	if err != nil {
		writeError(w, err)
		return
	}
	rows, err := a.Store.APIListAddresses(r.Context(), reader.Environments, page.After, page.Limit)
	if err != nil {
		writeError(w, fmt.Errorf("listing addresses: %w", err))
		return
	}
	out := make([]Address, 0, len(rows))
	for _, row := range rows {
		out = append(out, addressDTO(row))
	}
	render.JSON(w, http.StatusOK, Page[Address]{
		Data: out,
		Next: nextCursor(rows, page.Limit, func(r store.APIAddressRow) string { return r.ID }),
	})
}

// addressDTO is the only place an APIAddressRow becomes a published Address.
func addressDTO(r store.APIAddressRow) Address {
	return Address{
		ID:           r.ID,
		Address:      r.AddrText,
		Family:       r.AddrFamily,
		Asset:        r.AssetName,
		AssetID:      r.AssetID,
		Environments: r.Environments,
	}
}
