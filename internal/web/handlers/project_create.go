// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import "github.com/madalinignisca/invctl/internal/store"

// projectCreateForm is what web/templates/partials/project_create_form.html
// needs to render standalone, in any of its three modes (WP-G1 Task 14,
// docs/rbac-design.md §4).
//
// ONE PARTIAL, THREE MODES, ONE STRUCT -- not three structs and three
// templates. An asset, a service and a circuit form differ in which fields
// they collect, not in what the form IS: a project owner declaring "this
// new thing belongs to my project", submitted to a project-scoped create
// route and re-rendered in place on refusal. AssetCreateInProject,
// ServiceCreateInProject and CircuitCreateInProject (assets.go, services.go,
// circuits.go) each populate the one Mode they need and leave the other
// modes' fields at their zero value; the template only reads the ones its
// own Mode branch names.
type projectCreateForm struct {
	// Mode selects which fields the partial draws: "asset", "service" or
	// "circuit".
	Mode      string
	ProjectID string
	CSRF      string
	Errors    map[string]string

	// Kinds is the asset-kind or service-kind vocabulary, populated only for
	// the mode that uses it.
	Kinds []store.VocabularyTerm
	// Providers is the carrier picker, populated only for circuit mode.
	Providers []store.ProviderRow

	// Submitted values, redisplayed on a refused create so the operator does
	// not retype the whole form. Read from the raw request rather than from
	// the domain object a failed constructor may not have returned at all.
	Name       string
	Kind       string
	Code       string
	CID        string
	ProviderID string
}
