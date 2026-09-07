// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"strings"
	"testing"
)

// TestAWirelessPageNeverRendersThePSKPath holds the wireless pages to the
// disclosure boundary the rest of this codebase already draws.
//
// `psk_ref` holds a PATH, never a key, and the first version of the wireless
// detail page rendered it in full — reasoning, correctly, that a path is not a
// secret. That is true and it is the wrong question. `identity.secret_ref` is a
// path too, and `docs/DECISIONS.md` records that it "appears in no template at
// all. The asymmetry is deliberate." `docs/rbac-design.md` says why:
//
//	"Exposing every integration path in the estate to every authenticated
//	reader is a larger disclosure than the rest of the inventory ... This is
//	the boundary between 'the inventory is not a secret' and 'the way in is'."
//
// A PSK reference is the way in to a wireless network, and these pages are
// readable by every authenticated session including read-only ones.
//
// SO THE UI SAYS WHAT THE AUDIT SAYS. `docs/AUDIT.md` rule 12 requires
// `change_log` to record THAT a secret reference changed and never what to;
// this page records THAT one exists and never what it is.
//
// BOTH DIRECTIONS, like TestSnapshotRedactsSecretRef. Hiding the path is half
// the requirement: a page that said nothing at all would leave an operator
// unable to tell an SSID whose reference is withheld from one nobody has
// recorded a reference for, and that second reading is the one that gets
// somebody paged at three in the morning.
//
// E2E covers this too, and cannot replace this: `make e2e` is opt-in and needs
// a running instance, so a regression only Playwright can see is one CI cannot.
func TestAWirelessPageNeverRendersThePSKPath(t *testing.T) {
	// The value seeded for `corp` in internal/seed/seed_engine.go. Written out
	// rather than imported so that changing the seed cannot quietly make this
	// test look for something nothing renders.
	const seededPath = "kv/demo/wifi/corp/psk"

	h := newHarness(t)
	h.login("admin", "admin-password")

	list := body(t, h.get("/wireless", false))
	if strings.Contains(list, seededPath) {
		t.Error("the wireless list rendered a PSK path. It is the way in to that " +
			"network and this page is readable by every authenticated session")
	}

	// Find corp's detail page the way a reader reaches it, rather than
	// constructing an id: a UUIDv7 is regenerated on every seed run.
	id, ok := hrefFor(list, "/wireless/")
	if !ok {
		t.Fatal("no wireless LAN links on /wireless, so this test cannot reach a " +
			"detail page and would pass by checking nothing")
	}
	detail := body(t, h.get(id, false))

	if strings.Contains(detail, seededPath) {
		t.Errorf("%s rendered the PSK path in full. A path is not a secret, and a "+
			"complete map of credential paths readable by every account is a larger "+
			"disclosure than the inventory around it (docs/rbac-design.md)", id)
	}
	if !strings.Contains(detail, "PSK reference") {
		t.Errorf("%s says nothing about a PSK reference at all. Withholding the path "+
			"must not also withhold whether there IS one -- an operator cannot tell a "+
			"redacted reference from an SSID nobody recorded one for", id)
	}
}

// hrefFor returns the first href on the page beginning with prefix.
func hrefFor(page, prefix string) (string, bool) {
	for rest := page; ; {
		i := strings.Index(rest, `href="`+prefix)
		if i < 0 {
			return "", false
		}
		rest = rest[i+len(`href="`):]
		end := strings.IndexByte(rest, '"')
		if end < 0 {
			return "", false
		}
		href := rest[:end]
		// Skip the list page's own filters and anchors.
		if !strings.ContainsAny(href, "?#") && href != prefix {
			return href, true
		}
		rest = rest[end:]
	}
}
