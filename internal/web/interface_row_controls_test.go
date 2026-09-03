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

// WP-1.1 Task 2: unpatching a cable is a "link" write, and authorizeLinkSubjects
// (internal/store/network.go) requires the caller's permit to cover BOTH cabled
// assets, not just the one whose page is open. Before this the template offered
// Unpatch to an Administrator only, so a project owner who owned both ends of a
// cable -- and could already retire it at the store layer -- had no control to
// do so through the UI. This file pins the two-ended gate at the template
// boundary: both ends owned, near end only (cable runs to a foreign asset),
// far end only (viewing an asset the owner does not hold), Administrator, and
// an unpatched port, which must show no Unpatch control for anyone.
type interfaceRowFixture struct {
	// assetIn2 is a second asset linked to project alpha, so a cable can have
	// BOTH ends inside the owner's scope without depending on an asset being
	// cabled to itself.
	assetIn2 string

	// ifaceBothA/ifaceBothB: a cable between assetIn (owned) and assetIn2
	// (owned) -- the caller owns both ends.
	ifaceBothA, ifaceBothB string
	linkBoth               string

	// ifaceForeignNear is on assetIn (owned); ifaceForeignFar is on assetOut
	// (not owned) -- the caller owns only the near end of this cable.
	ifaceForeignNear, ifaceForeignFar string
	linkForeign                       string

	// ifaceUnpatched is on assetIn (owned) and carries no cable at all.
	ifaceUnpatched string
}

