// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Signs a FRESH browser context in as a given user -- the same shape
// user-administration.spec.js's own admin/colleague sign-ins already use,
// pulled out here because the two RBAC role-aware-UI specs (WP-G1 Task 17)
// each need a second, non-admin session the same way.
//
// A FRESH CONTEXT, NOT THE ONE playwright.config.js's `use` block already
// authenticates: that storageState is global-setup.js's admin session, and
// reusing it here would sign this "project owner" in as admin instead of
// exercising the login form at all. See user-administration.spec.js's own
// comment on the same point.
import { expect } from '@playwright/test';

/**
 * @param {import('@playwright/test').Browser} browser
 * @param {string} baseURL
 * @param {string} username
 * @param {string} password
 * @returns {Promise<{context: import('@playwright/test').BrowserContext, page: import('@playwright/test').Page}>}
 */
export async function signInAsFreshUser(browser, baseURL, username, password) {
  const freshState = { cookies: [], origins: [] };
  const context = await browser.newContext({ baseURL, storageState: freshState });
  const page = await context.newPage();
  await page.goto('/login');
  await page.locator('#username').fill(username);
  await page.locator('#password').fill(password);
  await Promise.all([
    page.waitForLoadState('networkidle'),
    page.locator('button[type="submit"]').click(),
  ]);
  // web/templates/layouts/base.html renders `.rail-foot .id` only for a
  // signed-in user -- its absence means the credentials were rejected, and
  // that has to fail loudly here rather than let every following step 404 or
  // bounce to /login in a way that looks unrelated. See global-setup.js's
  // identical check for the admin session.
  await expect(
    page.locator('.rail-foot .id'),
    `sign-in as "${username}" did not reach a signed-in page -- check the fixture ` +
      'credentials (INV_E2E_PROJECT_OWNER_USERNAME/PASSWORD) and that the target ' +
      'instance was started with INV_SEED_E2E_PROJECT_OWNER=true (see docs/E2E.md).',
  ).toBeVisible();
  return { context, page };
}

/**
 * Reads the CSRF token every non-GET request needs from `<body hx-headers>`
 * (internal/web/middleware -- justinas/nosurf), the same way
 * user-administration.spec.js's direct POST already does. `page.request` is
 * an API context: it never runs the page's own HTMX/hx-headers wiring, so a
 * direct write through it has to carry the token by hand.
 *
 * @param {import('@playwright/test').Page} page
 * @returns {Promise<string>}
 */
export async function csrfTokenFrom(page) {
  const raw = await page.locator('body').getAttribute('hx-headers');
  return JSON.parse(raw ?? '{}')['X-CSRF-Token'] ?? '';
}
