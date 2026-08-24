// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// THE CROWN JEWEL OF THIS SUITE. Guards the fix in
// web/templates/partials/custom_fields_form.html for the Critical where a
// retired select option was silently destroyed: with no matching <option>
// for the value an entity actually held, the browser fell back to its own
// blank "not set" choice, and the NEXT unrelated save on that form quietly
// wiped the stored value. Nothing about that bug shows up in a screenshot or
// a unit test against the template package alone -- it only exists in what a
// real browser does with a <select> whose value has no matching <option>,
// which is exactly why this is a browser test and not a Go one.
//
// Both directions, on real assets, asserted on the live DOM:
//   1. An asset that HOLDS a retired value: the <option> is present, is
//      marked "(retired)", and carries `selected`.
//   2. An asset that does NOT hold it: that same field's <select> exists,
//      but the option for the retired value is entirely ABSENT -- not just
//      unselected. Its presence there would be the bug.
//
// THIS TEST MUST NEVER SUBMIT THE FORM. It may run against the public demo,
// which is soft-delete only (CLAUDE.md: "Soft delete only, for entities."),
// and any write here leaves a permanent artefact on a shared instance. It
// only navigates to `?edit=<id>#edit` and reads the rendered DOM.
import { test, expect } from '@playwright/test';
import { resolveAssetPath } from '../helpers/resolve.js';

const BASE_URL = process.env.INV_E2E_BASE_URL;
const describe = BASE_URL ? test.describe : test.describe.skip;

// Stable named fixtures from the demo estate seed (docs/E2E.md). The field
// that carries a retired option is discovered from the DOM rather than
// hardcoded by its custom-field UUID or label, so a reseed that regenerates
// field IDs -- or renames the field -- does not make this test meaningless;
// only losing the retired-option fixture entirely does, and that fails with
// a message saying so rather than skipping.
const HOLDER = 'fw-dev-1';
const NON_HOLDER = 'hv-01';

describe('retired custom-field option invariant', () => {
  test('a retired option survives on the asset that holds it, and stays absent where it is not held', async ({ page }) => {
    const holderPath = await resolveAssetPath(page, HOLDER);
    await page.goto(`${holderPath}?edit=${holderPath.split('/').pop()}#edit`, {
      waitUntil: 'networkidle',
    });

    const holderSelects = page.locator('select[name^="cf_"]');
    const holderCount = await holderSelects.count();
    expect(holderCount, `${HOLDER} should render at least one custom-field select`).toBeGreaterThan(0);

    // Find the select/option pair actually marked retired on the holder.
    let fieldName;
    let retiredValue;
    let retiredLabel;
    for (let i = 0; i < holderCount; i++) {
      const sel = holderSelects.nth(i);
      const options = sel.locator('option');
      const optionCount = await options.count();
      for (let j = 0; j < optionCount; j++) {
        const opt = options.nth(j);
        const text = (await opt.textContent())?.trim() ?? '';
        if (/\(retired\)\s*$/.test(text)) {
          fieldName = await sel.getAttribute('name');
          retiredValue = await opt.getAttribute('value');
          retiredLabel = text;
          break;
        }
      }
      if (fieldName) break;
    }

    if (!fieldName) {
      throw new Error(
        `no select on ${HOLDER} has an option marked "(retired)" -- this ` +
          'suite expects the demo estate to have a retired custom-field ' +
          'option that this asset still holds. See docs/E2E.md.',
      );
    }

    // Direction 1: present, labelled retired, and selected -- this is what
    // "the browser keeps displaying the value this entity holds" means in
    // the DOM.
    const holderSelect = page.locator(`select[name="${fieldName}"]`);
    const holderRetiredOption = holderSelect.locator('option', { hasText: /\(retired\)\s*$/ });
    await expect(holderRetiredOption, `${HOLDER}: retired option should be present`).toHaveCount(1);
    await expect(holderRetiredOption).toHaveText(retiredLabel);
    await expect(holderSelect).toHaveValue(retiredValue ?? '');

    // Direction 2: same field, a different asset that never held that value.
    // The option must be entirely absent -- present-but-unselected would
    // still be a regression of a different kind (a value nobody entered
    // becoming choosable), and this is the one place the original Critical
    // actually lived: an absent option with no matching <select> value is
    // exactly the state a browser falls back from on submit.
    const nonHolderPath = await resolveAssetPath(page, NON_HOLDER);
    await page.goto(`${nonHolderPath}?edit=${nonHolderPath.split('/').pop()}#edit`, {
      waitUntil: 'networkidle',
    });
    const nonHolderSelect = page.locator(`select[name="${fieldName}"]`);
    await expect(nonHolderSelect, `${NON_HOLDER} should render the same field`).toHaveCount(1);
    const nonHolderRetiredOption = nonHolderSelect.locator('option', { hasText: /\(retired\)\s*$/ });
    await expect(
      nonHolderRetiredOption,
      `${NON_HOLDER} should NOT render the retired option it never held`,
    ).toHaveCount(0);
  });
});
