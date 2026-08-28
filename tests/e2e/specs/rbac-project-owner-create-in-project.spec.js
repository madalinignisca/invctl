// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// WP-G1 Task 17, Step 4, flow 2: a project owner creates a new asset from
// their OWN project page -- the form posts to
// POST /projects/{id}/assets/new (WP-G1 Task 14, app.AssetCreateInProject),
// never to the plain POST /assets an Administrator uses -- and the generic
// "New asset" entry point on the asset list page is absent from their
// navigation. What a real browser catches that a handler-level Go test
// cannot: whether the HTMX swap this form relies on (hx-post,
// `HX-Redirect` on success -- internal/web/render.Redirect) actually lands
// the browser on the new asset, and whether the route table's shape
// (routes.go registers the two create routes to two different handlers,
// deliberately) is what a real page's forms point at.
//
// WRITES (the second test only) -- same shape as
// rbac-project-owner-edit-boundary.spec.js's own header: refuses the shared
// public demo, signs in through a FRESH browser context, and its one write
// creates a brand NEW asset rather than touching any existing named
// fixture, so it cannot break another spec's by-name lookup.
//
// NEEDS THE SAME FIXTURE as rbac-project-owner-edit-boundary.spec.js -- see
// that file's header and internal/seed/seed_e2e_fixture.go for why
// (INV_SEED_E2E_PROJECT_OWNER=true, never on a shared or public deployment).
//
// THE SECOND TEST IS A DELIBERATE, DECLARED FAILURE TODAY, for the identical
// reason as the sibling spec: auth.CanWrite(RoleProjectOwner) is still false
// ahead of WP-G1 Task 13, so middleware.RequireAdmin refuses the create POST
// with 403 before AssetCreateInProject ever runs. test.fail() asserts
// exactly that, so this test starts failing FOR Playwright (an unexpected
// pass) the day Task 13 flips CanWrite -- see the sibling spec's header for
// the full reasoning, not repeated twice.
import { test, expect } from '@playwright/test';
import { resolveProjectPath, resolveAssetPath } from '../helpers/resolve.js';
import { signInAsFreshUser } from '../helpers/login.js';

const BASE_URL = process.env.INV_E2E_BASE_URL;
const describe = BASE_URL ? test.describe : test.describe.skip;

const looksLikeSharedDemo = BASE_URL && /invctl\.madalin\.me/.test(BASE_URL);
const describeHere = looksLikeSharedDemo ? test.describe.skip : describe;

const OWNER_USERNAME = process.env.INV_E2E_PROJECT_OWNER_USERNAME || 'e2e-project-owner';
const OWNER_PASSWORD = process.env.INV_E2E_PROJECT_OWNER_PASSWORD || 'e2e-project-owner-password';

// "platform" is the project internal/seed/seed_e2e_fixture.go assigns this
// owner account to.
const PROJECT_CODE = 'platform';

describeHere(
  'project owner create-in-project (writes in the second test -- local ' +
    'instance only, needs INV_SEED_E2E_PROJECT_OWNER=true)',
  () => {
    test.beforeAll(() => {
      if (looksLikeSharedDemo) {
        throw new Error(
          'rbac-project-owner-create-in-project.spec.js writes to the estate and must never ' +
            `run against the shared public demo (INV_E2E_BASE_URL=${BASE_URL}).`,
        );
      }
    });

    test('has no generic "New asset" entry point, but does have their own project\'s create-in-project form', async ({
      browser,
    }) => {
      const { context, page } = await signInAsFreshUser(browser, BASE_URL, OWNER_USERNAME, OWNER_PASSWORD);
      try {
        // The generic entry point: web/templates/pages/asset_list.html gates
        // its embedded "Add an asset" form (action="/assets", the plain
        // Administrator-only create route) on `.CanWrite`, page-wide -- still
        // false for every project owner today. This passes today for that
        // reason; it is a page-wide control, never converted to
        // CanWriteEntity (WP-G1 Task 17 Step 1 only converts entity-specific
        // controls on the asset/service/circuit list, detail and row
        // templates -- this is neither).
        await page.goto('/assets', { waitUntil: 'networkidle' });
        await expect(
          page.locator('form[action="/assets"]'),
          'a project owner should see no generic "New asset" form on the asset list page',
        ).toHaveCount(0);

        // Their own project's page DOES offer a way in: the create-in-project
        // form (web/templates/partials/project_create_form.html), posted to
        // the project-scoped route this spec's second test exercises.
        const projectPath = await resolveProjectPath(page, PROJECT_CODE);
        await page.goto(projectPath, { waitUntil: 'networkidle' });
        await expect(
          page.locator('#project-create-asset-form'),
          `${PROJECT_CODE}'s own page should offer the create-in-project asset form`,
        ).toHaveCount(1);
        // The id lives on the wrapping <div> (project_create_form.html), not
        // the <form> element itself -- the actual "action" lives one level
        // down.
        await expect(page.locator('#project-create-asset-form form')).toHaveAttribute(
          'action',
          new RegExp(`^/projects/[^/]+/assets/new$`),
        );
      } finally {
        await context.close();
      }
    });

    test('creating an asset from their project page succeeds and links it there -- TRIPWIRE, see this file\'s header', async ({
      browser,
    }) => {
      test.fail();
      const { context, page } = await signInAsFreshUser(browser, BASE_URL, OWNER_USERNAME, OWNER_PASSWORD);
      try {
        const projectPath = await resolveProjectPath(page, PROJECT_CODE);
        await page.goto(projectPath, { waitUntil: 'networkidle' });

        const newName = `e2e-owner-created-${Date.now()}`;
        await page.locator('#project-create-asset-form input[name="name"]').fill(newName);
        // Kind is left at whatever option the <select> defaults to -- any
        // valid kind proves the same route, and pinning one would couple
        // this test to the vocabulary's current ordering for no reason.

        const [response] = await Promise.all([
          page.waitForResponse(
            (r) => r.request().method() === 'POST' && r.url().endsWith(`${projectPath}/assets/new`),
          ),
          page.locator('#project-create-asset-form button[type="submit"]').click(),
        ]);
        // THE PART THAT FAILS TODAY: RequireAdmin answers 403 before
        // AssetCreateInProject is ever reached. On success, this route
        // answers via internal/web/render.Redirect for an HTMX request:
        // 204 with an HX-Redirect header, not a 3xx Location -- see that
        // function and CLAUDE.md's HTTP conventions.
        expect(response.status(), 'creating an asset from the owner\'s own project page').toBe(204);
        const redirectTo = response.headers()['hx-redirect'];
        expect(redirectTo, 'the create response should redirect to the new asset').toMatch(/^\/assets\//);

        // Reached only once the assertions above actually pass (post-Task
        // 13): HTMX follows HX-Redirect itself, so the browser should have
        // already landed on the new asset.
        await page.waitForURL(/\/assets\//);
        await expect(page.locator('h1')).toHaveText(newName);

        // And it is genuinely linked to the project it was created from --
        // not merely created. Re-resolves by name (never a captured id),
        // the same rule every other lookup in this suite follows.
        const createdPath = await resolveAssetPath(page, newName);
        await page.goto(projectPath, { waitUntil: 'networkidle' });
        const createdID = createdPath.split('/').pop();
        await expect(
          page.locator(`a[href="${createdPath}"]`),
          `${PROJECT_CODE}'s Assets panel should list the newly created ${newName} (${createdID})`,
        ).toBeVisible();
      } finally {
        await context.close();
      }
    });
  },
);
