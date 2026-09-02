// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// WP-1.1: the two seams a cost write on an asset has to pass, for a project
// owner, both directions.
//
//   1. app_user.can_see_costs is a SESSION grant, separate from role
//      (docs/rbac-design.md §3) -- an Observer or a project owner sees no
//      money at all until an Administrator flips it, checked by
//      middleware.RequireCostVisibility BEFORE the handler runs (routes.go's
//      writeCost, composed AFTER RequireWrite: "writing a number you are not
//      allowed to read is a blind write"). Ungranted, the whole "What it
//      costs" panel is absent from asset_detail.html (cost_panel begins
//      `{{if .CanSeeCosts}}`), and a direct POST is refused with 403 before
//      AddAssetCost ever runs.
//   2. Granted, the object-level gate is what asset_detail.html actually
//      wires the panel's write half to (WP-1.1's own fix, landed this week --
//      see this file's own second test): `cost_panel`'s CanWrite comes from
//      `(.CanWriteEntity "asset" .Asset.ID)`, the same per-object question
//      internal/store's tx.log answers for AddAssetCost, not
//      Administrator-only. A control that never renders here for a project
//      owner who owns the asset and holds the grant would be a real
//      regression, not a test bug -- that is what this file's second test
//      exists to catch.
//
// WRITES, both tests -- same shape as rbac-project-owner-edit-boundary.spec.js's
// own header: refuses the shared public demo, signs in through a FRESH
// browser context for the project owner, and the admin half of each test
// (granting/revoking can_see_costs, adding/retiring a cost line) also runs
// through its own fresh admin context rather than the suite's shared session,
// for the identical isolation reason that spec gives.
//
// EVERY RUN FORCES THE can_see_costs BASELINE EXPLICITLY (test 1 revokes it,
// test 2 grants it) rather than trusting whatever a previous run left behind
// -- the fixture account persists across runs (seed.StageE2EProjectOwner is
// idempotent on the account row, but NOT on this mutable grant column), so an
// assertion that assumed "no grant by default" would be one repeat run away
// from silently testing nothing.
//
// NEEDS ITS OWN FIXTURE, gated deliberately off by default -- see
// rbac-project-owner-edit-boundary.spec.js's identical header and
// internal/seed/seed_e2e_fixture.go's doc comment. Start the target instance
// with INV_SEED_E2E_PROJECT_OWNER=true (in addition to INV_SEED=true) before
// pointing this spec at it -- never on a shared or public deployment.
import { test, expect } from '@playwright/test';
import { resolveAssetPath } from '../helpers/resolve.js';
import { signInAsFreshUser, signInAsAdmin, csrfTokenFrom } from '../helpers/login.js';

const BASE_URL = process.env.INV_E2E_BASE_URL;
const describe = BASE_URL ? test.describe : test.describe.skip;

const looksLikeSharedDemo = BASE_URL && /invctl\.madalin\.me/.test(BASE_URL);
const describeHere = looksLikeSharedDemo ? test.describe.skip : describe;

const OWNER_USERNAME = process.env.INV_E2E_PROJECT_OWNER_USERNAME || 'e2e-project-owner';
const OWNER_PASSWORD = process.env.INV_E2E_PROJECT_OWNER_PASSWORD;
if (!OWNER_PASSWORD) {
  throw new Error(
    'INV_E2E_PROJECT_OWNER_PASSWORD is not set. Seed the fixture with the ' +
    'same value the app was started with (INV_SEED_E2E_PROJECT_OWNER=true ' +
    'plus that password) -- see docs/E2E.md.');
}

// hv-01 is owned by project "platform" (internal/seed/seed_projects.go), the
// project internal/seed/seed_e2e_fixture.go assigns this fixture account to
// -- the same asset rbac-project-owner-edit-boundary.spec.js uses, and for
// the same reason: it needs no INV_SEED_COMPANY. This spec never touches its
// "name" or "vendor" fields (the edit-boundary spec's own free-text target),
// only its cost lines, so the two specs cannot collide.
const ASSET_IN = 'hv-01';

/**
 * Sets (or clears) an account's can_see_costs grant via a direct,
 * authenticated POST to UserSetCosts's own route -- see this file's own
 * comment on the first test for why this is not the checkbox itself: its
 * `onchange="this.form.requestSubmit()"` is an inline handler this app's own
 * CSP (`script-src 'self'`, no 'unsafe-inline') blocks in a real browser, so
 * clicking it does not actually submit anything. `ownerRow`'s own `id`
 * attribute (user_row.html: `id="user-row-{{.User.ID}}"`) is where the user
 * id comes from -- never a hardcoded one, the same by-name-then-by-DOM
 * resolution every other id in this suite uses.
 *
 * @param {import('@playwright/test').Page} adminPage a signed-in Administrator
 * @param {import('@playwright/test').Locator} ownerRow the account's own <tr>
 * @param {boolean} grant
 */
