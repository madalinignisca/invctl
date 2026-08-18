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

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/web/render"
)

// ListEnvironments serves GET /api/v1/environments: the environments inside
// the caller's token scope, unpaginated -- the set is bounded and small, and
// it is the vocabulary a consumer needs before it can use ?env= at all. There
// are no query parameters.
func (a *API) ListEnvironments(w http.ResponseWriter, r *http.Request) {
	reader, _, err := readerAndQuery(r)
	if err != nil {
		writeError(w, err)
		return
	}
	envs, err := a.Store.APIListEnvironments(r.Context(), reader.Environments)
	if err != nil {
		writeError(w, fmt.Errorf("listing environments: %w", err))
		return
	}
	out := make([]Environment, 0, len(envs))
	for _, e := range envs {
		out = append(out, environmentDTO(e))
	}
	render.JSON(w, http.StatusOK, Page[Environment]{Data: out, Next: nil})
}

// environmentDTO is the only place a domain.Environment becomes a published
// Environment. Mapped field by field, deliberately: APIListEnvironments does
// `SELECT *` into domain.Environment, so this function is the only thing
// standing between the row's row_version (an optimistic-concurrency token,
// never part of the contract) and the client.
func environmentDTO(e domain.Environment) Environment {
	return Environment{
		ID:          e.ID,
		Code:        e.Code,
		Name:        e.Name,
		Role:        e.Role,
		InScope:     e.InScope,
		Criticality: e.Criticality,
	}
}
