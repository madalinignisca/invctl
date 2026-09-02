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
// authorizeDependencySubjects -- now proven THROUGH THE REAL UI a project
// owner actually has, not only through a direct POST simulating one.
//
// THIS FILE USED TO SAY THE CONTROLS WERE NOT OFFERED AT ALL. That was true
// when this file was first written (link_form and the "Add a dependency"
// disclosure were both `{{if .IsAdmin}}`, WP-1.0 item 1's DEFER list) and is
// no longer true as of three commits on this branch:
//   - abc63d1 "render write controls when project owner owns both dependency
//     ends" -- a dependency row's Edit/Retire/Verify controls now render for
//     a project owner who owns the consumer service AND the provider's
//     owning service (depRowData.CanWrite mirrors authorizeDependencySubjects).
//   - 5df85b1 "gate Unpatch on peer-asset ownership, not just administrator"
//     -- Unpatch renders for a project owner who owns BOTH end assets of a
//     cable.
//   - 8ad9689 "filter create-form options by authorization" -- the "Patch a
//     cable" and "Add a dependency" forms are gated on ownership of the NEAR
//     end alone (the asset/service the operator is already looking at), and
//     their far-end pickers (LinkForm.Targets, DependencyForm.AllEndpoints/
//     AllRoutes) are filtered to what the caller may actually write.
// So the two write-through-the-real-form tests below (cabling and
// dependency) now click the control, pick from the (filtered) dropdown and
// submit, the same shape rbac-project-owner-edit-boundary.spec.js's own
// second test uses for the asset edit form. The picker-filtering test is
// new: nothing in this suite previously asserted on what a picker DOES or
// DOES NOT offer, only on what a submission is refused for.
//
// THE FORGED-REQUEST CASE STILL NEEDS A DIRECT POST, AND STILL EARNS ITS
// PLACE. No UI in this application will ever construct a request naming a
// foreign asset's port or a foreign service's endpoint -- the picker itself
// filters those options out (proven by this file's own picker-filtering
// test) -- so the only way to exercise authorizeLinkSubjects' server-side
// refusal is to forge the request by hand, bypassing the (correct, filtered)
// form entirely. CLAUDE.md's own rule: "hiding a control is not the
// enforcement" -- a filtered dropdown is a courtesy for a well-behaved
// client, not the boundary. The two negative tests below keep doing exactly
// that, unchanged in shape from before this branch, and are now clearly
// labelled as the forged-request case rather than "there is no button".
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
// WRITES -- the cabling test creates (and then unpatches, through the UI) a
// real "link" row, and the dependency test creates (and then retires,
// through the UI) a real "dependency" row; both are soft-delete-only
// entities (CLAUDE.md), so even with cleanup each leaves a permanently
// retired row behind. Refuses the shared public demo the same way every
// other mutating spec in this suite does, signs the project owner in
// through a FRESH browser context, and touches no "name" field of any named
// fixture another spec resolves by name.
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
// uncabled" -- and it stays that way here: the negative tests and the
// picker-filtering test below only ever READ it (as the near end's one
// available "From" option, or as the one option the far-end picker should
// legitimately offer on this asset's own page); the write tests create their
// OWN brand-new ports instead of consuming it, so it stays free for the next
// run of this file too. See that test's own header for why.
const ASSET_IN = 'hv-03';
const IFACE_IN_A = 'eno3';

// sw-oob-1 is a BASE-fixture asset linked to no project at all (the same
// "outside the owner's project" fixture rbac-project-owner-edit-boundary.spec.js
// uses). Its "Management1" port is never patched by the seed's cable plant
// either, so it stays free for both negative tests below regardless of
// ordering, and it is what the picker-filtering test asserts is ABSENT from
// the owner's far-end picker and PRESENT in an administrator's.
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

