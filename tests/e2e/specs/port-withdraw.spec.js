// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// WITHDRAWING A PORT (migration 00062).
//
// WRITES, so it takes the same positive INV_E2E_DISPOSABLE opt-in as
// saved-views.spec.js and correction-paths.spec.js: retiring a port is a
// permanent, audited change and soft-delete means the row stays retired.
//
// The Go suite already covers the store invariant (a retired port is bare, and
// nothing can attach to one), that the control is offered only on a bare port,
// and that an Observer is refused. This file is for the claim that needs a real
// browser: THE FORM IS GATED BY hx-confirm, so submitting it requires accepting
// a native dialog. A Go test posts straight to the route and never sees that
// gate at all -- if the confirm were wrong or the form submitted without it, an
// operator would withdraw a port with one stray click and no test would notice.
import { test, expect } from '@playwright/test';

const BASE_URL = process.env.INV_E2E_BASE_URL;
const disposableOptIn = process.env.INV_E2E_DISPOSABLE === 'true';

function pointsAtLocalhost() {
  if (!BASE_URL) {
    return false;
  }
  try {
    const host = new URL(BASE_URL).hostname;
    return host === 'localhost' || host === '127.0.0.1';
  } catch {
    return false;
  }
}

const localTarget = pointsAtLocalhost();
const describeTitle = disposableOptIn
  ? 'withdrawing a port (mutates -- disposable local instance only)'
  : 'withdrawing a port (mutates -- disposable local instance only) ' +
    '[skipped: set INV_E2E_DISPOSABLE=true to run this spec]';
const describe = disposableOptIn ? test.describe : test.describe.skip;

describe(describeTitle, () => {
  test.beforeAll(() => {
    if (!localTarget) {
      throw new Error(
        `INV_E2E_DISPOSABLE=true requires INV_E2E_BASE_URL to resolve to ` +
          `localhost or 127.0.0.1 (got ${BASE_URL ?? '<unset>'}). Withdrawing a ` +
          'port writes an append-only change_log row and the retirement is ' +
          "permanent -- see this file's header and docs/E2E.md.",
      );
    }
  });

  test('needs the confirm dialog, and then the port is gone from the list', async ({ page }) => {
    // Find an asset with a Withdraw control. Walked rather than named: which
    // ports the seed leaves bare is a fixture detail, and pinning one would
    // fail for a reason unrelated to this feature.
    await page.goto('/assets', { waitUntil: 'networkidle' });
    const count = await page.locator('#asset-table a.id').count();
    expect(count, 'no assets on /assets').toBeGreaterThan(0);

    let row = null;
    for (let i = 0; i < count && !row; i += 1) {
      await page.goto('/assets', { waitUntil: 'networkidle' });
      await page.locator('#asset-table a.id').nth(i).click();
      await page.waitForLoadState('networkidle');
      const candidate = page
        .locator('tr', { has: page.locator('form[action*="/retire"] button:text-is("Withdraw")') })
        .filter({ has: page.locator('a[href*="/interfaces/"], td') })
        .first();
      if (await page.locator('form[action^="/interfaces/"][action$="/retire"]').count()) {
        row = page.locator('form[action^="/interfaces/"][action$="/retire"]').first();
      }
    }
    expect(row,
      'no asset in the estate offers Withdraw on any port. Either the seed ' +
      'leaves no bare port -- a fixture problem -- or the control has been lost ' +
      'from the ports table, which is the regression this checks.').not.toBeNull();

    const action = await row.getAttribute('action');
    const portName = await page
      .locator(`form[action="${action}"]`)
      .locator('xpath=ancestor::tr')
      .locator('td')
      .first()
      .innerText();

    // DISMISSING the confirm must NOT withdraw it.
    page.once('dialog', (d) => d.dismiss());
    await page.locator(`form[action="${action}"] button:text-is("Withdraw")`).click();
    await page.waitForTimeout(500);
    await expect(page.locator(`form[action="${action}"]`),
      'dismissing the confirm still withdrew the port -- hx-confirm is not ' +
      'gating the submit, so one stray click removes a port').toHaveCount(1);

    // ACCEPTING it does.
    page.once('dialog', (d) => d.accept());
    await page.locator(`form[action="${action}"] button:text-is("Withdraw")`).click();
    await page.waitForLoadState('networkidle');

    await expect(page.locator(`form[action="${action}"]`),
      `port ${portName.trim()} still offers Withdraw after being withdrawn`).toHaveCount(0);
  });
});
