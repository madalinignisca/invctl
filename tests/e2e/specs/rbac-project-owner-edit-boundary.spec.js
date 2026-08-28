// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// WP-G1 Task 17, Step 4, flow 1: a project owner signs in, sees the edit
// control on an asset their project owns and none on one it does not, and a
// direct POST to the outside asset is refused server-side -- not merely
// hidden. What a real browser catches that internal/web/rbac_ui_test.go's
// handler-level render assertions cannot: the permit surviving an actual
// signed-in session, cookies and all, through two separate page loads and a
// raw HTTP request.
//
// WRITES (the second test only) -- see docs/E2E.md's rule for a mutating
// spec, and user-administration.spec.js for the established shape: refuse
// the shared public demo outright, sign in through a FRESH browser context
// rather than the suite's shared admin session, and never touch the "name"
// field on a NAMED fixture (hv-01, sw-oob-1) other specs in this suite
// resolve by name -- every edit here targets "vendor" instead, which is
// free text nobody else's assertion depends on.
//
// NEEDS ITS OWN FIXTURE, gated deliberately off by default: see
// internal/seed/seed_e2e_fixture.go's doc comment for why there is no HTTP
// route by which this suite could create a project-owner assignment itself.
// Start the target instance with INV_SEED_E2E_PROJECT_OWNER=true (in
// addition to INV_SEED=true) before pointing this spec at it -- never on a
// shared or public deployment.
//
// THE SECOND TEST IS A DELIBERATE, DECLARED FAILURE TODAY, AND MUST STAY
// THAT WAY UNTIL WP-G1 TASK 13 LANDS. auth.CanWrite(RoleProjectOwner) is
// still false (Task 13 flips it, deliberately last, once every entity-scope
// check has been proven ahead of it going live) -- so middleware.RequireAdmin
// refuses the save POST with 403 before AssetUpdate ever runs, regardless of
// which asset or which fields. test.fail() asserts exactly that: this test
// currently fails, and Playwright reports it as an ERROR the day it starts
// passing -- which is the day CanWrite(project owner) goes live. That is the
// point: it forces whoever lands Task 13 to come back here and remove the
// test.fail() call, rather than this suite quietly reporting the same green
// or red across the flip and nobody noticing either way.
import { test, expect } from '@playwright/test';
import { resolveAssetPath } from '../helpers/resolve.js';
import { signInAsFreshUser, csrfTokenFrom } from '../helpers/login.js';

const BASE_URL = process.env.INV_E2E_BASE_URL;
const describe = BASE_URL ? test.describe : test.describe.skip;

// Same crude, deliberate refusal user-administration.spec.js uses: this spec
// writes, so it must never run against the shared public demo.
const looksLikeSharedDemo = BASE_URL && /invctl\.madalin\.me/.test(BASE_URL);
const describeHere = looksLikeSharedDemo ? test.describe.skip : describe;

const OWNER_USERNAME = process.env.INV_E2E_PROJECT_OWNER_USERNAME || 'e2e-project-owner';
// No fallback, deliberately: the seeder refuses to create this account
// without an operator-chosen password, so a spec that quietly substituted a
// published default would only ever be testing a login that cannot exist.
// See internal/seed/seed_e2e_fixture.go.
const OWNER_PASSWORD = process.env.INV_E2E_PROJECT_OWNER_PASSWORD;
if (!OWNER_PASSWORD) {
  throw new Error(
    'INV_E2E_PROJECT_OWNER_PASSWORD is not set. Seed the fixture with the ' +
    'same value the app was started with (INV_SEED_E2E_PROJECT_OWNER=true ' +
    'plus that password) -- see docs/E2E.md.');
}

// hv-01 is owned by project "platform" (internal/seed/seed_projects.go), the
// project internal/seed/seed_e2e_fixture.go assigns this fixture account to.
// sw-oob-1 is a BASE-fixture asset linked to no project at all -- see that
// same seed file's projectSpec list. Both are also named, stable fixtures
// other specs in this suite resolve by name (docs/E2E.md); this spec never
// writes their "name" field, only "vendor", so it cannot break them.
const ASSET_IN = 'hv-01';
const ASSET_OUT = 'sw-oob-1';