func setupInterfaceRowFixture(t *testing.T, ctx context.Context, h *harness, fx *boundaryFixtures) *interfaceRowFixture {
	t.Helper()
	admin := domain.AdministratorPermit(domain.SystemActor)

	assetIn2 := mustBoundaryAsset(t, ctx, h, "t-alpha-asset-2")
	link, err := domain.NewProjectAssetLink(fx.projectAlpha, assetIn2, domain.ProjectOwns, nil, h.store.Now())
	if err != nil {
		t.Fatalf("building the second in-scope asset link: %v", err)
	}
	if err := h.store.LinkProjectAsset(ctx, admin, link); err != nil {
		t.Fatalf("linking the second in-scope asset to alpha: %v", err)
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
	mkLink := func(aID, bID string) string {
		l, err := domain.NewLink(store.NewID(), aID, bID)
		if err != nil {
			t.Fatalf("building link: %v", err)
		}
		if err := h.store.CreateLink(ctx, admin, l); err != nil {
			t.Fatalf("creating link: %v", err)
		}
		return l.ID
	}

	ifaceBothA := mkIface(fx.assetIn, "row-fixture-both-a")
	ifaceBothB := mkIface(assetIn2, "row-fixture-both-b")
	linkBoth := mkLink(ifaceBothA, ifaceBothB)

	ifaceForeignNear := mkIface(fx.assetIn, "row-fixture-foreign-near")
	ifaceForeignFar := mkIface(fx.assetOut, "row-fixture-foreign-far")
	linkForeign := mkLink(ifaceForeignNear, ifaceForeignFar)

	ifaceUnpatched := mkIface(fx.assetIn, "row-fixture-unpatched")

	return &interfaceRowFixture{
		assetIn2:         assetIn2,
		ifaceBothA:       ifaceBothA,
		ifaceBothB:       ifaceBothB,
		linkBoth:         linkBoth,
		ifaceForeignNear: ifaceForeignNear,
		ifaceForeignFar:  ifaceForeignFar,
		linkForeign:      linkForeign,
		ifaceUnpatched:   ifaceUnpatched,
	}
}

// unpatchAction is the marker rendered only inside the Unpatch form on
// asset_detail.html -- its own hx-post/action target, unique per link.
func unpatchAction(linkID string) string {
	return `/links/` + linkID + `/retire`
}

// interfaceRowContaining isolates the single <tr>...</tr> block holding
// marker, so a caller can assert something is absent from THAT row without a
// page-wide substring search finding the same text on an unrelated row
// further down the same table. Named apart from costs_test.go's
// rowContaining, which locates a row by a different marker shape (a cost
// id's form attribute, not an arbitrary substring) and is not reusable here.
func interfaceRowContaining(t *testing.T, page, marker string) string {
	t.Helper()
	mi := strings.Index(page, marker)
	if mi < 0 {
		t.Fatalf("marker %q not found on the page at all", marker)
	}
	start := strings.LastIndex(page[:mi], "<tr>")
	if start < 0 {
		t.Fatalf("no <tr> opens before marker %q", marker)
	}
	end := strings.Index(page[mi:], "</tr>")
	if end < 0 {
		t.Fatalf("no </tr> closes after marker %q", marker)
	}
	return page[start : mi+end+len("</tr>")]
}

// TestUnpatchControlIsTwoEnded drives all four ownership combinations plus
// the unpatched-port case through one project owner and one Administrator.
func TestUnpatchControlIsTwoEnded(t *testing.T) {
	for _, eng := range boundaryEngines(t) {
		t.Run(eng.name, func(t *testing.T) {
			h, fx := setupBoundary(t, eng)
			ctx := context.Background()
			ifx := setupInterfaceRowFixture(t, ctx, h, fx)

			// Positive control: an Administrator sees Unpatch on the
			// foreign-asset cable's near-side page too -- proving the marker
			// itself renders before the owner checks below mean anything.
			h.login(boundaryAdminUser, boundaryAdminPassword)
			adminBody := body(t, h.get("/assets/"+fx.assetIn, false))
			if !strings.Contains(adminBody, unpatchAction(ifx.linkForeign)) {
				t.Fatalf("GET /assets/%s as Administrator does not carry %q -- the row partial "+
					"is not proven to render its Unpatch control at all", fx.assetIn, unpatchAction(ifx.linkForeign))
			}
			// Administrator, same cable, viewed from the far (foreign) end.
			adminFarBody := body(t, h.get("/assets/"+fx.assetOut, false))
			if !strings.Contains(adminFarBody, unpatchAction(ifx.linkForeign)) {
				t.Errorf("GET /assets/%s as Administrator does not carry %q", fx.assetOut, unpatchAction(ifx.linkForeign))
			}
			h.logout()

			h.login(boundaryOwnerUser, boundaryOwnerPassword)

			inBody := body(t, h.get("/assets/"+fx.assetIn, false))

			if !strings.Contains(inBody, unpatchAction(ifx.linkBoth)) {
				t.Errorf("GET /assets/%s as the owner of BOTH cabled assets does not carry %q -- "+
					"authorizeLinkSubjects grants this write at the store layer, and the row must "+
					"offer it", fx.assetIn, unpatchAction(ifx.linkBoth))
			}
			if strings.Contains(inBody, unpatchAction(ifx.linkForeign)) {
				t.Errorf("GET /assets/%s carries %q for a cable running to a FOREIGN asset -- the "+
					"owner holds only the near end, and the store refuses this write",
					fx.assetIn, unpatchAction(ifx.linkForeign))
			}
			// Editing the near port itself stays offered -- it is single-subject.
			if !strings.Contains(inBody, `?edit=`+ifx.ifaceForeignNear+`#ports`) {
				t.Errorf("GET /assets/%s does not offer Edit on the owner's own port %s, even "+
					"though Unpatch is correctly withheld for the foreign far end",
					fx.assetIn, ifx.ifaceForeignNear)
			}

			t.Run("owning the far asset only, viewed from that page, offers nothing", func(t *testing.T) {
				outBody := body(t, h.get("/assets/"+fx.assetOut, false))
				if strings.Contains(outBody, unpatchAction(ifx.linkForeign)) {
					t.Errorf("GET /assets/%s carries %q -- the owner does not own THIS asset "+
						"(assetOut), only the far end of the cable seen from assetIn's page, so "+
						"nothing on this page should be writable for them", fx.assetOut, unpatchAction(ifx.linkForeign))
				}
				if strings.Contains(outBody, `?edit=`+ifx.ifaceForeignFar+`#ports`) {
					t.Errorf("GET /assets/%s offers Edit on port %s, belonging to an asset the "+
						"owner does not own", fx.assetOut, ifx.ifaceForeignFar)
				}
			})

			t.Run("an unpatched port offers no Unpatch control, for anyone", func(t *testing.T) {
				if strings.Contains(inBody, `/links//retire`) {
					t.Error("an empty link id leaked an Unpatch form action")
				}
				// The unpatched port's own row carries an Edit link (the owner
				// owns the asset), so the row is proven to render at all --
				// but its own <tr> must not carry a retire form anywhere in it,
				// checked as an isolated block rather than a page-wide substring
				// search, since other rows on this page DO carry one.
				if !strings.Contains(inBody, `?edit=`+ifx.ifaceUnpatched+`#ports`) {
					t.Fatalf("GET /assets/%s does not offer Edit on the unpatched port %s -- the "+
						"row is not proven to render at all", fx.assetIn, ifx.ifaceUnpatched)
				}
				row := interfaceRowContaining(t, inBody, "row-fixture-unpatched")
				if strings.Contains(row, "Unpatch") {
					t.Errorf("the unpatched port's own row carries an Unpatch control:\n%s", row)
				}

				h.logout()
				h.login(boundaryAdminUser, boundaryAdminPassword)
				adminInBody := body(t, h.get("/assets/"+fx.assetIn, false))
				adminRow := interfaceRowContaining(t, adminInBody, "row-fixture-unpatched")
				if strings.Contains(adminRow, "Unpatch") {
					t.Errorf("as Administrator, the unpatched port's own row carries an Unpatch "+
						"control:\n%s", adminRow)
				}
				h.logout()
				h.login(boundaryOwnerUser, boundaryOwnerPassword)
			})
		})
	}
}
