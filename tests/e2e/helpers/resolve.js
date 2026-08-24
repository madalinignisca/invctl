// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Resolves an asset's URL path by its NAME, never a hardcoded ID.
//
// A fixed UUID is a fixture of one specific database: the demo gets reseeded,
// a local `make seed` generates fresh UUIDv7s every run, and a test pinned to
// one particular ID would pass against the instance it was written on and
// fail everywhere else for a reason that has nothing to do with a real bug.
// The asset list page's own filter (web/templates/pages/asset_list.html,
// `?q=`) already searches by name, so this walks the same path a person
// would: filter the list, then follow the row's own link.
//
// Throws with a message naming the missing fixture rather than letting a
// null locator produce a confusing timeout three lines later -- this suite's
// fixtures are the named assets in docs/E2E.md ("expects the demo estate"),
// and a missing one is exactly the failure that message exists to explain.

/**
 * @param {import('@playwright/test').Page} page
 * @param {string} name
 * @returns {Promise<string>} the asset's detail path, e.g. "/assets/<id>"
 */
export async function resolveAssetPath(page, name) {
  await page.goto(`/assets?q=${encodeURIComponent(name)}`, {
    waitUntil: 'networkidle',
  });
  // Exact match, not substring -- "hv-01" must not resolve to a row for
  // "hv-010" if the estate ever grows one.
  const link = page.locator('#asset-table a.id', { hasText: new RegExp(`^${escapeRegExp(name)}$`) }).first();
  const count = await link.count();
  if (count === 0) {
    throw new Error(
      `no asset named "${name}" was found via /assets?q=${name} -- this ` +
        'suite expects the demo estate. See docs/E2E.md ' +
        '(INV_SEED=true INV_SEED_COMPANY=true).',
    );
  }
  const href = await link.getAttribute('href');
  if (!href) {
    throw new Error(`asset row for "${name}" has no href`);
  }
  return href;
}

function escapeRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}
