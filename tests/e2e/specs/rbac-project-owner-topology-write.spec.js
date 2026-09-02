// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// WP-1.1: the two-ended object-level scope check on "link" (a patch cable)
// and "dependency" (a declared service edge) -- internal/store/network.go's
// authorizeLinkSubjects and internal/store/deps.go's
// authorizeDependencySubjects, both landed this branch (438c848, and its
// dependency twin) -- proven through a REAL signed-in project-owner session
// reaching the REAL router, not a handler test with router params injected
// by hand.
//
// THE UI DOES NOT OFFER EITHER CONTROL TO A PROJECT OWNER, ON PURPOSE, AND
// THAT IS NOT A BUG THIS SPEC IS WORKING AROUND. Both "link" and "dependency"
// are still on WP-1.0 item 1's DEFER list -- web/templates/pages/
// asset_detail.html's "Patch a cable" panel is gated `{{if .IsAdmin}}`
// (commit a3f0d61, "gate estate-config and topology controls to .IsAdmin,
// not .CanWrite") and service_detail.html's "Add a dependency" disclosure the
// same way, each with its own comment citing the DEFER list. So there is no
// button for this suite to click: every write below goes through a direct,
// authenticated POST from a real project-owner session -- cookies, CSRF
// token and all -- the same shape rbac-project-owner-edit-boundary.spec.js's
// own negative case already uses to prove a hidden control's absence is not
// the enforcement. Here it proves the opposite direction: that the ROUTE
// itself (write("POST /links", ...) / write("POST /services/{id}/dependencies",
// ...), both plain RequireWrite, not RequireAdministrator) is genuinely
// reachable by a project owner's session and that the object-level gate
// behind it is what actually decides the outcome -- exactly the gap an
// earlier WP-1.1 review flagged: "no test proves a project owner can
// actually reach the dependency write route through a real session, only
// handler-level tests exist."
//
// EACH END, INDEPENDENTLY. authorizeLinkSubjects and
// authorizeDependencySubjects each check TWO subjects in sequence and
// refuse if EITHER is out of scope. A case with both ends foreign proves
// nothing -- the FIRST check alone would already refuse it, and the second
// check's own correctness never gets exercised. This file's two cabling
// negative tests swap which end is foreign specifically so each check has to
// carry weight on its own; that exact gap was a blocking finding twice on
// this branch.
//
// WRITES -- the cabling success test creates (and then retires) a real
// "link" row, and the dependency test creates (and the admin half retires) a
// real "dependency" row; both are soft-delete-only entities (CLAUDE.md), so
// even with cleanup each leaves a permanently retired row behind, the same
// accepted trade-off rbac-project-owner-edit-boundary.spec.js's own writes
// make. Refuses the shared public demo the same way that spec does, signs
// the project owner in through a FRESH browser context, and touches no
// "name" field of any named fixture another spec resolves by name.
//
// NEEDS ITS OWN FIXTURE, gated deliberately off by default -- see
// rbac-project-owner-edit-boundary.spec.js's identical header and
// internal/seed/seed_e2e_fixture.go's doc comment. Start the target instance
// with INV_SEED_E2E_PROJECT_OWNER=true (in addition to INV_SEED=true) before
// pointing this spec at it -- never on a shared or public deployment.
import { test, expect } from '@playwright/test';
import { resolveAssetPath, resolveServicePath } from '../helpers/resolve.js';
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

// hv-03 is owned by project "platform" (internal/seed/seed_projects.go's
// ownsAssets: ["hv-01", "hv-02", "hv-03"]) -- the same project this fixture
// account is assigned to. "eno3" is the one free (never physically cabled)
// port anywhere across all three of platform's hypervisors -- hv-01 and
// hv-02 are fully patched, and hv-03's OWN bond0 already carries an active
// link of its own kind: internal/seed/seed_virtual.go's virtual bridge
// uplink cables "hv-03-br0/uplink" to "hv-03/bond0", so bond0 is already
// "patched" in exactly the sense CreateLink's own COUNT check cares about,
// even though nothing about that is a physical cable
// (ListAvailableInterfaces excludes it from the "To" picker for the same
// reason). eno3 itself is genuinely free -- seed.go's own comment: "hv-03 is
// the fixture's undated host... hv-03/eno3 exists and is deliberately left
// uncabled" -- and it stays that way here: the two negative tests below post
// against it but are refused, so it is never actually consumed. It is used
// ONLY for those two negative cases, never for the success case -- see that
// test's own header for why.
const ASSET_IN = 'hv-03';
const IFACE_IN_A = 'eno3';

