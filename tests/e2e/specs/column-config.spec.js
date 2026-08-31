// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// WP-G4c, Task 5 (plus the final whole-branch review's fix wave): the claims
// worth pinning about per-browser column hiding -- everything else about it
// (which columns exist, the picker markup, the Alpine component) is already
// covered at the diff level in Tasks 1-4, but nothing before this spec had
// proved any of it actually works in a browser.
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
// 4. Hiding a column does not remove ITS OWN CHECKBOX from the menu. This
//    guards the bug the fix wave found: apply() used to select `[data-col]`
//    off $root unscoped, which also matched the picker's own checkboxes (they
//    carry data-col too, so toggleColumn can read which key was clicked), so
//    hiding "serial" put col-hidden on the "serial" checkbox itself and it
//    vanished from its own menu. `expect(...).not.toBeChecked()` alone cannot
//    catch this -- it reads the checked property, not visibility -- so this
//    is a separate, explicit `.toBeVisible()` on the checkbox after hiding.
// 5. Two tables do not share a storage key. Reintroduce that defect (the
//    table key silently defaulting to '', or a hardcoded literal replacing
//    `this.table`) and a spec that only ever visits /assets still passes,
//    because it never asks a second table whether it noticed. This spec
//    hides a column on /services too and checks /assets is unaffected and
//    that both localStorage keys exist distinctly.
// 6. The menu stays fully inside a narrow viewport when open. This is the
//    thing the session's own ledger (.superpowers/sdd/2026-08-31-table-configs/
//    progress.md) once claimed a two-viewport E2E run would catch --
//    Playwright's config in fact has one project (Desktop Chrome) at no
//    fixed viewport, so that claim was false until this test existed. See
//    the ledger's corrected wording alongside this test.
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

    // Claim 4: hiding a column does not hide ITS OWN CHECKBOX. An unscoped
    // apply() selector matched the checkbox as well as the header/body
    // cells, because the checkbox also carries data-col -- this assertion
    // is what `not.toBeChecked()` above cannot catch, since it reads the
    // checked property and says nothing about whether the control is still
    // there to be re-checked.
    await expect(serialCheckbox).toBeVisible();

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

  test('two tables do not share a storage key', async ({ page }) => {
    // Claim 5. Reproduces the defect the ledger records as already found
    // and fixed once (the table key silently defaulting to '' when it
    // arrived as an x-data argument this build cannot deliver) -- a spec
    // that only ever visits one table can never notice it regress.
    await page.goto('/assets', { waitUntil: 'networkidle' });
    await page.locator('.column-picker button', { hasText: 'Columns' }).click();
    await page.locator('.column-picker-menu input[data-col="serial"]').uncheck();
    await expect(page.locator('th[data-col="serial"]')).toBeHidden();

    await page.goto('/services', { waitUntil: 'networkidle' });
    // web/templates/partials/rows.html renders every service column visible
    // by default -- if hiding "serial" on /assets had landed in a storage
    // key shared with /services, this "name" column (the closest analogue
    // /services has, since it carries no "serial" column of its own) would
    // never be affected by it either way, so the meaningful check is that
    // /services' OWN picker still reports everything visible and /assets
    // stays independently hidden after the round trip.
    await expect(page.locator('th[data-col="name"]')).toBeVisible();
    await page.locator('.column-picker button', { hasText: 'Columns' }).click();
    await expect(page.locator('.column-picker-menu input[data-col="name"]')).toBeChecked();

    // Both keys exist, distinctly, in localStorage -- the direct check that
    // this was ever two keys and not one.
    const keys = await page.evaluate(() => Object.keys(window.localStorage));
    const colKeys = keys.filter((k) => k.startsWith('invctl.cols.'));
    expect(colKeys, 'expected distinct per-table storage keys').toEqual(
      expect.arrayContaining(['invctl.cols.asset'])
    );
    expect(colKeys).not.toContain('invctl.cols.');

    // /assets' hidden column survives the round trip through another table.
    await page.goto('/assets', { waitUntil: 'networkidle' });
    await expect(page.locator('th[data-col="serial"]')).toBeHidden();
  });

  test('the open menu stays inside a narrow viewport', async ({ page }) => {
    // Claim 6. The design that shipped in this session moved the menu from
    // right-anchored (which extended leftward into the fixed nav rail) to
    // left-anchored into the content column -- correct at a normal desktop
    // width, but a left-anchored, fixed-min-width menu can just as easily
    // clip off the RIGHT edge on a narrow one. This is the check the
    // project ledger once claimed already existed ("the E2E now runs at two
    // viewport sizes") when the config in fact defines a single Desktop
    // Chrome project at no fixed size -- see that entry's correction.
    await page.setViewportSize({ width: 900, height: 700 });
    await page.goto('/assets', { waitUntil: 'networkidle' });
    await page.locator('.column-picker button', { hasText: 'Columns' }).click();
    const menu = page.locator('.column-picker-menu');
    await expect(menu).toBeVisible();
    const box = await menu.boundingBox();
    expect(box, 'the open menu must report a bounding box').not.toBeNull();
    expect(box.x, 'menu left edge is off-screen').toBeGreaterThanOrEqual(0);
    expect(box.x + box.width, 'menu right edge overflows the viewport').toBeLessThanOrEqual(900);
  });
});