describeHere(
  'project owner edit boundary (writes in the second test -- local instance ' +
    'only, needs INV_SEED_E2E_PROJECT_OWNER=true)',
  () => {
    test.beforeAll(() => {
      if (looksLikeSharedDemo) {
        throw new Error(
          'rbac-project-owner-edit-boundary.spec.js writes to the estate and must never run ' +
            `against the shared public demo (INV_E2E_BASE_URL=${BASE_URL}).`,
        );
      }
    });

    test('sees the edit link on their own project\'s asset, none on one outside it, and a direct write to the outside one is refused', async ({
      browser,
    }) => {
      const { context, page } = await signInAsFreshUser(browser, BASE_URL, OWNER_USERNAME, OWNER_PASSWORD);
      try {
        // --- The owned asset: the Edit link IS there. ---
        // This passes TODAY, unlike the save itself below -- asset_detail.html
        // gates this link on Base.CanWriteEntity("asset", id), which asks the
        // permit's Covers directly and does NOT depend on the still-false
        // CanWrite (see internal/web/handlers/app.go's CanWriteEntity doc
        // comment). It also keeps passing once Task 13 lands: object-level
        // scope is the boundary either side of that flip, not a consequence
        // of it.
        const inPath = await resolveAssetPath(page, ASSET_IN);
        const inID = inPath.split('/').pop();
        await page.goto(inPath, { waitUntil: 'networkidle' });
        await expect(
          page.locator(`a[href="${inPath}?edit=${inID}#edit"]`),
          `${ASSET_IN} is owned by this owner's project; its Edit link should render`,
        ).toBeVisible();
        // The form itself renders too, not merely the link that opens it.
        await page.goto(`${inPath}?edit=${inID}#edit`, { waitUntil: 'networkidle' });
        await expect(page.locator('#edit form'), `${ASSET_IN} should render its edit form`).toHaveCount(1);

        // --- The outside asset: no Edit link, and the form itself refuses
        // to render even if the query string is opened directly (a hidden
        // link is a courtesy; this is the same claim against the form). ---
        const outPath = await resolveAssetPath(page, ASSET_OUT);
        const outID = outPath.split('/').pop();
        await page.goto(outPath, { waitUntil: 'networkidle' });
        await expect(
          page.locator(`a[href="${outPath}?edit=${outID}#edit"]`),
          `${ASSET_OUT} is outside this owner's project; no Edit link should render`,
        ).toHaveCount(0);
        const currentName = (await page.locator('h1').textContent())?.trim();
        const currentKind = (await page.locator('.eyebrow').first().textContent())?.trim();
        await page.goto(`${outPath}?edit=${outID}#edit`, { waitUntil: 'networkidle' });
        await expect(
          page.locator('#edit form'),
          `${ASSET_OUT}'s edit form must not render even when opened directly by query string`,
        ).toHaveCount(0);

        // --- The server refusal itself: a direct POST, bypassing the
        // missing form entirely. THIS is the boundary CLAUDE.md's own rule
        // names -- "hiding a control is not the enforcement" -- and it is
        // asserted independently of whatever the UI drew above. Minimal,
        // deliberately: with the entity-scope check unreachable today (see
        // this file's header), nothing past RequireAdmin's role check is
        // exercised, so this payload does not carry a real row_version --
        // whoever revisits this alongside Task 13 will need to fetch one for
        // the assertion to mean anything past the role gate.
        const csrfToken = await csrfTokenFrom(page);
        const directPost = await page.request.post(outPath, {
          form: {
            csrf_token: csrfToken,
            name: currentName ?? '',
            kind: currentKind ?? '',
            vendor: 'sneaky-e2e-vendor',
          },
          // nosurf requires one of Sec-Fetch-Site, Origin or Referer on every
          // unsafe request; page.request sends none of those by default (see
          // user-administration.spec.js's identical note).
          headers: { Origin: BASE_URL },
        });
        expect(
          directPost.status(),
          `a project owner posting directly to ${outPath} (outside their project)`,
        ).toBe(403);

        // And nothing changed -- not merely "some response came back".
        await page.goto(outPath, { waitUntil: 'networkidle' });
        await expect(page.locator('h1')).toHaveText(currentName ?? '');
      } finally {
        await context.close();
      }
    });

    test('saving their own asset\'s edit form succeeds -- TRIPWIRE, see this file\'s header', async ({
      browser,
    }) => {
      test.fail();
      const { context, page } = await signInAsFreshUser(browser, BASE_URL, OWNER_USERNAME, OWNER_PASSWORD);
      try {
        const inPath = await resolveAssetPath(page, ASSET_IN);
        const inID = inPath.split('/').pop();
        await page.goto(`${inPath}?edit=${inID}#edit`, { waitUntil: 'networkidle' });

        const vendorInput = page.locator('#edit input[name="vendor"]');
        await expect(vendorInput).toBeVisible();
        const newVendor = `e2e-owner-edit-${Date.now()}`;
        await vendorInput.fill(newVendor);

        const [response] = await Promise.all([
          page.waitForResponse((r) => r.request().method() === 'POST' && r.url().includes(`/assets/${inID}`)),
          page.locator('#edit form button[type="submit"]').click(),
        ]);
        // THE PART THAT FAILS TODAY: RequireAdmin answers 403 before
        // AssetUpdate is ever reached, for any project owner, on any asset.
        expect(response.status(), `saving ${ASSET_IN} as its owning project's owner`).toBe(200);

        // Reached only once the line above actually passes (post-Task 13):
        // the save stuck, read back through a fresh page load rather than
        // trusted from the HTMX swap alone.
        await page.goto(`${inPath}?edit=${inID}#edit`, { waitUntil: 'networkidle' });
        await expect(page.locator('#edit input[name="vendor"]')).toHaveValue(newVendor);
      } finally {
        await context.close();
      }
    });
  },
);
