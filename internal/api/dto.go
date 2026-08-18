// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Package api holds the DTO structs that are the published contract of the
// read-only, token-scoped inventory API (WP-A2). These types are not the
// domain model and must never become it: a store or domain struct is shaped
// by the schema and changes whenever a migration does, while a DTO here is
// shaped by the contract and changes only when somebody deliberately decides
// to publish a new field. Adding a field to one of these structs is a
// deliberate edit to a published surface, not a convenience — it is reviewed
// like one. See dto_test.go for the guards that keep it that way.
package api

// Asset is the published view of an asset: identity, placement and the
// services it carries. It deliberately omits everything in
// internal/domain.Asset that is not part of the contract — purchase-adjacent
// fields, capacity numbers, team ownership, EOL dates, and the opaque attrs
// blob never appear here.
type Asset struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	Lifecycle    string   `json:"lifecycle"`
	Environments []string `json:"environments"`
	Site         *string  `json:"site"`
	Rack         *string  `json:"rack"`
	Role         *string  `json:"role"`
	Addresses    []string `json:"addresses"`
	Services     []string `json:"services"`
}

// Service is the published view of a service. Criticality is the domain's
// Tier field renamed for the contract, because "tier" is an internal word
// and "criticality" is what the UI calls it. Environments is a one-element
// slice built from the service's single EnvironmentID, so that the contract
// has one shape for "what is this in" across every entity even though the
// schema differs between assets and services.
type Service struct {
	ID           string   `json:"id"`
	Code         string   `json:"code"`
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	Lifecycle    string   `json:"lifecycle"`
	Environments []string `json:"environments"`
	Criticality  int      `json:"criticality"`
	Assets       []string `json:"assets"`
}

// Address is the published view of an IP address and the asset it is
// attached to, if any.
type Address struct {
	ID           string   `json:"id"`
	Address      string   `json:"address"`
	Family       int      `json:"family"`
	Asset        *string  `json:"asset"`
	AssetID      *string  `json:"asset_id"`
	Environments []string `json:"environments"`
}

// Environment is the published view of a segmentation boundary.
type Environment struct {
	ID          string `json:"id"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	InScope     bool   `json:"in_scope"`
	Criticality int    `json:"criticality"`
}
