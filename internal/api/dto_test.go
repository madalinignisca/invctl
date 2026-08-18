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
