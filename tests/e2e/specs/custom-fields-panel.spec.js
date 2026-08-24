// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Guards docs/custom-fields-design.md §4's read-only panel: an estate that
// has defined its own custom fields sees them rendered with real values,
// with descriptions where the field has one, and a link to the registry --
// on the actual asset detail page, not just in a template unit test that
// never renders inside the real layout.
import { test, expect } from '@playwright/test';
import { resolveAssetPath } from '../helpers/resolve.js';

const BASE_URL = process.env.INV_E2E_BASE_URL;
const describe = BASE_URL ? test.describe : test.describe.skip;

// hv-01 is one of this suite's stable named fixtures (see docs/E2E.md) and
// the demo estate's custom-field seed data gives it recorded values, not
// just blank slots -- see the "Defined by your organisation" assertion below,
// which fails loudly rather than silently if that ever stops being true.
const ASSET_NAME = 'hv-01';

describe('custom fields panel', () => {
  test('renders organisation-defined values with descriptions and a registry link', async ({ page }) => {
    const path = await resolveAssetPath(page, ASSET_NAME);
    await page.goto(path, { waitUntil: 'networkidle' });

    // web/templates/partials/custom_fields_show.html: this whole panel is
    // absent when no live field applies to the entity, so its presence is
    // itself the first assertion.
    const heading = page.getByRole('heading', { name: 'Defined by your organisation' });
    await expect(heading, `${ASSET_NAME} should have the custom fields panel`).toBeVisible();

    const panel = page.locator('.panel', { has: heading });
    await expect(panel).toBeVisible();

    // Each field is a <dt>/<dd> pair inside a <dl class="kv">.
    const rows = panel.locator('dl.kv dt');
    const rowCount = await rows.count();
    expect(rowCount, 'at least one custom field row').toBeGreaterThan(0);

    // At least one field renders a real value, not just "not recorded" --
    // this is the difference between "the panel exists" and "the panel shows
    // what this estate actually recorded", which is the whole point per
    // custom-fields-design.md's opening paragraph.
    const recordedValues = panel.locator('dl.kv dd:not(:has-text("not recorded"))');
    expect(
      await recordedValues.count(),
      `${ASSET_NAME} should show at least one recorded custom field value`,
    ).toBeGreaterThan(0);

    // At least one field has a description hint, proving the panel renders
    // more than a bare label.
    const descriptions = panel.locator('.field-hint');
    expect(
      await descriptions.count(),
      'at least one field should render its description',
    ).toBeGreaterThan(0);

    // The registry link, so "one of these looks wrong" has somewhere to go.
    await expect(panel.locator('a[href="/custom-fields"]')).toHaveCount(1);
  });
});
