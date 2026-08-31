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
	"sort"
	"strings"
	"testing"
)

// wireFields is the committed JSON shape of /api/v1, one entry per published
// type, each listing exactly the field names that go on the wire.
//
// docs/API.md's Compatibility section promises that within v1 a field is
// never removed and never renamed, and that new fields may be added. This is
// what makes that a checkable promise rather than an intention.
//
// A REMOVAL OR RENAME BREAKS SOMEBODY'S ANSIBLE RUN AT 03:00 and is the whole
// reason the version is in the path. An ADDITION is allowed, and shows up
// here as a line you have to add on purpose -- which is the point: it becomes
// a diff a reviewer sees, rather than something that happens to a consumer.
//
// The existing dto_mapping_test.go tests check the Go struct: got.Code
// against want.Code. Renaming a `json:"code"` tag to `json:"service_code"`
// leaves every one of them green and breaks every client. Only the tag is
// the contract.
var wireFields = map[string][]string{
	"Asset":       {"id", "name", "kind", "lifecycle", "environments", "site", "rack", "addresses", "services"},
	"Service":     {"id", "code", "name", "kind", "lifecycle", "environments", "criticality", "assets"},
	"Address":     {"id", "address", "family", "asset", "asset_id", "environments"},
	"Environment": {"id", "code", "name", "role", "in_scope", "criticality"},

	// Inventory marshals through a custom MarshalJSON rather than struct
	// tags -- the Ansible format puts group names at the top level, which no
	// fixed set of fields can express -- so reflection sees nothing here.
	// Its shape is pinned by the Ansible tests in this package, not by this
	// one; recorded as empty so that GAINING a tagged field shows up as a
	// change rather than passing silently.
	"Inventory":      {},
	"InventoryMeta":  {"hostvars"},
	"InventoryGroup": {"hosts"},
}

// TestTheV1WireShapeIsUnchanged fails on a removed or renamed JSON field in
// any published DTO, and on an added one until it is recorded above.
func TestTheV1WireShapeIsUnchanged(t *testing.T) {
	types := map[string]reflect.Type{
		"Asset":          reflect.TypeOf(Asset{}),
		"Service":        reflect.TypeOf(Service{}),
		"Address":        reflect.TypeOf(Address{}),
		"Environment":    reflect.TypeOf(Environment{}),
		"Inventory":      reflect.TypeOf(Inventory{}),
		"InventoryMeta":  reflect.TypeOf(InventoryMeta{}),
		"InventoryGroup": reflect.TypeOf(InventoryGroup{}),
	}
	if len(types) != len(wireFields) {
		t.Fatalf("this test drives %d types but wireFields records %d -- a published "+
			"type was added or removed without updating the contract", len(types), len(wireFields))
	}

	for name, typ := range types {
		want := append([]string(nil), wireFields[name]...)
		sort.Strings(want)

		var got []string
		for i := 0; i < typ.NumField(); i++ {
			tag := typ.Field(i).Tag.Get("json")
			if tag == "" || tag == "-" {
				continue
			}
			got = append(got, strings.Split(tag, ",")[0])
		}
		sort.Strings(got)

		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s's wire shape changed.\n  on the wire: %v\n  recorded:    %v\n"+
				"A field that was REMOVED or RENAMED is a breaking change to /api/v1 and "+
				"belongs in /api/v2 (docs/API.md, Compatibility). A field that was ADDED "+
				"is fine -- record it above so the addition is visible in the diff.",
				name, got, want)
		}
	}
}
