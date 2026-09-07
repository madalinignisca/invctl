// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// WP-F1: wireless LANs (docs/wireless-design.md, internal/web/handlers/wireless.go,
// web/templates/pages/wireless_list.html, wireless_detail.html, and the
// Radios panel on asset_detail.html). The store, the audit fold
// (interfaceWLANAudit, D7), the structures join and every string these
// pages can render are already asserted by Go tests
// (internal/store/wireless_test.go, internal/web/detail_pages_render_test.go).
// This file exists for the claims that specifically need a real browser:
//
//   1. /wireless is reached by CLICKING the nav rail's "Wireless" link
//      under Network, not by fetching the URL directly -- this project has
//      shipped a 404 on a button with every handler test green, because a
//      handler test injects router params by hand and never asks the
//      router whether anything can actually reach it (docs/E2E.md,
//      CLAUDE.md's evidence-gate note). From there, following an SSID link
//      to its own detail page.
//   2. An SSID's radios panel (wireless_detail.html) and the OWNING ASSET's
//      own Radios panel (asset_detail.html) agree about the same fact --
//      that this radio broadcasts this SSID -- rendered independently on
//      two different pages, which is a DOM-level claim no single Go test
//      makes (each page is asserted against its own template in isolation).
//   3. The seed's whole point (docs/wireless-design.md's seed comment,
//      internal/seed/seed_engine.go's wirelessLANs): `warehouse-scan` sits
//      on exactly one AP -- a standing single point of failure the impact
//      engine reports permanently, not on request (§2.2) -- while `corp`
//      sits on two, reduced to one rather than emptied by losing either.
//      Both structure findings depend on this exact shape, so asserting it
//      here catches a seed change a store test alone might not.
//   4. NOTHING this design forbids ever renders on a wireless page (D4,
//      §2.6): no PSK/passphrase input (`psk_ref` is a plain-text path,
//      never a secret -- there is nothing to reveal), and no channel, band,
//      frequency or RSSI field of any kind -- F1 declares only, and channel
//      planning is explicitly deferred (§2.6: "Adding only the declared
//      half now would build the trap this section exists to describe").
//      These are absence claims; each is written so it would fail if such
//      a field were added -- see the mutation this spec was proven against.
//
// Read-only. Never submits a form -- see docs/E2E.md, "This suite is
// read-only by default". The "Add radio" / "Declare a wireless LAN" forms
// are visible on these pages but nothing here clicks submit.
import { test, expect } from '@playwright/test';
import { resolveAssetPath } from '../helpers/resolve.js';

const BASE_URL = process.env.INV_E2E_BASE_URL;
const describe = BASE_URL ? test.describe : test.describe.skip;

// The three SSIDs the demo seed declares, and how many access points each
// one's radios span (internal/seed/seed_engine.go's wirelessLANs). Read by
// name off the /wireless list table, never a hardcoded id -- ids are fresh
// UUIDv7s on every seed run (docs/E2E.md).
const SSID_AP_COUNTS = { corp: 2, guest: 2, 'warehouse-scan': 1 };

// wirelessRow reads one SSID's own row on /wireless: its detail link and
// its own "APs" cell, exactly as the table renders them -- this always
// follows a link and reads a cell the page actually produced, rather than
// guessing a URL shape.
function wirelessRow(page, ssid) {
  return page.locator('table tbody tr', { has: page.locator(`a:text-is("${ssid}")`) });
}

// wlanRadiosPanel is the SSID detail page's radio membership table
// (wireless_detail.html's "N radios" panel) -- scoped by its own panel-note
// text, which is unique to this table on the page.
function wlanRadiosPanel(page) {
  return page.locator('.panel:has(.panel-note:text("This is the edge"))');
}

// assetRadiosPanel is the asset detail page's own Radios panel
// (asset_detail.html), scoped the same way panel-breakout-trace.spec.js
// scopes "Patching" -- by its heading, since the panel has no id.
function assetRadiosPanel(page) {
  return page.locator('.panel:has(h2:text("Radios"))');
}