async function setCostVisibility(adminPage, ownerRow, grant) {
  const rowID = await ownerRow.getAttribute('id');
  const userID = rowID?.match(/^user-row-(.+)$/)?.[1];
  if (!userID) {
    throw new Error(`could not read a user id out of row id "${rowID}"`);
  }
  const csrfToken = await csrfTokenFrom(adminPage);
  const response = await adminPage.request.post(`/users/${userID}/costs`, {
    form: grant ? { csrf_token: csrfToken, can_see_costs: 'true' } : { csrf_token: csrfToken },
    headers: { Origin: BASE_URL },
  });
  // UserSetCosts -> respondUserRow -> render.Redirect: 303 for a non-HTMX
  // request (this one -- page.request carries no HX-Request header), which
  // Playwright's APIRequestContext follows itself, so the final status here
  // is /users' own 200, not the intermediate 303.
  expect(response.status(), `setting can_see_costs=${grant} for user ${userID}`).toBe(200);
}

describeHere(
  'project owner cost-line write, both seams (writes -- local instance only, ' +
    'needs INV_SEED_E2E_PROJECT_OWNER=true)',
  () => {
    test.beforeAll(() => {
      if (looksLikeSharedDemo) {
        throw new Error(
          'rbac-project-owner-cost-grant.spec.js writes to the estate and must never run ' +
            `against the shared public demo (INV_E2E_BASE_URL=${BASE_URL}).`,
        );
      }
    });

    test('without the can_see_costs grant, the cost panel is absent and a direct write is refused', async ({
      browser,
    }) => {
      // --- Force the baseline: revoke the grant, whatever a previous run
      // left behind, through a DIRECT POST rather than the checkbox itself.
      //
      // THIS IS A DELIBERATE WORKAROUND FOR A REAL, PRE-EXISTING BUG THIS
      // TEST FOUND, not a shortcut of convenience: /users' "sees costs"
      // checkbox (user_row.html) submits itself via
      // `onchange="this.form.requestSubmit()"`, an INLINE event handler --
      // and this app's own CSP is `script-src 'self'` with no
      // 'unsafe-inline' (internal/web/middleware/middleware.go, asserted by
      // internal/web/web_test.go). A real, CSP-enforcing browser blocks it
      // outright ("Executing inline event handler violates the following
      // Content Security Policy directive 'script-src 'self''... The action
      // has been blocked", observed directly while writing this spec): the
      // checkbox LOOKS like it toggles (the native DOM checkbox state still
      // flips on click, unaffected by CSP), but the request is never sent,
      // so the grant never actually changes -- a checkbox that lies about
      // whether it worked. Pre-dates WP-1.1 (git blame: fd08e2a, "Show
      // project assignments on the user roster", unchanged since main) and
      // is out of this task's scope to fix, but it is real and it is live,
      // and it is worth a line in this suite's own PR precisely because a
      // Go template test cannot see it -- no CSP is enforced there. Fixture
      // setup for the actual thing this file tests (the cost-LINE write
      // seam) still has to work, so it goes through the same route the
      // broken checkbox would have posted to, carrying real CSRF and a real
      // session, same as every other direct-POST fixture step in this
      // suite. ---
      const { context: adminContext, page: adminPage } = await signInAsAdmin(browser, BASE_URL);
      try {
        await adminPage.goto('/users', { waitUntil: 'networkidle' });
        const ownerRow = adminPage.locator('tr', { has: adminPage.getByText(OWNER_USERNAME, { exact: true }) });
        await expect(
          ownerRow,
          `the E2E project-owner fixture account (${OWNER_USERNAME}) should already exist on /users -- ` +
            'check the target instance was started with INV_SEED_E2E_PROJECT_OWNER=true (see docs/E2E.md)',
        ).toBeVisible();
        await setCostVisibility(adminPage, ownerRow, false);

        // --- The project owner's own session: the whole "What it costs"
        // panel (id="costs") is absent -- cost_panel.html starts
        // `{{if .CanSeeCosts}}`, so an ungranted session gets no panel at
        // all, not merely a panel with no write controls. ---
        const { context: ownerContext, page: ownerPage } = await signInAsFreshUser(
          browser, BASE_URL, OWNER_USERNAME, OWNER_PASSWORD,
        );
        try {
          const assetPath = await resolveAssetPath(ownerPage, ASSET_IN);
          await ownerPage.goto(assetPath, { waitUntil: 'networkidle' });
          await expect(
            ownerPage.locator('#costs'),
            `${ASSET_IN}'s cost panel must not render for ${OWNER_USERNAME} without the can_see_costs grant`,
          ).toHaveCount(0);

          // --- A direct POST, bypassing the missing panel entirely, is
          // refused at the router -- not merely hidden from view. This is
          // middleware.RequireCostVisibility's OWN refusal (403, "You may
          // not view or change costs."), not AddAssetCost's -- it must never
          // reach the object-level gate at all, per routes.go's own comment
          // on why RequireWrite runs first and RequireCostVisibility second. ---
          const csrfToken = await csrfTokenFrom(ownerPage);
          const assetID = assetPath.split('/').pop();
          const directPost = await ownerPage.request.post(`${assetPath}/costs`, {
            form: {
              csrf_token: csrfToken,
              kind: 'hardware',
              period: 'once',
              amount: '10.00',
            },
            headers: { Origin: BASE_URL },
          });
          expect(
            directPost.status(),
            `${OWNER_USERNAME} posting a cost line to ${assetID} without the can_see_costs grant`,
          ).toBe(403);
          expect(await directPost.text()).toContain('You may not view or change costs.');
        } finally {
          await ownerContext.close();
        }
      } finally {
        await adminContext.close();
      }
    });

    test('granted, a project owner adds and reads back a cost line on their own project\'s asset', async ({
      browser,
    }) => {
      // --- Grant it, from a fresh admin session -- same direct-POST
      // workaround as the first test's revoke, for the same broken-checkbox
      // reason documented there in full. ---
      const { context: adminContext, page: adminPage } = await signInAsAdmin(browser, BASE_URL);
      try {
        await adminPage.goto('/users', { waitUntil: 'networkidle' });
        const ownerRow = adminPage.locator('tr', { has: adminPage.getByText(OWNER_USERNAME, { exact: true }) });
        await expect(ownerRow).toBeVisible();
        await setCostVisibility(adminPage, ownerRow, true);
      } finally {
        await adminContext.close();
      }

      const { context: ownerContext, page: ownerPage } = await signInAsFreshUser(
        browser, BASE_URL, OWNER_USERNAME, OWNER_PASSWORD,
      );
      try {
        const assetPath = await resolveAssetPath(ownerPage, ASSET_IN);
        await ownerPage.goto(assetPath, { waitUntil: 'networkidle' });

        // --- The panel renders, AND its write half does: CanWriteEntity
        // gates the "Add cost" form, not IsAdmin (this week's fix -- see this
        // file's own header). A rendered panel with no form would still be a
        // regression this test has to catch, so both are asserted. ---
        await expect(ownerPage.locator('#costs')).toBeVisible();
        const addCostForm = ownerPage.locator('#costs form[action$="/costs"]');
        await expect(
          addCostForm,
          `${ASSET_IN}'s "Add cost" form should render for ${OWNER_USERNAME}: they own the ` +
            'asset and hold the can_see_costs grant',
        ).toBeVisible();

        // A note unique to this run, so the read-back assertion below cannot
        // be satisfied by some other cost line already on this asset.
        const note = `e2e-owner-cost-${Date.now()}`;
        await addCostForm.locator('input[name="amount"]').fill('420.00');
        await addCostForm.locator('input[name="note"]').fill(note);

        const [response] = await Promise.all([
          ownerPage.waitForResponse((r) => r.request().method() === 'POST' && r.url().endsWith('/costs')),
          addCostForm.locator('button[type="submit"]').click(),
        ]);
        // afterCostWrite -> render.Redirect: 204 + HX-Redirect for an HTMX
        // request, not a 3xx Location -- CLAUDE.md's HTTP conventions, same
        // as every other successful write asserted this way in this suite.
        expect(response.status(), `adding a cost line to ${ASSET_IN} as its owning project's owner`).toBe(204);
        expect(response.headers()['hx-redirect']).toBe(assetPath);

        // Read back through a fresh page load, not trusted from the HTMX
        // swap alone.
        await ownerPage.goto(assetPath, { waitUntil: 'networkidle' });
        const costRow = ownerPage.locator('#costs tr', { hasText: note });
        await expect(costRow).toBeVisible();

        // --- Clean up: retire the line so repeated runs against the same
        // long-lived local instance keep accumulating one soft-retired
        // row rather than an ever-growing set of live ones on hv-01's
        // totals. The row itself persists (soft-delete only, CLAUDE.md) --
        // the same permanent-artefact trade-off
        // rbac-project-owner-edit-boundary.spec.js's own writes accept. ---
        await Promise.all([
          ownerPage.waitForResponse((r) => r.request().method() === 'POST' && r.url().includes('/retire')),
          costRow.locator('button', { hasText: 'Remove' }).click(),
        ]);
      } finally {
        await ownerContext.close();
      }
    });
  },
);
