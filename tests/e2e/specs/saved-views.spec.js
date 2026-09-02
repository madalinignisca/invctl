// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// WRITES, ON PURPOSE, AND MORE STRICTLY GATED THAN ANY OTHER SPEC HERE.
//
// Saving a view is the whole point of WP-G4b -- there is no way to prove it
// without a real POST to /views that leaves a row behind. docs/E2E.md's
// "This suite never writes" section already had two exceptions before this
// file (user-administration.spec.js and the two rbac-project-owner specs),
// each guarded by refusing to run against anything that LOOKS LIKE the
// shared public demo (a hostname match on invctl.madalin.me). This spec
// uses a STRICTER, POSITIVE opt-in instead of that negative denylist,
// because what it creates is not a throwaway account a later step in the
// same run demotes back -- a saved view is a soft-delete-only row
// (CLAUDE.md: "Soft delete only, for entities... Never delete"), so even
// this spec's own cleanup (the "remove" step below) leaves a retired row
// permanently on whatever instance it ran against. A hostname denylist only
// ever knows about hosts someone thought to list; this requires the
// operator to affirmatively say "this target is disposable" instead.
//
//   1. Without INV_E2E_DISPOSABLE=true, this spec SKIPS -- the one
//      legitimate runtime skip shape this suite recognises (CLAUDE.md: "a
//      runtime skip is legitimate only on an explicitly declared
//      precondition: an env-var opt-in"), exactly like INV_E2E_BASE_URL
//      itself. The skipped describe block's own title states the reason,
//      the same way the Makefile's `make e2e` prints one for the unset
//      BASE_URL case.
//   2. If INV_E2E_DISPOSABLE=true IS set but INV_E2E_BASE_URL does not
//      resolve to localhost or 127.0.0.1, this spec FAILS LOUDLY instead of
//      skipping. That combination is somebody actively asserting a
//      non-local host is disposable, and nothing in this file is in a
//      position to know that is true -- silently skipping would hide a
//      configuration mistake that is one flag away from writing a
//      permanent artefact onto a shared instance; silently running would be
//      worse.
//
// IF YOU ARE READING THIS BECAUSE THE SUITE FAILED: that is almost
// certainly case 2 above, not a real bug -- see docs/E2E.md's own warning
// that this suite's failures "look nothing like their cause". Point
// INV_E2E_BASE_URL at a disposable local instance you stood up yourself
// (INV_SEED=true INV_SEED_COMPANY=true; see docs/E2E.md's "demo estate"
// section for what that seeds and why this spec needs it) -- NEVER at the
// real deployment on this machine's :8088 (invctl-demo.service) and NEVER
// at https://invctl.madalin.me.
import { test, expect } from '@playwright/test';
import { signInAsFreshUser, csrfTokenFrom } from '../helpers/login.js';

const BASE_URL = process.env.INV_E2E_BASE_URL;
const disposableOptIn = process.env.INV_E2E_DISPOSABLE === 'true';

// Resolves to true only for a target this file is willing to write to.
// Wrapped in try/catch because an unset or malformed BASE_URL must read as
// "not local", never throw here -- the loud failure below is a deliberate
// test.beforeAll throw with a clear message, not an unhandled exception from
// a bad URL parse.
function pointsAtLocalhost() {
  if (!BASE_URL) {
    return false;
  }
  try {
    const host = new URL(BASE_URL).hostname;
    return host === 'localhost' || host === '127.0.0.1';
  } catch {
    return false;
  }
}

const localTarget = pointsAtLocalhost();

// Case 1 above: no opt-in, skip -- and say so in the title itself, since a
// skipped describe block never runs a beforeAll that could print anything.
const describeTitle = disposableOptIn
  ? 'saved views (mutates -- disposable local instance only)'
  : 'saved views (mutates -- disposable local instance only) ' +
    '[skipped: set INV_E2E_DISPOSABLE=true to run this spec]';

const describe = disposableOptIn ? test.describe : test.describe.skip;