describe('wireless LANs', () => {
  test('is reached from the nav rail, and an SSID leads to its own detail page', async ({ page }) => {
    const consoleErrors = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error') consoleErrors.push(msg.text());
    });

    await page.goto('/', { waitUntil: 'networkidle' });

    // nav.go's "Network" group holds "Wireless" (handlers/nav.go). Same
    // collapsed-by-default shape power-cost-section.spec.js already
    // handles: open the group first if the link isn't already showing.
    const group = page.locator('.rail-group[data-group="Network"]');
    await expect(group, 'the Network nav group should exist in the rail').toHaveCount(1);
    const link = group.locator('a.rail-link', { hasText: 'Wireless' });
    if (!(await link.isVisible())) {
      await group.locator('.rail-group-head').click();
    }
    await expect(link, 'the "Wireless" link should be reachable from the Network group').toBeVisible();

    await Promise.all([
      page.waitForLoadState('networkidle'),
      link.click(),
    ]);
    await expect(page, 'clicking "Wireless" should land on /wireless').toHaveURL(/\/wireless$/);

    // Click through from the list to one SSID's own detail page -- a real
    // link, not a constructed URL.
    const corpLink = page.locator('table tbody tr a:text-is("corp")');
    await expect(corpLink, 'the corp SSID should be listed').toBeVisible();
    await Promise.all([
      page.waitForLoadState('networkidle'),
      corpLink.click(),
    ]);
    await expect(page, 'clicking the SSID should land on its detail page').toHaveURL(/\/wireless\/[^/]+$/);
    await expect(page.locator('h1')).toHaveText('Corp');

    expect(consoleErrors, 'console errors across the wireless flow').toEqual([]);
  });

  test('the SSID detail page and the asset\'s own Radios panel agree about the same membership', async ({
    page,
  }) => {
    await page.goto('/wireless', { waitUntil: 'networkidle' });
    await page.locator('table tbody tr a:text-is("corp")').click();
    await page.waitForLoadState('networkidle');

    // Read corp's radios straight off its own detail page: which asset,
    // which radio.
    const panel = wlanRadiosPanel(page);
    const rows = panel.locator('table tbody tr');
    await expect(rows, 'corp should be broadcast by exactly two radios (both on radio-5g)').toHaveCount(2);
    const members = [];
    const count = await rows.count();
    for (let i = 0; i < count; i++) {
      const cells = rows.nth(i).locator('td');
      members.push({
        asset: (await cells.nth(0).textContent()).trim(),
        radio: (await cells.nth(1).textContent()).trim(),
      });
    }
    members.sort((a, b) => a.asset.localeCompare(b.asset));
    expect(members.map((m) => m.asset)).toEqual(['ap-1', 'ap-2']);

    // Now cross the page boundary: each of those assets' OWN Radios panel
    // must independently say the same radio broadcasts corp. This is a
    // second, separately-rendered template agreeing with the first --
    // nothing in this repo asserts both at once other than a real browser
    // following both links.
    for (const member of members) {
      const assetPath = await resolveAssetPath(page, member.asset);
      await page.goto(assetPath, { waitUntil: 'networkidle' });
      const assetPanel = assetRadiosPanel(page);
      const radioRow = assetPanel.locator('table tbody tr', {
        has: page.locator(`td.mono:text-is("${member.radio.replace(/\s+disabled$/, '').trim()}")`),
      }).first();
      // The row's radio cell is followed by "disabled" only when the radio
      // is administratively down (asset_detail.html) -- match the radio
      // name as a prefix rather than the whole cell for that reason.
      const radioCellText = await radioRow.locator('td').nth(0).textContent();
      expect(radioCellText.trim().startsWith(member.radio.split(' ')[0]), `${member.asset}'s ${member.radio} row`).toBe(true);
      const pills = radioRow.locator('td').nth(2).locator('.pill-info');
      await expect(pills, `${member.asset}'s ${member.radio} row should list the SSIDs it broadcasts`).toContainText(
        ['corp'],
        { useInnerText: false },
      );
    }
  });

  test('warehouse-scan sits on exactly one AP, and corp on two -- the seed\'s whole point', async ({ page }) => {
    await page.goto('/wireless', { waitUntil: 'networkidle' });

    for (const [ssid, expectedAPs] of Object.entries(SSID_AP_COUNTS)) {
      const row = wirelessRow(page, ssid);
      await expect(row, `${ssid} should be listed`).toHaveCount(1);
      // The list's own "APs" cell (wirelessListPage.WLANs[].AssetCount) --
      // read directly rather than re-derived, since this test's job is
      // confirming the browser renders what the page states, the same
      // split panel-breakout-trace.spec.js draws for its own ordering
      // claim.
      const apsCell = row.locator('td.num').nth(1);
      await expect(apsCell, `${ssid}'s APs column`).toHaveText(String(expectedAPs));
    }

    // Cross-checked on warehouse-scan's own detail page: exactly one
    // DISTINCT asset appears in its radios table, and it is ap-2 -- not
    // merely one row (a single AP with two radios on this SSID would also
    // produce one row's worth of asset text repeated, so this counts
    // distinct asset names, not rows).
    await wirelessRow(page, 'warehouse-scan').locator('a').first().click();
    await page.waitForLoadState('networkidle');
    const assets = await wlanRadiosPanel(page).locator('table tbody tr td:first-child a').allTextContents();
    const distinctAssets = [...new Set(assets.map((a) => a.trim()))];
    expect(distinctAssets, 'warehouse-scan is a standing single point of failure -- exactly one AP').toEqual([
      'ap-2',
    ]);
  });

  test('no passphrase field and no channel/band/frequency/RSSI field anywhere on the wireless pages (D4, §2.6)', async ({
    page,
  }) => {
    // §2.6: F1 declares only -- no operating channel, no planned channel,
    // no RSSI, no client count. These words would only legitimately appear
    // here if that boundary were crossed, so their absence is the claim,
    // not an incidental fact about today's copy.
    const forbiddenWords = /\b(channel|band|frequency|rssi)\b/i;

    async function assertPageClean(path, label) {
      await page.goto(path, { waitUntil: 'networkidle' });
      await expect(
        page.locator('input[type="password"]'),
        `${label}: a PSK/passphrase must never be an input a browser could mask or submit as a secret (D4)`,
      ).toHaveCount(0);
      const bodyText = await page.locator('body').innerText();
      expect(
        forbiddenWords.test(bodyText),
        `${label}: no channel/band/frequency/RSSI wording should appear anywhere (§2.6 -- observed wireless state is deferred, not half-built)`,
      ).toBe(false);
    }

    await assertPageClean('/wireless', '/wireless');
    for (const ssid of Object.keys(SSID_AP_COUNTS)) {
      await page.goto('/wireless', { waitUntil: 'networkidle' });
      const href = await page.locator(`table tbody tr a:text-is("${ssid}")`).first().getAttribute('href');
      await assertPageClean(href, `/wireless/{id} (${ssid})`);
    }

    // psk_ref itself is rendered as PLAIN TEXT precisely because it is a
    // path and not a secret (wireless_detail.html's own comment) -- corp is
    // the one SSID seeded with a psk_ref, so its detail page is where a
    // regression toward `type="password"` would actually show up.
    await page.goto('/wireless', { waitUntil: 'networkidle' });
    const corpHref = await page.locator('table tbody tr a:text-is("corp")').first().getAttribute('href');
    await page.goto(corpHref, { waitUntil: 'networkidle' });
    await expect(
      page.getByText('kv/demo/wifi/corp/psk', { exact: false }),
      'the seeded PSK reference should render as a plain visible path, not be hidden behind a masked field',
    ).toBeVisible();
  });
});
