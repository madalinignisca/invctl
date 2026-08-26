// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// WRITES. UNLIKE EVERY OTHER SPEC IN THIS SUITE, THIS ONE MUTATES THE
// ESTATE -- it creates an account, grants it Administrator, then demotes it
// back to Observer. docs/E2E.md is explicit that a mutating spec "must run
// against a disposable local instance only, and that constraint should be
// stated loudly at the top of that spec, not assumed" -- this is that spec,
// and this is that statement. NEVER point INV_E2E_BASE_URL at the shared
// public demo (docs/DEMO.md) while running this file: it would leave a real
// account on an estate other people are shown, exactly what the rest of this
// suite's read-only design exists to avoid.
//
// What this proves that no Go test can: that the router actually serves
// these six routes end to end, that the CSRF header injected on <body> is
// what makes a real browser's POST succeed at all, and that the write
// control on another page genuinely disappears from a SECOND, ALREADY-OPEN
// browser session the moment an administrator revokes it elsewhere -- not
// merely that the handler returns the right bytes when a test calls it
// directly (CLAUDE.md's evidence-gate note: a handler test with router
// params injected by hand never asks whether the route is reachable, or
// whether a session that was live a moment ago is still good for anything).
import { test, expect } from '@playwright/test';

const BASE_URL = process.env.INV_E2E_BASE_URL;
// Unset BASE_URL is this suite's one legitimate skip -- see docs/E2E.md.
const describe = BASE_URL ? test.describe : test.describe.skip;

// A crude but deliberate refusal to run this against anything that looks like
// the shared public demo. This is a mutating spec; the public instance is
// read-only-by-convention for every other spec in this suite specifically so
// it can be pointed at freely, and this file must not quietly break that.
const looksLikeSharedDemo = BASE_URL && /invctl\.madalin\.me/.test(BASE_URL);

// SKIPPED, not failed, when pointed at the shared demo -- and the distinction
// matters. An earlier version threw here, which was the right instinct (a
// mutating spec must never touch the public instance) with the wrong mechanism:
// it made a full-suite run against the demo permanently red, and that run is
// how a deployment is checked. A red suite that is red on purpose trains people
// to ignore a red suite.
//
// This is a legitimate skip by this project's own rule -- an explicitly
// declared precondition, exactly like an unset INV_E2E_BASE_URL -- and NOT the
// forbidden kind, which is a spec skipping because the thing under test looked
// missing. The reason is printed, so the skip is visible rather than silent.
const describeHere = looksLikeSharedDemo ? test.describe.skip : describe;

