// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// TestServiceDTOEnvironmentsIsAOneElementSlice pins item 7: a service's
// single EnvironmentCode becomes a one-element Environments slice, giving the
// contract one shape for "what is this in" across every entity even though
// the schema differs between assets (a set) and services (a single column).
func TestServiceDTOEnvironmentsIsAOneElementSlice(t *testing.T) {
	got := serviceDTO(store.APIServiceRow{
		ID: "s1", Code: "billing-api", Name: "billing-api", Kind: domain.SvcAPI,
		Lifecycle: domain.LifecycleActive, EnvironmentCode: "prod", Criticality: 1,
		Assets: []string{"vm-db-2"},
	})
	if want := []string{"prod"}; len(got.Environments) != 1 || got.Environments[0] != want[0] {
		t.Fatalf("got Environments %+v, want %+v", got.Environments, want)
	}
}

// TestServiceDTOCriticalityIsTheDomainTier pins item 7's other half: the
// schema's `tier` column becomes `criticality` at the contract boundary, with
// no transformation of the value itself.
func TestServiceDTOCriticalityIsTheDomainTier(t *testing.T) {
	got := serviceDTO(store.APIServiceRow{ID: "s1", Criticality: 3, EnvironmentCode: "prod"})
	if got.Criticality != 3 {
		t.Fatalf("got Criticality %d, want 3", got.Criticality)
	}
}

// TestServiceDTOExactMapping compares a full row to its DTO exactly, per the
// method note: a length or "contains" check is satisfied by many wrong
// answers.
func TestServiceDTOExactMapping(t *testing.T) {
	row := store.APIServiceRow{
		ID: "s1", Code: "billing-api", Name: "Billing API", Kind: domain.SvcAPI,
		Lifecycle: domain.LifecycleActive, EnvironmentCode: "prod", Criticality: 1,
		Assets: []string{"vm-db-2"},
	}
	want := Service{
		ID: "s1", Code: "billing-api", Name: "Billing API", Kind: domain.SvcAPI,
		Lifecycle: domain.LifecycleActive, Environments: []string{"prod"}, Criticality: 1,
		Assets: []string{"vm-db-2"},
	}
	got := serviceDTO(row)
	if got.ID != want.ID || got.Code != want.Code || got.Name != want.Name || got.Kind != want.Kind ||
		got.Lifecycle != want.Lifecycle || got.Criticality != want.Criticality ||
		!equalStrings(got.Environments, want.Environments) || !equalStrings(got.Assets, want.Assets) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestEnvironmentDTONeverPublishesRowVersion pins item 6 at the mapping
// function itself, not merely at the struct shape TestTheContractIsExactly
// TheseFields already covers: even a domain.Environment carrying a non-zero
// RowVersion produces a payload with no `row_version` key, because
// APIListEnvironments does SELECT * into the domain struct and environmentDTO
// is the only thing standing between that and the client.
//
// This checks only for the KEY, not for the row version's VALUE anywhere in
// the body -- a value-shaped substring check (e.g. looking for "7") is
// satisfied by coincidence the moment any other field legitimately contains
// that digit, which TestEnvironmentDTOExactMapping right below already
// covers properly by comparing the whole struct.
func TestEnvironmentDTONeverPublishesRowVersion(t *testing.T) {
	e := domain.Environment{
		ID: "e1", Code: "prod", Name: "Production", Role: domain.EnvRoleProduction,
		InScope: true, Criticality: 1,
		CreatedAt: time.Now().UTC().Format(time.RFC3339), UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		RowVersion: 7,
	}
	got := environmentDTO(e)
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if strings.Contains(string(body), "row_version") {
		t.Fatalf("environmentDTO leaked row_version: %s", body)
	}
}

// TestEnvironmentDTOExactMapping compares a full mapping exactly.
func TestEnvironmentDTOExactMapping(t *testing.T) {
	e := domain.Environment{
		ID: "e1", Code: "prod", Name: "Production", Role: domain.EnvRoleProduction,
		InScope: true, Criticality: 1, RowVersion: 9,
	}
	want := Environment{ID: "e1", Code: "prod", Name: "Production", Role: domain.EnvRoleProduction, InScope: true, Criticality: 1}
	if got := environmentDTO(e); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestAssetDTOExactMapping is the same exact-value discipline applied to the
// asset mapping.
func TestAssetDTOExactMapping(t *testing.T) {
	site := "dc-1"
	row := store.APIAssetRow{
		ID: "a1", Kind: domain.KindVM, Name: "vm-db-2", Lifecycle: domain.LifecycleActive,
		Site: &site, Rack: nil, Role: nil,
		Environments: []string{"prod"}, Addresses: []string{"10.2.0.14"}, Services: []string{"billing-api"},
	}
	got := assetDTO(row)
	if got.ID != "a1" || got.Kind != domain.KindVM || got.Name != "vm-db-2" || got.Lifecycle != domain.LifecycleActive {
		t.Fatalf("got %+v, want identity/kind/name/lifecycle to match the row", got)
	}
	if got.Site == nil || *got.Site != "dc-1" || got.Rack != nil || got.Role != nil {
		t.Fatalf("got Site=%v Rack=%v Role=%v, want Site=dc-1 Rack=nil Role=nil", got.Site, got.Rack, got.Role)
	}
	if !equalStrings(got.Environments, []string{"prod"}) || !equalStrings(got.Addresses, []string{"10.2.0.14"}) ||
		!equalStrings(got.Services, []string{"billing-api"}) {
		t.Fatalf("got %+v, want the row's environments/addresses/services unchanged", got)
	}
}

// TestAddressDTOExactMapping does the same for addresses.
func TestAddressDTOExactMapping(t *testing.T) {
	assetID, assetName := "a1", "vm-db-2"
	row := store.APIAddressRow{
		ID: "ip1", AddrText: "10.2.0.14", AddrFamily: 4,
		AssetID: &assetID, AssetName: &assetName, Environments: []string{"prod"},
	}
	got := addressDTO(row)
	if got.ID != "ip1" || got.Address != "10.2.0.14" || got.Family != 4 {
		t.Fatalf("got %+v, want identity/address/family to match the row", got)
	}
	if got.AssetID == nil || *got.AssetID != "a1" || got.Asset == nil || *got.Asset != "vm-db-2" {
		t.Fatalf("got AssetID=%v Asset=%v, want a1/vm-db-2", got.AssetID, got.Asset)
	}
	if !equalStrings(got.Environments, []string{"prod"}) {
		t.Fatalf("got Environments %+v, want [prod]", got.Environments)
	}
}

// TestAddressDTOWithNoAsset covers the nil path: an address reaching no
// asset (an FHRP virtual address) must publish nil, not a zero-value string.
func TestAddressDTOWithNoAsset(t *testing.T) {
	got := addressDTO(store.APIAddressRow{ID: "ip1", AddrText: "10.0.0.254", AddrFamily: 4})
	if got.AssetID != nil || got.Asset != nil {
		t.Fatalf("got AssetID=%v Asset=%v, want both nil for an address with no asset", got.AssetID, got.Asset)
	}
}
