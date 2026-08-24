// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Logs in once, before any spec runs, and saves the session so every spec
// starts already authenticated -- matching the read-only nature of this
// suite (see docs/E2E.md: it must never submit a form against a shared
// instance) with a single write of the one thing every spec legitimately
// needs to change: its own session cookie.
//
// WHEN BASE_URL IS UNSET THIS RETURNS IMMEDIATELY. It must not attempt a
// network call, because "unset" is the suite's one legitimate opt-out
// (CLAUDE.md's testing policy) and every spec's own test.describe.skip
// already reports the run as skipped for that reason. If BASE_URL IS set and
// login does not work -- wrong credentials, unreachable host, anything --
// this throws and aborts the whole run before a single test executes. That
// is deliberate: a login failure here must surface as a failed run, never as
// tests that quietly skip themselves because the thing they needed never
// showed up. See CLAUDE.md: "the page didn't load so I skipped" is exactly
// the shape of bug this suite exists to not reproduce.
import { chromium } from '@playwright/test';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const here = path.dirname(fileURLToPath(import.meta.url));
const authStatePath = path.join(here, 'auth-state.json');

export default async function globalSetup() {
  const baseURL = process.env.INV_E2E_BASE_URL;
  if (!baseURL) {
    return;
  }

  // The demo's own credentials are public by design (see the Makefile's
  // INV_ADMIN_PASSWORD default and docs/DEMO.md), so defaulting to them here
  // is not a leak -- it is what lets `INV_E2E_BASE_URL=<url>` alone be enough
  // to run this against a fresh local `make dev`. A real deployment would
  // never have these values, and must set INV_E2E_USERNAME/PASSWORD.
  const username = process.env.INV_E2E_USERNAME || 'admin';
  const password = process.env.INV_E2E_PASSWORD || 'demo-password';

  const browser = await chromium.launch();
  try {
    const context = await browser.newContext({ baseURL });
    const page = await context.newPage();
    await page.goto('/login');
    await page.locator('#username').fill(username);
    await page.locator('#password').fill(password);
    await Promise.all([
      page.waitForLoadState('networkidle'),
      page.locator('button[type="submit"]').click(),
    ]);

    // web/templates/layouts/base.html renders `.rail-foot .id` only for a
    // signed-in user; the login page has no nav rail at all. Its absence
    // after submitting the form means the credentials were rejected -- this
    // must fail the run, not proceed and let every later page 404 or bounce
    // to /login in a way that looks like an unrelated failure.
    const signedIn = await page.locator('.rail-foot .id').count();
    if (signedIn === 0) {
      throw new Error(
        `login failed for user "${username}" at ${baseURL} -- check ` +
          'INV_E2E_USERNAME / INV_E2E_PASSWORD, or that this instance seeds ' +
          'the demo admin account. See docs/E2E.md.',
      );
    }

    await context.storageState({ path: authStatePath });
  } finally {
    await browser.close();
  }
}