// sw-oob-1 is a BASE-fixture asset linked to no project at all (the same
// "outside the owner's project" fixture rbac-project-owner-edit-boundary.spec.js
// uses). Its "Management1" port is never patched by the seed's cable plant
// either, so it stays free for both negative tests below regardless of
// ordering.
const ASSET_OUT = 'sw-oob-1';
const IFACE_OUT = 'Management1';

// "sso" and "vault" are both owned by project "platform" too
// (seed_projects.go's ownsServices), distinct from the TEAM each is also
// assigned to in seed_services.go (svcSpec.owner sets app_user's team, not a
// project link) -- service scope for a project owner is resolved through the
// project link alone (domain/role.go: "service": ScopeProjectLinked), so it
// is ownsServices, not the team field, that makes both of these reachable.
const CONSUMER_SERVICE = 'sso';
const PROVIDER_SERVICE = 'vault';
const PROVIDER_ENDPOINT_NAME = 'api';

/**
 * Reads an interface's id off its own "Edit" link, which asset_detail.html
 * renders only when the viewer's permit covers the asset the port lives on
 * (`{{if $.CanWriteEntity "asset" $.Asset.ID}}`) -- so this only works for a
 * session that actually owns (or, for an admin, always covers) the asset in
 * question. Never a hardcoded id: this suite resolves every entity by name
 * (docs/E2E.md).
 *
 * @param {import('@playwright/test').Page} page a session whose permit
 *   covers `assetPath`'s asset
 * @param {string} assetPath
 * @param {string} ifaceName
 * @returns {Promise<string>}
 */
