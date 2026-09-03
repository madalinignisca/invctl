// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"context"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// fix-b item 3: vlan_detail.html's Remove control and its column used to be
// gated on the page-wide .CanWrite, over-offering Remove on a foreign port's
// row and, whenever a project owner's own port and a foreign one shared one
// VLAN's table, leaving the header one column short of the writable row --
// the same shape TestDependencyTableHeaderMatchesItsRows pins for
// dependencies. Port-to-VLAN membership is ScopeSubjectDerived through the
// port's OWNING ASSET, so vlanPortRow.CanWrite (vlans.go) is now
// `CanWriteEntity "asset" .AssetID`, computed once per port.
type vlanPortRowFixture struct {
	vlanID   string
	ifaceIn  string // on fx.assetIn, owned
	ifaceOut string // on fx.assetOut, not owned
}

func setupVLANPortRowFixture(t *testing.T, ctx context.Context, h *harness, fx *boundaryFixtures) *vlanPortRowFixture {
	t.Helper()
	admin := domain.AdministratorPermit(domain.SystemActor)

	vlan, err := domain.NewVLAN(store.NewID(), 3901, "vlan-port-row-fixture", nil)
	if err != nil {
		t.Fatalf("building the fixture VLAN: %v", err)
	}
	if err := h.store.CreateVLAN(ctx, admin, vlan); err != nil {
		t.Fatalf("creating the fixture VLAN: %v", err)
	}

	mkIface := func(assetID, name string) string {
		iface, err := domain.NewInterface(store.NewID(), assetID, name, domain.FFRJ45)
		if err != nil {
			t.Fatalf("building interface %s: %v", name, err)
		}
		if err := h.store.CreateInterface(ctx, admin, iface); err != nil {
			t.Fatalf("creating interface %s: %v", name, err)
		}
		return iface.ID
	}
	ifaceIn := mkIface(fx.assetIn, "vlan-row-fixture-in")
	ifaceOut := mkIface(fx.assetOut, "vlan-row-fixture-out")

	if err := h.store.AddPortToVLAN(ctx, admin, vlan.ID, ifaceIn, domain.VLANModeUntagged); err != nil {
		t.Fatalf("adding the in-scope port to the fixture VLAN: %v", err)
	}
	if err := h.store.AddPortToVLAN(ctx, admin, vlan.ID, ifaceOut, domain.VLANModeUntagged); err != nil {
		t.Fatalf("adding the out-of-scope port to the fixture VLAN: %v", err)
	}

	return &vlanPortRowFixture{vlanID: vlan.ID, ifaceIn: ifaceIn, ifaceOut: ifaceOut}
}

// vlanRemoveAction is the marker rendered only inside vlan_detail.html's
// Remove form -- its own POST action, unique per interface.
func vlanRemoveAction(vlanID, interfaceID string) string {
	return "/vlans/" + vlanID + "/ports/" + interfaceID + "/remove"
}

// TestVLANPortRemoveControlIsSubjectDerived pins the row control: a project
// owner sees Remove on their own asset's port and not on a foreign one, on
// the SAME VLAN's table.
func TestVLANPortRemoveControlIsSubjectDerived(t *testing.T) {
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)
			ctx := context.Background()
			vfx := setupVLANPortRowFixture(t, ctx, h, fx)

			// Positive control: an Administrator sees Remove on BOTH rows --
			// proving the marker itself renders before the owner checks below
			// mean anything.
			h.login(boundaryAdminUser, boundaryAdminPassword)
			adminBody := body(t, h.get("/vlans/"+vfx.vlanID, false))
			if !strings.Contains(adminBody, vlanRemoveAction(vfx.vlanID, vfx.ifaceIn)) {
				t.Fatalf("GET /vlans/%s as Administrator does not carry %q -- the row is not "+
					"proven to render its Remove control at all", vfx.vlanID, vlanRemoveAction(vfx.vlanID, vfx.ifaceIn))
			}
			if !strings.Contains(adminBody, vlanRemoveAction(vfx.vlanID, vfx.ifaceOut)) {
				t.Fatalf("GET /vlans/%s as Administrator does not carry %q", vfx.vlanID, vlanRemoveAction(vfx.vlanID, vfx.ifaceOut))
			}
			h.logout()

			h.login(boundaryOwnerUser, boundaryOwnerPassword)
			ownerBody := body(t, h.get("/vlans/"+vfx.vlanID, false))
			if !strings.Contains(ownerBody, vlanRemoveAction(vfx.vlanID, vfx.ifaceIn)) {
				t.Errorf("GET /vlans/%s as the owner of the port's asset does not carry %q -- "+
					"authorizeInterfaceSubject grants this write at the store layer, and the row "+
					"must offer it", vfx.vlanID, vlanRemoveAction(vfx.vlanID, vfx.ifaceIn))
			}
			if strings.Contains(ownerBody, vlanRemoveAction(vfx.vlanID, vfx.ifaceOut)) {
				t.Errorf("GET /vlans/%s carries %q for a port on a FOREIGN asset -- the owner "+
					"does not own it, and the store refuses this write", vfx.vlanID, vlanRemoveAction(vfx.vlanID, vfx.ifaceOut))
			}
		})
	}
}

