// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import (
	"encoding/json"
	"time"
)

// SavedView is one person's named set of list filters.
//
// The SUBJECT of this row is a person, which is true of nothing else in this
// product -- every other reference to app_user is attribution. See
// docs/saved-views-design.md §2 for why it earns a table where a column
// preference did not.
type SavedView struct {
	ID         string `db:"id"`
	UserID     string `db:"user_id"`
	Entity     string `db:"entity"`
	Name       string `db:"name"`
	Params     string `db:"params"`
	Lifecycle  string `db:"lifecycle"`
	CreatedAt  string `db:"created_at"`
	UpdatedAt  string `db:"updated_at"`
	RowVersion int    `db:"row_version"`
}

// The entities a view can be saved against. Matches saved_view's CHECK
// constraint; the DB check is the second line of defence, not the first.
const (
	SavedViewAsset   = "asset"
	SavedViewService = "service"
)

func validSavedViewEntity(e string) bool {
	return e == SavedViewAsset || e == SavedViewService
}

// NewSavedView validates and builds a view. It does NOT set an owner it was
// not given: the caller supplies userID, and the store checks that it is the
// caller's own before writing (internal/store/savedviews.go).
func NewSavedView(id, userID, entity, name, params string, now time.Time) (*SavedView, error) {
	ve := &ValidationError{}
	name = checkRequired(ve, "name", name)
	if !validSavedViewEntity(entity) {
		ve.Add("entity", "must be one of: asset, service")
	}
	if userID == "" {
		ve.Add("user_id", "is required")
	}
	// params must be a JSON OBJECT. It is stored opaque and never queried,
	// so this constructor is the only place its shape is checked. The
	// literal `null` unmarshals into a nil map with err == nil, which is
	// the one shape json.Unmarshal alone will not catch -- probe == nil
	// after a successful unmarshal means the input was exactly `null`.
	var probe map[string]any
	if err := json.Unmarshal([]byte(params), &probe); err != nil || probe == nil {
		ve.Add("params", "must be a JSON object")
	}
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	ts := FormatTime(now)
	return &SavedView{
		ID: id, UserID: userID, Entity: entity, Name: name, Params: params,
		Lifecycle: LifecycleActive, CreatedAt: ts, UpdatedAt: ts, RowVersion: 1,
	}, nil
}

// Validate re-checks a view built by hand or round-tripped from a form.
// NewSavedView is the only constructor, but Update paths mutate a struct and
// hand it back, so the store validates again before writing.
func (v *SavedView) Validate() error {
	ve := &ValidationError{}
	v.Name = checkRequired(ve, "name", v.Name)
	if !validSavedViewEntity(v.Entity) {
		ve.Add("entity", "must be one of: asset, service")
	}
	if v.UserID == "" {
		ve.Add("user_id", "is required")
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(v.Params), &probe); err != nil || probe == nil {
		ve.Add("params", "must be a JSON object")
	}
	return ve.OrNil()
}