async function resolveInterfaceID(page, assetPath, ifaceName) {
  await page.goto(assetPath, { waitUntil: 'networkidle' });
  // Scoped to the Interfaces panel specifically, not just any <table> on the
  // page: asset_detail.html renders more than one, and a bare `table tr`
  // could just as easily match another one entirely.
  const panel = page.locator('.panel', { has: page.getByRole('heading', { level: 2, name: 'Interfaces' }) });
  const row = panel.locator('tbody tr', { hasText: ifaceName }).first();
  // Exact text, case-SENSITIVE (a plain RegExp, not Playwright's hasText
  // string form, which matches case-insensitively): the port-level control is
  // "Edit" but a row that also carries an assigned address renders that
  // address's own, separate "edit" link inline in the same <tr>
  // (asset_detail.html) -- a case-insensitive substring match would collide
  // with it and make toHaveCount(1) below flaky depending on whether the
  // port happens to have an address.
  const editLink = row.locator('a', { hasText: /^Edit$/ });
  await expect(
    editLink,
    `${assetPath}'s "${ifaceName}" port should carry a visible Edit link for this session`,
  ).toHaveCount(1);
  const href = await editLink.getAttribute('href');
  const match = href?.match(/[?&]edit=([^&#]+)/);
  if (!match) {
    throw new Error(`could not read an interface id out of Edit link href "${href}" for "${ifaceName}"`);
  }
  return match[1];
}

describeHere(
  'project owner topology writes: cabling (two-ended, each end independently) and a ' +
    'dependency reaching the router (writes -- local instance only, needs ' +
    'INV_SEED_E2E_PROJECT_OWNER=true)',
  () => {
    test.beforeAll(() => {
      if (looksLikeSharedDemo) {
        throw new Error(
          'rbac-project-owner-topology-write.spec.js writes to the estate and must never run ' +
            `against the shared public demo (INV_E2E_BASE_URL=${BASE_URL}).`,
        );
      }
    });

    test('refuses to cable an owned interface to one on a foreign asset (the FAR end)', async ({
      browser,
    }) => {
      const { context: adminContext, page: adminPage } = await signInAsAdmin(browser, BASE_URL);
      let foreignIfaceID;
      try {
        const outPath = await resolveAssetPath(adminPage, ASSET_OUT);
        foreignIfaceID = await resolveInterfaceID(adminPage, outPath, IFACE_OUT);
      } finally {
        await adminContext.close();
      }

      const { context: ownerContext, page: ownerPage } = await signInAsFreshUser(
        browser, BASE_URL, OWNER_USERNAME, OWNER_PASSWORD,
      );
      try {
        const inPath = await resolveAssetPath(ownerPage, ASSET_IN);
        const ownedIfaceID = await resolveInterfaceID(ownerPage, inPath, IFACE_IN_A);

        const csrfToken = await csrfTokenFrom(ownerPage);
        const response = await ownerPage.request.post('/links', {
          form: {
            csrf_token: csrfToken,
            asset_id: inPath.split('/').pop(),
            a_interface_id: ownedIfaceID,
            target_interface_id: foreignIfaceID,
            medium: 'cat6a',
          },
          headers: { Origin: BASE_URL, 'HX-Request': 'true' },
        });
        expect(
          response.status(),
          `${OWNER_USERNAME} cabling their own ${IFACE_IN_A} to ${ASSET_OUT}'s ${IFACE_OUT} (foreign far end)`,
        ).toBe(403);
      } finally {
        await ownerContext.close();
      }
    });

    test('refuses to cable a foreign interface to an owned asset (the NEAR end)', async ({
      browser,
    }) => {
      const { context: adminContext, page: adminPage } = await signInAsAdmin(browser, BASE_URL);
      let foreignIfaceID;
      try {
        const outPath = await resolveAssetPath(adminPage, ASSET_OUT);
        foreignIfaceID = await resolveInterfaceID(adminPage, outPath, IFACE_OUT);
      } finally {
        await adminContext.close();
      }

      const { context: ownerContext, page: ownerPage } = await signInAsFreshUser(
        browser, BASE_URL, OWNER_USERNAME, OWNER_PASSWORD,
      );
      try {
        const inPath = await resolveAssetPath(ownerPage, ASSET_IN);
        const ownedIfaceID = await resolveInterfaceID(ownerPage, inPath, IFACE_IN_A);

        // Reversed from the test above: the FOREIGN interface is now the
        // SUBMITTED "a" (near) end, and the owned one is the target -- proof
        // that authorizeLinkSubjects's first check (the "a" asset) refuses on
        // its own, independent of whatever the second check would have said.
        const csrfToken = await csrfTokenFrom(ownerPage);
        const response = await ownerPage.request.post('/links', {
          form: {
            csrf_token: csrfToken,
            asset_id: inPath.split('/').pop(),
            a_interface_id: foreignIfaceID,
            target_interface_id: ownedIfaceID,
            medium: 'cat6a',
          },
          headers: { Origin: BASE_URL, 'HX-Request': 'true' },
        });
        expect(
          response.status(),
          `${OWNER_USERNAME} cabling ${ASSET_OUT}'s ${IFACE_OUT} (foreign near end) to their own ${IFACE_IN_A}`,
        ).toBe(403);
      } finally {
        await ownerContext.close();
      }
    });

    test('cables two interfaces on their own project\'s asset, reaching the router with no form to click', async ({
      browser,
    }) => {
      const { context: ownerContext, page: ownerPage } = await signInAsFreshUser(
        browser, BASE_URL, OWNER_USERNAME, OWNER_PASSWORD,
      );
      try {
        const inPath = await resolveAssetPath(ownerPage, ASSET_IN);

        // --- Set-up half, TWO real browser clicks: "Add interface"
        // (asset_detail.html's interface_form) renders for this session --
        // interface is ScopeSubjectDerived, not on the DEFER list link and
        // dependency are still on -- so a project owner who owns this asset
        // genuinely has this button, unlike "Patch a cable" below. BOTH ends
        // are freshly created here, unique to this run, rather than reusing
        // eno3 (the fixture's own one free port): a run that cabled eno3
        // itself would leave it patched for good (this route's own retire
        // is Administrator-only in the UI -- ScopeTopology, same DEFER list
        // -- so nothing here could free it again), and the very next run
        // against the same long-lived local instance would find it already
        // patched and fail on a conflict that has nothing to do with a real
        // bug. Two brand-new ports, cabled to each other and never touched
        // again, keep this test repeatable indefinitely with no cleanup
        // step at all. ---
        async function addInterface(name) {
          await ownerPage.goto(inPath, { waitUntil: 'networkidle' });
          await ownerPage.locator('#interface-form input[name="name"]').fill(name);
          await Promise.all([
            ownerPage.waitForResponse((r) => r.request().method() === 'POST' && r.url().endsWith('/interfaces')),
            ownerPage.locator('#interface-form button[type="submit"]').click(),
          ]);
        }
        const ifaceAName = `e2e-owner-iface-a-${Date.now()}`;
        const ifaceBName = `e2e-owner-iface-b-${Date.now()}`;
        await addInterface(ifaceAName);
        await addInterface(ifaceBName);

        const aIfaceID = await resolveInterfaceID(ownerPage, inPath, ifaceAName);
        const bIfaceID = await resolveInterfaceID(ownerPage, inPath, ifaceBName);

        // --- The cabling write itself: no form to click (link_form is
        // `{{if .IsAdmin}}` -- this file's own header), so a direct,
        // authenticated POST from this same real session. ---
        const csrfToken = await csrfTokenFrom(ownerPage);
        const response = await ownerPage.request.post('/links', {
          form: {
            csrf_token: csrfToken,
            asset_id: inPath.split('/').pop(),
            a_interface_id: aIfaceID,
            target_interface_id: bIfaceID,
            medium: 'cat6a',
          },
          headers: { Origin: BASE_URL, 'HX-Request': 'true' },
        });
        // LinkCreate -> render.Redirect: 204 + HX-Redirect for an HTMX
        // request, not a 3xx Location -- CLAUDE.md's HTTP conventions.
        expect(
          response.status(),
          `${OWNER_USERNAME} cabling their own new ${ifaceAName} to their own new ${ifaceBName} on their project's ${ASSET_IN}`,
        ).toBe(204);
        expect(response.headers()['hx-redirect']).toBe(inPath);

        // Read back through a fresh page load: ifaceAName now shows as
        // patched to ifaceBName, not trusted from the 204 alone.
        await ownerPage.goto(inPath, { waitUntil: 'networkidle' });
        const panel = ownerPage.locator('.panel', { has: ownerPage.getByRole('heading', { level: 2, name: 'Interfaces' }) });
        const row = panel.locator('tbody tr', { hasText: ifaceAName }).first();
        await expect(row).toContainText(ifaceBName);
      } finally {
        await ownerContext.close();
      }

      // No admin cleanup needed: both the interface and the link this test
      // creates are brand new, unique to this run (the interface name and,
      // through it, the link) -- nothing seeded is touched, and nothing a
      // repeated run would collide with, the same "creates a NEW asset
      // rather than touching any existing fixture" shape
      // rbac-project-owner-create-in-project.spec.js's own second test
      // already accepts leaving behind uncleaned.
    });

    test('declares a dependency between two of their project\'s services, reaching the router with no form to click', async ({
      browser,
    }) => {
      const { context: adminContext, page: adminPage } = await signInAsAdmin(browser, BASE_URL);
      let consumerPath;
      let providerEndpointID;
      try {
        consumerPath = await resolveServicePath(adminPage, CONSUMER_SERVICE);
        const providerPath = await resolveServicePath(adminPage, PROVIDER_SERVICE);
        await adminPage.goto(providerPath, { waitUntil: 'networkidle' });
        const endpointRow = adminPage.locator('#endpoints table tr', { hasText: PROVIDER_ENDPOINT_NAME }).first();
        const editLink = endpointRow.locator('a', { hasText: /^Edit$/ });
        await expect(editLink).toHaveCount(1);
        const href = await editLink.getAttribute('href');
        const match = href?.match(/[?&]edit=([^&#]+)/);
        if (!match) {
          throw new Error(`could not read ${PROVIDER_SERVICE}/${PROVIDER_ENDPOINT_NAME}'s endpoint id out of "${href}"`);
        }
        providerEndpointID = match[1];
      } finally {
        await adminContext.close();
      }

      const { context: ownerContext, page: ownerPage } = await signInAsFreshUser(
        browser, BASE_URL, OWNER_USERNAME, OWNER_PASSWORD,
      );
      // A note unique to this run, so the admin cleanup below can find
      // exactly this edge and nothing else declared between these services.
      const failureMode = `e2e fixture ${Date.now()}: proving the route is reachable, not real failure semantics`;
      try {
        const csrfToken = await csrfTokenFrom(ownerPage);
        const response = await ownerPage.request.post(`${consumerPath}/dependencies`, {
          form: {
            csrf_token: csrfToken,
            provider_endpoint_id: providerEndpointID,
            nature: 'hard',
            failure_mode: failureMode,
          },
          headers: { Origin: BASE_URL, 'HX-Request': 'true' },
        });
        // DependencyCreate -> render.Redirect: 204 + HX-Redirect, same
        // convention as every other successful write in this suite.
        expect(
          response.status(),
          `${OWNER_USERNAME} declaring a dependency from ${CONSUMER_SERVICE} to ` +
            `${PROVIDER_SERVICE}/${PROVIDER_ENDPOINT_NAME}, both owned by their own project`,
        ).toBe(204);
        expect(response.headers()['hx-redirect']).toBe(consumerPath);
      } finally {
        await ownerContext.close();
      }

      // --- Cleanup: retire the edge from an admin session, the only session
      // "Add a dependency" and its row-level "Retire" button render for
      // (dependency is ScopeTopology, still Administrator-only in the UI --
      // this file's own header). The dependency row itself stays, retired. ---
      const { context: adminCleanupContext, page: adminCleanupPage } = await signInAsAdmin(browser, BASE_URL);
      try {
        await adminCleanupPage.goto(consumerPath, { waitUntil: 'networkidle' });
        const depRow = adminCleanupPage.locator('tr', { hasText: failureMode });
        await expect(depRow).toBeVisible();
        adminCleanupPage.once('dialog', (dialog) => dialog.accept());
        await Promise.all([
          adminCleanupPage.waitForResponse((r) => r.request().method() === 'POST' && r.url().includes('/retire')),
          depRow.locator('button', { hasText: 'Retire' }).click(),
        ]);
      } finally {
        await adminCleanupContext.close();
      }
    });
  },
);
