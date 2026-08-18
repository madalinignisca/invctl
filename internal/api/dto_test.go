// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"reflect"
	"strings"
	"testing"
)

// dtoTypes is every struct published by this package. A new DTO goes here, and
// the guards below then apply to it without anybody remembering to add them.
func dtoTypes() []reflect.Type {
	return []reflect.Type{
		reflect.TypeOf(Asset{}),
		reflect.TypeOf(Service{}),
		reflect.TypeOf(Address{}),
		reflect.TypeOf(Environment{}),
	}
}

func TestTheAPINeverExposesMoney(t *testing.T) {
	forbidden := []string{"cost", "price", "amount", "supplier", "tariff",
		"currency", "invoice", "spend", "budget", "amorti"}
	assertNoFieldMatches(t, forbidden,
		"WP-A2 publishes topology, not commercial terms; a leaked read token must not expose what the estate costs")
}

func TestTheAPINeverExposesPersonalData(t *testing.T) {
	forbidden := []string{"actor", "contact", "email", "username", "person", "owner"}
	assertNoFieldMatches(t, forbidden,
		"invariant 5: no personal data. Teams and roles, and not on this surface at all")
}

func TestTheAPINeverExposesObservedState(t *testing.T) {
	forbidden := []string{"observed", "health", "state_since", "reporter",
		"last_report", "reported_at"}
	assertNoFieldMatches(t, forbidden,
		"this surface publishes declared state; observed state has its own direction and its own principal")
}

func TestTheAPINeverExposesASecretReference(t *testing.T) {
	forbidden := []string{"secret", "token", "password", "hash", "key"}
	assertNoFieldMatches(t, forbidden,
		"a secret reference is a path and still never belongs in a published payload")
}

func TestEveryDTOFieldHasAJSONTag(t *testing.T) {
	for _, typ := range dtoTypes() {
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if f.Tag.Get("json") == "" {
				t.Errorf("%s.%s has no json tag; the contract must be explicit, never derived from a Go name",
					typ.Name(), f.Name)
			}
		}
	}
}

func TestNoDTOEmbedsAStoreOrDomainStruct(t *testing.T) {
	for _, typ := range dtoTypes() {
		for i := 0; i < typ.NumField(); i++ {
			if typ.Field(i).Anonymous {
				t.Errorf("%s embeds %s; a DTO is shaped by the contract and a store struct is shaped by the schema, "+
					"and embedding one means the next migration silently changes the published surface",
					typ.Name(), typ.Field(i).Type)
			}
		}
	}
}

// TestTheContractIsExactlyTheseFields is the backstop the denylists above
// cannot be: a denylist only stops what its author thought to forbid, so a
// field like team ownership -- never named "cost" or "owner" or "secret" --
// would pass every guard above and re-enter the contract silently. This test
// instead asserts the published field set by name, in full, both directions.
// If it fails, either a field was added or removed from a DTO in dto.go and
// the map below needs the matching literal edit, or a field vanished from a
// DTO that this map still expects. Either way: the DTOs are a published
// contract, adding or removing a field is a deliberate act, and updating this
// literal is how you declare that you meant it -- not a chore to silence.
func TestTheContractIsExactlyTheseFields(t *testing.T) {
	want := map[string][]string{
		"Asset":       {"id", "name", "kind", "lifecycle", "environments", "site", "rack", "role", "addresses", "services"},
		"Service":     {"id", "code", "name", "kind", "lifecycle", "environments", "criticality", "assets"},
		"Address":     {"id", "address", "family", "asset", "asset_id", "environments"},
		"Environment": {"id", "code", "name", "role", "in_scope", "criticality"},
	}

	for _, typ := range dtoTypes() {
		wantFields, ok := want[typ.Name()]
		if !ok {
			t.Errorf("%s is in dtoTypes() but has no entry in `want`; a DTO added to the package "+
				"is a deliberate publication and this test needs the matching literal field list, "+
				"or it is exactly the silent hole this test exists to close", typ.Name())
			continue
		}

		wantSet := make(map[string]bool, len(wantFields))
		for _, f := range wantFields {
			wantSet[f] = true
		}

		gotSet := make(map[string]bool, typ.NumField())
		for i := 0; i < typ.NumField(); i++ {
			gotSet[typ.Field(i).Tag.Get("json")] = true
		}

		for f := range gotSet {
			if !wantSet[f] {
				t.Errorf("%s has a json tag %q that is not in the expected contract for this type; "+
					"the DTOs are a published contract and adding a field is a deliberate act -- "+
					"if you meant to publish it, add %q to `want[%q]` in this test to say so",
					typ.Name(), f, f, typ.Name())
			}
		}
		for f := range wantSet {
			if !gotSet[f] {
				t.Errorf("%s no longer has a json tag %q that this test expects; "+
					"either the field was removed deliberately, in which case remove %q from `want[%q]`, "+
					"or something regressed the published contract",
					typ.Name(), f, f, typ.Name())
			}
		}
	}
}

// assertNoFieldMatches lowercases every field name and json tag of every DTO
// and refuses any that contains one of the forbidden substrings.
func assertNoFieldMatches(t *testing.T, forbidden []string, why string) {
	t.Helper()
	for _, typ := range dtoTypes() {
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			hay := strings.ToLower(f.Name + " " + f.Tag.Get("json"))
			for _, bad := range forbidden {
				if strings.Contains(hay, bad) {
					t.Errorf("%s.%s (json %q) matches %q -- %s",
						typ.Name(), f.Name, f.Tag.Get("json"), bad, why)
				}
			}
		}
	}
}