describe(describeTitle, () => {
  test.beforeAll(() => {
    // Case 2 above: the opt-in is set but the target does not look local.
    // Unreachable while the whole block is skipped (disposableOptIn false);
    // kept as the actual enforcement when it is NOT skipped, which is the
    // state where a mistake here would matter.
    if (!localTarget) {
      throw new Error(
        `INV_E2E_DISPOSABLE=true requires INV_E2E_BASE_URL to resolve to ` +
          `localhost or 127.0.0.1 (got ${BASE_URL ?? '<unset>'}). This spec ` +
          'saves a permanent saved_view row and this project never assumes a ' +
          'non-local host is disposable -- see this file\'s own header and ' +
          "docs/E2E.md's \"This suite never writes\" section.",
      );
    }
  });

  test('saving, reopening and removing a view genuinely applies and later clears the filter', async ({
    page,
  }) => {
    // A name unique per run: this instance is disposable but the spec may
    // still be run against it more than once before it is thrown away, and
    // a second run must not collide with the first run's still-present row.
    const viewName = `e2e-firewalls-${Date.now()}`;

    // --- Baseline: unfiltered /assets shows both a hypervisor and a
    // firewall (docs/E2E.md's named fixtures: hv-01, fw-dev-1). ---
    await page.goto('/assets', { waitUntil: 'networkidle' });
    await expect(page.locator('#asset-table a.id', { hasText: /^hv-01$/ })).toBeVisible();
    await expect(page.locator('#asset-table a.id', { hasText: /^fw-dev-1$/ })).toBeVisible();

    // --- Apply a filter: kind=firewall. web/templates/pages/asset_list.html's
    // toolbar swaps #asset-table via hx-get on 'change', which a real
    // <select> firing selectOption satisfies without needing the 'q' box's
    // keyup debounce. ---
    await Promise.all([
      page.waitForResponse(
        (r) => r.request().method() === 'GET' && r.url().includes('/assets?') && r.url().includes('kind=firewall'),
      ),
      page.locator('#f-kind').selectOption('firewall'),
    ]);
    await expect(page.locator('#asset-table a.id', { hasText: /^fw-dev-1$/ })).toBeVisible();
    await expect(page.locator('#asset-table a.id', { hasText: /^hv-01$/ })).toHaveCount(0);

    // --- Open the Views menu AFTER filtering, not before: the whole
    // #asset-table fragment (menu included) is swapped on every filter
    // change, so an open menu closes the moment the filter above ran --
    // see web/templates/partials/asset_table.html's own comment on exactly
    // this. ---
    await page.locator('.saved-views button', { hasText: 'Views' }).click();
    await expect(page.locator('.saved-views-menu')).toBeVisible();

    // --- Save the currently-applied filter under a name. The form carries
    // hx-post; on success the handler answers HX-Redirect (render.Redirect,
    // internal/web/handlers/savedviews.go), which htmx turns into a real
    // browser navigation to the SAME URL the address bar already shows
    // (the filter's own hx-push-url already put ?kind=firewall there before
    // this save even ran). Two things this rules out, in order:
    //   - waitForURL: resolves the instant its pattern matches the CURRENT
    //     url, which is already true before the click ever fires here, so it
    //     proves nothing.
    //   - waitForResponse on the document GET: the response can arrive before
    //     the navigation it belongs to actually COMMITS in the frame, so a
    //     goto() issued right after still raced a same-URL navigation that
    //     was, from Playwright's point of view, still in flight
    //     (net::ERR_ABORTED / "interrupted by another navigation to the same
    //     URL" on the very next page.goto -- observed directly while writing
    //     this spec).
    // waitForEvent('framenavigated') is the one signal that only fires once
    // the frame has actually navigated -- an event, not a poll of current
    // state, so it cannot resolve on a state that was already true before
    // the click. ---
    await page.locator('.saved-views-menu input[name="name"]').fill(viewName);
    await Promise.all([
      page.waitForEvent('framenavigated', (frame) => frame === page.mainFrame() && frame.url().includes('kind=firewall')),
      page.locator('.saved-view-save button[type="submit"]').click(),
    ]);
    await page.waitForLoadState('networkidle');

    // --- "Reload": deliberately NOT page.reload(), which would refresh a
    // URL that already carries ?kind=firewall from the redirect above and
    // prove nothing about the saved view itself -- the filter would still
    // be "applied" purely because it is sitting in the address bar. Instead,
    // navigate back to a genuinely clean, unfiltered /assets, the same way a
    // person closing this tab and opening a fresh one would, so the saved
    // view is the ONLY thing that can reapply the filter afterward. ---
    await page.goto('/assets', { waitUntil: 'networkidle' });
    expect(page.url()).not.toContain('kind=');
    await expect(page.locator('#asset-table a.id', { hasText: /^hv-01$/ })).toBeVisible();

    // --- Open Views again and follow the saved view. The link is a plain
    // <a href>, a full navigation, not an hx-get -- see this file's own
    // header and saved_views.html's comment on why: it is what lets
    // waitForURL below observe the navigation directly rather than an XHR. ---
    await page.locator('.saved-views button', { hasText: 'Views' }).click();
    const savedRow = page.locator('.saved-view-row', { hasText: viewName });
    await expect(savedRow).toBeVisible();
    const href = await savedRow.locator('a').getAttribute('href');
    expect(href, 'the saved view\'s own link should carry the filter it stored').toBe('/assets?kind=firewall');

    await Promise.all([page.waitForURL(/\/assets\?kind=firewall/), savedRow.locator('a').click()]);

    // The claim this whole spec exists to prove: genuinely applied, checked
    // BOTH ways -- the URL a person could paste into a ticket, and the
    // actual rendered rows, not merely one or the other.
    expect(page.url()).toContain('kind=firewall');
    await expect(page.locator('#asset-table a.id', { hasText: /^fw-dev-1$/ })).toBeVisible();
    await expect(page.locator('#asset-table a.id', { hasText: /^hv-01$/ })).toHaveCount(0);

    // --- Remove it. The "Remove" form has no hx-post (saved_views.html) --
    // a plain browser POST, so this is a real navigation too. RetireSavedView
    // redirects to the entity's base list ("/assets"), unfiltered, regardless
    // of what the retired view's own filter was. ---
    await page.locator('.saved-views button', { hasText: 'Views' }).click();
    const rowToRemove = page.locator('.saved-view-row', { hasText: viewName });
    await Promise.all([
      page.waitForURL((url) => url.pathname === '/assets' && url.search === ''),
      rowToRemove.locator('button', { hasText: 'Remove' }).click(),
    ]);

    // Soft-deleted, not hard-deleted (CLAUDE.md) -- the assertion here is
    // only that the MENU no longer offers it, which is all a person-facing
    // "remove" flow promises; the row itself persists with lifecycle =
    // 'retired' in the database, unchecked by this browser-level spec.
    await page.locator('.saved-views button', { hasText: 'Views' }).click();
    await expect(page.locator('.saved-view-row', { hasText: viewName })).toHaveCount(0);
  });

  // The test above only ever posts kind=firewall -- one value under one key
  // -- so it cannot catch the highest-value defect the whole-branch review
  // found: params used to be stored as map[string]string, which silently
  // KEPT ONLY THE FIRST value posted under a repeating key. "tag" is exactly
  // such a key (asset_list.html's #f-tag is a <select multiple>), and losing
  // all but the first tag turns an AND-combined filter into a far wider one
  // on reopen -- a populated, plausible, WRONG result, not an empty one
  // somebody would double-check.
  //
  // This proves the round trip at both ends: the stored params carry every
  // tag posted, and the reopened view's own query string carries every tag
  // as a SEPARATE ?tag= parameter (not the first only), and narrows the
  // table to exactly the asset that carries every one of them.
  test('a view saved with more than one tag reopens with every tag, not just the first', async ({
    page,
  }) => {
    const tagACode = `e2e-tag-a-${Date.now()}`;
    const tagBCode = `e2e-tag-b-${Date.now()}`;
    const viewName = `e2e-two-tags-${Date.now()}`;

    // --- Create tag A and apply it to fw-dev-1, via the asset detail page's
    // "create and apply" fields (docs/tags-design.md §4a) -- one tag per
    // submission, since that fieldset offers only one code/label/description
    // triple at a time. ---
    await page.goto('/assets', { waitUntil: 'networkidle' });
    await page.locator('#asset-table a.id', { hasText: /^fw-dev-1$/ }).click();
    await page.waitForLoadState('networkidle');
    const fwAssetPath = new URL(page.url()).pathname; // "/assets/<fw-dev-1's id>"

    await page.locator('input[name="new_tag_code"]').fill(tagACode);
    await page.locator('input[name="new_tag_label"]').fill(tagACode);
    await page.locator('input[name="new_tag_description"]').fill('e2e fixture tag A for the multi-tag saved-view spec');
    await Promise.all([
      page.waitForURL((url) => url.pathname === fwAssetPath),
      page.locator('button[type="submit"]', { hasText: 'Save tags' }).click(),
    ]);
    await page.waitForLoadState('networkidle');
    // The tag's real id, read back from its own (now ticked, Applied)
    // checkbox -- needed below to select it in the list filter and to
    // assert on the saved view's query string without guessing at ordering.
    const tagAId = await page
      .locator('label.check', { hasText: tagACode })
      .locator('input[name="tag_id"]')
      .getAttribute('value');

    // --- Create tag B, applied to the SAME asset (fw-dev-1), a second
    // submission against the same fixed set of checkboxes -- tag A stays
    // ticked since Applied already carries it. ---
    await page.locator('input[name="new_tag_code"]').fill(tagBCode);
    await page.locator('input[name="new_tag_label"]').fill(tagBCode);
    await page.locator('input[name="new_tag_description"]').fill('e2e fixture tag B for the multi-tag saved-view spec');
    await Promise.all([
      page.waitForURL((url) => url.pathname === fwAssetPath),
      page.locator('button[type="submit"]', { hasText: 'Save tags' }).click(),
    ]);
    await page.waitForLoadState('networkidle');
    const tagBId = await page
      .locator('label.check', { hasText: tagBCode })
      .locator('input[name="tag_id"]')
      .getAttribute('value');

    // --- Apply ONLY tag A to hv-01, so it matches tag A alone and NOT
    // "carries every tag in the filter" -- the asset that proves AND
    // semantics survived the round trip, not just that a tag round-tripped
    // at all. ---
    await page.goto('/assets', { waitUntil: 'networkidle' });
    await page.locator('#asset-table a.id', { hasText: /^hv-01$/ }).click();
    await page.waitForLoadState('networkidle');
    const hvAssetPath = new URL(page.url()).pathname;
    await page.locator(`input[name="tag_id"][value="${tagAId}"]`).check();
    await Promise.all([
      page.waitForURL((url) => url.pathname === hvAssetPath),
      page.locator('button[type="submit"]', { hasText: 'Save tags' }).click(),
    ]);
    await page.waitForLoadState('networkidle');

    // --- Filter /assets by BOTH tags at once (the multi-select, not two
    // separate filters) -- AND-combined (docs/tags-design.md §5), so only
    // fw-dev-1 (both tags) should remain; hv-01 (tag A only) should not. ---
    await page.goto('/assets', { waitUntil: 'networkidle' });
    await Promise.all([
      page.waitForResponse(
        (r) =>
          r.request().method() === 'GET' &&
          r.url().includes('/assets?') &&
          r.url().includes(`tag=${tagAId}`) &&
          r.url().includes(`tag=${tagBId}`),
      ),
      page.locator('#f-tag').selectOption([tagAId, tagBId]),
    ]);
    await expect(page.locator('#asset-table a.id', { hasText: /^fw-dev-1$/ })).toBeVisible();
    await expect(page.locator('#asset-table a.id', { hasText: /^hv-01$/ })).toHaveCount(0);

    // --- Save this two-tag filter under a name, then reopen after a clean,
    // unfiltered navigation -- same "prove it survives a real reload"
    // shape as the single-filter test above. ---
    await page.locator('.saved-views button', { hasText: 'Views' }).click();
    await expect(page.locator('.saved-views-menu')).toBeVisible();
    await page.locator('.saved-views-menu input[name="name"]').fill(viewName);
    await Promise.all([
      page.waitForEvent(
        'framenavigated',
        (frame) => frame === page.mainFrame() && frame.url().includes(`tag=${tagAId}`) && frame.url().includes(`tag=${tagBId}`),
      ),
      page.locator('.saved-view-save button[type="submit"]').click(),
    ]);
    await page.waitForLoadState('networkidle');

    await page.goto('/assets', { waitUntil: 'networkidle' });
    expect(page.url()).not.toContain('tag=');

    await page.locator('.saved-views button', { hasText: 'Views' }).click();
    const savedRow = page.locator('.saved-view-row', { hasText: viewName });
    await expect(savedRow).toBeVisible();
    const href = await savedRow.locator('a').getAttribute('href');
    // THE ASSERTION FIX 1 EXISTS FOR: both tags present as separate ?tag=
    // parameters. Before the fix, this link would carry only whichever tag
    // happened to be read as "the" single value -- exactly the silent
    // widening the review flagged.
    expect(href).toContain(`tag=${tagAId}`);
    expect(href).toContain(`tag=${tagBId}`);
    expect((href.match(/tag=/g) || []).length).toBe(2);

    await Promise.all([
      page.waitForURL((url) => url.searchParams.getAll('tag').length === 2),
      savedRow.locator('a').click(),
    ]);
    await expect(page.locator('#asset-table a.id', { hasText: /^fw-dev-1$/ })).toBeVisible();
    await expect(page.locator('#asset-table a.id', { hasText: /^hv-01$/ })).toHaveCount(0);

    // --- Clean up the saved view, like the single-filter test does. The
    // tags and the tag applications on fw-dev-1/hv-01 are left in place,
    // the same permanent-artefact tradeoff this whole spec's header already
    // accepts for the view row itself -- disposable-instance-only, never a
    // shared target. ---
    await page.locator('.saved-views button', { hasText: 'Views' }).click();
    const rowToRemoveTwoTags = page.locator('.saved-view-row', { hasText: viewName });
    await Promise.all([
      page.waitForURL((url) => url.pathname === '/assets' && url.search === ''),
      rowToRemoveTwoTags.locator('button', { hasText: 'Remove' }).click(),
    ]);
    await page.locator('.saved-views button', { hasText: 'Views' }).click();
    await expect(page.locator('.saved-view-row', { hasText: viewName })).toHaveCount(0);
  });

  // WP-1.1 Task 5's first caller of internal/store.UpdateSavedView:
  // POST /views/{id}/rename (routes.go, registered `self` -- authentication
  // is the whole route-level gate, ownership is enforced downstream). This
  // test proves the ordinary path: renaming persists and survives a genuine
  // reload, the same "not page.reload()" discipline the save/reopen flow
  // above follows -- only the STORED name can be what the menu shows next.
  test('renaming a saved view persists the new name and clears the old one, after a genuine reload', async ({
    page,
  }) => {
    const beforeName = `e2e-rename-before-${Date.now()}`;
    const afterName = `e2e-rename-after-${Date.now()}`;

    await page.goto('/assets', { waitUntil: 'networkidle' });
    await Promise.all([
      page.waitForResponse(
        (r) => r.request().method() === 'GET' && r.url().includes('/assets?') && r.url().includes('kind=firewall'),
      ),
      page.locator('#f-kind').selectOption('firewall'),
    ]);

    await page.locator('.saved-views button', { hasText: 'Views' }).click();
    // .saved-view-save, not the bare `.saved-views-menu input[name="name"]`
    // the tests above use: EVERY existing row also carries its own hidden
    // `.saved-view-rename input[name="name"]` (saved_views.html), present in
    // the DOM regardless of CSS visibility, so a bare selector is only
    // unique when this account happens to have zero other saved views at
    // the moment -- true in a fresh run, not guaranteed on a long-lived
    // instance. `.saved-view-save` names the "Save this view" form
    // specifically, which is unique on the page either way.
    await page.locator('.saved-view-save input[name="name"]').fill(beforeName);
    await Promise.all([
      page.waitForEvent('framenavigated', (frame) => frame === page.mainFrame() && frame.url().includes('kind=firewall')),
      page.locator('.saved-view-save button[type="submit"]').click(),
    ]);
    await page.waitForLoadState('networkidle');

    // --- Rename it, from a genuinely clean, unfiltered page -- not the
    // redirect target above, the same "close this tab, open a fresh one"
    // discipline the reopen step below follows. saved_views.html's rename
    // control is a SECOND, hidden form per row (`.saved-view-rename`,
    // revealed by startRename toggling `.is-editing` on the row -- CSP-safe
    // DOM state, not an Alpine comparison; see that template's own comment),
    // so the click sequence is Views -> Rename (reveals the form) -> fill
    // -> its own Save, not the "Save this view" form further down. ---
    await page.goto('/assets', { waitUntil: 'networkidle' });
    await page.locator('.saved-views button', { hasText: 'Views' }).click();
    const row = page.locator('.saved-view-row', { hasText: beforeName });
    await expect(row).toBeVisible();
    await row.locator('.sv-rename-btn').click();
    await row.locator('.saved-view-rename input[name="name"]').fill(afterName);
    await Promise.all([
      page.waitForEvent('framenavigated', (frame) => frame === page.mainFrame() && frame.url().includes('kind=firewall')),
      row.locator('.saved-view-rename button[type="submit"]').click(),
    ]);
    await page.waitForLoadState('networkidle');

    // --- A genuine reload: navigate away to a clean /assets and back,
    // rather than trusting the HTMX-redirected page still in front of us. ---
    await page.goto('/assets', { waitUntil: 'networkidle' });
    await page.locator('.saved-views button', { hasText: 'Views' }).click();
    await expect(page.locator('.saved-view-row', { hasText: afterName })).toBeVisible();
    await expect(page.locator('.saved-view-row', { hasText: beforeName })).toHaveCount(0);

    // Clean up -- same permanent-soft-retired-row trade-off as every other
    // write in this file.
    const rowAfter = page.locator('.saved-view-row', { hasText: afterName });
    await Promise.all([
      page.waitForURL((url) => url.pathname === '/assets' && url.search === ''),
      rowAfter.locator('button', { hasText: 'Remove' }).click(),
    ]);
  });

  // The other half of the same claim: a saved view is PRIVATE, and that has
  // no exception for who is asking. internal/store/savedviews.go's
  // authorizeSavedViewOwner is explicit: "NO ADMINISTRATOR EXCEPTION,
  // deliberately... Administrators administer the estate, not other
  // people's shortcuts." A second signed-in person -- genuinely different,
  // not the same session reused -- must be refused even though POST
  // /views/{id}/rename requires nothing beyond RequireAuth at the route
  // table (it is registered `self`, not `write`): the refusal has to come
  // from ownership, not from a role or a write grant this second person
  // might simply lack.
  test('a second signed-in person cannot rename someone else\'s saved view, even with a direct request', async ({
    page, browser,
  }) => {
    const viewName = `e2e-private-${Date.now()}`;

    // --- The default `page` fixture (the admin session global-setup.js
    // signs in) is the OWNING session here -- create a view under it, same
    // as every other test in this file. ---
    await page.goto('/assets', { waitUntil: 'networkidle' });
    await page.locator('.saved-views button', { hasText: 'Views' }).click();
    await page.locator('.saved-view-save input[name="name"]').fill(viewName);
    await Promise.all([
      // Saved from a clean, unfiltered /assets, so the redirect target is
      // exactly "/assets" with no query string -- unlike the filtered saves
      // elsewhere in this file, there is no filter substring to match on.
      page.waitForEvent('framenavigated', (frame) => frame === page.mainFrame() && frame.url().endsWith('/assets')),
      page.locator('.saved-view-save button[type="submit"]').click(),
    ]);
    await page.waitForLoadState('networkidle');

    await page.goto('/assets', { waitUntil: 'networkidle' });
    await page.locator('.saved-views button', { hasText: 'Views' }).click();
    const row = page.locator('.saved-view-row', { hasText: viewName });
    await expect(row).toBeVisible();
    // The id lives on the row's own data-view attribute (saved_views.html)
    // -- readable here only because the view is THIS session's own; a
    // second person's Views menu never lists it at all (a saved view is
    // private, scoped to its owner -- SavedViewOptionsFor), so there is no
    // UI path for them to even discover this id, let alone use it. That is
    // exactly why the boundary assertion below has to be a direct request:
    // the control itself is invisible to a second person, and "hiding a
    // control is not the enforcement" (CLAUDE.md) cuts both ways -- a
    // boundary only ever proven by a missing button is not proven at all.
    const viewID = await row.getAttribute('data-view');
    expect(viewID, 'the saved view row should carry its own id').toBeTruthy();

    // --- A second, genuinely different signed-in person: a throwaway
    // Observer account, created the same way user-administration.spec.js's
    // own colleague is (POST /users, an Administrator-only route) -- NOT the
    // project-owner fixture, which this file does not otherwise depend on
    // and should not start requiring just to prove a saved view is private
    // from anyone who is not its owner. ---
    const colleagueUsername = `e2e-sv-colleague-${Date.now()}`;
    const colleaguePassword = 'e2e-colleague-password-123';
    await page.goto('/users', { waitUntil: 'networkidle' });
    await page.locator('#u-username').fill(colleagueUsername);
    await page.locator('#u-password').fill(colleaguePassword);
    await Promise.all([
      page.waitForLoadState('networkidle'),
      page.locator('#user-form button[type="submit"]').click(),
    ]);
    await expect(page.locator('tr', { has: page.getByText(colleagueUsername, { exact: true }) })).toBeVisible();

    const { context: colleagueContext, page: colleaguePage } = await signInAsFreshUser(
      browser, BASE_URL, colleagueUsername, colleaguePassword,
    );
    try {
      const csrfToken = await csrfTokenFrom(colleaguePage);
      const response = await colleaguePage.request.post(`/views/${viewID}/rename`, {
        form: { csrf_token: csrfToken, name: 'sneaky-rename' },
        // nosurf requires one of Sec-Fetch-Site, Origin or Referer on every
        // unsafe request (see every other direct POST in this suite).
        headers: { Origin: BASE_URL },
      });
      expect(response.status(), "a second signed-in person renaming someone else's saved view").toBe(403);
    } finally {
      await colleagueContext.close();
    }

    // The name never moved.
    await page.goto('/assets', { waitUntil: 'networkidle' });
    await page.locator('.saved-views button', { hasText: 'Views' }).click();
    await expect(page.locator('.saved-view-row', { hasText: viewName })).toBeVisible();
    await expect(page.locator('.saved-view-row', { hasText: 'sneaky-rename' })).toHaveCount(0);

    // Clean up.
    const rowToRemove = page.locator('.saved-view-row', { hasText: viewName });
    await Promise.all([
      page.waitForURL((url) => url.pathname === '/assets' && url.search === ''),
      rowToRemove.locator('button', { hasText: 'Remove' }).click(),
    ]);
  });
});
