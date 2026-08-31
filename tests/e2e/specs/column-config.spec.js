// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// WP-G4c, Task 5: the three claims worth pinning about per-browser column
// hiding on the asset list -- everything else about it (which columns exist,
// the picker markup, the Alpine component) is already covered at the diff
// level in Tasks 1-4, but nothing before this spec had proved any of it
// actually works in a browser.
//
// 1. Hiding a column through the Columns menu actually hides the header
//    cell -- the CSS/Alpine wiring, not just the localStorage write.
// 2. It survives a reload. This is the whole point of persisting to
//    localStorage rather than leaving it as in-memory Alpine state: a page
//    reload re-inits the columnPicker component from storage
//    (web/static/app.js's init() -> read() -> apply()), and only a real
//    browser reload exercises that path.
// 3. The CSV export still contains the hidden column. This is the claim
//    most likely to be "fixed" into a bug later by someone who assumes an
//    export should mirror the screen -- it must not, because the export is
//    an importable round-trip artifact with a fixed header, independent of
//    any one browser's local display preference (docs/table-configs-design.md
//    §4: "CSV export is untouched").
//
// Uses the shared signed-in session global-setup.js already saved (this is a
// read-only navigation plus a client-side localStorage write, not a form
// submission against the estate -- see docs/E2E.md's "this suite never
// writes"), the same way custom-fields-panel.spec.js and smoke.spec.js do.
import { test, expect } from '@playwright/test';

const BASE_URL = process.env.INV_E2E_BASE_URL;
const describe = BASE_URL ? test.describe : test.describe.skip;

describe('column configuration (WP-G4c)', () => {
  test('a hidden column stays hidden across a reload and never leaves the CSV', async ({ page }) => {
    await page.goto('/assets', { waitUntil: 'networkidle' });

    // web/templates/partials/asset_table.html renders every column header
    // for a write-capable reader by default -- nothing hidden until this
    // test hides it.
    const serialHeader = page.locator('th[data-col="serial"]');
    await expect(serialHeader).toBeVisible();

    // Open the picker and uncheck "serial" -- web/templates/partials/
    // column_picker.html's menu is x-show="open", closed until the button
    // is clicked.
    await page.locator('.column-picker button', { hasText: 'Columns' }).click();
    const serialCheckbox = page.locator('.column-picker-menu input[data-col="serial"]');
    await expect(serialCheckbox).toBeChecked();
    await serialCheckbox.uncheck();

    // Claim 1: hiding a column hides it.
    await expect(serialHeader).toBeHidden();

    // Claim 2: it survives a reload -- the assertion that proves the
    // localStorage write works, not merely that toggling a class in the DOM
    // does.
    await page.reload({ waitUntil: 'networkidle' });
    await expect(page.locator('th[data-col="serial"]')).toBeHidden();
    // The picker's own checkbox state is re-derived from storage too
    // (apply() re-syncs every input[data-col] on init), so the control
    // itself must not silently disagree with what it hid.
    await page.locator('.column-picker button', { hasText: 'Columns' }).click();
    await expect(page.locator('.column-picker-menu input[data-col="serial"]')).not.toBeChecked();

    // Claim 3: the CSV export is a fixed, importable header -- hiding a
    // column on screen must never remove it from the export.
    const csv = await page.request.get('/assets?format=csv');
    expect(csv.ok(), 'GET /assets?format=csv').toBeTruthy();
    const body = await csv.text();
    expect(body.toLowerCase(), 'the CSV export should still contain the "serial" column').toContain('serial');
  });
});