// TestVLANPortTableHeaderMatchesItsRows is the VLAN-page sibling of
// TestDependencyTableHeaderMatchesItsRows: with a mixed table (one writable
// row, one not), every data row's <td> count must match the header's <th>
// count, for every row -- not just the first.
func TestVLANPortTableHeaderMatchesItsRows(t *testing.T) {
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)
			ctx := context.Background()
			vfx := setupVLANPortRowFixture(t, ctx, h, fx)

			check := func(who string) {
				page := body(t, h.get("/vlans/"+vfx.vlanID, false))
				header, rows := tableColumns(t, page, "<th>Asset</th>")
				for i, row := range rows {
					if row != header {
						t.Errorf("%s: data row %d has %d <td> cells against a %d-<th> header -- "+
							"misaligned by %d", who, i, row, header, row-header)
					}
				}
			}

			h.login(boundaryAdminUser, boundaryAdminPassword)
			check("an Administrator")
			h.logout()

			// The mixed case: this owner's row (ifaceIn, writable) sits beside
			// the foreign row (ifaceOut, not writable) in the SAME table.
			h.login(boundaryOwnerUser, boundaryOwnerPassword)
			check("a project owner with a MIXED VLAN port table")
			h.logout()

			h.login(boundaryObserverUser, boundaryObserverPass)
			check("an Observer owning nothing")
			h.logout()
		})
	}
}

// TestVLANAddPortOptionsAreFilteredToOwnedAssets pins the Add-port picker
// half of fix-b item 3: AddPortToVLAN/SetInterfaceVLANs is ScopeSubjectDerived
// through the port's OWNING ASSET, so offering a port on an asset the caller
// does not own is offered-and-refused -- the defect Task 3 already closed
// for the link and dependency pickers, filtered here with the same helper
// (writableInterfaceOptions).
func TestVLANAddPortOptionsAreFilteredToOwnedAssets(t *testing.T) {
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)
			ctx := context.Background()
			vfx := setupVLANPortRowFixture(t, ctx, h, fx)

			h.login(boundaryAdminUser, boundaryAdminPassword)
			adminBody := body(t, h.get("/vlans/"+vfx.vlanID, false))
			adminSelect := selectBlock(t, adminBody, "vlan-port")
			if !strings.Contains(adminSelect, `value="`+vfx.ifaceIn+`"`) {
				t.Fatalf("Administrator's vlan-port picker does not list %s -- the option is not "+
					"proven to render at all", vfx.ifaceIn)
			}
			if !strings.Contains(adminSelect, `value="`+vfx.ifaceOut+`"`) {
				t.Fatalf("Administrator's vlan-port picker does not list %s -- the option is not "+
					"proven to render at all", vfx.ifaceOut)
			}
			h.logout()

			h.login(boundaryOwnerUser, boundaryOwnerPassword)
			ownerBody := body(t, h.get("/vlans/"+vfx.vlanID, false))
			ownerSelect := selectBlock(t, ownerBody, "vlan-port")
			if !strings.Contains(ownerSelect, `value="`+vfx.ifaceIn+`"`) {
				t.Errorf("the owner's vlan-port picker does not list %s, a port on an asset they "+
					"own -- the filter is too strict", vfx.ifaceIn)
			}
			if strings.Contains(ownerSelect, `value="`+vfx.ifaceOut+`"`) {
				t.Errorf("the owner's vlan-port picker lists %s, a port on a FOREIGN asset -- "+
					"offering it is the defect the two-ended-write sweep removed from the link and "+
					"dependency pickers", vfx.ifaceOut)
			}
		})
	}
}
