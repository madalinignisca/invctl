// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Guards the first thing anybody notices when this is broken: can a real
// person reach the app, sign in, and see confirmation they're looking at the
// build they think they are. global-setup.js has already logged in and saved
// the session before this file runs (see docs/E2E.md); this spec re-drives
// the login form itself from a clean session, because "the nav renders once
// authenticated" is a different claim from "authenticating actually works",
// and only the second one is what a person hits first.
import { test, expect } from '@playwright/test';

const BASE_URL = process.env.INV_E2E_BASE_URL;
// Unset BASE_URL is this suite's one legitimate skip (see docs/E2E.md and
// playwright.config.js) -- never a skip because a page or element was
// missing.
const describe = BASE_URL ? test.describe : test.describe.skip;

describe('login and version', () => {
  // A clean, unauthenticated context: the storageState from global-setup.js
  // would skip straight past the form this test exists to exercise.
  test.use({ storageState: { cookies: [], origins: [] } });

  test('admin can sign in and the nav shows the running build', async ({ page }) => {
    const username = process.env.INV_E2E_USERNAME || 'admin';
    const password = process.env.INV_E2E_PASSWORD || 'demo-password';

    const resp = await page.goto('/login');
    expect(resp?.status(), 'GET /login').toBeLessThan(400);

    await page.locator('#username').fill(username);
    await page.locator('#password').fill(password);
    await Promise.all([
      page.waitForLoadState('networkidle'),
      page.locator('button[type="submit"]').click(),
    ]);

    // Login failed silently is a 200 that re-renders the login form with a
    // flash error (web/templates/pages/login.html) -- assert we actually
    // left it, not just that the request didn't error.
    await expect(page).not.toHaveURL(/\/login/);

    // web/templates/layouts/base.html: the nav rail's `.id` only renders for
    // a signed-in `.User`, showing `.User.Display` -- a display name (e.g.
    // the seeded admin's is "Seeded administrator"), not the login username
    // that was typed, so this asserts presence and non-blankness rather than
    // an exact match against `username`. Found empirically: the first version
    // of this assertion compared against the literal username and failed
    // against the real demo, which is exactly the kind of thing this test is
    // supposed to catch about template/fixture mismatches -- proof it can
    // fail, kept here as the reason, not removed once it passed.
    const displayName = page.locator('.rail-foot .id');
    await expect(displayName).toBeVisible();
    expect((await displayName.textContent())?.trim(), 'signed-in display name').not.toBe('');

    // Same template: `.build` only renders `{{if .User}}`, so its presence
    // here is itself proof of the authenticated render path, not just a
    // string search over the whole page. The value is asserted as a shape
    // (dev build, or a `git describe` tag/commit form) rather than one exact
    // string -- CLAUDE.md's Makefile note is explicit that VERSION is derived
    // from git at build time and is never hand-typed, so a literal pin here
    // would break on every release this suite is supposed to survive.
    const build = page.locator('.build');
    await expect(build).toBeVisible();
    const version = (await build.textContent())?.trim() ?? '';
    expect(version, 'build version string').toMatch(/^(dev|v?\d+\.\d+\.\d+.*)$/);
  });
});