describeHere('user administration (mutates -- local instance only)', () => {
  test.beforeAll(() => {
    if (looksLikeSharedDemo) {
      // Unreachable while the suite is skipped; kept as the second line of
      // defence if somebody re-enables the describe without reading the top of
      // this file.
      throw new Error(
        'user-administration.spec.js writes to the estate and must never run against ' +
          `the shared public demo (INV_E2E_BASE_URL=${BASE_URL}).`,
      );
    }
  });

  test('demoting a colleague removes their write access from an already-open session', async ({
    browser,
  }, testInfo) => {
    const username = process.env.INV_E2E_USERNAME || 'admin';
    const password = process.env.INV_E2E_PASSWORD || 'demo-password';
    // Unique per run so repeat runs against the same throwaway instance
    // never collide on app_user's UNIQUE username.
    const colleagueUsername = `e2e-colleague-${testInfo.repeatEachIndex}-${Date.now()}`;
    const colleaguePassword = 'e2e-colleague-password-123';

    // Two things playwright.config.js's `use` block sets globally that this
    // spec must override on every manually created context:
    //   - baseURL: needed, so every relative page.goto() below resolves.
    //   - storageState: global-setup.js's already-authenticated admin
    //     session. Both this test's OWN sessions have to start from a truly
    //     clean cookie jar -- reusing that session here would make the
    //     "admin" and "colleague" browser contexts start pre-authenticated,
    //     the login form would never appear, and the whole premise of "an
    //     already-open session notices the demotion" would rest on a session
    //     this test never actually created.
    const baseURL = BASE_URL;
    const freshState = { cookies: [], origins: [] };

    // --- Admin session: sign in, create the colleague, promote them. ---
    const adminContext = await browser.newContext({ baseURL, storageState: freshState });
    const adminPage = await adminContext.newPage();
    await adminPage.goto('/login');
    await adminPage.locator('#username').fill(username);
    await adminPage.locator('#password').fill(password);
    await Promise.all([
      adminPage.waitForLoadState('networkidle'),
      adminPage.locator('button[type="submit"]').click(),
    ]);
    await expect(adminPage.locator('.rail-foot .id')).toBeVisible();

    await adminPage.goto('/users');

    // The signed-in "admin" account write access through INV_ADMIN_USERS,
    // not through app_user.role -- its own row is role=observer by design
    // (Authorizer.isAdministrator). Spec §8's last-Administrator guard counts
    // the ROLE column, so promoting the colleague to Administrator and then
    // demoting them back would make THEM the sole real Administrator and the
    // demotion would be correctly refused -- not a bug, but not what this
    // flow is testing either. Making "admin" a genuine second Administrator
    // first is what lets the colleague's own demotion succeed.
    const adminOwnRow = adminPage.locator('tr', { has: adminPage.getByText(username, { exact: true }) });
    await adminOwnRow.locator('select[name="role"]').selectOption('administrator');
    await Promise.all([
      adminPage.waitForLoadState('networkidle'),
      adminOwnRow.locator('button', { hasText: 'Save' }).click(),
    ]);

    await adminPage.locator('#u-username').fill(colleagueUsername);
    await adminPage.locator('#u-password').fill(colleaguePassword);
    await Promise.all([
      adminPage.waitForLoadState('networkidle'),
      adminPage.locator('#user-form button[type="submit"]').click(),
    ]);

    const colleagueRow = adminPage.locator('tr', { has: adminPage.getByText(colleagueUsername, { exact: true }) });
    await expect(colleagueRow).toBeVisible();

    // Promote to Administrator and save.
    await colleagueRow.locator('select[name="role"]').selectOption('administrator');
    await Promise.all([
      adminPage.waitForLoadState('networkidle'),
      colleagueRow.locator('button', { hasText: 'Save' }).click(),
    ]);

    // --- Colleague session: sign in as the newly promoted administrator. ---
    const colleagueContext = await browser.newContext({ baseURL, storageState: freshState });
    const colleaguePage = await colleagueContext.newPage();
    await colleaguePage.goto('/login');
    await colleaguePage.locator('#username').fill(colleagueUsername);
    await colleaguePage.locator('#password').fill(colleaguePassword);
    await Promise.all([
      colleaguePage.waitForLoadState('networkidle'),
      colleaguePage.locator('button[type="submit"]').click(),
    ]);
    await expect(colleaguePage.locator('.rail-foot .id')).toBeVisible();

    // /teams is a stable, pre-existing write-gated page (web/templates/pages/
    // team_list.html): the "Add a team" form only renders {{if .CanWrite}}.
    await colleaguePage.goto('/teams');
    await expect(colleaguePage.locator('form[action="/teams"]')).toBeVisible();
    await expect(colleaguePage.locator('.rail-foot')).toContainText('read / write');

    // --- Back to the admin session: demote the colleague to Observer. ---
    await adminPage.goto('/users');
    const rowAfterPromotion = adminPage.locator('tr', {
      has: adminPage.getByText(colleagueUsername, { exact: true }),
    });
    await rowAfterPromotion.locator('select[name="role"]').selectOption('observer');
    await Promise.all([
      adminPage.waitForLoadState('networkidle'),
      rowAfterPromotion.locator('button', { hasText: 'Save' }).click(),
    ]);

    // --- The colleague's own, already-open session reloads. ---
    await colleaguePage.reload();
    await expect(colleaguePage.locator('.rail-foot')).toContainText('read only');
    await expect(colleaguePage.locator('form[action="/teams"]')).toHaveCount(0);

    // A direct POST, bypassing the missing form entirely, is refused at the
    // router -- not merely hidden from view.
    const csrfToken = await colleaguePage
      .locator('body')
      .getAttribute('hx-headers')
      .then((raw) => JSON.parse(raw ?? '{}')['X-CSRF-Token']);
    const directPost = await colleaguePage.request.post('/teams', {
      form: { csrf_token: csrfToken ?? '', code: 'sneaky-e2e', name: 'Sneaky' },
      // nosurf (internal/web/middleware/middleware.go) requires one of
      // Sec-Fetch-Site, Origin or Referer on every unsafe request, the same
      // way a real browser's own form submission always carries one --
      // page.request is an API context and sends neither by default, which
      // reads as a malformed request and answers 400 before RequireAdmin
      // (the thing this assertion is actually about) is ever reached.
      headers: { Origin: baseURL },
    });
    expect(directPost.status(), 'a demoted account posting directly to /teams').toBe(403);

    await adminContext.close();
    await colleagueContext.close();
  });
});
