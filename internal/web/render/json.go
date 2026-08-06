// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// JSON responses exist for exactly one surface: the observed-state webhook.
//
// CLAUDE.md's "no JSON API for the UI" is unchanged and still absolute -- every
// handler a browser reaches returns HTML. This is the machine-facing route
// docs/AUDIT.md's amended definition of done carves out ("or is the
// observed-state webhook and satisfies rule 6 in full"), whose caller is a
// monitoring system that has no use for a form partial and no way to render one.
//
// It lives here rather than in the handler so that the one place a response is
// written for a machine is as visible as the places one is written for a person.

// JSON writes a value as a JSON response.
//
// The body is marshalled before the header is written, so a value that fails to
// marshal produces a clean 500 rather than a 200 with a truncated body -- the
// same reason Page renders into a buffer first.
func JSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		slog.Error("marshalling json response", "error", err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// A machine-facing response is never cacheable: it reports what happened to
	// one report, and a cached answer would describe a different one.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		slog.Debug("writing json response", "error", err)
	}
}

// APIError is the failure shape for the machine-facing route.
//
// Value carries the offending input back when there is one, which rule 13
// requires for an unmapped state ("422 with the offending value echoed"). It is
// caller-supplied text and is truncated by the caller before it gets here; JSON
// encoding handles the escaping.
type APIError struct {
	Error string `json:"error"`
	Field string `json:"field,omitempty"`
	Value string `json:"value,omitempty"`
	Index *int   `json:"index,omitempty"`
}

// JSONError writes a failure response for the machine-facing route.
func JSONError(w http.ResponseWriter, status int, message string) {
	JSON(w, status, APIError{Error: message})
}