// "orders-api" is owned by project "commerce", not "platform"
// (seed_projects.go's ownsServices for "commerce": ["orders-api",
// "orders-web", "partner-gateway"]) -- a service this fixture's project
// owner has no write access to anywhere. Its "http" endpoint exists
// unconditionally in the BASE fixture (seed_services.go), so it is a stable,
// always-present example of an endpoint that must be absent from the
// owner's own provider picker and present in an administrator's -- the
// dependency twin of ASSET_OUT/IFACE_OUT above.
const FOREIGN_SERVICE = 'orders-api';
const FOREIGN_ENDPOINT_NAME = 'http';

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
  'project owner topology writes through the real UI: cabling (two-ended, each end ' +
    'independently) and a dependency, both via the actual form; a forged out-of-scope ' +
    'request is still refused server-side (writes -- local instance only, needs ' +
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

    test('refuses a forged link POST naming a foreign FAR end -- the picker itself would never offer this option', async ({
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

        // A direct POST, not a form submission: ASSET_OUT's port never
        // appears as a #link-target option for this session (proven by the
        // picker-filtering test below), so there is no form field a real
        // browser could have produced this request from. This is exactly
        // the forged-request shape this file's header explains.
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

    test('refuses a forged link POST naming a foreign NEAR end -- the picker itself would never offer this option', async ({
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
        // ASSET_IN's own asset page is not even where this "a" end lives, so
        // this specific shape could never come from a form either way.
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

    test('a project owner\'s far-end and provider-endpoint pickers are filtered to what they may write; an administrator\'s identical pickers list the whole estate', async ({
      browser,
    }) => {
      // --- Owner side: the pickers on ASSET_IN's and CONSUMER_SERVICE's own
      // pages must offer only what this owner may write -- never nothing
      // (an empty picker would be indistinguishable from a filter that hid
      // everything from everyone, the exact gap the admin half below closes)
      // and never ASSET_OUT/FOREIGN_SERVICE. ---
      const { context: ownerContext, page: ownerPage } = await signInAsFreshUser(
        browser, BASE_URL, OWNER_USERNAME, OWNER_PASSWORD,
      );
      try {
        const inPath = await resolveAssetPath(ownerPage, ASSET_IN);
        await ownerPage.goto(inPath, { waitUntil: 'networkidle' });
        const targetOptions = ownerPage.locator('#link-target option');
        await expect(
          targetOptions.filter({ hasText: new RegExp(`^${ASSET_IN} / ${IFACE_IN_A}$`) }),
          `${ASSET_IN}'s own free port should appear in its own far-end picker -- an empty picker would ` +
            'pass this assertion for the wrong reason',
        ).toHaveCount(1);
        await expect(
          targetOptions.filter({ hasText: new RegExp(`^${ASSET_OUT}\\b`) }),
          `${ASSET_OUT} is outside this owner's project; none of its ports should appear in the far-end picker`,
        ).toHaveCount(0);

        const consumerPath = await resolveServicePath(ownerPage, CONSUMER_SERVICE);
        await ownerPage.goto(consumerPath, { waitUntil: 'networkidle' });
        // The disclosure has to be opened before its contents are visible --
        // service_detail.html wraps "Add a dependency" in a plain <details>,
        // not an Alpine x-show, so Playwright's actionability check on a
        // <select> inside it fails until the <summary> is clicked.
        await ownerPage.getByText('Add a dependency').click();
        const endpointOptions = ownerPage.locator('#dep-endpoint option');
        await expect(
          endpointOptions.filter({ hasText: new RegExp(`^${PROVIDER_SERVICE} / ${PROVIDER_ENDPOINT_NAME}\\b`) }),
          `${PROVIDER_SERVICE}/${PROVIDER_ENDPOINT_NAME} is owned by this owner's own project and should ` +
            'appear in the provider picker',
        ).toHaveCount(1);
        await expect(
          endpointOptions.filter({ hasText: new RegExp(`^${FOREIGN_SERVICE} / `) }),
          `${FOREIGN_SERVICE} belongs to a project this owner does not own; none of its endpoints should ` +
            'appear in the provider picker',
        ).toHaveCount(0);
      } finally {
        await ownerContext.close();
      }

      // --- Administrator side: the SAME two pages, the SAME two pickers,
      // now listing the options the owner's session above proved absent.
      // Without this half, a filter that hid every option from every viewer
      // -- including an administrator -- would satisfy every assertion
      // above and prove nothing about scoping. ---
      const { context: adminContext, page: adminPage } = await signInAsAdmin(browser, BASE_URL);
      try {
        const inPath = await resolveAssetPath(adminPage, ASSET_IN);
        await adminPage.goto(inPath, { waitUntil: 'networkidle' });
        await expect(
          adminPage.locator('#link-target option').filter({ hasText: new RegExp(`^${ASSET_OUT} / ${IFACE_OUT}$`) }),
          `an administrator's far-end picker on ${ASSET_IN} should list ${ASSET_OUT}/${IFACE_OUT} even though ` +
            'it belongs to no project a viewer would otherwise be scoped to',
        ).toHaveCount(1);

        const consumerPath = await resolveServicePath(adminPage, CONSUMER_SERVICE);
        await adminPage.goto(consumerPath, { waitUntil: 'networkidle' });
        await adminPage.getByText('Add a dependency').click();
        await expect(
          adminPage.locator('#dep-endpoint option').filter({ hasText: new RegExp(`^${FOREIGN_SERVICE} / ${FOREIGN_ENDPOINT_NAME}\\b`) }),
          `an administrator's provider picker on ${CONSUMER_SERVICE} should list ${FOREIGN_SERVICE}/` +
            `${FOREIGN_ENDPOINT_NAME} even though the project owner used above cannot write it`,
        ).toHaveCount(1);
      } finally {
        await adminContext.close();
      }
    });

    test('cables two interfaces on their own project\'s asset through the real "Patch a cable" form, then unpatches it through the real "Unpatch" button', async ({
      browser,
    }) => {
      const { context: ownerContext, page: ownerPage } = await signInAsFreshUser(
        browser, BASE_URL, OWNER_USERNAME, OWNER_PASSWORD,
      );
      try {
        const inPath = await resolveAssetPath(ownerPage, ASSET_IN);

        // --- Set-up half, TWO real browser clicks through "Add interface"
        // (asset_detail.html's interface_form) -- interface is
        // ScopeSubjectDerived with a single subject, so this control has
        // rendered for a project owner since well before this branch; the
        // point being proven here is the cable, not the ports. BOTH ends are
        // freshly created here, unique to this run, rather than reusing
        // eno3 (the fixture's own one free port): a run that cabled eno3
        // itself would leave it patched for good (nothing in this file frees
        // it back, by design -- see ASSET_IN's own comment above), and the
        // very next run against the same long-lived local instance would
        // find it already patched and fail on a conflict that has nothing to
        // do with a real bug. Two brand-new ports, cabled to each other and
        // unpatched again by the end of this test, keep it repeatable
        // indefinitely with no admin cleanup step at all. ---
        async function addInterface(name) {
          await ownerPage.goto(inPath, { waitUntil: 'networkidle' });
          await ownerPage.locator('#interface-form input[name="name"]').fill(name);
          await Promise.all([
            ownerPage.waitForResponse((r) => r.request().method() === 'POST' && r.url().endsWith('/interfaces')),
            ownerPage.locator('#interface-form button[type="submit"]').click(),
          ]);
        }
        const ifaceAName = `e2e-owner-ui-iface-a-${Date.now()}`;
        const ifaceBName = `e2e-owner-ui-iface-b-${Date.now()}`;
        await addInterface(ifaceAName);
        await addInterface(ifaceBName);

        // --- The cabling write itself, through the real "Patch a cable"
        // form (web/templates/partials/forms.html's link_form,
        // asset_detail.html's Interfaces panel) -- gated on
        // CanWriteEntity("asset", ASSET_IN) alone (Task 3's near-end rule),
        // which this owner satisfies, and no longer on .IsAdmin (5df85b1,
        // 8ad9689). The "From" select only ever carries THIS asset's own
        // unpatched ports (web/templates/partials/forms.html's link_form:
        // `{{range .Interfaces}}`), so ifaceAName resolves by its bare name;
        // the "To" select is filtered but still carries the far end's
        // asset name (`{{.AssetName}} / {{.Name}}`), so ifaceBName resolves
        // as "ASSET_IN / ifaceBName". ---
        await ownerPage.goto(inPath, { waitUntil: 'networkidle' });
        await ownerPage.locator('#link-from').selectOption({ label: ifaceAName });
        await ownerPage.locator('#link-target').selectOption({ label: `${ASSET_IN} / ${ifaceBName}` });
        await ownerPage.locator('#link-medium').fill('cat6a');
        const [response] = await Promise.all([
          ownerPage.waitForResponse((r) => r.request().method() === 'POST' && r.url().endsWith('/links')),
          ownerPage.locator('#link-form button[type="submit"]').click(),
        ]);
        // LinkCreate -> render.Redirect: 204 + HX-Redirect for an HTMX
        // request, not a 3xx Location -- CLAUDE.md's HTTP conventions, same
        // as every other successful write in this suite.
        expect(
          response.status(),
          `${OWNER_USERNAME} cabling their own new ${ifaceAName} to their own new ${ifaceBName} on their project's ${ASSET_IN}, through the real form`,
        ).toBe(204);
        expect(response.headers()['hx-redirect']).toBe(inPath);

        // Read back through a fresh page load: ifaceAName now shows as
        // patched to ifaceBName, not trusted from the 204 alone.
        await ownerPage.goto(inPath, { waitUntil: 'networkidle' });
        const panel = ownerPage.locator('.panel', { has: ownerPage.getByRole('heading', { level: 2, name: 'Interfaces' }) });
        const rowA = panel.locator('tbody tr', { hasText: ifaceAName }).first();
        await expect(rowA).toContainText(ifaceBName);

        // --- The unpatch, through the real "Unpatch" button on that same
        // row (asset_detail.html's Interfaces panel). Gated on
        // `.IsPatched (or $.IsAdmin (and CanWriteEntity(this asset)
        // CanWriteEntity(peer asset)))` -- 5df85b1's whole point -- and both
        // ends here are ASSET_IN itself, which this owner covers twice
        // over. hx-confirm fires a native confirm() dialog before htmx sends
        // the request, so it has to be accepted from JS before the click,
        // not after. ---
        ownerPage.once('dialog', (dialog) => dialog.accept());
        const [unpatchResponse] = await Promise.all([
          ownerPage.waitForResponse((r) => r.request().method() === 'POST' && r.url().includes('/links/') && r.url().endsWith('/retire')),
          rowA.locator('button', { hasText: 'Unpatch' }).click(),
        ]);
        // LinkRetire -> render.Redirect too: same 204 + HX-Redirect shape.
        expect(unpatchResponse.status(), `${OWNER_USERNAME} unpatching their own cable, through the real button`).toBe(204);
        expect(unpatchResponse.headers()['hx-redirect']).toBe(inPath);

        // Read back again: the peer column is cleared, proving the retire
        // stuck rather than merely responding.
        await ownerPage.goto(inPath, { waitUntil: 'networkidle' });
        const rowAAfter = panel.locator('tbody tr', { hasText: ifaceAName }).first();
        await expect(rowAAfter).not.toContainText(ifaceBName);
      } finally {
        await ownerContext.close();
      }

      // No admin cleanup needed: both interfaces are brand new, unique to
      // this run, and the cable between them ends the test unpatched --
      // nothing seeded is touched, and nothing a repeated run would collide
      // with. The interface rows themselves stay (interfaces are not
      // soft-delete-able through any route this suite could reach either
      // way), the same "creates rows and leaves them, cabled or not" trade
      // rbac-project-owner-create-in-project.spec.js's own second test
      // already accepts.
    });

    test('declares a dependency between two of their project\'s services through the real "Add a dependency" form, then retires it through their own, now-visible "Retire" button', async ({
      browser,
    }) => {
      const { context: ownerContext, page: ownerPage } = await signInAsFreshUser(
        browser, BASE_URL, OWNER_USERNAME, OWNER_PASSWORD,
      );
      // A note unique to this run, so the row can be found again by text
      // rather than by a remembered id.
      const failureMode = `e2e fixture ${Date.now()}: proving the UI route works, not real failure semantics`;
      try {
        const consumerPath = await resolveServicePath(ownerPage, CONSUMER_SERVICE);
        await ownerPage.goto(consumerPath, { waitUntil: 'networkidle' });
        // Gated on CanWriteEntity("service", CONSUMER_SERVICE) alone (Task
        // 3's near-end rule, service_detail.html) -- true for this owner,
        // no longer .IsAdmin (abc63d1, 8ad9689). The disclosure has to be
        // opened before the <select> inside it is interactable.
        await ownerPage.getByText('Add a dependency').click();

        // The default provider kind is "an endpoint" (the select's first
        // <option>, forms.html's dependency_form), so #dep-endpoint is
        // already the visible field -- no need to touch #dep-provider-kind
        // first. Selecting by VALUE (read off the matching <option>'s own
        // attribute), not by label text, because the label carries the
        // endpoint's address too (`{{.ServiceCode}} / {{.Name}}
        // ({{.Addr}})`) and this test does not need to know or assert that
        // address to prove the write reaches the router.
        const providerOption = ownerPage.locator('#dep-endpoint option', {
          hasText: new RegExp(`^${PROVIDER_SERVICE} / ${PROVIDER_ENDPOINT_NAME}\\b`),
        });
        await expect(providerOption, `${PROVIDER_SERVICE}/${PROVIDER_ENDPOINT_NAME} should be a selectable option for this owner`).toHaveCount(1);
        const providerValue = await providerOption.getAttribute('value');
        await ownerPage.locator('#dep-endpoint').selectOption(providerValue ?? '');
        await ownerPage.locator('#dep-failure').fill(failureMode);

        const [response] = await Promise.all([
          ownerPage.waitForResponse((r) => r.request().method() === 'POST' && r.url().includes('/dependencies')),
          ownerPage.locator('#dependency-form button[type="submit"]').click(),
        ]);
        // DependencyCreate -> render.Redirect: 204 + HX-Redirect, same
        // convention as every other successful write in this suite.
        expect(
          response.status(),
          `${OWNER_USERNAME} declaring a dependency from ${CONSUMER_SERVICE} to ` +
            `${PROVIDER_SERVICE}/${PROVIDER_ENDPOINT_NAME}, both owned by their own project, through the real form`,
        ).toBe(204);
        expect(response.headers()['hx-redirect']).toBe(consumerPath);

        // Read back through a fresh page load, not trusted from the 204
        // alone.
        await ownerPage.goto(consumerPath, { waitUntil: 'networkidle' });
        const depRow = ownerPage.locator('tr', { hasText: failureMode });
        await expect(depRow).toBeVisible();

        // --- The retire, through this SAME owner's own session. depRowData.
        // CanWrite (abc63d1) mirrors authorizeDependencySubjects: this owner
        // covers both CONSUMER_SERVICE and PROVIDER_SERVICE, so the row's
        // own "Retire" button is rendered for them -- unlike before this
        // branch, when only an administrator's session ever saw it. Proving
        // this half in the SAME session that created the row is the point:
        // the capability is reachable end-to-end without ever handing off
        // to a second, more-privileged session. ---
        ownerPage.once('dialog', (dialog) => dialog.accept());
        const [retireResponse] = await Promise.all([
          ownerPage.waitForResponse((r) => r.request().method() === 'POST' && r.url().includes('/retire')),
          depRow.locator('button', { hasText: 'Retire' }).click(),
        ]);
        expect(retireResponse.status(), `${OWNER_USERNAME} retiring their own dependency, through their own now-visible button`).toBe(204);
      } finally {
        await ownerContext.close();
      }

      // No admin cleanup needed: the owner's own session retired the row
      // above -- the dependency row itself stays, retired, the same
      // soft-delete trade this file's cabling test makes for its interfaces.
    });
  },
);
